// Package engine — bcd_qemu.go generates a correct Windows BCD store by
// headlessly booting the install media's own WinPE (boot.wim) under
// QEMU+OVMF with the real target disk attached, and running the real
// Microsoft bcdboot.exe inside it against the real GPT partition layout.
// This avoids hand-crafting the BCD's binary device elements, which is
// fragile and easy to get silently wrong.
package engine

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// writeWindowsBCD is the entry point called from registerWindowsInGRUB.
// isoPath is the original Windows install ISO (or install.wim's parent ISO),
// targetDiskPath is the whole physical disk (e.g. /dev/sda), targetPart is
// the partition Windows was applied to (e.g. /dev/sda3).
func writeWindowsBCD(isoPath, targetDiskPath, targetPart string, logFn func(string)) error {
	if logFn == nil {
		logFn = func(string) {}
	}

	if err := ensureQEMUInstalled(logFn); err != nil {
		return fmt.Errorf("dependency resolution failed: %w", err)
	}
	if _, err := exec.LookPath("wimlib-imagex"); err != nil {
		return fmt.Errorf("wimlib-imagex required to stage WinPE autorun, not found on PATH")
	}

	_, targetPartNum, err := SplitDiskAndPartition(targetPart)
	if err != nil {
		return fmt.Errorf("parsing target partition number: %w", err)
	}

	// Do NOT search targetDiskPath for an ESP — in a multi-disk dual-boot
	// setup (Windows on one physical disk, Kali/GRUB's own ESP on another),
	// there is usually exactly one ESP, and it lives on whichever disk the
	// system actually boots from, not on the Windows target disk. Resolve
	// the real mounted host ESP device via findmnt instead.
	espMountSrc, err := exec.Command("findmnt", "-n", "-o", "SOURCE", "/boot/efi").Output()
	if err != nil || strings.TrimSpace(string(espMountSrc)) == "" {
		return fmt.Errorf("could not resolve host ESP device from /boot/efi mount: %w", err)
	}
	espDev := strings.TrimSpace(string(espMountSrc))
	_, espPartNum, err := SplitDiskAndPartition(espDev)
	if err != nil {
		return fmt.Errorf("parsing ESP partition number: %w", err)
	}

	ovmfCode, ovmfVarsTemplate, err := locateOVMF()
	if err != nil {
		return fmt.Errorf("locating OVMF UEFI firmware: %w", err)
	}

	stageDir, err := os.MkdirTemp("", "orch-winpe-stage-*")
	if err != nil {
		return fmt.Errorf("creating WinPE staging dir: %w", err)
	}
	defer os.RemoveAll(stageDir)

	isoMount := "/mnt/orch_bcd_iso_source"
	_ = os.MkdirAll(isoMount, 0755)
	_ = exec.Command("umount", "-l", isoMount).Run()
	if out, err := exec.Command("mount", "-o", "loop,ro", isoPath, isoMount).CombinedOutput(); err != nil {
		return fmt.Errorf("mounting install ISO for WinPE staging: %w (%s)", err, string(out))
	}
	defer exec.Command("umount", "-l", isoMount).Run()

	efiBootBin := filepath.Join(isoMount, "efi", "boot", "bootx64.efi")
	if _, err := os.Stat(efiBootBin); err != nil {
		return fmt.Errorf("install media has no UEFI boot loader at efi/boot/bootx64.efi, cannot boot WinPE under QEMU: %w", err)
	}

	logFn("=> [BCD ENGINE] Staging writable WinPE boot tree from install media...")
	if out, err := exec.Command("cp", "-r", isoMount+"/.", stageDir).CombinedOutput(); err != nil {
		return fmt.Errorf("copying ISO tree to staging dir: %w (%s)", err, string(out))
	}

	bootWimPath := filepath.Join(stageDir, "sources", "boot.wim")
	if _, err := os.Stat(bootWimPath); err != nil {
		return fmt.Errorf("sources/boot.wim not found on install media: %w", err)
	}
	_ = exec.Command("chmod", "+w", bootWimPath).Run()

	winpeIndex, err := findWinPEImageIndex(bootWimPath)
	if err != nil {
		return fmt.Errorf("identifying WinPE image index in boot.wim: %w", err)
	}

	varsPath := filepath.Join(stageDir, "OVMF_VARS.fd")
	if out, err := exec.Command("cp", ovmfVarsTemplate, varsPath).CombinedOutput(); err != nil {
		return fmt.Errorf("copying OVMF vars template: %w (%s)", err, string(out))
	}

	logFn("=> [BCD ENGINE] Launching headless UEFI WinPE to run bcdboot against real disk GUIDs (2-4 min)...")

	// Drive index bookkeeping: QEMU assigns WinPE's internal disk numbers
	// in the order drives are attached below. The WinPE boot media itself
	// (staged FAT dir) occupies disk 0. The Windows target disk is always
	// attached next, so it's disk 1. If the ESP lives on a separate
	// physical disk, it's attached last, becoming disk 2 — otherwise the
	// ESP partition is just another partition on disk 1.
	const winpeBootDiskNum = 0
	targetDiskNum := 1
	espDiskNum := 1

	espDiskParent, _, splitErr := SplitDiskAndPartition(espDev)
	separateEspDisk := splitErr == nil && espDiskParent != targetDiskPath
	if separateEspDisk {
		espDiskNum = 2
	}

	qemuArgs := []string{
		"-machine", "q35,accel=kvm:tcg",
		"-m", "3072",
		"-drive", fmt.Sprintf("if=pflash,format=raw,readonly=on,file=%s", ovmfCode),
		"-drive", fmt.Sprintf("if=pflash,format=raw,file=%s", varsPath),
		// Our modified WinPE, exposed as a plain FAT-formatted removable disk.
		"-drive", fmt.Sprintf("file=fat:rw:%s,format=raw,if=ide,media=disk", stageDir),
		// The real Windows target disk — WinPE ships native IDE/AHCI
		// support, so no extra drivers are needed (unlike virtio).
		"-drive", fmt.Sprintf("file=%s,format=raw,if=ide,cache=none", targetDiskPath),
	}

	if separateEspDisk {
		// ESP lives on a different physical disk than Windows (e.g. Windows
		// on a secondary 1TB drive, ESP/GRUB's boot disk is the NVMe) —
		// attach that disk too, or bcdboot has nothing to write the BCD to.
		logFn(fmt.Sprintf("=> [BCD ENGINE] ESP is on a separate physical disk (%s) — attaching alongside target disk...", espDiskParent))
		qemuArgs = append(qemuArgs, "-drive", fmt.Sprintf("file=%s,format=raw,if=ide,cache=none", espDiskParent))
	}

	qemuArgs = append(qemuArgs, "-nographic", "-serial", "mon:stdio", "-no-reboot")

	// Re-inject the runner now that we know the real disk numbers, rather
	// than assuming "disk 0" for everything.
	logFn(fmt.Sprintf("=> [BCD ENGINE] Injecting autonomous bcdboot runner into boot.wim (index %d)...", winpeIndex))
	if err := injectBCDBootRunner(bootWimPath, winpeIndex, targetDiskNum, targetPartNum, espDiskNum, espPartNum); err != nil {
		return fmt.Errorf("injecting WinPE autorun script: %w", err)
	}

	cmd := exec.Command("qemu-system-x86_64", qemuArgs...)

	qemuOut, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("qemu WinPE run failed: %w\n%s", err, string(qemuOut))
	}

	logFn("=> [BCD ENGINE] Verifying BCD store was written to host ESP...")
	verifyMount := "/mnt/orch_bcd_verify"
	_ = os.MkdirAll(verifyMount, 0755)
	_ = exec.Command("umount", "-l", verifyMount).Run()
	out, err := exec.Command("mount", espDev, verifyMount).CombinedOutput()
	if err != nil {
		return fmt.Errorf("mounting ESP %s for BCD verification: %w (%s)", espDev, err, string(out))
	}
	defer exec.Command("umount", "-l", verifyMount).Run()

	bcdPath := filepath.Join(verifyMount, "EFI", "Microsoft", "Boot", "BCD")
	if fi, statErr := os.Stat(bcdPath); statErr != nil || fi.Size() == 0 {
		return fmt.Errorf("BCD verification failed: %s not present after WinPE run", bcdPath)
	}

	logFn("=> [BCD ENGINE 100%] Windows BCD successfully generated against real partition GUIDs.")
	return nil
}

func ensureQEMUInstalled(logFn func(string)) error {
	if _, err := exec.LookPath("qemu-system-x86_64"); err == nil {
		return nil
	}
	logFn("=> [BCD ENGINE] qemu-system-x86_64 missing. Installing via host package manager...")
	if _, err := exec.LookPath("apt-get"); err == nil {
		_ = exec.Command("apt-get", "update", "-y").Run()
		if out, err := exec.Command("apt-get", "install", "-y", "qemu-system-x86", "ovmf").CombinedOutput(); err != nil {
			return fmt.Errorf("apt-get install qemu-system-x86 ovmf failed: %w (%s)", err, string(out))
		}
		return nil
	} else if _, err := exec.LookPath("dnf"); err == nil {
		if out, err := exec.Command("dnf", "install", "-y", "qemu-system-x86", "edk2-ovmf").CombinedOutput(); err != nil {
			return fmt.Errorf("dnf install qemu/ovmf failed: %w (%s)", err, string(out))
		}
		return nil
	} else if _, err := exec.LookPath("pacman"); err == nil {
		if out, err := exec.Command("pacman", "-Sy", "--noconfirm", "qemu-system-x86", "edk2-ovmf").CombinedOutput(); err != nil {
			return fmt.Errorf("pacman install qemu/ovmf failed: %w (%s)", err, string(out))
		}
		return nil
	}
	return errors.New("cannot resolve host package manager to autonomously install qemu-system-x86_64/OVMF")
}

func locateOVMF() (codePath, varsPath string, err error) {
	candidates := [][2]string{
		{"/usr/share/OVMF/OVMF_CODE_4M.fd", "/usr/share/OVMF/OVMF_VARS_4M.fd"},
		{"/usr/share/OVMF/OVMF_CODE.fd", "/usr/share/OVMF/OVMF_VARS.fd"},
		{"/usr/share/edk2/x64/OVMF_CODE.fd", "/usr/share/edk2/x64/OVMF_VARS.fd"},
		{"/usr/share/edk2-ovmf/x64/OVMF_CODE.fd", "/usr/share/edk2-ovmf/x64/OVMF_VARS.fd"},
		{"/usr/share/qemu/OVMF_CODE.fd", "/usr/share/qemu/OVMF_VARS.fd"},
	}
	for _, c := range candidates {
		if _, e1 := os.Stat(c[0]); e1 == nil {
			if _, e2 := os.Stat(c[1]); e2 == nil {
				return c[0], c[1], nil
			}
		}
	}
	return "", "", errors.New("OVMF_CODE.fd / OVMF_VARS.fd not found in standard locations after install")
}

func findWinPEImageIndex(bootWimPath string) (int, error) {
	indexes, err := ListWimIndexes(bootWimPath)
	if err != nil {
		return 0, err
	}
	if len(indexes) == 0 {
		return 0, errors.New("boot.wim contains no images")
	}
	for _, idx := range indexes {
		n := strings.ToLower(idx.Name)
		d := strings.ToLower(idx.Description)
		if strings.Contains(n, "windows pe") || strings.Contains(d, "windows pe") || strings.Contains(n, "winpe") {
			return idx.Index, nil
		}
	}
	// Retail install media typically ships Setup's WinPE as the last image.
	return indexes[len(indexes)-1].Index, nil
}

// injectBCDBootRunner overrides WinPE's normal auto-launch (which would run
// Setup's GUI) with a batch script that assigns drive letters by partition
// number and runs the real bcdboot.exe, then shuts the VM down.
func injectBCDBootRunner(bootWimPath string, imageIndex int, targetDiskNum int, targetPartNum string, espDiskNum int, espPartNum string) error {
	tmpDir, err := os.MkdirTemp("", "orch-winpe-inject-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	winpeshl := "[LaunchApps]\r\n\"cmd.exe\",\"/c X:\\bcdfix.cmd\"\r\n"
	winpeshlPath := filepath.Join(tmpDir, "winpeshl.ini")
	if err := os.WriteFile(winpeshlPath, []byte(winpeshl), 0644); err != nil {
		return err
	}

	bcdfix := fmt.Sprintf(`@echo off
(
echo select disk %d
echo select partition %s
echo assign letter=W
) > X:\dp_target.txt
diskpart /s X:\dp_target.txt

(
echo select disk %d
echo select partition %s
echo assign letter=S
) > X:\dp_esp.txt
diskpart /s X:\dp_esp.txt

bcdboot W:\Windows /s S: /f UEFI

wpeutil shutdown
`, targetDiskNum, targetPartNum, espDiskNum, espPartNum)
	bcdfixPath := filepath.Join(tmpDir, "bcdfix.cmd")
	if err := os.WriteFile(bcdfixPath, []byte(bcdfix), 0644); err != nil {
		return err
	}

	idxStr := strconv.Itoa(imageIndex)
	cmd := exec.Command("wimlib-imagex", "update", bootWimPath, idxStr,
		"--command", "add "+winpeshlPath+" /Windows/System32/winpeshl.ini",
		"--command", "add "+bcdfixPath+" /bcdfix.cmd",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("wimlib-imagex update failed: %w\n%s", err, string(out))
	}
	return nil
}
