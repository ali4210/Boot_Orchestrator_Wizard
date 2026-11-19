// Package engine — backup.go implements autonomous storage discovery,
// live filesystem snapshot streaming with SHA256 manifests, NTFS-3G drivers,
// and firmware-aware (UEFI/GPT vs MBR) bare-metal disaster recovery.
package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type BlockDevice struct {
	Name       string        `json:"name"`
	Size       string        `json:"size"`
	Type       string        `json:"type"`
	Mountpoint string        `json:"mountpoint"`
	Model      string        `json:"model"`
	RM         bool          `json:"rm"`
	FSType     string        `json:"fstype"`
	Children   []BlockDevice `json:"children,omitempty"`
}

type lsblkOutput struct {
	BlockDevices []BlockDevice `json:"blockdevices"`
}

func ensureNTFS3G(logFn func(string)) error {
	if _, err := exec.LookPath("ntfs-3g"); err == nil {
		return nil
	}

	logFn("=> [DRIVER CHECK] ntfs-3g missing. Installing non-interactively...")

	if _, err := exec.LookPath("apt-get"); err == nil {
		cmd := execCommand("apt-get", "update", "-qq")
		cmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
		_ = cmd.Run()

		cmdInstall := execCommand("apt-get", "install", "-y", "-qq", "ntfs-3g")
		cmdInstall.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
		if out, err := cmdInstall.CombinedOutput(); err != nil {
			return fmt.Errorf("installing ntfs-3g failed: %w (%s)", err, strings.TrimSpace(string(out)))
		}
		logFn("=> [DRIVER OK] ntfs-3g installed.")
		return nil
	}
	return fmt.Errorf("apt-get not available to install ntfs-3g")
}

// resolveBackupMountNode is called with what might already be a whole-disk
// path; if so, it tries partition 1 of that disk instead. Audit 1C.8: the
// nvme/mmcblk-substring special case (else bare "path+1") mis-parses loop
// devices (loop0 -> loop01 instead of loop0p1); built via the shared
// JoinDiskAndPartition instead.
func resolveBackupMountNode(devPath string) string {
	if strings.HasPrefix(devPath, "/dev/") && !strings.Contains(devPath, "1") && !strings.Contains(devPath, "2") {
		p1 := JoinDiskAndPartition(devPath, "1")
		if _, err := os.Stat(p1); err == nil {
			return p1
		}
	}
	return devPath
}

func AutoMountDrive(devPath string, logFn func(string)) (string, func(), error) {
	if logFn == nil {
		logFn = func(string) {}
	}

	devPath = resolveBackupMountNode(devPath)

	mountPoint := filepath.Join("/mnt", "orch_drive_"+filepath.Base(devPath))
	_ = fsRunner.MkdirAll(mountPoint, 0755)

	fsTypeOut, _ := execCommand("blkid", "-s", "TYPE", "-o", "value", devPath).Output()
	detectedFS := strings.ToLower(strings.TrimSpace(string(fsTypeOut)))

	if detectedFS == "ntfs" {
		if err := ensureNTFS3G(logFn); err != nil {
			return "", func() {}, err
		}
		cmdNTFS := execCommand("ntfs-3g", "-o", "remove_hiberfile,rw", devPath, mountPoint)
		if _, err := cmdNTFS.CombinedOutput(); err == nil {
			return mountPoint, makeCleanup(mountPoint), nil
		}
		cmdFallback := execCommand("mount", "-t", "ntfs-3g", devPath, mountPoint)
		if _, errF := cmdFallback.CombinedOutput(); errF == nil {
			return mountPoint, makeCleanup(mountPoint), nil
		}
	}

	cmd := execCommand("mount", devPath, mountPoint)
	if _, err := cmd.CombinedOutput(); err == nil {
		return mountPoint, makeCleanup(mountPoint), nil
	}

	if errInstall := ensureNTFS3G(logFn); errInstall == nil {
		cmdRetry := execCommand("ntfs-3g", devPath, mountPoint)
		if _, errRetry := cmdRetry.CombinedOutput(); errRetry == nil {
			return mountPoint, makeCleanup(mountPoint), nil
		}
	}

	return "", func() {}, fmt.Errorf("failed to mount %s", devPath)
}

func makeCleanup(mountPoint string) func() {
	return func() {
		_ = execCommand("umount", "-l", mountPoint).Run()
		_ = fsRunner.Remove(mountPoint)
	}
}

// DetectBackupTargets queries storage devices. Removable disks are presented as
// whole raw disk targets (/dev/sdX) so that dynamic autonomous partitioning into
// ext4 (ORCH_STORAGE) and FAT32 (ORCH_BOOT) is triggered.
func DetectBackupTargets() ([]BlockDevice, error) {
	out, err := execCommand("lsblk", "-J", "-b", "-o", "NAME,SIZE,TYPE,MOUNTPOINT,MODEL,RM,HOTPLUG,TRAN,FSTYPE").Output()
	if err != nil {
		return nil, fmt.Errorf("querying storage topology: %w", err)
	}

	var parsed struct {
		BlockDevices []struct {
			Name       string `json:"name"`
			Size       uint64 `json:"size"`
			Type       string `json:"type"`
			Mountpoint string `json:"mountpoint"`
			Model      string `json:"model"`
			RM         bool   `json:"rm"`
			Hotplug    bool   `json:"hotplug"`
			Tran       string `json:"tran"`
			FSType     string `json:"fstype"`
			Children   []struct {
				Name       string `json:"name"`
				Size       uint64 `json:"size"`
				Type       string `json:"type"`
				Mountpoint string `json:"mountpoint"`
				Model      string `json:"model"`
				RM         bool   `json:"rm"`
				Hotplug    bool   `json:"hotplug"`
				Tran       string `json:"tran"`
				FSType     string `json:"fstype"`
			} `json:"children"`
		} `json:"blockdevices"`
	}

	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, fmt.Errorf("parsing lsblk JSON: %w", err)
	}

	pDisk, _, _, _, _ := DetectActiveRootDisk()
	pDiskBase := filepath.Base(pDisk)

	var valid []BlockDevice

	for _, dev := range parsed.BlockDevices {
		if strings.HasPrefix(dev.Name, "sr") || dev.Type == "rom" || dev.Mountpoint == "[SWAP]" {
			continue
		}
		if strings.HasPrefix(dev.Name, pDiskBase) || dev.Name == pDiskBase || dev.Mountpoint == "/" {
			continue
		}

		// Audit 1C-followup: this used to also treat ANY non-root disk whose
		// name merely started with "sd" as removable/safe-to-wipe — on a
		// machine with more than two SATA/SAS disks, an internal data disk
		// (sdb, sdc, ...) would be silently offered as a backup-staging
		// target and then repartitioned. Classify removable only by lsblk's
		// own TRAN=="usb" or RM signal; fail closed otherwise.
		isRemovableDisk := dev.Type == "disk" && (dev.RM || strings.ToLower(dev.Tran) == "usb")

		if isRemovableDisk {
			valid = append(valid, BlockDevice{
				Name:       dev.Name,
				Size:       formatBytes(dev.Size),
				Type:       "disk",
				Mountpoint: "(Auto 2-Partition: ext4 + FAT32)",
				Model:      dev.Model,
				RM:         dev.RM,
				FSType:     "dynamic (ext4+fat32)",
			})
			continue
		}

		if len(dev.Children) > 0 {
			for _, child := range dev.Children {
				if child.Mountpoint == "/" || child.Mountpoint == "[SWAP]" {
					continue
				}
				devPath := "/dev/" + child.Name
				valid = append(valid, BlockDevice{
					Name:       devPath,
					Size:       formatBytes(child.Size),
					Type:       child.Type,
					Mountpoint: child.Mountpoint,
					Model:      child.Model,
					RM:         child.RM,
					FSType:     child.FSType,
				})
			}
		} else if dev.Type == "part" || dev.FSType != "" {
			devPath := "/dev/" + dev.Name
			valid = append(valid, BlockDevice{
				Name:       devPath,
				Size:       formatBytes(dev.Size),
				Type:       dev.Type,
				Mountpoint: dev.Mountpoint,
				Model:      dev.Model,
				RM:         dev.RM,
				FSType:     dev.FSType,
			})
		}
	}

	return valid, nil
}

func EstimateHostBackupSize() uint64 {
	sources := []string{"/home", "/etc", "/root", "/var/local", "/opt"}
	var total uint64
	for _, s := range sources {
		out, err := execCommand("du", "-sb", s).Output()
		if err == nil {
			parts := strings.Fields(string(out))
			if len(parts) > 0 {
				if b, parseErr := strconv.ParseUint(parts[0], 10, 64); parseErr == nil {
					total += b
				}
			}
		}
	}
	if total == 0 {
		total = 5 * 1024 * 1024 * 1024
	}
	compressedEstimate := uint64(float64(total) * 0.45)
	if compressedEstimate < 100*1024*1024 {
		compressedEstimate = 100 * 1024 * 1024
	}
	return compressedEstimate
}

func CreateHostBackupWithProgress(targetPath string, onProgress func(written int64, step string)) (string, error) {
	destDir := targetPath
	var cleanup func()

	if strings.HasPrefix(targetPath, "/dev/") {
		mp, clean, err := AutoMountDrive(targetPath, func(msg string) {
			onProgress(0, msg)
		})
		if err != nil {
			return "", err
		}
		destDir = mp
		cleanup = clean
	}
	if cleanup != nil {
		defer cleanup()
	}

	timestamp := time.Now().Format("20060102-150405")
	backupFileName := fmt.Sprintf("host_os_backup_%s.tar.gz", timestamp)
	fullDestPath := filepath.Join(destDir, backupFileName)

	sources := []string{"/home", "/etc", "/root", "/var/local", "/opt"}
	var validSources []string
	for _, s := range sources {
		if _, err := os.Stat(s); err == nil {
			validSources = append(validSources, s)
		}
	}

	targetEst := EstimateHostBackupSize()

	tarArgs := append([]string{
		"-czpf", fullDestPath,
		"--exclude=/var/cache",
		"--exclude=/var/tmp",
		"--exclude=/tmp",
	}, validSources...)

	cmd := execCommand("tar", tarArgs...)
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("starting backup: %w", err)
	}

	ticker := time.NewTicker(300 * time.Millisecond)
	done := make(chan error, 1)

	go func() {
		done <- cmd.Wait()
	}()

	for {
		select {
		case err := <-done:
			ticker.Stop()
			if err != nil {
				return "", fmt.Errorf("compression failed: %w", err)
			}
			onProgress(int64(targetEst), "compressing: 100.0% (Finalizing sync)")
			onProgress(0, "=> Computing SHA256 checksum manifest on USB media...")
			chkCmd := execCommand("sha256sum", filepath.Base(fullDestPath))
			chkCmd.Dir = destDir
			if chkOut, cErr := chkCmd.Output(); cErr == nil {
				_ = fsRunner.WriteFile(fullDestPath+".sha256", chkOut, 0644)
			}
			_ = execCommand("sync").Run()
			return fullDestPath, nil

		case <-ticker.C:
			if fi, err := os.Stat(fullDestPath); err == nil {
				curSize := fi.Size()
				pct := (float64(curSize) / float64(targetEst)) * 100.0
				if pct > 99.0 {
					pct = 99.0
				}
				stepMsg := fmt.Sprintf("compressing: %.1f%% (%s written)", pct, formatBytes(uint64(curSize)))
				onProgress(curSize, stepMsg)
			}
		}
	}
}

func formatBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

type BackupArchiveDescriptor struct {
	FilePath    string
	FileName    string
	DiskDevice  string
	MountPoint  string
	SizeBytes   int64
	HasChecksum bool
}

func FindBackupArchives() ([]BackupArchiveDescriptor, error) {
	targets, err := DetectBackupTargets()
	if err != nil {
		return nil, err
	}

	var found []BackupArchiveDescriptor

	for _, t := range targets {
		mountDir := t.Mountpoint
		var cleanup func()

		if mountDir == "" || strings.HasPrefix(mountDir, "(") {
			mp, clean, err := AutoMountDrive(t.Name, nil)
			if err != nil {
				continue
			}
			mountDir = mp
			cleanup = clean
		}

		entries, rErr := os.ReadDir(mountDir)
		if rErr == nil {
			for _, e := range entries {
				if !e.IsDir() && strings.HasPrefix(e.Name(), "host_os_backup_") && strings.HasSuffix(e.Name(), ".tar.gz") {
					fullPath := filepath.Join(mountDir, e.Name())
					info, iErr := e.Info()
					if iErr != nil {
						continue
					}
					_, cErr := os.Stat(fullPath + ".sha256")
					found = append(found, BackupArchiveDescriptor{
						FilePath:    fullPath,
						FileName:    e.Name(),
						DiskDevice:  t.Name,
						MountPoint:  mountDir,
						SizeBytes:   info.Size(),
						HasChecksum: cErr == nil,
					})
				}
			}
		}

		if cleanup != nil {
			cleanup()
		}
	}

	return found, nil
}

func isUEFIBooted() bool {
	_, err := os.Stat("/sys/firmware/efi")
	return err == nil
}

func RestoreHostFromBackup(archivePath, targetDisk string, logFn func(string)) error {
	if logFn == nil {
		logFn = func(string) {}
	}

	logFn("=> [DISASTER RESTORE] Verifying backup checksum manifest...")
	if _, err := os.Stat(archivePath + ".sha256"); err == nil {
		cmdCheck := execCommand("sha256sum", "-c", archivePath+".sha256")
		cmdCheck.Dir = filepath.Dir(archivePath)
		if out, err := cmdCheck.CombinedOutput(); err != nil {
			return fmt.Errorf("cryptographic verification failed: archive corrupted: %s", string(out))
		}
		logFn("=> Checksum matched: Image verified 100% authentic.")
	}

	_ = execCommand("swapoff", "-a").Run()

	isUEFI := isUEFIBooted()
	baseDisk := strings.TrimPrefix(targetDisk, "/dev/")
	sep := ""
	if len(baseDisk) > 0 && (baseDisk[len(baseDisk)-1] >= '0' && baseDisk[len(baseDisk)-1] <= '9') {
		sep = "p"
	}

	var espPart, rootPart string

	// Audit item 25 (silent-error audit): every parted/mkfs call in this
	// block used to discard its error. If, say, "mklabel gpt" silently
	// failed (disk busy, permission denied, device vanished), every
	// subsequent mkpart/mkfs call below it would operate against a disk
	// that was never actually relabeled — some would then also fail
	// silently, others might succeed against a stale/unexpected existing
	// layout, and the function would sail on to mount whatever espPart/
	// rootPart happened to compute to. A full failure (no partition at
	// all) does eventually surface at the mkfs.ext4/mount calls further
	// down, which already correctly checked their errors — but a PARTIAL
	// failure (e.g. the ESP mkpart/mkfs failing while the root partition
	// succeeds) would not: the ESP mount below silently creates an empty
	// directory instead of a real mount, extraction proceeds anyway, and
	// disaster recovery finishes "successfully" having produced an
	// unbootable UEFI system with no real ESP content. Every call that
	// actually changes the disk's structure now checks its error and
	// aborts loudly; partprobe (a kernel re-read nudge, not a state
	// change) is left as best-effort.
	if isUEFI {
		logFn(fmt.Sprintf("=> [UEFI DETECTED] Partitioning %s with GPT layout...", targetDisk))
		if out, err := execCommand("parted", "-s", targetDisk, "mklabel", "gpt").CombinedOutput(); err != nil {
			return fmt.Errorf("creating GPT partition table on %s: %w (%s)", targetDisk, err, out)
		}
		if out, err := execCommand("parted", "-s", targetDisk, "mkpart", "ESP", "fat32", "1MiB", "1025MiB").CombinedOutput(); err != nil {
			return fmt.Errorf("creating ESP partition on %s: %w (%s)", targetDisk, err, out)
		}
		if out, err := execCommand("parted", "-s", targetDisk, "set", "1", "esp", "on").CombinedOutput(); err != nil {
			return fmt.Errorf("marking ESP flag on %s partition 1: %w (%s)", targetDisk, err, out)
		}
		if out, err := execCommand("parted", "-s", targetDisk, "mkpart", "primary", "ext4", "1025MiB", "100%").CombinedOutput(); err != nil {
			return fmt.Errorf("creating root partition on %s: %w (%s)", targetDisk, err, out)
		}
		_ = execCommand("partprobe", targetDisk).Run()
		time.Sleep(1 * time.Second)

		espPart = fmt.Sprintf("/dev/%s%s1", baseDisk, sep)
		rootPart = fmt.Sprintf("/dev/%s%s2", baseDisk, sep)

		logFn(fmt.Sprintf("=> Formatting EFI system partition %s...", espPart))
		if out, err := execCommand("mkfs.vfat", "-F32", "-n", "BOOT", espPart).CombinedOutput(); err != nil {
			return fmt.Errorf("formatting ESP %s: %w (%s)", espPart, err, out)
		}
	} else {
		logFn(fmt.Sprintf("=> [LEGACY BIOS DETECTED] Partitioning %s with MBR layout...", targetDisk))
		if out, err := execCommand("parted", "-s", targetDisk, "mklabel", "msdos").CombinedOutput(); err != nil {
			return fmt.Errorf("creating MBR partition table on %s: %w (%s)", targetDisk, err, out)
		}
		if out, err := execCommand("parted", "-s", targetDisk, "mkpart", "primary", "ext4", "1MiB", "100%").CombinedOutput(); err != nil {
			return fmt.Errorf("creating root partition on %s: %w (%s)", targetDisk, err, out)
		}
		if out, err := execCommand("parted", "-s", targetDisk, "set", "1", "boot", "on").CombinedOutput(); err != nil {
			return fmt.Errorf("marking boot flag on %s partition 1: %w (%s)", targetDisk, err, out)
		}
		_ = execCommand("partprobe", targetDisk).Run()
		time.Sleep(1 * time.Second)

		rootPart = fmt.Sprintf("/dev/%s%s1", baseDisk, sep)
	}

	logFn(fmt.Sprintf("=> Formatting root filesystem on %s...", rootPart))
	if out, err := execCommand("mkfs.ext4", "-F", "-L", "ROOT", rootPart).CombinedOutput(); err != nil {
		return fmt.Errorf("mkfs.ext4 failed: %w (%s)", err, string(out))
	}

	stageDir, err := os.MkdirTemp("", "orch-restore-*")
	if err != nil {
		return err
	}
	defer fsRunner.RemoveAll(stageDir)

	if out, err := execCommand("mount", rootPart, stageDir).CombinedOutput(); err != nil {
		return fmt.Errorf("mounting restore target failed: %w (%s)", err, string(out))
	}
	defer func() {
		_ = execCommand("umount", "-l", stageDir).Run()
		_ = execCommand("sync").Run()
	}()

	if isUEFI {
		efiDir := filepath.Join(stageDir, "boot", "efi")
		_ = fsRunner.MkdirAll(efiDir, 0755)
		if out, err := execCommand("mount", espPart, efiDir).CombinedOutput(); err != nil {
			return fmt.Errorf("mounting ESP %s at %s: %w (%s)", espPart, efiDir, err, out)
		}
		defer func() {
			_ = execCommand("umount", "-l", efiDir).Run()
		}()
	}

	logFn(fmt.Sprintf("=> Extracting system files from %s...", archivePath))
	tarCmd := execCommand("tar", "-xpf", archivePath, "-C", stageDir)
	if out, err := tarCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("extraction failed: %w (%s)", err, string(out))
	}

	uuidOut, err := execCommand("blkid", "-s", "UUID", "-o", "value", rootPart).Output()
	if err == nil {
		newUUID := strings.TrimSpace(string(uuidOut))
		fstabPath := filepath.Join(stageDir, "etc", "fstab")
		fstabContent := fmt.Sprintf("# Created by Disaster Recovery Orchestrator\nUUID=%s / ext4 errors=remount-ro 0 1\n", newUUID)
		if isUEFI {
			espUUIDOut, _ := execCommand("blkid", "-s", "UUID", "-o", "value", espPart).Output()
			espUUID := strings.TrimSpace(string(espUUIDOut))
			fstabContent += fmt.Sprintf("UUID=%s /boot/efi vfat umask=0077 0 1\n", espUUID)
		}
		_ = fsRunner.WriteFile(fstabPath, []byte(fstabContent), 0644)
	}

	binds := []string{"/dev", "/proc", "/sys", "/dev/pts"}
	for _, b := range binds {
		dest := filepath.Join(stageDir, b)
		_ = fsRunner.MkdirAll(dest, 0755)
		_ = execCommand("mount", "--bind", b, dest).Run()
	}
	defer func() {
		for i := len(binds) - 1; i >= 0; i-- {
			_ = execCommand("umount", "-l", filepath.Join(stageDir, binds[i])).Run()
		}
	}()

	if hostDNS, err := os.ReadFile("/etc/resolv.conf"); err == nil {
		_ = fsRunner.WriteFile(filepath.Join(stageDir, "etc", "resolv.conf"), hostDNS, 0644)
	}

	logFn(fmt.Sprintf("=> Reinstalling GRUB bootloader to %s...", targetDisk))
	grubScript := buildGrubReinstallScript(targetDisk, isUEFI, runtime.GOARCH)

	chrootCmd := execCommand("chroot", stageDir, "/bin/sh", "-c", grubScript)
	_ = chrootCmd.Run()

	_ = execCommand("sync").Run()
	logFn("=> [RESTORE COMPLETE] Operating system successfully restored.")
	return nil
}

// buildGrubReinstallScript builds the chroot shell script that reinstalls
// GRUB onto targetDisk during disaster recovery. goarch is the restoring
// host's own architecture (e.g. runtime.GOARCH) — grub-install's
// --target= value must match the host's EFI firmware architecture, not be
// hardcoded to x86_64-efi, or a restore performed on arm64 host hardware
// would silently install an x86_64 GRUB payload that the arm64 UEFI
// firmware cannot execute, leaving the "restored" system unbootable.
// Extracted as a pure function so the arch-selection logic is directly
// testable without needing a real chroot/disk.
func buildGrubReinstallScript(targetDisk string, isUEFI bool, goarch string) string {
	if isUEFI {
		_, _, _, grubEFIFormat := efiBootNamesForArch(goarch)
		return fmt.Sprintf(`
grub-install --target=%s --efi-directory=/boot/efi --bootloader-id=kali --recheck || grub-install %s
update-grub || grub-mkconfig -o /boot/grub/grub.cfg
update-initramfs -u -k all || true
`, grubEFIFormat, targetDisk)
	}
	return fmt.Sprintf(`
grub-install %s || grub-install --recheck %s
update-grub || grub-mkconfig -o /boot/grub/grub.cfg
update-initramfs -u -k all || true
`, targetDisk, targetDisk)
}
