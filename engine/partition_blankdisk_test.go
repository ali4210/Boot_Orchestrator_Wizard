//go:build linux

package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"boot-orchestrator/hypervisor"
)

// TestRunProvision_BlankUnlabelledDisk_RefusesPlainlyRealMode and its
// dry-run counterpart below are the regression tests for a bug reported
// after PR #2 merged: a completely blank target disk (a real
// `truncate -s 40G` + `losetup -P` loop device, never given a
// `parted mklabel` at all) made provisioning abort at partition-shrink
// with the raw, unhelpful "reading partition table ... exit status 1" —
// parted's own "unrecognised disk label" message buried inside it,
// without ever saying what that actually means or what to do about it.
//
// Investigated and decided deliberately, not assumed: this tool adds a
// second OS alongside an existing one (CarveHardwarePartitionAtBoundary
// shrinks or finds free space next to something already there) — a disk
// with no partition table at all has nothing to dual-boot alongside, so
// this path requires one to already exist, the same way
// RestoreHostFromBackup (engine/backup.go) is a deliberately SEPARATE
// feature for the one case this project does want to build a fresh GPT
// label on a disk from scratch: disaster-recovery restore onto a known
// blank replacement disk. Auto-creating a label here instead — treating
// parted's "unrecognised disk label" as license to silently wipe and
// relabel — would be wrong for the more dangerous reason, not just the
// less convenient one: the exact same parted message is also what a
// corrupted or unsupported-scheme table looks like, and this tool has no
// way to tell "genuinely blank" apart from "unknown, do not touch" from
// that message alone.
//
// Fixed in engine/partition.go: CarveHardwarePartitionAtBoundary now
// recognizes parted's "unrecognised disk label" output specifically and
// returns a plain, actionable error instead of the raw wrapped one.
// MANUAL_TEST_PLAN.md's headless-mode section now tells testers
// building a disposable test disk from scratch to run
// `parted <disk> mklabel gpt` first.
//
// Both tests below confirm the SAME fix, since diskReadRunner() (the
// read that produces this error) routes to the same real `parted` call
// in both real mode and dry-run — reported explicitly per the governing
// instruction's request to reproduce in both. Real mode additionally
// confirms the disk's bytes are genuinely untouched: the abort happens
// at the read, before any mutating parted/mkfs call is ever reached.
func TestRunProvision_BlankUnlabelledDisk_RefusesPlainlyRealMode(t *testing.T) {
	requireLoopImageTools(t)
	if os.Geteuid() != 0 {
		t.Skip("losetup -P requires root; skipping under a normal, unprivileged `go test` run")
	}

	loopDev, imgPath, cleanup := blankUnlabelledLoopDevice(t)
	defer cleanup()

	beforeSHA := sha256sumStreaming(t, imgPath)

	req := blankDiskProvisionRequest(t, loopDev)
	var updates []string
	result := RunProvision(context.Background(), req, filepath.Join(t.TempDir(), "grub-default"), func(u StepUpdate) {
		updates = append(updates, u.StepName)
	})
	stepLog := strings.Join(updates, "\n")

	if result.Success {
		t.Fatalf("expected RunProvision to refuse a disk with no partition table, got Success=true\nstep log:\n%s", stepLog)
	}
	if result.FailedAtStep != "partition-shrink" {
		t.Fatalf("expected FailedAtStep=\"partition-shrink\", got %q\nstep log:\n%s", result.FailedAtStep, stepLog)
	}
	assertPlainBlankDiskError(t, result.OriginalError, loopDev)

	afterSHA := sha256sumStreaming(t, imgPath)
	if beforeSHA != afterSHA {
		t.Errorf("disk image bytes changed even though provisioning refused before any mutating call: before=%s after=%s", beforeSHA, afterSHA)
	}
}

func TestRunProvision_BlankUnlabelledDisk_RefusesPlainlyDryRun(t *testing.T) {
	requireLoopImageTools(t)
	if os.Geteuid() != 0 {
		t.Skip("losetup -P requires root; skipping under a normal, unprivileged `go test` run")
	}

	loopDev, _, cleanup := blankUnlabelledLoopDevice(t)
	defer cleanup()

	restore := SetDryRun(true)
	defer restore()

	req := blankDiskProvisionRequest(t, loopDev)
	var updates []string
	result := RunProvision(context.Background(), req, filepath.Join(t.TempDir(), "grub-default"), func(u StepUpdate) {
		updates = append(updates, u.StepName)
	})
	stepLog := strings.Join(updates, "\n")

	// This is the same honest result as real mode, not a dry-run-only
	// quirk: diskReadRunner() (engine/dryrun.go) intentionally runs this
	// specific read for real even under --dry-run (see item 71's fix in
	// an earlier round), precisely so dry-run reports what the disk's
	// real state actually is instead of silently faking "looks fine."
	if result.Success {
		t.Fatalf("expected dry-run to also refuse a disk with no partition table (diskReadRunner reads this for real), got Success=true\nstep log:\n%s", stepLog)
	}
	if result.FailedAtStep != "partition-shrink" {
		t.Fatalf("expected FailedAtStep=\"partition-shrink\", got %q\nstep log:\n%s", result.FailedAtStep, stepLog)
	}
	assertPlainBlankDiskError(t, result.OriginalError, loopDev)
}

func blankUnlabelledLoopDevice(t *testing.T) (loopDev, imgPath string, cleanup func()) {
	t.Helper()
	dir := t.TempDir()
	imgPath = filepath.Join(dir, "disk.img")
	mustRun(t, "truncate", "-s", "40G", imgPath)
	// Deliberately no `parted ... mklabel` at all — a genuinely blank disk.

	loopOut := mustRun(t, "losetup", "-f", "--show", "-P", imgPath)
	loopDev = strings.TrimSpace(loopOut)
	return loopDev, imgPath, func() { _ = runQuiet("losetup", "-d", loopDev) }
}

func blankDiskProvisionRequest(t *testing.T, loopDev string) ProvisionRequest {
	t.Helper()
	dir := t.TempDir()
	payloadPath := filepath.Join(dir, "payload.iso")
	if err := os.WriteFile(payloadPath, []byte("fake-payload-bytes-not-a-real-iso"), 0644); err != nil {
		t.Fatalf("seeding fake payload: %v", err)
	}
	return ProvisionRequest{
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
}

func assertPlainBlankDiskError(t *testing.T, err error, loopDev string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected a non-nil error")
	}
	msg := err.Error()
	for _, want := range []string{loopDev + " has no partition table", "mklabel gpt"} {
		if !strings.Contains(msg, want) {
			t.Errorf("expected error to contain %q, got: %v", want, err)
		}
	}
}
