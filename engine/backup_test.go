package engine

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

// withFakeFindmntAndLsblk puts fake `findmnt` and `lsblk` binaries first on
// PATH (never touches the real system's mounts or disks). findmntRules
// maps a mountpoint argument (e.g. "/", "/run/live/medium") to the SOURCE
// findmnt should report for it; any mountpoint not in the map causes the
// fake to exit 1 (not a mountpoint), matching real findmnt's behavior.
// lsblkJSON is the fixed JSON lsblk prints regardless of arguments.
func withFakeFindmntAndLsblk(t *testing.T, findmntRules map[string]string, lsblkJSON string) {
	t.Helper()
	binDir := t.TempDir()

	var script strings.Builder
	script.WriteString("#!/bin/sh\ntarget=\"$4\"\ncase \"$target\" in\n")
	for mnt, src := range findmntRules {
		script.WriteString("  \"" + mnt + "\") echo \"" + src + "\"; exit 0 ;;\n")
	}
	script.WriteString("  *) exit 1 ;;\nesac\n")
	writeFakeBin(t, binDir, "findmnt", script.String())
	writeFakeBin(t, binDir, "lsblk", "cat <<'FAKE_LSBLK_EOF'\n"+lsblkJSON+"\nFAKE_LSBLK_EOF\n")

	origPath := os.Getenv("PATH")
	t.Cleanup(func() { os.Setenv("PATH", origPath) })
	os.Setenv("PATH", binDir+":"+origPath)
}

// TestDetectBackupTargets_InternalSecondDiskNotTreatedAsRemovable is the
// regression test for the audit follow-up: DetectBackupTargets used to
// treat ANY non-root disk whose name merely started with "sd" as a
// removable backup-staging target (`strings.HasPrefix(dev.Name, "sd") &&
// dev.Name != pDiskBase`) — on a machine with more than one SATA/SAS disk,
// an internal secondary data disk would be silently offered as something
// safe to repartition into ext4+FAT32. It must now be classified removable
// only by lsblk's own RM or TRAN=="usb" signal.
func TestDetectBackupTargets_InternalSecondDiskNotTreatedAsRemovable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("withFakeLsblk's #!/bin/sh fake-binary mechanism only works on a real POSIX shell — unrelated to DetectBackupTargets's own correctness, which already degrades gracefully on Windows (clean wrapped error, no panic, when the real lsblk isn't found there); see TestDetectBackupTargets_NoLsblkOnPATH_ReturnsCleanErrorNotPanic, which runs on every platform")
	}
	// DetectActiveRootDisk (called internally) reads the real host's own
	// root mount — read-only, and irrelevant here since neither fixture
	// disk is named after whatever that happens to be on the test machine.
	fixture := `{
  "blockdevices": [
    {"name": "sdb", "size": 2000000000000, "type": "disk", "mountpoint": null, "model": "InternalDataDrive", "rm": false, "hotplug": false, "tran": "sata", "fstype": null},
    {"name": "sdz", "size": 32000000000, "type": "disk", "mountpoint": null, "model": "Cruzer", "rm": true, "hotplug": true, "tran": "usb", "fstype": null}
  ]
}`
	withFakeLsblk(t, fixture)

	targets, err := DetectBackupTargets()
	if err != nil {
		t.Fatalf("DetectBackupTargets: %v", err)
	}

	for _, tgt := range targets {
		if tgt.Name == "sdb" {
			t.Fatalf("internal disk 'sdb' (rm=false, tran=sata) was wrongly classified as a removable backup target: %+v", targets)
		}
	}

	found := false
	for _, tgt := range targets {
		if tgt.Name == "sdz" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected the genuine USB disk 'sdz' (rm=true, tran=usb) to be classified as removable, got: %+v", targets)
	}
}

// TestDetectBackupTargets_ExcludesLiveUSBBootDiskEvenOnOverlayRoot is the
// regression test for follow-up F9: when the tool is running from a live
// USB session, "/" is typically an overlay/aufs combining a read-only
// squashfs with a writable layer, NOT a direct partition of the boot USB
// — findmnt reports SOURCE "overlay" for "/", not a block device. Before
// DetectActiveRootDisk's live-boot fallback, that non-device rootDev fell
// through to the wrong default disk, so DetectBackupTargets's
// self-exclusion check never actually excluded the live USB — it would
// have been offered as a valid backup target to repartition, even though
// it's the disk the tool is currently running from.
func TestDetectBackupTargets_ExcludesLiveUSBBootDiskEvenOnOverlayRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("withFakeFindmntAndLsblk's #!/bin/sh fake-binary mechanism only works on a real POSIX shell — same reason as TestDetectBackupTargets_InternalSecondDiskNotTreatedAsRemovable above")
	}
	withFakeFindmntAndLsblk(t,
		map[string]string{
			"/":                "overlay",
			"/run/live/medium": "/dev/sdz1",
		},
		`{
  "blockdevices": [
    {"name": "sda", "size": 500000000000, "type": "disk", "mountpoint": null, "model": "InternalDataDrive", "rm": false, "hotplug": false, "tran": "sata", "fstype": null},
    {"name": "sdy", "size": 16000000000, "type": "disk", "mountpoint": null, "model": "OtherUSBStick", "rm": true, "hotplug": true, "tran": "usb", "fstype": null},
    {"name": "sdz", "size": 32000000000, "type": "disk", "mountpoint": null, "model": "LiveBootUSB", "rm": true, "hotplug": true, "tran": "usb", "fstype": null}
  ]
}`)

	targets, err := DetectBackupTargets()
	if err != nil {
		t.Fatalf("DetectBackupTargets: %v", err)
	}

	for _, tgt := range targets {
		if tgt.Name == "sdz" {
			t.Fatalf("the live-boot USB disk 'sdz' (the disk this tool is running from, via the overlay root's /run/live/medium) was wrongly offered as a backup target: %+v", targets)
		}
		if tgt.Name == "sda" {
			t.Fatalf("internal non-removable disk 'sda' was wrongly offered as a backup target: %+v", targets)
		}
	}

	found := false
	for _, tgt := range targets {
		if tgt.Name == "sdy" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected the other, genuinely pluggable USB disk 'sdy' to still be offered as a backup target, got: %+v", targets)
	}
}

// TestDetectBackupTargets_NoLsblkOnPATH_ReturnsCleanErrorNotPanic is the
// regression/reachability test for audit follow-up (Windows CI):
// DetectBackupTargets is reachable on a Windows host for real —
// ui/model.go's scanBackupDrivesCmd calls it unconditionally, with no
// runtime.GOOS guard, whenever the TUI's backup screen is opened — and
// Windows has no lsblk at all. Runs on every platform (not Windows-only):
// the point is proving DetectBackupTargets's own error handling is
// correct, which is a property worth checking everywhere, not simulating
// Windows specifically. Confirms the already-correct behavior (a clean,
// wrapped error, never a panic) rather than assuming it from reading the
// source — an empty PATH makes `lsblk` genuinely unresolvable on any
// platform, the same shape of failure as "lsblk not found" on Windows.
func TestDetectBackupTargets_NoLsblkOnPATH_ReturnsCleanErrorNotPanic(t *testing.T) {
	origPath := os.Getenv("PATH")
	t.Cleanup(func() { os.Setenv("PATH", origPath) })
	os.Setenv("PATH", t.TempDir()) // a real, empty directory — guaranteed no lsblk

	targets, err := DetectBackupTargets()
	if err == nil {
		t.Fatalf("expected a clean error when lsblk cannot be found, got nil (targets: %+v)", targets)
	}
	if targets != nil {
		t.Fatalf("expected nil targets alongside the error, got: %+v", targets)
	}
	if !strings.Contains(err.Error(), "querying storage topology") {
		t.Fatalf("expected the error to name what it was doing, got: %v", err)
	}
}

// TestRestoreHostFromBackup_AbortsOnPartitionTableFailure is the
// regression test for audit item 25 (silent-error audit): every
// parted/mkfs call that sets up the disaster-recovery target disk used to
// discard its error entirely. A nonexistent target disk makes parted's
// very first call (mklabel) fail immediately and harmlessly — no loop
// device or root privileges needed, since parted errors out before
// touching anything — and RestoreHostFromBackup must propagate that
// failure instead of sailing on to mkfs/mount/extract against partitions
// that were never actually created. Exercises both the UEFI (GPT) and
// BIOS (MBR) branches, since isUEFIBooted() depends on the real test
// machine's own firmware mode and this bug was identical in both.
func TestRestoreHostFromBackup_AbortsOnPartitionTableFailure(t *testing.T) {
	if _, err := exec.LookPath("parted"); err != nil {
		t.Skip("parted not available, skipping")
	}

	const bogusDisk = "/dev/orch-test-nonexistent-disk-for-backup-test-xyz"
	if _, statErr := os.Stat(bogusDisk); statErr == nil {
		t.Skipf("unexpectedly found a real device at %s, refusing to risk touching it", bogusDisk)
	}

	err := RestoreHostFromBackup("/nonexistent/archive-for-test.tar", bogusDisk, nil)
	if err == nil {
		t.Fatalf("expected RestoreHostFromBackup to fail when parted cannot create a partition table on a nonexistent disk, got nil error")
	}
	if !strings.Contains(err.Error(), "partition table") {
		t.Errorf("expected a partition-table-creation error message, got: %v", err)
	}
}

// TestRestoreHostFromBackup_DryRunNeverTouchesRealDiskAndSucceeds is the
// end-to-end regression test for item 45's closure of audit 2.4 (no real
// dry-run mode), using the exact same bogus-disk fixture as
// TestRestoreHostFromBackup_AbortsOnPartitionTableFailure above. In real
// mode, that fixture makes parted's very first call fail immediately
// (proven above). Under engine.SetDryRun(true), every exec.Command call
// in RestoreHostFromBackup's entire chain — partition, format, mount,
// extract, fstab, grub-install — is intercepted and reports synthetic
// success instead, so the function should sail all the way through to a
// nil error. If even one exec.Command call inside this function had been
// missed by item 45's migration, it would still try to run real parted
// against the nonexistent disk and fail with the same "partition table"
// error the real-mode test above expects — making this test a genuine,
// specific proof of full coverage for this function, not just a smoke
// test of the dry-run switch itself.
func TestRestoreHostFromBackup_DryRunNeverTouchesRealDiskAndSucceeds(t *testing.T) {
	restore := SetDryRun(true)
	defer restore()

	const bogusDisk = "/dev/orch-test-nonexistent-disk-for-backup-test-xyz"
	if _, statErr := os.Stat(bogusDisk); statErr == nil {
		t.Skipf("unexpectedly found a real device at %s, refusing to risk touching it", bogusDisk)
	}

	err := RestoreHostFromBackup("/nonexistent/archive-for-test.tar", bogusDisk, nil)
	if err != nil {
		t.Fatalf("expected RestoreHostFromBackup to report synthetic success under dry-run (every mutating call intercepted), got: %v", err)
	}

	if _, statErr := os.Stat(bogusDisk); statErr == nil {
		t.Fatalf("dry-run run somehow caused %s to exist afterward", bogusDisk)
	}
}

// TestBuildGrubReinstallScript_MatchesHostArchNotHardcodedAmd64 is the
// regression test for audit finding 2.6 (x86_64 hardcoded despite ARM64
// support), as it applies to disaster-recovery restore: before this fix,
// the UEFI grub-install invocation always used --target=x86_64-efi
// regardless of the restoring host's actual architecture. On real arm64
// host hardware that produces a restored system with an x86_64 GRUB
// payload the arm64 UEFI firmware cannot execute — a silent, unbootable
// restore, the exact failure class item 25 already fixed for ignored
// command errors but which this specific hardcoded string survived.
func TestBuildGrubReinstallScript_MatchesHostArchNotHardcodedAmd64(t *testing.T) {
	arm64Script := buildGrubReinstallScript("/dev/sda", true, "arm64")
	if strings.Contains(arm64Script, "x86_64-efi") {
		t.Errorf("arm64 host restore script must not target x86_64-efi, got:\n%s", arm64Script)
	}
	if !strings.Contains(arm64Script, "arm64-efi") {
		t.Errorf("arm64 host restore script must target arm64-efi, got:\n%s", arm64Script)
	}

	amd64Script := buildGrubReinstallScript("/dev/sda", true, "amd64")
	if !strings.Contains(amd64Script, "x86_64-efi") {
		t.Errorf("amd64 host restore script must target x86_64-efi, got:\n%s", amd64Script)
	}

	biosScript := buildGrubReinstallScript("/dev/sda", false, "arm64")
	if strings.Contains(biosScript, "--target=") {
		t.Errorf("BIOS (non-UEFI) restore must not pass --target= at all, got:\n%s", biosScript)
	}
}
