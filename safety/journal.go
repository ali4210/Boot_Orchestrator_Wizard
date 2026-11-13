package safety

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// StepStatus represents the lifecycle state of a single journaled step.
type StepStatus string

const (
	StepPending    StepStatus = "pending"
	StepRunning    StepStatus = "running"
	StepPaused     StepStatus = "paused"
	StepCommitted  StepStatus = "committed"
	StepFailed     StepStatus = "failed"
	StepRolledBack StepStatus = "rolled_back"
)

// SessionState records the overall transaction context across disks.
type SessionState struct {
	TargetOSID      string `json:"target_os_id,omitempty"`
	DistroName      string `json:"distro_name,omitempty"`
	ProvisionMode   string `json:"provision_mode,omitempty"`    // "single-boot", "dual-boot", "triple-boot"
	TargetDisk      string `json:"target_disk,omitempty"`       // e.g. "/dev/sda" (best-effort, resolved fresh on revert)
	TargetPartition string `json:"target_partition,omitempty"`  // e.g. "/dev/sda2" (best-effort, resolved fresh on revert)
	TargetPartNum   string `json:"target_part_num,omitempty"`   // e.g. "2"
	TargetPartUUID  string `json:"target_part_uuid,omitempty"`  // stable fingerprint — survives device renaming
	TargetFSType    string `json:"target_fs_type,omitempty"`    // fs type recorded at creation, for a sanity check on revert
	TargetSizeBytes int64  `json:"target_size_bytes,omitempty"` // partition size recorded at creation, for a sanity check on revert
	ShrunkPartition string `json:"shrunk_partition,omitempty"`  // e.g. "/dev/sda1" (best-effort, resolved fresh on revert)
	ShrunkPartNum   string `json:"shrunk_part_num,omitempty"`   // e.g. "1"
	ShrunkPartUUID  string `json:"shrunk_part_uuid,omitempty"`  // stable fingerprint of the host partition being expanded back
	ShrunkFSType    string `json:"shrunk_fs_type,omitempty"`    // "ext4", "ntfs", "btrfs"
	EFIDirName      string `json:"efi_dir_name,omitempty"`      // e.g. "Microsoft", "parrot"
	DownloadURL     string `json:"download_url,omitempty"`
	BytesDownloaded int64  `json:"bytes_downloaded,omitempty"`
	TotalBytes      int64  `json:"total_bytes,omitempty"`
	IsPaused        bool   `json:"is_paused,omitempty"`
}

// Step is one atomic unit of work in a provisioning transaction.
type Step struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	Status       StepStatus      `json:"status"`
	StartedAt    time.Time       `json:"started_at,omitempty"`
	FinishedAt   time.Time       `json:"finished_at,omitempty"`
	Error        string          `json:"error,omitempty"`
	RollbackData json.RawMessage `json:"rollback_data,omitempty"`
}

// Journal is the on-disk transactional log.
type Journal struct {
	Path      string       `json:"-"`
	TxnID     string       `json:"txn_id"`
	CreatedAt time.Time    `json:"created_at"`
	UpdatedAt time.Time    `json:"updated_at"`
	Session   SessionState `json:"session"`
	Steps     []*Step      `json:"steps"`

	mu sync.Mutex
}

// RollbackFunc undoes one step given its stored rollback data.
type RollbackFunc func(data json.RawMessage) error

// NewJournal creates a fresh journal file at path, auto-healing 0-byte crashed stubs.
func NewJournal(path, txnID string) (*Journal, error) {
	if fi, err := os.Stat(path); err == nil {
		if fi.Size() == 0 {
			_ = os.Remove(path)
		} else {
			return nil, fmt.Errorf("journal already exists at %s — call LoadJournal to resume or purge it first", path)
		}
	}
	j := &Journal{
		Path:      path,
		TxnID:     txnID,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Steps:     []*Step{},
	}
	if err := j.flush(); err != nil {
		return nil, err
	}
	return j, nil
}

// LoadJournal reads an existing journal.json and rejects empty/invalid files cleanly.
func LoadJournal(path string) (*Journal, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if fi.Size() == 0 {
		_ = os.Remove(path)
		return nil, fmt.Errorf("journal at %s was 0 bytes (purged stale stub)", path)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var j Journal
	if err := json.Unmarshal(raw, &j); err != nil {
		return nil, fmt.Errorf("journal at %s is corrupt: %w", path, err)
	}
	j.Path = path
	return &j, nil
}

// HasIncompleteSteps reports whether an active operation is currently running or paused.
func (j *Journal) HasIncompleteSteps() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.hasIncompleteStepsLocked()
}

func (j *Journal) hasIncompleteStepsLocked() bool {
	if j.Session.IsPaused {
		return true
	}
	for _, s := range j.Steps {
		if s.Status == StepRunning || s.Status == StepPending || s.Status == StepPaused {
			return true
		}
	}
	return false
}

// GetUnfinishedSession returns the stored session parameters if an interrupted session exists.
func (j *Journal) GetUnfinishedSession() (*SessionState, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.hasIncompleteStepsLocked() {
		return &j.Session, true
	}
	return nil, false
}

// SetSessionState records high-level workflow parameters.
func (j *Journal) SetSessionState(state SessionState) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.Session = state
	return j.flushLocked()
}

// Begin registers a step as running and persists immediately.
func (j *Journal) Begin(name string, rollbackData any) (*Step, error) {
	j.mu.Lock()
	defer j.mu.Unlock()

	raw, err := json.Marshal(rollbackData)
	if err != nil {
		return nil, fmt.Errorf("marshal rollback data for step %q: %w", name, err)
	}
	step := &Step{
		ID:           fmt.Sprintf("%s-%03d", name, len(j.Steps)+1),
		Name:         name,
		Status:       StepRunning,
		StartedAt:    time.Now(),
		RollbackData: raw,
	}
	j.Steps = append(j.Steps, step)
	if err := j.flushLocked(); err != nil {
		return nil, err
	}
	return step, nil
}

// Pause flags a running step as paused and persists the byte offset checkpoint.
func (j *Journal) Pause(step *Step, bytesRead, totalBytes int64) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	step.Status = StepPaused
	j.Session.IsPaused = true
	j.Session.BytesDownloaded = bytesRead
	j.Session.TotalBytes = totalBytes
	return j.flushLocked()
}

// Commit marks a step as successfully completed.
func (j *Journal) Commit(step *Step) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	step.Status = StepCommitted
	step.FinishedAt = time.Now()
	return j.flushLocked()
}

// Fail marks a step as failed and records the error.
func (j *Journal) Fail(step *Step, cause error) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	step.Status = StepFailed
	step.FinishedAt = time.Now()
	if cause != nil {
		step.Error = cause.Error()
	}
	return j.flushLocked()
}

// RollbackAll undoes steps in reverse chronological order.
func (j *Journal) RollbackAll(handlers map[string]RollbackFunc) error {
	j.mu.Lock()
	defer j.mu.Unlock()

	var errs []string
	for i := len(j.Steps) - 1; i >= 0; i-- {
		s := j.Steps[i]
		if s.Status != StepCommitted && s.Status != StepFailed && s.Status != StepPaused && s.Status != StepRunning {
			continue
		}
		fn, ok := handlers[s.Name]
		if !ok {
			errs = append(errs, fmt.Sprintf("step %s: no rollback handler registered for %q", s.ID, s.Name))
			continue
		}
		if err := fn(s.RollbackData); err != nil {
			errs = append(errs, fmt.Sprintf("step %s: rollback failed: %v", s.ID, err))
			continue
		}
		s.Status = StepRolledBack
	}
	if err := j.flushLocked(); err != nil {
		errs = append(errs, fmt.Sprintf("journal write after rollback failed: %v", err))
	}
	if len(errs) > 0 {
		return fmt.Errorf("rollback completed with %d error(s):\n%s", len(errs), joinLines(errs))
	}
	return nil
}

// FinalizeAndRecord cleans up the transactional journal upon successful completion,
// but first persists the session's identity fingerprint (PARTUUID etc.) into a
// small permanent record file, so a later revert can still find and undo this
// install after the transactional journal itself is gone. Pass the same
// SessionState you were tracking (it's read out before wiping, so this must be
// called before/instead of the old bare Finalize()).
func (j *Journal) FinalizeAndRecord(recordPath string) error {
	j.mu.Lock()
	defer j.mu.Unlock()

	if j.hasIncompleteStepsLocked() {
		return fmt.Errorf("refusing to finalize: active operations still in flight")
	}

	// Persist the durable fact of "this OS was installed here" before wiping
	// the transactional state. Best-effort: a failure here shouldn't block
	// finishing the provision, but it does mean the install becomes
	// undiscoverable by DiscoverSecondaryOSes later — worth logging upstream.
	if recordPath != "" && (j.Session.TargetPartUUID != "" || j.Session.TargetPartition != "") {
		record := &Journal{
			Path:      recordPath,
			TxnID:     j.TxnID,
			CreatedAt: j.CreatedAt,
			UpdatedAt: time.Now(),
			Session:   j.Session,
			Steps:     nil,
		}
		_ = record.flush()
	}

	j.Steps = nil
	j.Session = SessionState{}
	if j.Path != "" {
		_ = os.Remove(j.Path)
	}
	return nil
}

// Finalize is kept for backward compatibility — it cleans up the transactional
// journal without persisting an install record. Prefer FinalizeAndRecord for
// any provisioning path that creates a revertible dual-boot partition.
func (j *Journal) Finalize() error {
	return j.FinalizeAndRecord("")
}

// PurgeForce unconditionally removes the journal file from disk.
func (j *Journal) PurgeForce() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.Steps = nil
	j.Session = SessionState{}
	if j.Path != "" {
		return os.Remove(j.Path)
	}
	return nil
}

func (j *Journal) flush() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.flushLocked()
}

func (j *Journal) flushLocked() error {
	j.UpdatedAt = time.Now()
	raw, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(j.Path)
	if dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0755)
	}
	tmp, err := os.CreateTemp(dir, ".journal-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return os.Rename(tmpPath, j.Path)
}

func joinLines(lines []string) string {
	out := ""
	for _, l := range lines {
		out += "  - " + l + "\n"
	}
	return out
}
