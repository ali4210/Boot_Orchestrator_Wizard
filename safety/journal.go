package safety

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

	// OriginalSwapSizeBytes/OriginalSwapUUID record a trailing swap slice
	// that install-time partitioning converted into the target OS
	// partition (follow-up F14), so revert can recreate an equivalent swap
	// partition instead of permanently losing the host's swap space. Zero/
	// empty when no swap was converted (the common case: carved from free
	// space, or a plain shrink).
	OriginalSwapSizeBytes int64  `json:"original_swap_size_bytes,omitempty"`
	OriginalSwapUUID      string `json:"original_swap_uuid,omitempty"`

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

// DefaultJournalPath returns the path the journal should be created/loaded
// at by default: a dedicated, non-world-writable state directory outside
// the git repository, so an unprivileged user — including the very user
// who invoked `sudo` to run this tool — cannot tamper with journal
// contents between runs and steer what a LATER elevated run does to real
// disks (follow-up F12; see the "Security tradeoff" note in PROGRESS.md).
// Each candidate directory is created mode 0700, so whichever process
// first creates it (normally the root-elevated engine) is the only one
// that can read or write inside it afterward. Falls through to the next
// candidate if a directory can't be created (e.g. not running as root, or
// a read-only /var), and as an absolute last resort returns a
// repo-relative path matching this tool's original behavior.
//
// Windows caveat: os.MkdirAll's mode parameter does not translate into a
// meaningful Windows ACL (Go only maps it to the read-only attribute bit),
// so on Windows this function picks a conventional per-machine location
// (%ProgramData%) but does NOT actually achieve the same non-admin-writable
// guarantee it does on Unix — see the "Security tradeoff" note in
// PROGRESS.md.
func DefaultJournalPath() string {
	var candidates []string
	if runtime.GOOS == "windows" {
		if pd := os.Getenv("ProgramData"); pd != "" {
			candidates = append(candidates, filepath.Join(pd, "BootOrchestrator"))
		}
	} else {
		candidates = []string{"/var/lib/boot-orchestrator", "/var/tmp/boot-orchestrator"}
	}
	for _, dir := range candidates {
		if err := os.MkdirAll(dir, 0700); err == nil {
			return filepath.Join(dir, "journal.json")
		}
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		dir := filepath.Join(home, ".local", "state", "boot-orchestrator")
		if err := os.MkdirAll(dir, 0700); err == nil {
			return filepath.Join(dir, "journal.json")
		}
	}
	return "orchestrator_journal.json"
}

// NewJournal creates a fresh journal file at path, auto-healing 0-byte crashed stubs.
//
// Follow-up F12: this used to chown the newly created file back to
// SUDO_UID/SUDO_GID so it wouldn't be left root-owned in the invoking
// user's repo checkout. That chown is now removed rather than kept: this
// engine's journal records exactly the state (target disk/partition,
// rollback data) a LATER elevated run trusts to decide what to do to real
// hardware, so handing an unprivileged user write access to it was the
// opposite of what a security boundary should do. DefaultJournalPath
// instead keeps the journal in a directory the invoking user can't write
// to at all, which is the actual fix autorun.sh's root-owned-file warning
// was only ever a band-aid for.
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

// allowedStepNames is every step name this engine's own code ever creates
// via Journal.Begin, plus every name its RollbackAll handler maps key
// against. Follow-up F12 defense-in-depth: a journal loaded from disk is
// state an earlier (possibly different-privilege, possibly tampered)
// process produced. Restricting step names to this fixed set on load means
// a corrupted or hand-edited journal can't make RollbackAll invoke a
// handler for a step name this version of the engine never actually
// creates, or otherwise smuggle unexpected behavior through Step.Name.
var allowedStepNames = map[string]bool{
	"image-download":       true,
	"partition-shrink":     true,
	"image-deploy":         true,
	"bootloader-configure": true,
	"revert-engine":        true,
	"docker-isolation":     true,
	"uefi-set-boot-next":   true,
	"uefi-set-boot-order":  true,
	"bcd-mutation":         true,
	"bcd-add-entry":        true,
	"grub-set-default":     true,
}

// looksLikeDevicePath reports whether s is a plausible Linux block device
// path with no path-traversal segment — structural validation, not an
// existence check (see the "Security tradeoff" note in PROGRESS.md for why
// this stops short of verifying the device exists/matches: device paths
// legitimately get renumbered between boots, which is exactly why
// SessionState separately carries PARTUUID fields for resume to re-resolve
// against, rather than trusting the raw path). Empty is allowed, since not
// every session has every field populated.
func looksLikeDevicePath(s string) bool {
	if s == "" {
		return true
	}
	if strings.Contains(s, "..") {
		return false
	}
	return strings.HasPrefix(s, "/dev/") && filepath.Clean(s) == s
}

// validateLoadedJournal rejects a journal whose contents could not have
// been produced by this engine's own code paths (follow-up F12 defense in
// depth, checked on every LoadJournal regardless of where the journal file
// lives).
func validateLoadedJournal(j *Journal) error {
	for _, field := range []string{j.Session.TargetDisk, j.Session.TargetPartition, j.Session.ShrunkPartition} {
		if !looksLikeDevicePath(field) {
			return fmt.Errorf("journal contains a malformed or suspicious device path %q", field)
		}
	}
	for _, s := range j.Steps {
		if s == nil {
			continue
		}
		if !allowedStepNames[s.Name] {
			return fmt.Errorf("journal contains an unrecognized step name %q", s.Name)
		}
	}
	return nil
}

// LoadJournal reads an existing journal.json, rejects empty/invalid files
// cleanly, and validates its contents (follow-up F12) before returning it.
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
	if err := validateLoadedJournal(&j); err != nil {
		return nil, fmt.Errorf("journal at %s failed validation: %w", path, err)
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
