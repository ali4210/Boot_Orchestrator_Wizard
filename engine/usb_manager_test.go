package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withFakeLsblk puts a fake `lsblk` on PATH (ahead of the real one) that
// ignores every argument and prints jsonOutput to stdout. Used so
// ProbeRemovableTargets/ProbeUSBPayloads can be exercised for real, without
// depending on — or touching — whatever disks actually exist on the machine
// running the test.
func withFakeLsblk(t *testing.T, jsonOutput string) {
	t.Helper()
	binDir := t.TempDir()
	script := "#!/bin/sh\ncat <<'FAKE_LSBLK_EOF'\n" + jsonOutput + "\nFAKE_LSBLK_EOF\n"
	if err := os.WriteFile(filepath.Join(binDir, "lsblk"), []byte(script), 0755); err != nil {
		t.Fatalf("writing fake lsblk: %v", err)
	}
	origPath := os.Getenv("PATH")
	t.Cleanup(func() { os.Setenv("PATH", origPath) })
	os.Setenv("PATH", binDir+":"+origPath)
}

// fakeLsblkWithInternalSDC is a device list containing one internal SATA
// disk coincidentally named "sdc" (not removable, no "usb" transport) and
// one genuine USB disk. Regression fixture for the audit follow-up: a
// positional "sdc/sdd/sde" name guess must never classify the internal disk
// as USB.
const fakeLsblkWithInternalSDC = `{
  "blockdevices": [
    {"name": "sda", "size": "256G", "tran": "sata", "mountpoint": null, "fstype": null, "type": "disk", "rm": "0", "vendor": "ATA", "model": "InternalBoot"},
    {"name": "sdc", "size": "2T", "tran": "sata", "mountpoint": null, "fstype": null, "type": "disk", "rm": "0", "vendor": "ATA", "model": "InternalDataDrive"},
    {"name": "sdz", "size": "32G", "tran": "usb", "mountpoint": null, "fstype": null, "type": "disk", "rm": "1", "vendor": "SanDisk", "model": "Cruzer"}
  ]
}`

// TestProbeRemovableTargets_InternalSDCNotTreatedAsUSB is the regression
// test for the positional-name-guess removal: an internal, non-removable,
// non-USB disk that happens to be named "sdc" must never appear as a
// removable target, even though the pre-fix code treated any sdc/sdd/sde
// name as USB unconditionally.
func TestProbeRemovableTargets_InternalSDCNotTreatedAsUSB(t *testing.T) {
	withFakeLsblk(t, fakeLsblkWithInternalSDC)

	targets, err := ProbeRemovableTargets()
	if err != nil {
		t.Fatalf("ProbeRemovableTargets: %v", err)
	}

	for _, tgt := range targets {
		if tgt.Name == "sdc" {
			t.Fatalf("internal disk 'sdc' was wrongly classified as a removable USB target: %+v", targets)
		}
	}

	found := false
	for _, tgt := range targets {
		if tgt.Name == "sdz" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected the genuine USB disk 'sdz' (tran=usb, rm=1) to be classified as removable, got: %+v", targets)
	}
}

// writeSparsePayloadFile creates a sparse file of at least 50MB (the
// minimum ProbeUSBPayloads/addPayload requires to treat something as a
// real OS payload rather than a stray small file) without actually writing
// 50MB of data to disk.
func writeSparsePayloadFile(t *testing.T, path string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("creating sparse payload file: %v", err)
	}
	defer f.Close()
	if _, err := f.WriteAt([]byte{0}, 51*1024*1024); err != nil {
		t.Fatalf("writing sparse payload file: %v", err)
	}
}

// TestProbeUSBPayloads_InternalSDCNotScanned is the same regression for
// ProbeUSBPayloads' independent isUSBDisk check. Both disks' child
// partitions already have a Mountpoint set in the fixture, so the function
// never has to call the real `mount` command — it only walks each
// already-mounted path (here, a temp directory this test controls) looking
// for payload files.
func TestProbeUSBPayloads_InternalSDCNotScanned(t *testing.T) {
	internalMount := t.TempDir()
	writeSparsePayloadFile(t, filepath.Join(internalMount, "internal-data.iso"))

	usbMount := t.TempDir()
	writeSparsePayloadFile(t, filepath.Join(usbMount, "os_image.payload"))

	fixture := `{
  "blockdevices": [
    {"name": "sdc", "tran": "sata", "type": "disk", "rm": "0", "children": [
      {"name": "sdc1", "tran": "sata", "type": "part", "rm": "0", "fstype": "ext4", "mountpoint": "` + internalMount + `"}
    ]},
    {"name": "sdz", "tran": "usb", "type": "disk", "rm": "1", "children": [
      {"name": "sdz1", "tran": "usb", "type": "part", "rm": "1", "fstype": "ext4", "mountpoint": "` + usbMount + `"}
    ]}
  ]
}`
	withFakeLsblk(t, fixture)

	payloads, err := ProbeUSBPayloads()
	if err != nil {
		t.Fatalf("ProbeUSBPayloads: %v", err)
	}
	for _, p := range payloads {
		if strings.HasPrefix(p.DeviceName, "sdc") {
			t.Fatalf("internal disk 'sdc' was wrongly scanned as USB media: %+v", payloads)
		}
	}

	found := false
	for _, p := range payloads {
		if p.DeviceName == "sdz1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected the genuine USB partition's payload to still be found, got: %+v", payloads)
	}
}

// TestEfiBootNamesForArch_SelectsCorrectBinariesPerArch is the regression
// test for Phase 6 item 36: ArmAutonomousUSBBootloader used to hardcode
// x86_64 EFI binary names/GRUB target format unconditionally, silently
// producing a USB stick that could never boot on arm64 host firmware (a
// Raspberry Pi, an ARM server, Asahi Linux, ...) even though this project
// builds and runs fine there. Each architecture must get its own,
// correct set of names — never a mix, and never silently falling back to
// amd64's names for a real arm64 host.
func TestEfiBootNamesForArch_SelectsCorrectBinariesPerArch(t *testing.T) {
	shim, grub, mok, format := efiBootNamesForArch("amd64")
	if shim != "BOOTX64.EFI" || grub != "grubx64.efi" || mok != "mmx64.efi" || format != "x86_64-efi" {
		t.Fatalf("amd64: got shim=%q grub=%q mok=%q format=%q", shim, grub, mok, format)
	}

	shim, grub, mok, format = efiBootNamesForArch("arm64")
	if shim != "BOOTAA64.EFI" || grub != "grubaa64.efi" || mok != "mmaa64.efi" || format != "arm64-efi" {
		t.Fatalf("arm64: got shim=%q grub=%q mok=%q format=%q", shim, grub, mok, format)
	}

	// An unrecognized GOARCH must fall back to the amd64 names (the
	// common case for this tool's existing user base) rather than
	// panicking or returning empty/mixed values.
	shim, grub, mok, format = efiBootNamesForArch("riscv64")
	if shim != "BOOTX64.EFI" || grub != "grubx64.efi" || mok != "mmx64.efi" || format != "x86_64-efi" {
		t.Fatalf("unrecognized arch fallback: got shim=%q grub=%q mok=%q format=%q", shim, grub, mok, format)
	}
}

// TestGrubEFIArchSuffix_MatchesShimPackagingConventions proves the
// shim/mok filename suffix helper returns the exact suffixes Debian/
// Ubuntu's shim packaging actually uses for each architecture.
func TestGrubEFIArchSuffix_MatchesShimPackagingConventions(t *testing.T) {
	if got := grubEFIArchSuffix("x86_64-efi"); got != "x64" {
		t.Errorf("x86_64-efi: got %q, want %q", got, "x64")
	}
	if got := grubEFIArchSuffix("arm64-efi"); got != "aa64" {
		t.Errorf("arm64-efi: got %q, want %q", got, "aa64")
	}
}
