package engine

import "testing"

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
