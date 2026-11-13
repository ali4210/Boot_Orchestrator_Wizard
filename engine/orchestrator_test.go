package engine

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"boot-orchestrator/safety"
)

// TestConfigureBootloaderStep_LinuxBranchFailurePropagates is the regression
// test for audit bug 2.1: when AutoConfigureDualBoot fails on the generic
// (non-macOS, non-Windows) branch, configureBootloaderStep must report
// failure to its caller, must never emit the "dual-boot bootloader
// synchronized" success update, and must leave the journal step marked
// failed rather than committed.
func TestConfigureBootloaderStep_LinuxBranchFailurePropagates(t *testing.T) {
	origFn := autoConfigureDualBootFn
	t.Cleanup(func() { autoConfigureDualBootFn = origFn })

	wantErr := errors.New("update-grub: exit status 1")
	autoConfigureDualBootFn = func(targetPartition, grubDefaultPath string, creds Credentials, logFn func(string)) error {
		return wantErr
	}

	journalPath := filepath.Join(t.TempDir(), "journal.json")
	journal, err := safety.NewJournal(journalPath, "test-txn")
	if err != nil {
		t.Fatalf("NewJournal: %v", err)
	}

	var updates []StepUpdate
	onUpdate := func(u StepUpdate) { updates = append(updates, u) }

	failCalled := false
	fail := func(stepName string, cause error) ProvisionResult {
		failCalled = true
		return ProvisionResult{Success: false, FailedAtStep: stepName, OriginalError: cause}
	}

	req := ProvisionRequest{TargetPartition: "/dev/loop0p1"}
	res, failed := configureBootloaderStep(journal, false /* isMacOS */, false /* isWindows */, req, "", "", onUpdate, fail)

	if !failed {
		t.Fatalf("expected configureBootloaderStep to signal failure to its caller, got failed=false")
	}
	if !failCalled {
		t.Fatalf("expected the fail() closure to be invoked")
	}
	if res.Success {
		t.Fatalf("expected ProvisionResult.Success=false on bootloader failure, got true")
	}

	for _, u := range updates {
		if strings.Contains(u.StepName, "dual-boot bootloader synchronized") {
			t.Fatalf("got success update %q after AutoConfigureDualBoot returned an error", u.StepName)
		}
	}

	if len(journal.Steps) != 1 {
		t.Fatalf("expected exactly 1 journal step, got %d", len(journal.Steps))
	}
	if journal.Steps[0].Status != safety.StepFailed {
		t.Fatalf("expected journal step status=%q, got %q", safety.StepFailed, journal.Steps[0].Status)
	}
}
