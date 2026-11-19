//go:build linux

package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"boot-orchestrator/hypervisor"
	"boot-orchestrator/safety"
)

// TestRunProvision_DryRunAgainstRealLoopDeviceNeverTouchesTheImage is
// audit follow-up G3's real dry-run proof: RunProvision — the actual
// top-level provisioning entry point, not a unit of it in isolation — is
// run for real under engine.SetDryRun(true) against a real, loop-mounted
// disk image, and the image file's own sha256 is asserted identical
// before and after. If any reachable step still wrote to the real
// backing file, this test would catch it directly (not infer it from a
// mocked call log), which is the strongest form of proof this project's
// own discipline asks for.
//
// What this proves, precisely: RunProvision's TargetDiskPath/TargetPartition
// point at a real block device backed by a real file; TargetPartition
// already exists as a real block device, so RunProvision takes the
// "reuse the already-carved partition" shortcut (engine/orchestrator.go,
// "Secondary disk or Virtual Machine partition carving") straight to
// image-deploy, skipping CarveHardwarePartitionAtBoundary's own
// complexity — the most direct real path to extractLiveRootFS available
// without needing a second, pre-shrunk host partition. The payload
// "download" is a real local-file copy (engine/streamer.go — deliberately
// not intercepted, see that file's doc comment) to keep this realistic.
//
// What this does NOT prove, stated plainly rather than implied: the run
// does not reach mkfs.ext4 or unsquashfs. It fails at "image-deploy",
// confirmed (not assumed) to be extractLiveRootFS's real >= 20GB
// size-floor check — the test's loop image is deliberately smaller than
// that, specifically so hashing the whole file before/after stays fast.
// Separately, and just as structurally, even a large-enough target would
// still stop short of mkfs.ext4 under dry-run: the loop-back
// "mount -o loop,ro" of the payload is itself faked (mount is a
// mutating-looking command intercepted like any other), so nothing would
// ever actually be accessible at the ISO mount point — extractLiveRootFS's
// own G1 fix (locateSquashfsOrErr) would report "unsupported ISO layout"
// and return before mkfs.ext4 regardless, not because the payload is
// really unsupported, but because dry-run cannot fake the SIDE EFFECT of
// a mount (making content appear at a path) the way it can fake a
// command's exit code. This is the same, already-documented "a step
// whose own success check depends on real output may correctly report
// failure in dry-run" limitation (docs/dryrun-inventory.md), just via a
// mount's side effect rather than a command's stdout. Either way,
// mkfs.ext4 is structurally unreachable under dry-run for this path —
// itself a safety property, just not evidence that deploy-phase logic
// beyond it was exercised.
func TestRunProvision_DryRunAgainstRealLoopDeviceNeverTouchesTheImage(t *testing.T) {
	requireLoopImageTools(t)
	if os.Geteuid() != 0 {
		t.Skip("losetup -P requires root; skipping under a normal, unprivileged `go test` run (e.g. `sudo go test ./engine/... -run TestRunProvision_DryRun`)")
	}

	dir := t.TempDir()
	imgPath := filepath.Join(dir, "disk.img")
	// Deliberately small (not the real >= 20GB extractLiveRootFS requires
	// for an actual install) so hashing the whole file before/after stays
	// fast — the full-file sha256 comparison below is the point of this
	// test, not exercising extractLiveRootFS's own size floor, which is a
	// real, separate, already-tested safety check unrelated to dry-run.
	mustRun(t, "truncate", "-s", "256M", imgPath)
	mustRun(t, "parted", "-s", imgPath, "mklabel", "gpt")
	mustRun(t, "parted", "-s", imgPath, "mkpart", "primary", "ext4", "1MiB", "100%")

	loopOut := mustRun(t, "losetup", "-f", "--show", "-P", imgPath)
	loopDev := strings.TrimSpace(loopOut)
	t.Cleanup(func() { _ = runQuiet("losetup", "-d", loopDev) })

	targetPart := loopDev + "p1"

	beforeSHA := fileSHA256(t, imgPath)

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
		TargetPartition:  targetPart,
		BootLabel:        "Test Linux Distro",
		HypervisorKind:   hypervisor.KindVMware,
		Credentials:      Credentials{Username: "janedoe", Password: "a-fine-password"},
	}

	var updates []string
	result := RunProvision(context.Background(), req, filepath.Join(dir, "grub-default"), func(u StepUpdate) {
		updates = append(updates, u.StepName)
	})
	t.Logf("RunProvision result: Success=%v FailedAtStep=%q OriginalError=%v", result.Success, result.FailedAtStep, result.OriginalError)
	t.Logf("step log:\n%s", strings.Join(updates, "\n"))

	afterSHA := fileSHA256(t, imgPath)
	if beforeSHA != afterSHA {
		t.Fatalf("disk image bytes changed under dry-run: before=%s after=%s\nstep log:\n%s", beforeSHA, afterSHA, strings.Join(updates, "\n"))
	}

	if result.Success {
		t.Fatalf("expected RunProvision to NOT report success under dry-run (the test's loop image is deliberately smaller than extractLiveRootFS's real >= 20GB floor) — a reported success here would be a false claim; step log:\n%s", strings.Join(updates, "\n"))
	}
	if result.FailedAtStep == "partition-shrink" || result.FailedAtStep == "image-download" {
		t.Fatalf("expected RunProvision to get past partition-shrink/image-download under dry-run (confirms diskReadRunner's fix), got FailedAtStep=%q: %v", result.FailedAtStep, result.OriginalError)
	}
	if result.FailedAtStep != "image-deploy" {
		t.Fatalf("expected RunProvision to fail at image-deploy (extractLiveRootFS's real >= 20GB size-floor check, documented above), got FailedAtStep=%q: %v\nstep log:\n%s", result.FailedAtStep, result.OriginalError, strings.Join(updates, "\n"))
	}
	if result.OriginalError == nil || !strings.Contains(result.OriginalError.Error(), "20 GB safety threshold") {
		t.Fatalf("expected the real 20GB safety-threshold error specifically (confirming WHY it stopped here, not just that it did), got: %v", result.OriginalError)
	}

	// Belt-and-suspenders: confirm the journal itself (a real file this
	// test's own safety package wrote) never recorded an mkfs.ext4 call
	// log entry anywhere, i.e. nothing downstream silently bypassed the
	// dry-run switch either.
	if j, jErr := safety.LoadJournal(req.JournalPath); jErr == nil {
		for _, step := range j.Steps {
			if strings.Contains(step.Name, "mkfs") {
				t.Fatalf("journal recorded an mkfs step under dry-run: %+v", step)
			}
		}
	}
}

// runQuiet runs name/args and discards the result — used only for
// best-effort cleanup where a failure is not itself a test failure.
func runQuiet(name string, args ...string) error {
	return execCommand(name, args...).Run()
}
