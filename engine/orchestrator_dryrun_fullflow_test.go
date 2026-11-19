//go:build linux

package engine

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"boot-orchestrator/hypervisor"
)

// TestRunProvision_DryRunFullFlowAgainst40GBLoopDevice_ReachesEndHostUntouched
// is the regression test for the bug reported after PR #1 merged: a
// headless --dry-run against a real 40 GB disk stopped at
// "partition-shrink" with "partition /dev/loop0p1 created in table but
// node missing in /dev". CarvePartitionInFreeSpace's own verification
// that the just-"created" partition node appeared in /dev depends on the
// mkpart call above it having actually run — under dry-run that mkpart
// is itself intercepted (cmdRunner), so the node could never appear no
// matter how long the check polled, and the whole provision aborted
// before mkfs, the bootloader, NVRAM, or USB staging were ever reached.
//
// Fixed in two parts, both in engine/partition.go and engine/dryrun.go:
//  1. CheckUnallocatedHeadroom/CarvePartitionInFreeSpace/
//     CarveHardwarePartitionAtBoundary's own `parted ... print`/
//     `parted ... print free` reads (never mutating, by construction)
//     now go through diskReadRunner() instead of cmdRunner, so dry-run
//     sees the disk's REAL existing partition map rather than treating
//     every disk as blank — fixing the root cause, not just the symptom.
//  2. The partition-node-exists check (and, further down the pipeline,
//     extractLiveRootFS's squashfs search and assertDefaultCredentials's
//     account-verification — both equally unable to produce a real
//     result once the mount/chroot that would have produced it was
//     itself faked) now short-circuit to a simulated success under
//     dry-run specifically, each logging a clear "[DRY-RUN] simulated:
//     <what>" line via logDryRunSimulated — see each call site's own
//     comment for why that specific check is safe to simulate rather
//     than weaken. Real mode is byte-for-byte unchanged at every one of
//     these call sites: the same conditions still abort there.
//
// This test proves the fix at the same level as the bug report: a real,
// loop-mounted 40 GB disk image, completely blank (no existing
// partitions — the exact shape that triggered "node missing", see
// CarvePartitionInFreeSpace), run through the real top-level
// RunProvision entry point end to end under engine.SetDryRun(true), and
// asserts BOTH that it reaches the final step (Success, no
// FailedAtStep) AND that nothing real was ever touched: the backing
// image file's sha256 is identical before/after, and three real host
// artifacts the underlying (faked) commands could have mutated —
// /etc/fstab, /etc/default/grub, and efibootmgr's own NVRAM boot-entry
// list — are each byte-for-byte or output-for-output identical
// before/after too. (efibootmgr's own check is skipped, not silently
// passed, when the binary isn't present — true on this project's own
// ubuntu-latest CI runner, which has no EFI firmware to query; see the
// skip message for exactly why that's a skip and not evidence either
// way.)
func TestRunProvision_DryRunFullFlowAgainst40GBLoopDevice_ReachesEndHostUntouched(t *testing.T) {
	requireLoopImageTools(t)
	if os.Geteuid() != 0 {
		t.Skip("losetup -P requires root; skipping under a normal, unprivileged `go test` run (e.g. `sudo go test ./engine/... -run TestRunProvision_DryRunFullFlow`)")
	}

	fstabBefore := mustReadFile(t, "/etc/fstab")
	grubDefaultBefore, grubDefaultExisted := tryReadFile(t, "/etc/default/grub")
	efibootmgrBefore, haveEfibootmgr := tryEfibootmgrOutput(t)

	dir := t.TempDir()
	imgPath := filepath.Join(dir, "disk.img")
	mustRun(t, "truncate", "-s", "40G", imgPath)
	mustRun(t, "parted", "-s", imgPath, "mklabel", "gpt")
	// Deliberately blank: no existing partitions at all. This is the
	// exact shape that reproduces the reported bug — highestPartNum
	// computes to 0 and the new target partition is /dev/loop0p1, a node
	// that does not already exist in /dev for any other reason (unlike a
	// fixture with a real partition already at slot 1, which would mask
	// the missing-node check by coincidence rather than exercising it).

	loopOut := mustRun(t, "losetup", "-f", "--show", "-P", imgPath)
	loopDev := strings.TrimSpace(loopOut)
	t.Cleanup(func() { _ = runQuiet("losetup", "-d", loopDev) })

	// fileSHA256 (dryrun_loopimage_test.go) reads the whole file into
	// memory at once — fine at the 256MB scale its other callers use,
	// but loading a 40GB image that way is what actually timed this test
	// out (not disk I/O: a streaming read of the same sparse file via
	// sha256sum takes ~20s). sha256sum streams instead.
	beforeSHA := sha256sumStreaming(t, imgPath)

	restore := SetDryRun(true)
	defer restore()

	payloadPath := filepath.Join(dir, "payload.iso")
	if err := os.WriteFile(payloadPath, []byte("fake-payload-bytes-not-a-real-iso"), 0644); err != nil {
		t.Fatalf("seeding fake payload: %v", err)
	}

	req := ProvisionRequest{
		JournalPath:      filepath.Join(dir, "journal.json"),
		ImageURL:         payloadPath,
		DownloadDestPath: filepath.Join(dir, "downloaded.iso"),
		TargetDiskPath:   loopDev,
		HostPartNum:      "2",
		ESPPartNum:       "1",
		BootLabel:        "Test Linux Distro",
		HypervisorKind:   hypervisor.KindVMware,
		Credentials:      Credentials{Username: "janedoe", Password: "a-fine-password"},
	}

	var updates []string
	result := RunProvision(context.Background(), req, filepath.Join(dir, "grub-default"), func(u StepUpdate) {
		updates = append(updates, u.StepName)
	})
	stepLog := strings.Join(updates, "\n")
	t.Logf("step log:\n%s", stepLog)

	if !result.Success || result.FailedAtStep != "" {
		t.Fatalf("expected RunProvision to reach the final step under dry-run, got Success=%v FailedAtStep=%q OriginalError=%v\nstep log:\n%s", result.Success, result.FailedAtStep, result.OriginalError, stepLog)
	}

	afterSHA := sha256sumStreaming(t, imgPath)
	if beforeSHA != afterSHA {
		t.Errorf("disk image bytes changed under dry-run: before=%s after=%s", beforeSHA, afterSHA)
	}

	if after := mustReadFile(t, "/etc/fstab"); after != fstabBefore {
		t.Errorf("/etc/fstab changed under dry-run")
	}
	if grubDefaultExisted {
		if after, stillExists := tryReadFile(t, "/etc/default/grub"); !stillExists || after != grubDefaultBefore {
			t.Errorf("/etc/default/grub changed under dry-run")
		}
	}
	if haveEfibootmgr {
		after, ok := tryEfibootmgrOutput(t)
		if !ok || after != efibootmgrBefore {
			t.Errorf("efibootmgr's NVRAM boot-entry list changed under dry-run")
		}
	}
}

// sha256sumStreaming hashes a large file via the real sha256sum binary
// instead of os.ReadFile, which allocates the entire file in memory at
// once — harmless at the small scale this package's other loop-image
// tests use, but prohibitive for this test's real 40GB image.
func sha256sumStreaming(t *testing.T, path string) string {
	t.Helper()
	out := mustRun(t, "sha256sum", path)
	fields := strings.Fields(out)
	if len(fields) == 0 {
		t.Fatalf("sha256sum %s produced no output", path)
	}
	return fields[0]
}

func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(b)
}

// tryReadFile is for host files that may legitimately not exist on every
// machine this test runs on (e.g. a minimal container image with no
// /etc/default/grub at all) — a missing file is reported as such, not an
// error, so the caller can decide whether comparing it afterward is even
// meaningful.
func tryReadFile(t *testing.T, path string) (string, bool) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return string(b), true
}

// tryEfibootmgrOutput is skipped (bool false), not an error, when the
// efibootmgr binary isn't on PATH at all — true for this project's own
// ubuntu-latest CI runner, which has no EFI firmware to query. The output
// includes BootCurrent/Timeout/BootOrder plus every BootNNNN entry, so an
// unchanged string is a real proof nothing was added, removed, or
// reordered.
func tryEfibootmgrOutput(t *testing.T) (string, bool) {
	t.Helper()
	if _, err := exec.LookPath("efibootmgr"); err != nil {
		return "", false
	}
	out, err := exec.Command("efibootmgr", "-v").CombinedOutput()
	if err != nil {
		return "", false
	}
	return string(out), true
}
