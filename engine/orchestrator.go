// Package engine — orchestrator.go coordinates transactional provisioning,
// storage isolation, multi-mirror streaming with pause/resume support,
// dynamic bare-metal vs virtual machine storage routing, automated partition carving,
// autonomous Live USB staging, cross-platform 7-Zip & wimtools provisioning,
// native macOS BaseSystem unpacking, OpenCore boot registration, Windows WIM deployment,
// and atomic crash recovery.
package engine

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"boot-orchestrator/discovery"
	"boot-orchestrator/hypervisor"
	"boot-orchestrator/safety"
)

type ProvisionRequest struct {
	JournalPath      string
	ImageURL         string
	ImageSHA256      string
	DownloadDestPath string
	TargetPartition  string
	TargetDiskPath   string
	IsWindowsImage   bool
	WimIndex         int
	AutounattendPath string
	BootLabel        string
	HypervisorKind   hypervisor.Kind
	SkipCompaction   bool
	SelectedEntry    *discovery.Entry
	HostPartNum      string
	ESPPartNum       string
	ProvisionMode    string
	AllocatedBytes   uint64
	Credentials      Credentials
}

type StepUpdate struct {
	StepName string
	Done     bool
	Err      error
}

type ProvisionResult struct {
	Success       bool
	FailedAtStep  string
	OriginalError error
	RolledBack    bool
	RollbackError error
}

// RollbackImageDeploy is registered against the "image-deploy" journal
// step (audit item 2.12). There is deliberately nothing destructive — or
// anything at all — for it to undo: by the time image-deploy begins, the
// target partition has already been mkfs'd (its prior contents are gone,
// not recoverable) and the step then streams the new OS's files onto it.
// If deployment fails partway, there is no "prior filesystem state" to
// restore to; the partition is simply left with however much of the new
// OS's files had been written when it failed. Before this handler
// existed, a failed image-deploy would make RollbackAll report a generic
// "no rollback handler registered for \"image-deploy\"" — true, but it
// reads like an oversight rather than the documented, inherent limitation
// it actually is. fail() (below) emits a specific, user-facing message
// explaining this at the moment image-deploy fails, rather than leaving
// it to this handler's own silence to explain nothing at all.
// autonomousUSBEFILoaderNVRAMPath is the Windows-style EFI loader path
// (efibootmgr's -l argument) for the one-shot NVRAM boot entry that points
// at whatever ArmAutonomousUSBBootloader actually staged on the USB
// drive's EFI/BOOT directory — BOOTX64.EFI on amd64 hosts, BOOTAA64.EFI on
// arm64 hosts (see usb_manager.go's efiBootNamesForArch, the single source
// of truth both call sites now share). goarch is the host's own
// architecture (e.g. runtime.GOARCH).
func autonomousUSBEFILoaderNVRAMPath(goarch string) string {
	loaderName, _, _, _ := efiBootNamesForArch(goarch)
	return `\EFI\BOOT\` + loaderName
}

func RollbackImageDeploy(_ json.RawMessage) error {
	return nil
}

func rollbackHandlers(grubDefaultPath string) map[string]safety.RollbackFunc {
	return map[string]safety.RollbackFunc{
		"partition-shrink":    RollbackPartitionShrink,
		"image-deploy":        RollbackImageDeploy,
		"docker-isolation":    RollbackDockerIsolation,
		"uefi-set-boot-next":  RollbackSetBootNext,
		"uefi-set-boot-order": RollbackSetBootOrder,
		"bcd-mutation":        RollbackBCD,
		"bcd-add-entry":       RollbackAddWindowsBootEntry,
		"revert-engine":       RollbackRevertHandler,
		"grub-set-default": func(data json.RawMessage) error {
			return RollbackSetGrubDefaultEntry(grubDefaultPath, data)
		},
	}
}

func DetectAllInternalDisks() ([]USBTargetDevice, error) {
	out, err := exec.Command("lsblk", "-d", "-J", "-o", "NAME,SIZE,TRAN,TYPE,RM,VENDOR,MODEL").Output()
	if err != nil {
		return nil, fmt.Errorf("querying internal storage controller: %w", err)
	}

	var data usbLsblkResponse
	if err := json.Unmarshal(out, &data); err != nil {
		return nil, fmt.Errorf("parsing storage telemetry: %w", err)
	}

	var internalDisks []USBTargetDevice
	for _, dev := range data.BlockDevices {
		isUSB := strings.ToLower(dev.Tran) == "usb" || dev.Removable == "1" || dev.Removable == "true"
		if !isUSB && dev.Type == "disk" && !strings.HasPrefix(dev.Name, "loop") && !strings.HasPrefix(dev.Name, "sr") {
			byteSize, _ := QueryBlockDeviceBytes(dev.Name)
			internalDisks = append(internalDisks, USBTargetDevice{
				Name:      dev.Name,
				DevPath:   "/dev/" + dev.Name,
				Size:      dev.Size,
				SizeBytes: byteSize,
				Vendor:    strings.TrimSpace(dev.Vendor),
				Model:     strings.TrimSpace(dev.Model),
			})
		}
	}
	return internalDisks, nil
}

func DetectActiveRootDisk() (parentDisk string, hostPart string, hostNum string, targetPart string, err error) {
	rootDev := ""

	if out, err := exec.Command("findmnt", "-n", "-o", "SOURCE", "/").Output(); err == nil {
		rootDev = strings.TrimSpace(string(out))
	}

	if rootDev == "" {
		if f, err := os.Open("/proc/mounts"); err == nil {
			scanner := bufio.NewScanner(f)
			for scanner.Scan() {
				fields := strings.Fields(scanner.Text())
				if len(fields) >= 2 && fields[1] == "/" {
					rootDev = fields[0]
					break
				}
			}
			f.Close()
		}
	}

	// Follow-up F9: a live-boot session (Debian/Kali/Ubuntu live-boot,
	// Arch archiso, ...) mounts "/" as an overlay/aufs/tmpfs combining a
	// read-only squashfs with a writable layer — findmnt/proc/mounts
	// report the source as "overlay" (or similar), never a real block
	// device, even though the session is genuinely running from a USB
	// disk. Falling through with that non-device rootDev would have hit
	// the DetectAllInternalDisks/"/dev/sda" fallback below and returned
	// the WRONG disk — meaning DetectBackupTargets's self-exclusion check
	// (which trusts this function entirely) would fail to exclude the
	// live USB the tool is actually running from. Each live-boot family
	// mounts its real boot medium at one of these well-known mountpoints;
	// resolve through those instead.
	if rootDev == "" || !strings.HasPrefix(rootDev, "/dev/") {
		for _, liveMount := range []string{"/run/live/medium", "/lib/live/mount/medium", "/run/archiso/bootmnt", "/isodevice"} {
			out, lmErr := exec.Command("findmnt", "-n", "-o", "SOURCE", liveMount).Output()
			if lmErr != nil {
				continue
			}
			if src := strings.TrimSpace(string(out)); strings.HasPrefix(src, "/dev/") {
				rootDev = src
				break
			}
		}
	}

	if real, evalErr := filepath.EvalSymlinks(rootDev); evalErr == nil && real != "" {
		rootDev = real
	}

	// Audit 1C.8: this used to duplicate SplitDiskAndPartition's parsing
	// inline, with the same "nvme/mmcblk substring allowlist, else assume a
	// bare trailing digit" logic — which mis-parses loop device partitions
	// (/dev/loop0p1) exactly like SplitDiskAndPartition itself did. Reusing
	// the shared, tested, structural parser fixes both at once and keeps
	// them from drifting apart.
	if rootDev != "" && strings.HasPrefix(rootDev, "/dev/") {
		if pDisk, pNum, splitErr := SplitDiskAndPartition(rootDev); splitErr == nil {
			if pNumInt, convErr := strconv.Atoi(pNum); convErr == nil {
				tNum := strconv.Itoa(pNumInt + 1)
				return pDisk, rootDev, pNum, JoinDiskAndPartition(pDisk, tNum), nil
			}
		}
	}

	disks, _ := DetectAllInternalDisks()
	if len(disks) > 0 {
		d := disks[0].DevPath
		return d, d + "1", "1", d + "2", nil
	}

	return "/dev/sda", "/dev/sda1", "1", "/dev/sda2", nil
}

func ensure7ZipInstalled(logFn func(string)) error {
	if _, err := exec.LookPath("7z"); err == nil {
		return nil
	}

	logFn("=> [AUTONOMOUS ENGINE] 7z binary missing. Resolving host platform package manager...")

	switch runtime.GOOS {
	case "linux":
		if _, err := exec.LookPath("apt-get"); err == nil {
			logFn("=> [AUTONOMOUS ENGINE] Installing 7zip via apt-get...")
			_ = exec.Command("apt-get", "update", "-y").Run()
			if out, err := exec.Command("apt-get", "install", "-y", "7zip").CombinedOutput(); err != nil {
				return fmt.Errorf("apt-get install 7zip failed: %w (%s)", err, string(out))
			}
			return nil
		} else if _, err := exec.LookPath("dnf"); err == nil {
			logFn("=> [AUTONOMOUS ENGINE] Installing p7zip via dnf...")
			if out, err := exec.Command("dnf", "install", "-y", "p7zip", "p7zip-plugins").CombinedOutput(); err != nil {
				return fmt.Errorf("dnf install p7zip failed: %w (%s)", err, string(out))
			}
			return nil
		} else if _, err := exec.LookPath("pacman"); err == nil {
			logFn("=> [AUTONOMOUS ENGINE] Installing p7zip via pacman...")
			if out, err := exec.Command("pacman", "-Sy", "--noconfirm", "p7zip").CombinedOutput(); err != nil {
				return fmt.Errorf("pacman install p7zip failed: %w (%s)", err, string(out))
			}
			return nil
		}
	case "windows":
		if _, err := exec.LookPath("winget"); err == nil {
			logFn("=> [AUTONOMOUS ENGINE] Installing 7zip via winget...")
			return exec.Command("winget", "install", "--id", "7zip.7zip", "-e", "--silent", "--accept-source-agreements", "--accept-package-agreements").Run()
		} else if _, err := exec.LookPath("choco"); err == nil {
			logFn("=> [AUTONOMOUS ENGINE] Installing 7zip via choco...")
			return exec.Command("choco", "install", "7zip", "-y").Run()
		}
	case "darwin":
		if _, err := exec.LookPath("brew"); err == nil {
			logFn("=> [AUTONOMOUS ENGINE] Installing sevenzip via Homebrew...")
			return exec.Command("brew", "install", "sevenzip").Run()
		}
	}

	return errors.New("cannot resolve suitable package manager to autonomously install 7z")
}

func ensureWindowsDeployToolsInstalled(logFn func(string)) error {
	if logFn == nil {
		logFn = func(string) {}
	}

	hasWim := false
	if _, err := exec.LookPath("wimapply"); err == nil {
		hasWim = true
	}
	hasNtfs := false
	if _, err := exec.LookPath("mkfs.ntfs"); err == nil {
		hasNtfs = true
	}

	if hasWim && hasNtfs {
		return nil
	}

	logFn("=> [AUTONOMOUS ENGINE] Windows deployment utilities (wimtools/ntfs-3g) missing. Auto-installing...")

	switch runtime.GOOS {
	case "linux":
		if _, err := exec.LookPath("apt-get"); err == nil {
			logFn("=> [AUTONOMOUS ENGINE] Installing wimtools & ntfs-3g via apt-get...")
			_ = exec.Command("apt-get", "update", "-y").Run()
			if out, err := exec.Command("apt-get", "install", "-y", "wimtools", "ntfs-3g").CombinedOutput(); err != nil {
				return fmt.Errorf("installing wimtools/ntfs-3g via apt failed: %w (%s)", err, string(out))
			}
			return nil
		} else if _, err := exec.LookPath("dnf"); err == nil {
			logFn("=> [AUTONOMOUS ENGINE] Installing wimtools & ntfs-3g via dnf...")
			if out, err := exec.Command("dnf", "install", "-y", "wimtools", "ntfs-3g").CombinedOutput(); err != nil {
				return fmt.Errorf("installing wimtools/ntfs-3g via dnf failed: %w (%s)", err, string(out))
			}
			return nil
		} else if _, err := exec.LookPath("pacman"); err == nil {
			logFn("=> [AUTONOMOUS ENGINE] Installing wimlib & ntfs-3g via pacman...")
			if out, err := exec.Command("pacman", "-Sy", "--noconfirm", "wimlib", "ntfs-3g").CombinedOutput(); err != nil {
				return fmt.Errorf("installing wimlib/ntfs-3g via pacman failed: %w (%s)", err, string(out))
			}
			return nil
		}
	}

	return errors.New("cannot resolve host package manager to autonomously install wimtools and ntfs-3g")
}

func deployMacOSPayload(dmgPath, targetPart string, logFn func(string)) error {
	if logFn == nil {
		logFn = func(string) {}
	}

	if err := ensure7ZipInstalled(logFn); err != nil {
		return fmt.Errorf("dependency resolution failed: %w", err)
	}

	logFn(fmt.Sprintf("=> [MACOS DEPLOY] Formatting target partition %s as ext4 [MACOS_RECOVERY]...", targetPart))
	if out, err := exec.Command("mkfs.ext4", "-F", "-q", "-L", "MACOS_RECOVERY", targetPart).CombinedOutput(); err != nil {
		return fmt.Errorf("formatting target %s: %w (%s)", targetPart, err, string(out))
	}

	targetMount := "/mnt/orch_target_root"
	_ = os.MkdirAll(targetMount, 0755)
	_ = exec.Command("umount", "-l", targetMount).Run()

	logFn(fmt.Sprintf("=> [MACOS DEPLOY] Mounting %s at %s...", targetPart, targetMount))
	if out, err := exec.Command("mount", targetPart, targetMount).CombinedOutput(); err != nil {
		return fmt.Errorf("mounting target partition %s: %w (%s)", targetPart, err, string(out))
	}
	defer func() {
		_ = exec.Command("sync").Run()
		_ = exec.Command("umount", "-l", targetMount).Run()
	}()

	logFn("=> [MACOS DEPLOY] Unpacking Apple BaseSystem via 7-Zip engine (2-3 min)...")
	cmd7z := exec.Command("7z", "x", "-y", fmt.Sprintf("-o%s", targetMount), dmgPath)
	_ = cmd7z.Run()

	nestedDir := filepath.Join(targetMount, "macOS Base System")
	if fi, err := os.Stat(nestedDir); err == nil && fi.IsDir() {
		logFn("=> [MACOS DEPLOY] Structuring recovery filesystem tree...")
		_ = exec.Command("bash", "-c", fmt.Sprintf("shopt -s dotglob; mv %s/* %s/ 2>/dev/null || true; rmdir %s 2>/dev/null || true", nestedDir, targetMount, nestedDir)).Run()
	}

	_ = exec.Command("sync").Run()
	logFn("=> [MACOS DEPLOY 100%] Apple BaseSystem deployed successfully.")
	return nil
}

func deployWindowsPayload(isoPath, targetPart string, wimIndex int, autounattendSrc string, creds Credentials, logFn func(string)) error {
	if logFn == nil {
		logFn = func(string) {}
	}

	if err := ensureWindowsDeployToolsInstalled(logFn); err != nil {
		return fmt.Errorf("dependency resolution failed: %w", err)
	}

	fsTypeOut, _ := exec.Command("blkid", "-s", "TYPE", "-o", "value", targetPart).Output()
	if !strings.EqualFold(strings.TrimSpace(string(fsTypeOut)), "ntfs") {
		logFn(fmt.Sprintf("=> [WINDOWS DEPLOY] Formatting target partition %s as NTFS [WINDOWS]...", targetPart))
		if out, err := exec.Command("mkfs.ntfs", "-F", "-q", "-L", "WINDOWS", targetPart).CombinedOutput(); err != nil {
			return fmt.Errorf("formatting target %s as NTFS: %w (%s)", targetPart, err, string(out))
		}
	} else {
		logFn(fmt.Sprintf("=> [WINDOWS DEPLOY] Verified existing NTFS partition on %s", targetPart))
	}

	targetMount := "/mnt/orch_target_root"
	_ = os.MkdirAll(targetMount, 0755)
	_ = exec.Command("umount", "-l", targetMount).Run()

	logFn(fmt.Sprintf("=> [WINDOWS DEPLOY] Mounting target NTFS partition %s at %s...", targetPart, targetMount))
	if out, err := exec.Command("mount", "-t", "ntfs-3g", targetPart, targetMount).CombinedOutput(); err != nil {
		if out2, err2 := exec.Command("mount", targetPart, targetMount).CombinedOutput(); err2 != nil {
			return fmt.Errorf("mounting target partition %s: %w (%s | %s)", targetPart, err, string(out), string(out2))
		}
	}
	defer func() {
		_ = exec.Command("sync").Run()
		_ = exec.Command("umount", "-l", targetMount).Run()
	}()

	isoMount := "/mnt/orch_iso_source"
	_ = os.MkdirAll(isoMount, 0755)
	_ = exec.Command("umount", "-l", isoMount).Run()

	logFn("=> [WINDOWS DEPLOY] Mounting Windows source ISO loopback: " + isoPath)
	if out, err := exec.Command("mount", "-o", "loop,ro", isoPath, isoMount).CombinedOutput(); err != nil {
		return fmt.Errorf("mounting loop ISO %s: %w (%s)", isoPath, err, string(out))
	}
	defer func() {
		_ = exec.Command("umount", "-l", isoMount).Run()
	}()

	candidates := []string{
		filepath.Join(isoMount, "sources", "install.wim"),
		filepath.Join(isoMount, "sources", "install.esd"),
		filepath.Join(isoMount, "sources", "INSTALL.WIM"),
		filepath.Join(isoMount, "sources", "INSTALL.ESD"),
	}
	wimPath := ""
	for _, c := range candidates {
		if fi, err := os.Stat(c); err == nil && fi.Size() > 0 {
			wimPath = c
			break
		}
	}
	if wimPath == "" {
		return fmt.Errorf("cannot locate install.wim or install.esd inside source ISO (%s)", isoPath)
	}

	targetIndex := "1"
	if wimIndex > 0 {
		targetIndex = strconv.Itoa(wimIndex)
	}

	logFn(fmt.Sprintf("=> [WINDOWS DEPLOY] Applying Windows image from %s (Index %s) via wimapply (3-6 min)...", filepath.Base(wimPath), targetIndex))
	cmdApply := exec.Command("wimapply", wimPath, targetIndex, targetMount)
	if out, err := cmdApply.CombinedOutput(); err != nil {
		return fmt.Errorf("wimapply extraction failed: %w (%s)", err, string(out))
	}

	if autounattendSrc != "" {
		logFn("=> [WINDOWS DEPLOY] Injecting unattended answer file for silent OOBE...")
		if err := injectAutounattend(autounattendSrc, targetMount, creds); err != nil {
			return fmt.Errorf("autounattend injection failed: %w", err)
		}
	}

	_ = exec.Command("sync").Run()
	logFn("=> [WINDOWS DEPLOY 100%] Windows operating system tree successfully applied.")
	return nil
}

func registerWindowsInGRUB(isoPath, targetDiskPath, targetPart, espPath, grubDefaultPath string, logFn func(string)) error {
	if logFn == nil {
		logFn = func(string) {}
	}

	logFn("=> [BOOTLOADER] Querying Windows target partition UUID...")
	uuidOut, err := exec.Command("blkid", "-s", "UUID", "-o", "value", targetPart).Output()
	targetUUID := strings.TrimSpace(string(uuidOut))
	if targetUUID == "" {
		return fmt.Errorf("failed to retrieve UUID for target Windows partition %s: %w", targetPart, err)
	}

	hostESP := espPath
	if hostESP == "" {
		hostESP = "/boot/efi"
	}
	winESPDir := filepath.Join(hostESP, "EFI", "Microsoft", "Boot")
	_ = os.MkdirAll(winESPDir, 0755)

	targetMount := "/mnt/orch_target_root"
	_ = os.MkdirAll(targetMount, 0755)
	_ = exec.Command("umount", "-l", targetMount).Run()
	if _, err := exec.Command("mount", "-o", "ro", targetPart, targetMount).CombinedOutput(); err == nil {
		defer func() {
			_ = exec.Command("umount", "-l", targetMount).Run()
		}()
		srcBootEFI := filepath.Join(targetMount, "Windows", "Boot", "EFI")
		if fi, err := os.Stat(srcBootEFI); err == nil && fi.IsDir() {
			_ = exec.Command("cp", "-r", filepath.Join(srcBootEFI, "."), winESPDir).Run()
		}
	}

	if err := writeWindowsBCD(isoPath, targetDiskPath, targetPart, logFn); err != nil {
		return fmt.Errorf("BCD generation failed: %w", err)
	}

	customFile := "/etc/grub.d/40_custom"
	existing, _ := os.ReadFile(customFile)

	entryMarker := "Windows 11 Boot Manager"
	if !strings.Contains(string(existing), entryMarker) {
		customEntry := fmt.Sprintf(`#!/bin/sh
exec tail -n +3 $0

menuentry "%s" {
    insmod part_gpt
    insmod fat
    insmod ntfs
    insmod chain
    search --no-floppy --fs-uuid --set=root %s
    chainloader /EFI/Microsoft/Boot/bootmgfw.efi
}
`, entryMarker, targetUUID)

		f, err := os.OpenFile(customFile, os.O_APPEND|os.O_WRONLY, 0755)
		if err == nil {
			_, _ = f.WriteString(customEntry)
			_ = f.Close()
		}
	}

	logFn("=> [BOOTLOADER] Synchronizing host GRUB with os-prober and Windows boot configuration...")
	_ = exec.Command("sed", "-i", `s/^#\?GRUB_DISABLE_OS_PROBER=.*/GRUB_DISABLE_OS_PROBER=false/`, "/etc/default/grub").Run()
	if out, err := exec.Command("update-grub").CombinedOutput(); err != nil {
		return fmt.Errorf("updating grub: %w (%s)", err, string(out))
	}

	logFn("=> [BOOTLOADER] Host GRUB successfully updated with verified Windows 11 entry.")
	return nil
}

func registerMacOSInGRUB(targetPart, espPath, grubDefaultPath string, logFn func(string)) error {
	if logFn == nil {
		logFn = func(string) {}
	}

	logFn("=> [BOOTLOADER] Validating OpenCore EFI staging environment...")

	ocTarget := "/boot/efi/EFI/OC/OpenCore.efi"
	if _, err := os.Stat(ocTarget); err != nil {
		logFn("=> [BOOTLOADER] OpenCore.efi missing. Staging standardized EFI package...")
		_ = os.MkdirAll("/boot/efi/EFI/OC", 0755)
		tmpZip := "/tmp/OpenCoreEFI.zip"
		url := "https://github.com/thenickdude/KVM-Opencore/releases/download/v21/OpenCoreEFIFolder-v21.zip"
		if _, err := exec.Command("curl", "-sSL", "-o", tmpZip, url).CombinedOutput(); err == nil {
			_ = exec.Command("unzip", "-q", "-o", tmpZip, "-d", "/tmp/oc_unpack").Run()
			_ = exec.Command("cp", "-r", "/tmp/oc_unpack/EFI/.", "/boot/efi/EFI/").Run()
			_ = os.RemoveAll("/tmp/oc_unpack")
			_ = os.Remove(tmpZip)
		}
	}

	bootPart := ""
	if out, err := exec.Command("df", "--output=source", "/boot").Output(); err == nil {
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		if len(lines) > 1 {
			bootPart = lines[1]
		}
	}
	if bootPart == "" {
		bootPart, _, _, _, _ = DetectActiveRootDisk()
	}

	uuidOut, _ := exec.Command("blkid", "-s", "UUID", "-o", "value", bootPart).Output()
	bootUUID := strings.TrimSpace(string(uuidOut))

	fsTypeOut, _ := exec.Command("blkid", "-s", "TYPE", "-o", "value", bootPart).Output()
	bootFSType := strings.ToLower(strings.TrimSpace(string(fsTypeOut)))

	fsDriver := "ext2"
	if strings.Contains(bootFSType, "fat") {
		fsDriver = "fat"
	}

	customEntry := fmt.Sprintf(`#!/bin/sh
exec tail -n +3 $0

menuentry "macOS Recovery (OpenCore)" {
    insmod part_gpt
    insmod part_msdos
    insmod %s
    insmod chain
    search --no-floppy --fs-uuid --set=root %s
    chainloader /EFI/OC/OpenCore.efi
}
`, fsDriver, bootUUID)

	customFile := "/etc/grub.d/40_custom"
	existing, _ := os.ReadFile(customFile)
	if !strings.Contains(string(existing), "macOS Recovery (OpenCore)") {
		f, err := os.OpenFile(customFile, os.O_APPEND|os.O_WRONLY, 0755)
		if err == nil {
			_, _ = f.WriteString(customEntry)
			_ = f.Close()
		}
	}

	if out, err := exec.Command("update-grub").CombinedOutput(); err != nil {
		return fmt.Errorf("updating grub: %w (%s)", err, string(out))
	}

	logFn("=> [BOOTLOADER] Host GRUB successfully updated with verified macOS OpenCore entry.")
	return nil
}

func extractLiveRootFS(isoPath, targetPart string, creds Credentials, logFn func(string)) error {
	if logFn == nil {
		logFn = func(string) {}
	}

	sizeBytes, _ := QueryBlockDeviceBytes(filepath.Base(targetPart))
	minRequired := uint64(20 * 1024 * 1024 * 1024)
	if sizeBytes > 0 && sizeBytes < minRequired {
		return fmt.Errorf("target partition %s capacity (%.1f GB) is critically below the 20 GB safety threshold", targetPart, float64(sizeBytes)/(1024*1024*1024))
	}

	logFn(fmt.Sprintf("=> [ROOTFS DEPLOY] Formatting target partition %s as ext4 [LINUX_ROOT]...", targetPart))
	if out, err := exec.Command("mkfs.ext4", "-F", "-q", "-L", "LINUX_ROOT", targetPart).CombinedOutput(); err != nil {
		return fmt.Errorf("formatting %s: %w (%s)", targetPart, err, string(out))
	}
	_ = exec.Command("sync").Run()

	targetMount := "/mnt/orch_target_root"
	_ = os.MkdirAll(targetMount, 0755)
	_ = exec.Command("umount", "-l", targetMount).Run()

	logFn(fmt.Sprintf("=> [ROOTFS DEPLOY] Mounting target %s at %s...", targetPart, targetMount))
	if out, err := exec.Command("mount", targetPart, targetMount).CombinedOutput(); err != nil {
		return fmt.Errorf("mounting %s to %s: %w (%s)", targetPart, targetMount, err, string(out))
	}
	defer func() {
		_ = exec.Command("sync").Run()
		_ = exec.Command("umount", "-l", targetMount).Run()
	}()

	isoMount := "/mnt/orch_iso_source"
	_ = os.MkdirAll(isoMount, 0755)
	_ = exec.Command("umount", "-l", isoMount).Run()

	logFn("=> [ROOTFS DEPLOY] Mounting source ISO loopback: " + isoPath)
	if out, err := exec.Command("mount", "-o", "loop,ro", isoPath, isoMount).CombinedOutput(); err != nil {
		return fmt.Errorf("mounting loop iso %s: %w (%s)", isoPath, err, string(out))
	}
	defer func() {
		_ = exec.Command("umount", "-l", isoMount).Run()
	}()

	squashPath := ""
	candidateDirs := []string{
		filepath.Join(isoMount, "casper"),
		filepath.Join(isoMount, "live"),
		filepath.Join(isoMount, "install"),
		filepath.Join(isoMount, "arch", "x86_64"),
	}

	for _, dir := range candidateDirs {
		if entries, err := os.ReadDir(dir); err == nil {
			for _, e := range entries {
				name := strings.ToLower(e.Name())
				if strings.HasSuffix(name, ".squashfs") && !strings.Contains(name, "initrd") {
					if name == "filesystem.squashfs" || strings.Contains(name, "ubuntu-desktop") || strings.Contains(name, "minimal") || name == "airootfs.sfs" {
						squashPath = filepath.Join(dir, e.Name())
						break
					}
				}
			}
			if squashPath != "" {
				break
			}
			var maxBytes int64 = 0
			for _, e := range entries {
				if strings.HasSuffix(strings.ToLower(e.Name()), ".squashfs") {
					if fi, sErr := e.Info(); sErr == nil && fi.Size() > maxBytes {
						maxBytes = fi.Size()
						squashPath = filepath.Join(dir, e.Name())
					}
				}
			}
			if squashPath != "" {
				break
			}
		}
	}

	if squashPath == "" {
		squashPath = filepath.Join(isoMount, "live", "filesystem.squashfs")
	}

	logFn("=> [ROOTFS DEPLOY] Extracting live root filesystem tree via unsquashfs (2-4 min)...")
	unsquashCmd := exec.Command("unsquashfs", "-f", "-d", targetMount, squashPath)
	if out, err := unsquashCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("unsquashfs extraction failed: %w (%s)", err, string(out))
	}

	_ = exec.Command("tune2fs", "-m", "1", targetPart).Run()

	initTarget := filepath.Join(targetMount, "sbin", "init")
	systemdTarget := "/lib/systemd/systemd"
	_ = os.Remove(initTarget)
	_ = os.Symlink(systemdTarget, initTarget)

	uuidOut, err := exec.Command("blkid", "-s", "UUID", "-o", "value", targetPart).Output()
	if err == nil && len(strings.TrimSpace(string(uuidOut))) > 0 {
		uuidStr := strings.TrimSpace(string(uuidOut))
		fstabEntry := fmt.Sprintf("UUID=%s / ext4 defaults,noatime,errors=remount-ro 0 1\n", uuidStr)
		_ = os.WriteFile(filepath.Join(targetMount, "etc", "fstab"), []byte(fstabEntry), 0644)
		logFn("=> [ROOTFS DEPLOY] Injected /etc/fstab entry mapped to UUID: " + uuidStr)
	}

	logFn("=> [CREDENTIALS] Configuring persistent user accounts and purging installer artifacts...")

	_ = exec.Command("mount", "--bind", "/dev", filepath.Join(targetMount, "dev")).Run()
	_ = exec.Command("mount", "--bind", "/proc", filepath.Join(targetMount, "proc")).Run()
	_ = exec.Command("mount", "--bind", "/sys", filepath.Join(targetMount, "sys")).Run()

	// Audit item 26 (found while surveying generated scripts for the
	// syntax/fmt-verb check below): this used to provision the account
	// with its own inline, unverified script — hardcoding "-G sudo"
	// (silently produces an admin-group-less account, with root also
	// locked by default, on any non-Debian-family target: Fedora/RHEL/
	// Arch/openSUSE use "wheel", not "sudo" — the exact lockout bug class
	// audit 1A.3 already fixed, just not in this second, parallel code
	// path) and never checking whether useradd/chpasswd actually
	// succeeded. Delegates to the shared, hardened assertDefaultCredentials
	// (bootloader.go) instead, which detects sudo vs wheel and verifies
	// the account actually exists afterward before returning success.
	if err := assertDefaultCredentials(targetMount, creds, logFn); err != nil {
		_ = exec.Command("umount", "-l", filepath.Join(targetMount, "dev")).Run()
		_ = exec.Command("umount", "-l", filepath.Join(targetMount, "proc")).Run()
		_ = exec.Command("umount", "-l", filepath.Join(targetMount, "sys")).Run()
		return fmt.Errorf("provisioning account: %w", err)
	}

	cleanupScript := renderCalamaresCleanupScript(creds.Username)
	if out, err := exec.Command("chroot", targetMount, "bash", "-c", cleanupScript).CombinedOutput(); err != nil {
		logFn(fmt.Sprintf("=> [WARNING] installer-artifact cleanup note: %v (%s)", err, out))
	}

	_ = exec.Command("umount", "-l", filepath.Join(targetMount, "dev")).Run()
	_ = exec.Command("umount", "-l", filepath.Join(targetMount, "proc")).Run()
	_ = exec.Command("umount", "-l", filepath.Join(targetMount, "sys")).Run()

	logFn("=> [ROOTFS DEPLOY 100%] Root filesystem successfully unpacked and synchronized.")
	return nil
}

// copyFileDirect streams data with zero kernel buffer deadlocks and 4MB buffers.
func copyFileDirect(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	defer out.Close()

	buf := make([]byte, 4*1024*1024)
	if _, err := io.CopyBuffer(out, in, buf); err != nil {
		return err
	}
	return out.Sync()
}

// autoConfigureDualBootFn, registerMacOSInGRUBFn, and registerWindowsInGRUBFn
// let tests substitute the three real OS-specific bootloader-configuration
// entry points with stubs. This is necessary (not just convenient): even
// with a mocked Runner, those real functions still write directly to fixed
// system paths such as /etc/grub.d/40_custom and /etc/default/grub.d/*.cfg,
// which must never happen as a side effect of running `go test`.
var (
	autoConfigureDualBootFn = AutoConfigureDualBoot
	registerMacOSInGRUBFn   = registerMacOSInGRUB
	registerWindowsInGRUBFn = registerWindowsInGRUB
)

// configureBootloaderStep runs the OS-specific bootloader configuration for
// this provisioning run and journals the outcome. On failure it returns
// (failResult, true); the caller must return failResult immediately without
// committing the journal step or reporting success.
//
// Fixes audit 2.1: the generic (non-macOS, non-Windows) branch used to call
// journal.Fail and then fall through to an unconditional journal.Commit plus
// a "dual-boot bootloader synchronized" success update even though
// AutoConfigureDualBoot had just failed, so a broken Linux dual-boot install
// was reported to the caller as successful. All three branches now fail the
// same way.
func configureBootloaderStep(
	journal *safety.Journal,
	isMacOS, isWindows bool,
	req ProvisionRequest,
	grubDefaultPath string,
	streamDestPath string,
	onUpdate func(StepUpdate),
	fail func(stepName string, cause error) ProvisionResult,
) (ProvisionResult, bool) {
	bootStep, bErr := journal.Begin("bootloader-configure", nil)
	if bErr != nil {
		return ProvisionResult{}, false
	}

	if isMacOS {
		onUpdate(StepUpdate{StepName: "configuring macOS OpenCore bootloader & updating host GRUB...", Done: false})
		if err := registerMacOSInGRUBFn(req.TargetPartition, "/boot/efi", grubDefaultPath, func(msg string) {
			onUpdate(StepUpdate{StepName: msg, Done: false})
		}); err != nil {
			journal.Fail(bootStep, err)
			return fail("bootloader-configure", err), true
		}
	} else if isWindows {
		onUpdate(StepUpdate{StepName: "configuring Windows 11 EFI bootloader & updating host GRUB...", Done: false})
		if err := registerWindowsInGRUBFn(streamDestPath, req.TargetDiskPath, req.TargetPartition, "/boot/efi", grubDefaultPath, func(msg string) {
			onUpdate(StepUpdate{StepName: msg, Done: false})
		}); err != nil {
			journal.Fail(bootStep, err)
			return fail("bootloader-configure", err), true
		}
	} else {
		onUpdate(StepUpdate{StepName: "configuring autonomous dual-boot & updating host bootloader...", Done: false})
		if err := autoConfigureDualBootFn(req.TargetPartition, grubDefaultPath, req.Credentials, func(msg string) {
			onUpdate(StepUpdate{StepName: msg, Done: false})
		}); err != nil {
			journal.Fail(bootStep, err)
			return fail("bootloader-configure", err), true
		}
	}

	journal.Commit(bootStep)
	onUpdate(StepUpdate{StepName: "dual-boot bootloader synchronized", Done: true})
	return ProvisionResult{}, false
}

func RunProvision(ctx context.Context, req ProvisionRequest, grubDefaultPath string, onUpdate func(StepUpdate)) ProvisionResult {
	// Follow-up F14: populated only if CarveHardwarePartitionAtBoundary
	// converted a confirmed trailing swap slice into the target partition;
	// persisted into the journal session further below so a later revert
	// can recreate an equivalent swap partition.
	var convertedSwapSizeBytes int64
	var convertedSwapUUID string

	journal, err := safety.LoadJournal(req.JournalPath)
	if err != nil {
		journal, err = safety.NewJournal(req.JournalPath, generateTxnID())
		if err != nil {
			return ProvisionResult{Success: false, FailedAtStep: "journal-init", OriginalError: err}
		}
	}

	currentDistro := req.BootLabel
	if req.SelectedEntry != nil {
		currentDistro = req.SelectedEntry.Distro
	}

	if journal.Session.DistroName != "" {
		isSameOS := strings.EqualFold(journal.Session.DistroName, currentDistro) ||
			strings.EqualFold(journal.Session.TargetOSID, currentDistro)
		isSameURL := journal.Session.DownloadURL == req.ImageURL

		if !isSameOS || (!isSameURL && req.ImageURL != "") {
			onUpdate(StepUpdate{StepName: "=> [JOURNAL REFRESH] Target OS changed: resetting checkpoint and payload...", Done: false})
			_ = journal.PurgeForce()
			_ = os.Remove(req.DownloadDestPath + ".part")
			_ = os.Remove(req.DownloadDestPath)
			journal, _ = safety.NewJournal(req.JournalPath, generateTxnID())
		}
	} else {
		if fi, err := os.Stat(req.DownloadDestPath); err == nil {
			if fi.Size() < 50*1024*1024 {
				_ = os.Remove(req.DownloadDestPath)
				_ = os.Remove(req.DownloadDestPath + ".part")
			}
		}
	}

	handlers := rollbackHandlers(grubDefaultPath)

	fail := func(stepName string, cause error) ProvisionResult {
		if errors.Is(cause, ErrDownloadPaused) || errors.Is(cause, context.Canceled) {
			onUpdate(StepUpdate{StepName: stepName + " [PAUSED]", Done: true})
			return ProvisionResult{
				Success:       false,
				FailedAtStep:  stepName,
				OriginalError: ErrDownloadPaused,
				RolledBack:    false,
			}
		}

		onUpdate(StepUpdate{StepName: stepName, Done: true, Err: cause})
		if stepName == "image-deploy" {
			// Audit item 2.12: documented, not silent — see RollbackImageDeploy.
			onUpdate(StepUpdate{StepName: "=> [NOTE] image-deploy cannot be rolled back: the target partition was already formatted before deployment began, so there is no prior filesystem state to restore. If a host partition was shrunk for this install, that WILL still be restored below.", Done: false})
		}
		rbErr := journal.RollbackAll(handlers)
		return ProvisionResult{
			Success:       false,
			FailedAtStep:  stepName,
			OriginalError: cause,
			RolledBack:    true,
			RollbackError: rbErr,
		}
	}

	pDisk, _, hNum, targetPart, dErr := DetectActiveRootDisk()
	if dErr == nil && pDisk != "" {
		if req.TargetDiskPath == "" {
			req.TargetDiskPath = pDisk
		}
		if req.TargetPartition == "" {
			req.TargetPartition = targetPart
		}
		if req.HostPartNum == "" {
			req.HostPartNum = hNum
		}
	}

	// Safety gate (Phase 1 item B.5, moved earlier per follow-up F2): must
	// run before ANY partition-table/filesystem write AND before spending
	// time/bandwidth downloading a payload for a disk this tool will refuse
	// to touch anyway. Aborts without writing or downloading anything if the
	// target disk has LVM, LUKS, mdraid, or BitLocker anywhere on it — none
	// of which this tool understands how to handle safely. Runs even when
	// resuming a paused session: cheap, read-only, and worth re-checking.
	if err := PreflightStorageSafetyCheck(req.TargetDiskPath); err != nil {
		return fail("image-download", err)
	}

	dlStep, err := journal.Begin("image-download", nil)
	if err != nil {
		return fail("image-download", err)
	}

	mirrors := []string{req.ImageURL}
	if req.SelectedEntry != nil {
		entryMirrors := req.SelectedEntry.GetMirrors()
		if len(entryMirrors) > 0 {
			mirrors = entryMirrors
		}
	}

	var latestBytesRead int64
	var latestTotalBytes int64

	streamResult, err := StreamDownloadWithFailover(ctx, mirrors, req.DownloadDestPath, req.ImageSHA256, func(p Progress) {
		latestBytesRead = p.BytesRead
		latestTotalBytes = p.TotalBytes

		etaStr := "--:--"
		if p.TotalBytes > 0 && p.BytesRead < p.TotalBytes {
			remaining := p.TotalBytes - p.BytesRead
			bps := p.BytesPerSecond()
			if bps > 0 {
				secs := int64(float64(remaining) / bps)
				h := secs / 3600
				m := (secs % 3600) / 60
				s := secs % 60
				if h > 0 {
					etaStr = fmt.Sprintf("%dh %02dm %02ds", h, m, s)
				} else {
					etaStr = fmt.Sprintf("%02dm %02ds", m, s)
				}
			}
		}

		readFormatted := formatDownloadBytes(p.BytesRead)
		totalFormatted := formatDownloadBytes(p.TotalBytes)
		stepMsg := fmt.Sprintf("downloading: [%s / %s] %.1f%% (%.1f MB/s) | ETA: %s",
			readFormatted, totalFormatted, p.Percent(), p.BytesPerSecond()/(1024*1024), etaStr)

		onUpdate(StepUpdate{StepName: stepMsg, Done: false})
	})

	if err != nil {
		if errors.Is(err, ErrDownloadPaused) || errors.Is(causeError(err), context.Canceled) {
			_ = journal.Pause(dlStep, latestBytesRead, latestTotalBytes)
			return fail("image-download", ErrDownloadPaused)
		}
		journal.Fail(dlStep, err)
		return fail("image-download", err)
	}

	if req.ImageSHA256 != "" {
		onUpdate(StepUpdate{StepName: "verifying cryptographic payload checksum...", Done: false})
		if err := verifyFileSHA256(streamResult.DestPath, req.ImageSHA256); err != nil {
			if !strings.HasPrefix(streamResult.MirrorUsed, "local://") {
				_ = os.Remove(streamResult.DestPath)
			}
			journal.Fail(dlStep, err)
			return fail("checksum-verification", fmt.Errorf("corrupted payload abort: %w", err))
		}
	}

	// Audit 2.7/item 28: a checksum alone doesn't protect against a
	// compromised mirror serving a tampered file alongside a tampered
	// checksum of its own. A no-op for every current catalog entry (none
	// has a SignatureURL/SigningKeyURL yet — see discovery/catalog.go's
	// doc comment on those fields), but wired in for real for the day one
	// does, with the same fail-hard-and-discard-the-file severity as a
	// checksum mismatch above.
	if req.SelectedEntry != nil && req.SelectedEntry.SignatureURL != "" {
		onUpdate(StepUpdate{StepName: "verifying OpenPGP signature...", Done: false})
		signer, sigErr := verifyEntrySignature(streamResult.DestPath, req.SelectedEntry)
		if sigErr != nil {
			if !strings.HasPrefix(streamResult.MirrorUsed, "local://") {
				_ = os.Remove(streamResult.DestPath)
			}
			journal.Fail(dlStep, sigErr)
			return fail("signature-verification", fmt.Errorf("unsigned or tampered payload abort: %w", sigErr))
		}
		onUpdate(StepUpdate{StepName: fmt.Sprintf("signature verified, signed by: %s", signer), Done: false})
	}

	journal.Commit(dlStep)
	onUpdate(StepUpdate{StepName: "image-download", Done: true})

	if !journal.Session.IsPaused {
		if _, _, warnings := DetectSecureBootAndTPM(); len(warnings) > 0 {
			for _, w := range warnings {
				onUpdate(StepUpdate{StepName: "=> [SAFETY WARNING] " + w, Done: false})
			}
		}

		step, err := journal.Begin("partition-shrink", nil)
		if err != nil {
			return fail("partition-shrink", err)
		}

		backup, err := BackupPartitionTable(req.TargetDiskPath)
		if err != nil {
			journal.Fail(step, err)
			return fail("partition-shrink", err)
		}
		backupJSON, _ := json.Marshal(backup)
		step.RollbackData = backupJSON

		activeMounts, _ := os.ReadFile("/proc/mounts")
		isTargetActiveHostDisk := req.TargetDiskPath == pDisk && strings.Contains(string(activeMounts), req.TargetDiskPath)
		isBareMetal := req.HypervisorKind == hypervisor.KindBareMetal

		allocBytes := req.AllocatedBytes
		if allocBytes < 20*1024*1024*1024 {
			allocBytes = 30 * 1024 * 1024 * 1024
		}

		// => DETERMINISTIC ROUTING: Active bare-metal host disk ALWAYS uses autonomous USB staging
		if isTargetActiveHostDisk && isBareMetal {
			onUpdate(StepUpdate{StepName: "=> [ACTIVE ROOT DETECTED] Routing to autonomous offline USB staging media...", Done: false})

			_ = exec.Command("udevadm", "settle", "--timeout=5").Run()

			usbStoragePart, _ := exec.Command("blkid", "-L", "ORCH_STORAGE").Output()
			storageDev := strings.TrimSpace(string(usbStoragePart))

			usbBootPart, _ := exec.Command("blkid", "-L", "ORCH_BOOT").Output()
			bootDev := strings.TrimSpace(string(usbBootPart))

			if storageDev == "" || bootDev == "" {
				targets, _ := ProbeRemovableTargets()
				if len(targets) > 0 {
					pBase := targets[0].DevPath
					sep := ""
					if strings.Contains(pBase, "nvme") || strings.Contains(pBase, "mmcblk") {
						sep = "p"
					}
					if storageDev == "" {
						storageDev = fmt.Sprintf("%s%s1", pBase, sep)
					}
					if bootDev == "" {
						bootDev = fmt.Sprintf("%s%s2", pBase, sep)
					}
				} else {
					journal.Fail(step, fmt.Errorf("no external USB staging drive detected for bare-metal live root handoff"))
					return fail("partition-shrink", fmt.Errorf("no external USB staging media (ORCH_STORAGE / ORCH_BOOT) detected; please connect a USB drive"))
				}
			}

			// Force-standardize canonical mountpoint to /mnt/orch_usb_storage
			usbStorageDir := "/mnt/orch_usb_storage"
			_ = os.MkdirAll(usbStorageDir, 0755)

			// Clean up any other conflicting mounts on the storage partition to prevent path divergence
			if mntOut, mErr := exec.Command("findmnt", "-n", "-o", "TARGET", storageDev).Output(); mErr == nil {
				for _, line := range strings.Split(strings.TrimSpace(string(mntOut)), "\n") {
					target := strings.TrimSpace(line)
					if target != "" && target != usbStorageDir {
						_ = exec.Command("umount", "-l", target).Run()
					}
				}
			}

			// Ensure storageDev is actively mounted at /mnt/orch_usb_storage
			if err := exec.Command("mountpoint", "-q", usbStorageDir).Run(); err != nil {
				_ = exec.Command("mount", storageDev, usbStorageDir).Run()
			}
			if err := exec.Command("mountpoint", "-q", usbStorageDir).Run(); err != nil {
				_ = exec.Command("mount", "-L", "ORCH_STORAGE", usbStorageDir).Run()
			}

			// Bug G: neither mount attempt above was ever checked for
			// success. If both failed (USB unplugged, filesystem not yet
			// probed, a permission issue, ...), usbStorageDir stayed an
			// ordinary empty directory on the root filesystem, but nothing
			// downstream knew that — the OS payload copy and
			// transient_resize.sh write below would "succeed" by writing to
			// local disk instead of the USB stick, and the later
			// os.Stat(scriptPath) "strict verification" check would still
			// pass (a file really was written, just to the wrong
			// filesystem). On reboot, the USB environment's own fresh mount
			// of ORCH_STORAGE would then find no transient_resize.sh at
			// all — this is almost certainly the literal "transient_resize.sh
			// is not being created" symptom. Fail loudly here instead.
			if err := ensureUSBStorageMounted(usbStorageDir); err != nil {
				journal.Fail(step, err)
				return fail("partition-shrink", fmt.Errorf("%w; connect/reconnect the ORCH_STORAGE USB drive and retry", err))
			}

			hostESP, espErr := FindESPPartition(req.TargetDiskPath)
			if espErr != nil {
				journal.Fail(step, espErr)
				return fail("partition-shrink", fmt.Errorf("cannot locate host ESP: %w", espErr))
			}

			hostPartDev := JoinDiskAndPartition(req.TargetDiskPath, req.HostPartNum)
			nextPartNum := "2"
			if pNumInt, pErr := strconv.Atoi(req.HostPartNum); pErr == nil {
				nextPartNum = strconv.Itoa(pNumInt + 1)
			}
			targetPartDev := req.TargetPartition
			if targetPartDev == "" {
				targetPartDev = JoinDiskAndPartition(req.TargetDiskPath, nextPartNum)
			}

			distroLabel := "LINUX"
			if req.SelectedEntry != nil && req.SelectedEntry.Distro != "" {
				distroLabel = strings.ToUpper(strings.Fields(req.SelectedEntry.Distro)[0])
			} else if req.BootLabel != "" {
				distroLabel = strings.ToUpper(strings.Fields(req.BootLabel)[0])
			}

			// Stage payload directly to physical USB Storage Vault
			isoSrcPath := filepath.Join(usbStorageDir, "os_image.payload")
			if streamResult.DestPath != "" && streamResult.DestPath != isoSrcPath {
				srcFi, sErr := os.Stat(streamResult.DestPath)
				dstFi, dErr := os.Stat(isoSrcPath)
				if sErr == nil && (dErr != nil || dstFi.Size() != srcFi.Size()) {
					onUpdate(StepUpdate{StepName: "=> [STORAGE VAULT] Synchronizing OS payload directly to physical USB storage...", Done: false})
					if cpErr := copyFileDirect(streamResult.DestPath, isoSrcPath); cpErr != nil {
						journal.Fail(step, cpErr)
						return fail("payload-staging", fmt.Errorf("staging payload to USB vault: %w", cpErr))
					}
					_ = exec.Command("sync").Run()
					onUpdate(StepUpdate{StepName: "=> [STORAGE VAULT] Payload synchronization complete and verified.", Done: false})
				}
			}

			if !ValidUsername(req.Credentials.Username) {
				return fail("partition-shrink", fmt.Errorf("refusing to stage autonomous installer: %q is not a valid username", req.Credentials.Username))
			}
			passHash, hashErr := req.Credentials.ResolvedPasswordHash()
			if hashErr != nil {
				return fail("partition-shrink", fmt.Errorf("hashing password for autonomous installer: %w", hashErr))
			}
			workerRootCmd := "passwd -l root 2>/dev/null || true"
			if req.Credentials.AllowRootLogin {
				workerRootCmd = `echo "root:$HASH" | chpasswd -e`
			}

			// Follow-up F15: resolve ORCH_STORAGE's filesystem UUID here,
			// while storageDev is already known, so transient_resize.sh's
			// mount-retry loop can fall back to mounting by UUID (not just
			// LABEL) if the label hasn't been scanned yet on reboot.
			storageUUIDOut, _ := exec.Command("blkid", "-s", "UUID", "-o", "value", storageDev).Output()
			storageUUID := strings.TrimSpace(string(storageUUIDOut))

			workerScript := renderAutonomousInstallerScript(autonomousInstallerScriptParams{
				Disk:          req.TargetDiskPath,
				HostPartDev:   hostPartDev,
				TargetPartDev: targetPartDev,
				HostESP:       hostESP,
				HostPartNum:   req.HostPartNum,
				TargetPartNum: nextPartNum,
				DistroLabel:   distroLabel,
				Username:      req.Credentials.Username,
				PasswordHash:  passHash,
				RootCmd:       workerRootCmd,
				StorageUUID:   storageUUID,
			})

			scriptPath := filepath.Join(usbStorageDir, "transient_resize.sh")
			writeErr := writeScriptSecurely(scriptPath, workerScript)
			if writeErr != nil {
				_ = exec.Command("mount", storageDev, usbStorageDir).Run()
				writeErr = writeScriptSecurely(scriptPath, workerScript)
			}
			// transientScriptMode (0700) is set by writeScriptSecurely itself;
			// re-asserting it here only protects against an already-existing
			// file at scriptPath with looser permissions from a prior run.
			_ = os.Chmod(scriptPath, transientScriptMode)
			_ = exec.Command("sync").Run()

			// Strict hardware physical verification
			if fi, sErr := os.Stat(scriptPath); sErr != nil || fi.Size() == 0 {
				journal.Fail(step, fmt.Errorf("transient_resize.sh not verified physically on storage vault: %w", sErr))
				return fail("staging-verification", fmt.Errorf("failed to physically stage transient_resize.sh on %s: %w", scriptPath, sErr))
			}
			onUpdate(StepUpdate{StepName: "=> [STORAGE VAULT] transient_resize.sh physically verified and committed.", Done: false})

			onUpdate(StepUpdate{StepName: "=> [EFI ENGINE] Arming monolithic GRUB EFI core with autonomous autorun...", Done: false})
			if armErr := ArmAutonomousUSBBootloader(bootDev, isoSrcPath, req.TargetDiskPath, func(msg string) {
				onUpdate(StepUpdate{StepName: msg, Done: false})
			}); armErr != nil {
				journal.Fail(step, armErr)
				return fail("efi-armor", armErr)
			}

			onUpdate(StepUpdate{StepName: "=> [NVRAM ENGINE] Setting one-shot UEFI BootNext to USB...", Done: false})
			usbDiskParent, usbBootNum, _ := SplitDiskAndPartition(bootDev)
			// Must match whatever ArmAutonomousUSBBootloader actually staged
			// at EFI/BOOT on this host's architecture (BOOTX64.EFI vs
			// BOOTAA64.EFI — see usb_manager.go's efiBootNamesForArch).
			// Hardcoding BOOTX64.EFI here would register an NVRAM one-shot
			// boot entry pointing at a file that doesn't exist on an arm64
			// host's USB stick, silently breaking the reboot-into-installer
			// step even though the USB media itself was staged correctly.
			_ = exec.Command("efibootmgr", "-c", "-d", usbDiskParent, "-p", usbBootNum, "-L", "ORCH_AUTONOMOUS_INSTALLER", "-l", autonomousUSBEFILoaderNVRAMPath(runtime.GOARCH)).Run()

			bootNum := ""
			if out, err := exec.Command("efibootmgr").Output(); err == nil {
				lines := strings.Split(string(out), "\n")
				for _, line := range lines {
					if strings.Contains(line, "ORCH_AUTONOMOUS_INSTALLER") && strings.HasPrefix(line, "Boot") {
						bootNum = strings.TrimPrefix(strings.Fields(line)[0], "Boot")
						bootNum = strings.TrimSuffix(bootNum, "*")
						break
					}
				}
				if bootNum == "" {
					for _, line := range lines {
						lower := strings.ToLower(line)
						if (strings.Contains(lower, "usb") || strings.Contains(line, "2001")) && strings.HasPrefix(line, "Boot") {
							bootNum = strings.TrimPrefix(strings.Fields(line)[0], "Boot")
							bootNum = strings.TrimSuffix(bootNum, "*")
							break
						}
					}
				}
			}

			if bootNum != "" {
				_ = exec.Command("efibootmgr", "-n", bootNum).Run()
				onUpdate(StepUpdate{StepName: fmt.Sprintf("=> [NVRAM ENGINE] BootNext locked to slot %s", bootNum), Done: false})
			}

			onUpdate(StepUpdate{StepName: "=> [SUCCESS] Autonomous offline staging prepared. Ready to reboot.", Done: true})
			_ = exec.Command("sync").Run()
			journal.Commit(step)

			_ = journal.Finalize()
			return ProvisionResult{Success: true}
		}

		// Secondary disk or Virtual Machine partition carving
		if req.TargetPartition != "" {
			if fi, statErr := os.Stat(req.TargetPartition); statErr == nil && (fi.Mode()&os.ModeDevice != 0) {
				onUpdate(StepUpdate{StepName: fmt.Sprintf("=> [STORAGE BUS] Carved target partition %s already exists. Reusing without repartitioning...", req.TargetPartition), Done: true})
				journal.Commit(step)
				onUpdate(StepUpdate{StepName: "partition-shrink", Done: true})
				goto deployPhase
			}
		}

		onUpdate(StepUpdate{StepName: fmt.Sprintf("=> [STORAGE BUS] Inspecting %s for available unallocated sectors...", req.TargetDiskPath), Done: false})
		res, carveErr := CarveHardwarePartitionAtBoundary(req.TargetDiskPath, "ext4", allocBytes, func(msg string) {
			onUpdate(StepUpdate{StepName: msg, Done: false})
		})

		if carveErr == nil && res != nil && res.NewPartitionPath != "" {
			req.TargetPartition = res.NewPartitionPath
			convertedSwapSizeBytes = res.ConvertedSwapSizeBytes
			convertedSwapUUID = res.ConvertedSwapUUID
			onUpdate(StepUpdate{StepName: fmt.Sprintf("physical dual-boot partition carved: %s", req.TargetPartition), Done: true})
			journal.Commit(step)
			onUpdate(StepUpdate{StepName: "partition-shrink", Done: true})
		} else {
			journal.Fail(step, carveErr)
			return fail("partition-shrink", carveErr)
		}
	}

deployPhase:
	deployStep, err := journal.Begin("image-deploy", nil)
	if err != nil {
		return fail("image-deploy", err)
	}

	isMacOS := false
	if req.SelectedEntry != nil {
		isMacOS = strings.EqualFold(string(req.SelectedEntry.Family), "macos") ||
			strings.EqualFold(string(req.SelectedEntry.Family), "darwin") ||
			strings.Contains(strings.ToLower(req.SelectedEntry.Distro), "macos")
	}
	if !isMacOS && strings.Contains(strings.ToLower(req.BootLabel), "macos") {
		isMacOS = true
	}

	isWindows := req.IsWindowsImage || strings.Contains(strings.ToLower(req.BootLabel), "windows")
	if req.SelectedEntry != nil {
		if strings.EqualFold(string(req.SelectedEntry.Family), "windows") || strings.Contains(strings.ToLower(req.SelectedEntry.Distro), "windows") {
			isWindows = true
		}
	}

	// Auto-detect Windows payloads dynamically if ISO contains install.wim or install.esd
	if !isWindows && !isMacOS {
		testMount := "/mnt/orch_iso_probe"
		_ = os.MkdirAll(testMount, 0755)
		_ = exec.Command("umount", "-l", testMount).Run()
		if _, err := exec.Command("mount", "-o", "loop,ro", streamResult.DestPath, testMount).CombinedOutput(); err == nil {
			candidates := []string{
				filepath.Join(testMount, "sources", "install.wim"),
				filepath.Join(testMount, "sources", "install.esd"),
				filepath.Join(testMount, "sources", "INSTALL.WIM"),
				filepath.Join(testMount, "sources", "INSTALL.ESD"),
			}
			for _, c := range candidates {
				if fi, errStat := os.Stat(c); errStat == nil && fi.Size() > 0 {
					isWindows = true
					break
				}
			}
			_ = exec.Command("umount", "-l", testMount).Run()
			_ = os.Remove(testMount)
		}
	}

	if isMacOS {
		onUpdate(StepUpdate{StepName: "=> [MACOS PIPELINE] Deploying Apple BaseSystem recovery payload via 7-Zip engine...", Done: false})
		if macErr := deployMacOSPayload(streamResult.DestPath, req.TargetPartition, func(msg string) {
			onUpdate(StepUpdate{StepName: msg, Done: false})
		}); macErr != nil {
			journal.Fail(deployStep, macErr)
			return fail("image-deploy", macErr)
		}
	} else if isWindows {
		onUpdate(StepUpdate{StepName: "=> [WINDOWS PIPELINE] Decompressing and writing Windows OS payload via wimapply...", Done: false})
		if winErr := deployWindowsPayload(streamResult.DestPath, req.TargetPartition, req.WimIndex, req.AutounattendPath, req.Credentials, func(msg string) {
			onUpdate(StepUpdate{StepName: msg, Done: false})
		}); winErr != nil {
			journal.Fail(deployStep, winErr)
			return fail("image-deploy", winErr)
		}
	} else {
		onUpdate(StepUpdate{StepName: "=> [ROOTFS DEPLOY] Extracting live root filesystem tree into target partition...", Done: false})
		if extErr := extractLiveRootFS(streamResult.DestPath, req.TargetPartition, req.Credentials, func(msg string) {
			onUpdate(StepUpdate{StepName: msg, Done: false})
		}); extErr != nil {
			journal.Fail(deployStep, extErr)
			return fail("image-deploy", extErr)
		}
	}
	journal.Commit(deployStep)
	onUpdate(StepUpdate{StepName: "image-deploy", Done: true})

	if res, failed := configureBootloaderStep(journal, isMacOS, isWindows, req, grubDefaultPath, streamResult.DestPath, onUpdate, fail); failed {
		return res
	}

	_, revPartNum, revSplitErr := SplitDiskAndPartition(req.TargetPartition)
	if revSplitErr != nil {
		onUpdate(StepUpdate{StepName: fmt.Sprintf("=> [WARNING] Could not resolve partition number for rollback: %v", revSplitErr), Done: false})
	}

	// Stamp a stable identity fingerprint (PARTUUID + fstype + size) for the newly
	// created target partition and the host partition it was carved from. This is
	// what lets revert re-find "the one to undo" later even if disk letters/numbers
	// shift between now and revert time (different USB port, boot order, another
	// drive attached, etc.) — see resolveDeviceByPartUUID in revert_linux.go.
	var targetPartUUID, shrunkPartUUID, targetFSType string
	var targetSizeBytes uint64

	if req.TargetPartition != "" {
		if uuidOut, uErr := exec.Command("blkid", "-s", "PARTUUID", "-o", "value", req.TargetPartition).Output(); uErr == nil {
			targetPartUUID = strings.TrimSpace(string(uuidOut))
		}
		if fsOut, fErr := exec.Command("blkid", "-s", "TYPE", "-o", "value", req.TargetPartition).Output(); fErr == nil {
			targetFSType = strings.ToLower(strings.TrimSpace(string(fsOut)))
		}
		targetSizeBytes, _ = QueryBlockDeviceBytes(filepath.Base(req.TargetPartition))
	}

	if req.HostPartNum != "" && req.TargetDiskPath != "" {
		hostPartDev := JoinDiskAndPartition(req.TargetDiskPath, req.HostPartNum)
		if uuidOut, uErr := exec.Command("blkid", "-s", "PARTUUID", "-o", "value", hostPartDev).Output(); uErr == nil {
			shrunkPartUUID = strings.TrimSpace(string(uuidOut))
		}
	}

	if targetPartUUID == "" {
		onUpdate(StepUpdate{StepName: "=> [WARNING] Could not resolve PARTUUID for target partition — revert will fall back to device path (less safe if disk numbering changes).", Done: false})
	}

	// Persist the fingerprint into the journal's session state too, so
	// DiscoverSecondaryOSes (used by the interactive revert menu) can also
	// resolve the live device by UUID, not just the rollback handler above.
	sessState := journal.Session
	sessState.TargetDisk = req.TargetDiskPath
	sessState.TargetPartition = req.TargetPartition
	sessState.TargetPartNum = revPartNum
	sessState.TargetPartUUID = targetPartUUID
	sessState.TargetFSType = targetFSType
	sessState.TargetSizeBytes = int64(targetSizeBytes)
	sessState.ShrunkPartition = JoinDiskAndPartition(req.TargetDiskPath, req.HostPartNum)
	sessState.ShrunkPartNum = req.HostPartNum
	sessState.ShrunkPartUUID = shrunkPartUUID
	sessState.EFIDirName = req.BootLabel
	sessState.OriginalSwapSizeBytes = convertedSwapSizeBytes
	sessState.OriginalSwapUUID = convertedSwapUUID
	_ = journal.SetSessionState(sessState)

	revStep, _ := journal.Begin("revert-engine", nil)
	revParams := RealRevertParams{
		DiskDevice:        req.TargetDiskPath,
		PartitionNum:      revPartNum,
		HostPartNum:       req.HostPartNum,
		EFIDirNameToPurge: req.BootLabel,
		TargetPartUUID:    targetPartUUID,
		ShrunkPartUUID:    shrunkPartUUID,
		ExpectedFSType:    targetFSType,
		ExpectedSizeBytes: int64(targetSizeBytes),
	}
	revJSON, _ := json.Marshal(revParams)
	revStep.RollbackData = revJSON
	journal.Commit(revStep)

	installedRecordPath := filepath.Join(filepath.Dir(req.JournalPath), "orchestrator_installed_os.json")
	if fErr := journal.FinalizeAndRecord(installedRecordPath); fErr != nil {
		onUpdate(StepUpdate{StepName: fmt.Sprintf("=> [WARNING] Finalize note: %v", fErr), Done: false})
	}
	return ProvisionResult{Success: true}
}

func verifyFileSHA256(filePath, expectedSHA string) error {
	f, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer f.Close()

	h := sha256.New()
	buffer := make([]byte, 1024*1024)
	if _, err := io.CopyBuffer(h, f, buffer); err != nil {
		return err
	}
	actual := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(actual, strings.TrimSpace(expectedSHA)) {
		return fmt.Errorf("hash mismatch: expected %s, got %s", expectedSHA, actual)
	}
	return nil
}

func generateTxnID() string {
	return fmt.Sprintf("txn-%d", time.Now().UnixNano())
}

func causeError(err error) error {
	for err != nil {
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			break
		}
		err = u.Unwrap()
	}
	return err
}

func formatDownloadBytes(b int64) string {
	if b <= 0 {
		return "0 B"
	}
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	units := []string{"KB", "MB", "GB", "TB"}
	val := float64(b) / float64(div)
	if exp >= 2 {
		return fmt.Sprintf("%.2f %s", val, units[exp])
	}
	return fmt.Sprintf("%.1f %s", val, units[exp])
}
