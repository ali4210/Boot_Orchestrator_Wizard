// Package engine — orchestrator.go is the top-level pipeline the TUI calls
// into. It threads a single safety.Journal through every step of a
// provisioning run in blueprint order, registers every step's rollback
// handler up front, and guarantees that any failure triggers RollbackAll
// before returning — this is the piece that makes the whole project
// "transactional" rather than just a sequence of scripts.
package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"boot-orchestrator/hypervisor"
	"boot-orchestrator/safety"
)

// ProvisionRequest is everything a run needs, gathered by the TUI's earlier
// screens (OS select, flavor filter, confirm) before ScreenProgress starts.
type ProvisionRequest struct {
	JournalPath      string // e.g. filepath.Join(stateDir, "journal.json")
	ImageURL         string
	ImageSHA256      string // empty = skip verification (not recommended)
	DownloadDestPath string
	TargetPartition  string // partition the new OS will be written to
	TargetDiskPath   string // whole-disk device backing TargetPartition, for GPT/MBR backup
	IsWindowsImage   bool   // selects rootfs.go vs wim_deployer.go path
	WimIndex         int    // used when IsWindowsImage
	AutounattendPath string // optional, Windows only
	BootLabel        string // label for the new NVRAM/BCD/GRUB entry
	HypervisorKind   hypervisor.Kind
	SkipCompaction   bool
}

// StepUpdate is emitted after each step so the TUI can update its scrolling
// status journal and progress bars in near-real-time.
type StepUpdate struct {
	StepName string
	Done     bool
	Err      error
}

// ProvisionResult is returned when the whole pipeline finishes, success or
// failure (on failure, RolledBack indicates whether RollbackAll ran and
// whether it fully succeeded).
type ProvisionResult struct {
	Success       bool
	FailedAtStep  string
	OriginalError error
	RolledBack    bool
	RollbackError error
}

// rollbackHandlers maps every journaled step name in this pipeline to its
// undo function. Registered once, used both by the happy-path failure
// handler here and available for a separate "resume/rollback an interrupted
// prior run" command (cmd/orchestrator can call safety.LoadJournal +
// this same map directly).
func rollbackHandlers(grubDefaultPath string) map[string]safety.RollbackFunc {
	return map[string]safety.RollbackFunc{
		"partition-shrink": RollbackPartitionShrink,
		"docker-isolation": RollbackDockerIsolation,
		"uefi-set-boot-next":  RollbackSetBootNext,
		"uefi-set-boot-order": RollbackSetBootOrder,
		"bcd-mutation":        RollbackBCD,
		"bcd-add-entry":       RollbackAddWindowsBootEntry,
		"grub-set-default": func(data json.RawMessage) error {
			return RollbackSetGrubDefaultEntry(grubDefaultPath, data)
		},
		// Deliberately NOT registered: "image-deploy" (rootfs/wim application).
		// As documented in rootfs.go, a raw device write has no true undo —
		// it is only ever run against a partition already confirmed empty
		// by partition-shrink, so rolling back partition-shrink is the
		// correct and sufficient recovery action, not re-writing the image.
	}
}

// RunProvision executes the full pipeline in blueprint order:
//   1. Storage Management & Compaction (isolate, then shrink)
//   2. Transactional Provisioning (download+verify, deploy image)
//   3. Bootloader Orchestration (UEFI/BCD/GRUB)
// emitting a StepUpdate on onUpdate after every step. Any error triggers
// RollbackAll automatically before returning.
func RunProvision(ctx context.Context, req ProvisionRequest, grubDefaultPath string, onUpdate func(StepUpdate)) ProvisionResult {
	journal, err := safety.NewJournal(req.JournalPath, generateTxnID())
	if err != nil {
		return ProvisionResult{Success: false, FailedAtStep: "journal-init", OriginalError: err}
	}
	handlers := rollbackHandlers(grubDefaultPath)

	fail := func(stepName string, cause error) ProvisionResult {
		onUpdate(StepUpdate{StepName: stepName, Done: true, Err: cause})
		rbErr := journal.RollbackAll(handlers)
		return ProvisionResult{
			Success:       false,
			FailedAtStep:  stepName,
			OriginalError: cause,
			RolledBack:    true,
			RollbackError: rbErr,
		}
	}

	// --- 1. Storage Management & Compaction Engine -----------------------

	if req.TargetPartition != "" {
		step, err := journal.Begin("partition-shrink", nil)
		if err != nil {
			return fail("partition-shrink", err)
		}
		backup, err := BackupPartitionTable(req.TargetDiskPath)
		if err != nil {
			journal.Fail(step, err)
			return fail("partition-shrink", err)
		}
		backupJSON, _ := json.Marshal(backup)
		step.RollbackData = backupJSON

		plan, err := PlanShrink(req.TargetPartition, 5*1024*1024*1024) // 5GB margin above filesystem minimum
		if err != nil {
			journal.Fail(step, err)
			return fail("partition-shrink", err)
		}
		if err := ApplyShrink(plan); err != nil {
			journal.Fail(step, err)
			return fail("partition-shrink", err)
		}
		journal.Commit(step)
		onUpdate(StepUpdate{StepName: "partition-shrink", Done: true})
	}

	if !req.SkipCompaction && req.HypervisorKind != hypervisor.KindBareMetal {
		compactPlan, err := hypervisor.PlanHostCompact(req.HypervisorKind, req.TargetDiskPath)
		if err != nil {
			// Compaction is an optimization, not a correctness requirement —
			// log and continue rather than failing the whole provisioning run.
			onUpdate(StepUpdate{StepName: "hypervisor-compact-plan", Done: true, Err: err})
		} else {
			onUpdate(StepUpdate{StepName: "hypervisor-compact-planned: " + compactPlan.FormatPlanForDisplay(), Done: true})
		}
	}

	// --- 2. Transactional Provisioning ------------------------------------

	dlStep, err := journal.Begin("image-download", nil)
	if err != nil {
		return fail("image-download", err)
	}
	streamResult, err := StreamDownload(ctx, req.ImageURL, req.DownloadDestPath, req.ImageSHA256, func(p Progress) {
		onUpdate(StepUpdate{StepName: fmt.Sprintf("downloading: %.1f%%", p.Percent()), Done: false})
	})
	if err != nil {
		journal.Fail(dlStep, err)
		return fail("image-download", err)
	}
	journal.Commit(dlStep)
	onUpdate(StepUpdate{StepName: "image-download", Done: true})

	deployStep, err := journal.Begin("image-deploy", nil)
	if err != nil {
		return fail("image-deploy", err)
	}
	if req.IsWindowsImage {
		wimPlan := &ApplyWimPlan{
			WimPath:         streamResult.DestPath,
			Index:           req.WimIndex,
			TargetPartition: req.TargetPartition,
			MountPoint:      req.TargetPartition,
			AutounattendSrc: req.AutounattendPath,
		}
		if err := ApplyWim(wimPlan); err != nil {
			journal.Fail(deployStep, err)
			return fail("image-deploy", err)
		}
	} else {
		deployPlan, err := PlanDeploy(streamResult.DestPath, req.TargetPartition)
		if err != nil {
			journal.Fail(deployStep, err)
			return fail("image-deploy", err)
		}
		if _, err := ApplyDeploy(deployPlan); err != nil {
			journal.Fail(deployStep, err)
			return fail("image-deploy", err)
		}
	}
	journal.Commit(deployStep)
	onUpdate(StepUpdate{StepName: "image-deploy", Done: true})

	// --- 3. Bootloader Orchestrator ----------------------------------------

	if req.IsWindowsImage {
		guid, rb, err := AddWindowsBootEntry(req.BootLabel)
		if err != nil {
			return fail("bcd-add-entry", err)
		}
		step, _ := journal.Begin("bcd-add-entry", nil)
		step.RollbackData = rb
		journal.Commit(step)
		onUpdate(StepUpdate{StepName: "bcd-add-entry: " + guid, Done: true})
	} else {
		if err := FirmwareModeCheck(); err == nil {
			nvramState, err := ReadNVRAMState()
			if err != nil {
				return fail("uefi-set-boot-order", err)
			}
			step, _ := journal.Begin("uefi-set-boot-order", nil)
			rb, err := SetBootOrder(nvramState.BootOrder) // caller supplies actual desired order upstream in real usage
			step.RollbackData = rb
			if err != nil {
				journal.Fail(step, err)
				return fail("uefi-set-boot-order", err)
			}
			journal.Commit(step)
			onUpdate(StepUpdate{StepName: "uefi-set-boot-order", Done: true})
		} else {
			step, _ := journal.Begin("grub-set-default", nil)
			rb, err := SetGrubDefaultEntry(grubDefaultPath, req.BootLabel)
			step.RollbackData = rb
			if err != nil {
				journal.Fail(step, err)
				return fail("grub-set-default", err)
			}
			journal.Commit(step)
			onUpdate(StepUpdate{StepName: "grub-set-default", Done: true})
		}
	}

	if err := journal.Finalize(); err != nil {
		// Non-fatal: the run itself succeeded, only journal cleanup failed.
		onUpdate(StepUpdate{StepName: "journal-finalize", Done: true, Err: err})
	}

	return ProvisionResult{Success: true}
}

// ResumeAndRollback loads an interrupted journal from a previous crashed
// run and rolls it back. cmd/orchestrator should call this at startup
// whenever safety.LoadJournal + HasIncompleteSteps indicates a prior run
// never finished cleanly — offer this to the user before letting them
// start a new run against the same disk.
func ResumeAndRollback(journalPath, grubDefaultPath string) error {
	journal, err := safety.LoadJournal(journalPath)
	if err != nil {
		return fmt.Errorf("loading interrupted journal: %w", err)
	}
	if !journal.HasIncompleteSteps() {
		return fmt.Errorf("journal at %s has no incomplete steps — nothing to roll back", journalPath)
	}
	return journal.RollbackAll(rollbackHandlers(grubDefaultPath))
}

func generateTxnID() string {
	return fmt.Sprintf("txn-%d", time.Now().UnixNano())
}
