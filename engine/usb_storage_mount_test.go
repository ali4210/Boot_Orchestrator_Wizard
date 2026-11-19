package engine

import (
	"os/exec"
	"testing"
)

// TestEnsureUSBStorageMounted_RejectsOrdinaryDirectory is the regression
// test for audit bug G: an ordinary, unmounted directory (exactly what
// usbStorageDir is if both of the orchestrator's own mount attempts fail
// silently) must be rejected, not treated as if the USB storage partition
// were actually mounted there.
func TestEnsureUSBStorageMounted_RejectsOrdinaryDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := ensureUSBStorageMounted(dir); err == nil {
		t.Fatalf("expected an ordinary, unmounted directory to be rejected")
	}
}

// TestEnsureUSBStorageMounted_AcceptsARealMount proves the positive case
// with an actual (harmless, RAM-backed, never touching any real disk)
// tmpfs mount, so this isn't just testing "always returns an error".
func TestEnsureUSBStorageMounted_AcceptsARealMount(t *testing.T) {
	if _, err := exec.LookPath("mountpoint"); err != nil {
		t.Skip("mountpoint not available, skipping")
	}
	if exec.Command("sudo", "-n", "true").Run() != nil {
		t.Skip("passwordless sudo not available, skipping the real-mount case")
	}

	dir := t.TempDir()
	if out, err := exec.Command("sudo", "-n", "mount", "-t", "tmpfs", "tmpfs", dir).CombinedOutput(); err != nil {
		t.Skipf("could not create a tmpfs mount for this test: %v (%s)", err, out)
	}
	t.Cleanup(func() {
		_ = exec.Command("sudo", "-n", "umount", dir).Run()
	})

	if err := ensureUSBStorageMounted(dir); err != nil {
		t.Fatalf("expected a real tmpfs mount to be accepted, got: %v", err)
	}
}
