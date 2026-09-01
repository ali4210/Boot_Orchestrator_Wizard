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
	StepPending   StepStatus = "pending"
	StepRunning   StepStatus = "running"
	StepCommitted StepStatus = "committed"
	StepFailed    StepStatus = "failed"
	StepRolledBack StepStatus = "rolled_back"
)

// Step is one atomic unit of work in a provisioning transaction.
// RollbackData is opaque, step-defined state needed to undo the action
// (e.g. original partition table bytes, original BootNext value, temp file paths).
type Step struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	Status       StepStatus      `json:"status"`
	StartedAt    time.Time       `json:"started_at,omitempty"`
	FinishedAt   time.Time       `json:"finished_at,omitempty"`
	Error        string          `json:"error,omitempty"`
	RollbackData json.RawMessage `json:"rollback_data,omitempty"`
}

// Journal is the on-disk transactional log. It is written after every
// state change so that a crash or power loss mid-run leaves enough
// information for RollbackAll to restore the host to its prior state.
type Journal struct {
	Path      string    `json:"-"`
	TxnID     string    `json:"txn_id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Steps     []*Step   `json:"steps"`

	mu sync.Mutex
}

// RollbackFunc undoes one step given its stored rollback data.
// Registered per step-name by the engine packages that know how to
// reverse their own actions (partition, uefi, wim_deployer, etc.).
type RollbackFunc func(data json.RawMessage) error

// NewJournal creates a fresh journal file at path, failing if one
// already exists (use LoadJournal to resume/inspect an interrupted run).
func NewJournal(path, txnID string) (*Journal, error) {
	if _, err := os.Stat(path); err == nil {
		return nil, fmt.Errorf("journal already exists at %s — call LoadJournal to resume or remove it first", path)
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

// LoadJournal reads an existing journal.json — used at startup to detect
// an interrupted prior run and offer rollback before doing anything else.
func LoadJournal(path string) (*Journal, error) {
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

// HasIncompleteSteps reports whether the journal recorded a run that never
// reached a clean, fully-committed end — i.e. rollback should be offered.
func (j *Journal) HasIncompleteSteps() bool {
	for _, s := range j.Steps {
		if s.Status == StepRunning || s.Status == StepFailed || s.Status == StepPending {
			return true
		}
	}
	return false
}

// Begin registers a new step as pending+running and persists immediately,
// so a crash right after this call still leaves a record to roll back.
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

// Commit marks a step as successfully completed.
func (j *Journal) Commit(step *Step) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	step.Status = StepCommitted
	step.FinishedAt = time.Now()
	return j.flushLocked()
}

// Fail marks a step as failed and records the error. The caller should
// then invoke RollbackAll to unwind everything committed so far.
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

// RollbackAll walks committed/failed steps in reverse order and invokes
// the matching handler from handlers (keyed by step Name) to undo them.
// It keeps going even if an individual rollback fails, collecting all
// errors, because leaving later steps un-rolled-back is worse than a
// partial rollback report.
func (j *Journal) RollbackAll(handlers map[string]RollbackFunc) error {
	j.mu.Lock()
	defer j.mu.Unlock()

	var errs []string
	for i := len(j.Steps) - 1; i >= 0; i-- {
		s := j.Steps[i]
		if s.Status != StepCommitted && s.Status != StepFailed {
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

// Finalize deletes the journal file once a run has fully committed with
// no failures — a clean run shouldn't leave a stale journal offering a
// rollback that no longer makes sense.
func (j *Journal) Finalize() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.HasIncompleteSteps() {
		return fmt.Errorf("refusing to finalize: journal still has incomplete steps")
	}
	return os.Remove(j.Path)
}

func (j *Journal) flush() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.flushLocked()
}

// flushLocked writes the journal atomically (temp file + rename) so a
// crash mid-write never corrupts the on-disk record we rely on to recover.
func (j *Journal) flushLocked() error {
	j.UpdatedAt = time.Now()
	raw, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(j.Path)
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
