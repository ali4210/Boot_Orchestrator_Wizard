package engine

import (
	"os"
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
