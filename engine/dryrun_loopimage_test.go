//go:build linux

package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fileSHA256 hashes path's current real content — used to prove a
// dry-run operation genuinely never wrote to a file, not just that it
// returned success.
func fileSHA256(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// TestBackupPartitionTable_WorksUnderDryRunAgainstRealLoopDevice is the
// end-to-end proof for diskReadRunner's fix (see dryrun.go), against a
// real loop-mounted disk image rather than a unit-level mock: with
// dry-run enabled, BackupPartitionTable must still read the real GPT
// bytes from a real block device (rather than erroring out on an empty,
// unparseable blockdev result, which is what made every single dry run
// abort at "partition-shrink" before this fix — see dryrun.go's
// diskReadRunner doc comment) and must not write anything to the backing
// image file in the process — checked via the image file's own sha256
// before and after, the same technique item 50's RunProvision-level
// proof uses.
func TestBackupPartitionTable_WorksUnderDryRunAgainstRealLoopDevice(t *testing.T) {
	requireLoopImageTools(t)
	if os.Geteuid() != 0 {
		t.Skip("losetup -P requires root; skipping under a normal, unprivileged `go test` run (e.g. `sudo go test ./engine/... -run TestBackupPartitionTable`)")
	}

	dir := t.TempDir()
	imgPath := filepath.Join(dir, "disk.img")
	mustRun(t, "truncate", "-s", "64M", imgPath)
	mustRun(t, "parted", "-s", imgPath, "mklabel", "gpt")
	mustRun(t, "parted", "-s", imgPath, "mkpart", "primary", "ext4", "1MiB", "100%")

	loopOut := mustRun(t, "losetup", "-f", "--show", "-P", imgPath)
	loopDev := strings.TrimSpace(loopOut)
	t.Cleanup(func() { _ = exec.Command("losetup", "-d", loopDev).Run() })

	beforeSHA := fileSHA256(t, imgPath)

	restore := SetDryRun(true)
	defer restore()

	backup, err := BackupPartitionTable(loopDev)
	if err != nil {
		t.Fatalf("expected BackupPartitionTable to succeed under dry-run against a real loop device, got: %v", err)
	}
	if !strings.Contains(string(backup.PrimaryBytes), "EFI PART") {
		t.Fatalf("expected the real GPT header signature (\"EFI PART\", at LBA 1 — LBA 0 is the protective MBR) to have been read for real, got %d bytes with none of that signature", len(backup.PrimaryBytes))
	}
	if backup.SectorSize != 512 {
		t.Fatalf("expected a real, plausible sector size (512), got %d — blockdev's real output was not actually used", backup.SectorSize)
	}

	afterSHA := fileSHA256(t, imgPath)
	if beforeSHA != afterSHA {
		t.Fatalf("disk image bytes changed from reading its partition table under dry-run: before=%s after=%s", beforeSHA, afterSHA)
	}
}
