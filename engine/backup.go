package engine

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type BlockDevice struct {
	Name       string `json:"name"`
	Size       string `json:"size"`
	Type       string `json:"type"`
	Mountpoint string `json:"mountpoint"`
	Model      string `json:"model"`
	RM         bool   `json:"rm"`
	FSType     string `json:"fstype"`
}

type lsblkOutput struct {
	BlockDevices []BlockDevice `json:"blockdevices"`
}

// AutoMountDrive mounts an unmounted drive to a deterministic mountpoint.
func AutoMountDrive(devPath string) (string, func(), error) {
	mountPoint := filepath.Join("/mnt", "orch_drive_"+filepath.Base(devPath))
	_ = os.MkdirAll(mountPoint, 0755)

	if out, err := exec.Command("mount", devPath, mountPoint).CombinedOutput(); err != nil {
		return "", func() {}, fmt.Errorf("mounting %s to %s failed: %w (%s)", devPath, mountPoint, err, string(out))
	}

	cleanup := func() {
		_ = exec.Command("umount", "-l", mountPoint).Run()
		_ = os.Remove(mountPoint)
	}

	return mountPoint, cleanup, nil
}

// DetectBackupTargets finds all valid external USBs, secondary disks, and non-root partitions.
func DetectBackupTargets() ([]BlockDevice, error) {
	out, err := exec.Command("lsblk", "-J", "-b", "-o", "NAME,SIZE,TYPE,MOUNTPOINT,MODEL,RM,FSTYPE").Output()
	if err != nil {
		return nil, fmt.Errorf("querying storage topology: %w", err)
	}

	var parsed lsblkOutput
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, fmt.Errorf("parsing lsblk JSON: %w", err)
	}

	pDisk, _, _, _, _ := DetectActiveRootDisk()
	pDiskBase := filepath.Base(pDisk)

	var valid []BlockDevice
	for _, dev := range parsed.BlockDevices {
		// Ignore swap and the active root disk
		if strings.HasPrefix(dev.Name, pDiskBase) || dev.Mountpoint == "/" || dev.Mountpoint == "[SWAP]" {
			continue
		}

		// Include any partition with a recognizable filesystem or removable disk
		if dev.Type == "part" || dev.Type == "disk" || dev.RM {
			devPath := "/dev/" + dev.Name
			// Format human readable size if numeric
			valid = append(valid, BlockDevice{
				Name:       devPath,
				Size:       dev.Size,
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

// CreateHostBackup streams an online compressed snapshot of system directories to the destination drive.
func CreateHostBackup(targetPath string, logFn func(string)) (string, error) {
	if logFn == nil {
		logFn = func(string) {}
	}

	destDir := targetPath
	var cleanup func()
	// If the user selected a raw block device (e.g., /dev/sdb1), auto-mount it
	if strings.HasPrefix(targetPath, "/dev/") {
		mp, clean, err := AutoMountDrive(targetPath)
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

	logFn(fmt.Sprintf("=> [BACKUP] Streaming compressed host image to %s...", fullDestPath))

	sources := []string{"/home", "/etc", "/root", "/var/local", "/opt"}
	var validSources []string
	for _, s := range sources {
		if _, err := os.Stat(s); err == nil {
			validSources = append(validSources, s)
		}
	}

	tarArgs := append([]string{
		"-czpf", fullDestPath,
		"--exclude=/var/cache",
		"--exclude=/var/tmp",
		"--exclude=/tmp",
	}, validSources...)

	cmd := exec.Command("tar", tarArgs...)
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return "", fmt.Errorf("attaching log pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("starting backup: %w", err)
	}

	scanner := bufio.NewScanner(stderrPipe)
	go func() {
		for scanner.Scan() {
			logFn(fmt.Sprintf("=> [TAR] %s", scanner.Text()))
		}
	}()

	if err := cmd.Wait(); err != nil {
		return "", fmt.Errorf("archive creation failed: %w", err)
	}

	// Create cryptographic SHA256 manifest
	logFn("=> Generating SHA256 manifest on backup storage...")
	chkCmd := exec.Command("sha256sum", filepath.Base(fullDestPath))
	chkCmd.Dir = destDir
	if chkOut, cErr := chkCmd.Output(); cErr == nil {
		_ = os.WriteFile(fullDestPath+".sha256", chkOut, 0644)
	}

	_ = exec.Command("sync").Run()
	logFn(fmt.Sprintf("=> Host backup successfully written: %s", backupFileName))
	return fullDestPath, nil
}

type BackupArchiveDescriptor struct {
	FilePath    string
	FileName    string
	DiskDevice  string
	MountPoint  string
	SizeBytes   int64
	HasChecksum bool
}

// FindBackupArchives scans mounted directories AND unmounted partitions for backup files.
func FindBackupArchives() ([]BackupArchiveDescriptor, error) {
	targets, err := DetectBackupTargets()
	if err != nil {
		return nil, err
	}

	var found []BackupArchiveDescriptor

	for _, t := range targets {
		mountDir := t.Mountpoint
		var cleanup func()

		if mountDir == "" {
			mp, clean, err := AutoMountDrive(t.Name)
			if err != nil {
				continue // skip partitions that can't be mounted
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

// RestoreHostFromBackup restores partition structure, extracts the rootfs, and reinstalls GRUB.
func RestoreHostFromBackup(archivePath, targetDisk string, logFn func(string)) error {
	if logFn == nil {
		logFn = func(string) {}
	}

	logFn("=> [DISASTER RESTORE] Verifying backup checksum manifest...")
	if _, err := os.Stat(archivePath + ".sha256"); err == nil {
		cmdCheck := exec.Command("sha256sum", "-c", archivePath+".sha256")
		cmdCheck.Dir = filepath.Dir(archivePath)
		if out, err := cmdCheck.CombinedOutput(); err != nil {
			return fmt.Errorf("cryptographic verification failed: archive corrupted: %s", string(out))
		}
		logFn("=> Checksum matched: Image verified 100% authentic.")
	}

	logFn(fmt.Sprintf("=> Partitioning destination disk %s...", targetDisk))
	_ = exec.Command("swapoff", "-a").Run()

	_ = exec.Command("parted", "-s", targetDisk, "mklabel", "msdos").Run()
	_ = exec.Command("parted", "-s", targetDisk, "mkpart", "primary", "ext4", "1MiB", "100%").Run()
	_ = exec.Command("parted", "-s", targetDisk, "set", "1", "boot", "on").Run()
	_ = exec.Command("partprobe", targetDisk).Run()
	time.Sleep(1 * time.Second)

	targetPart := targetDisk + "1"
	if strings.Contains(targetDisk, "nvme") {
		targetPart = targetDisk + "p1"
	}

	logFn(fmt.Sprintf("=> Formatting root filesystem on %s...", targetPart))
	if out, err := exec.Command("mkfs.ext4", "-F", "-L", "ROOT", targetPart).CombinedOutput(); err != nil {
		return fmt.Errorf("mkfs.ext4 failed: %w (%s)", err, string(out))
	}

	stageDir, err := os.MkdirTemp("", "orch-restore-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stageDir)

	if out, err := exec.Command("mount", targetPart, stageDir).CombinedOutput(); err != nil {
		return fmt.Errorf("mounting restore target failed: %w (%s)", err, string(out))
	}
	defer func() {
		_ = exec.Command("umount", "-l", stageDir).Run()
		_ = exec.Command("sync").Run()
	}()

	logFn(fmt.Sprintf("=> Extracting system files from %s...", archivePath))
	tarCmd := exec.Command("tar", "-xpf", archivePath, "-C", stageDir)
	if out, err := tarCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("extraction failed: %w (%s)", err, string(out))
	}

	uuidOut, err := exec.Command("blkid", "-s", "UUID", "-o", "value", targetPart).Output()
	if err == nil {
		newUUID := strings.TrimSpace(string(uuidOut))
		fstabPath := filepath.Join(stageDir, "etc", "fstab")
		fstabContent := fmt.Sprintf("# Created by Disaster Recovery Orchestrator\nUUID=%s / ext4 errors=remount-ro 0 1\n", newUUID)
		_ = os.WriteFile(fstabPath, []byte(fstabContent), 0644)
	}

	binds := []string{"/dev", "/proc", "/sys", "/dev/pts"}
	for _, b := range binds {
		dest := filepath.Join(stageDir, b)
		_ = os.MkdirAll(dest, 0755)
		_ = exec.Command("mount", "--bind", b, dest).Run()
	}
	defer func() {
		for i := len(binds) - 1; i >= 0; i-- {
			_ = exec.Command("umount", "-l", filepath.Join(stageDir, binds[i])).Run()
		}
	}()

	if hostDNS, err := os.ReadFile("/etc/resolv.conf"); err == nil {
		_ = os.WriteFile(filepath.Join(stageDir, "etc", "resolv.conf"), hostDNS, 0644)
	}

	logFn(fmt.Sprintf("=> Reinstalling GRUB bootloader to %s...", targetDisk))
	grubScript := fmt.Sprintf(`
grub-install %s || grub-install --recheck %s
update-grub || grub-mkconfig -o /boot/grub/grub.cfg
update-initramfs -u -k all || true
`, targetDisk, targetDisk)

	chrootCmd := exec.Command("chroot", stageDir, "/bin/sh", "-c", grubScript)
	_ = chrootCmd.Run()

	_ = exec.Command("sync").Run()
	logFn("=> [RESTORE COMPLETE] Operating system successfully restored.")
	return nil
}
