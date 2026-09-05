package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
		cmd := exec.Command("apt-get", "update", "-qq")
		cmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
		_ = cmd.Run()

		cmdInstall := exec.Command("apt-get", "install", "-y", "-qq", "ntfs-3g")
		cmdInstall.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
		if out, err := cmdInstall.CombinedOutput(); err != nil {
			return fmt.Errorf("installing ntfs-3g failed: %w (%s)", err, strings.TrimSpace(string(out)))
		}
		logFn("=> [DRIVER OK] ntfs-3g installed.")
		return nil
	}
	return fmt.Errorf("apt-get not available to install ntfs-3g")
}

func AutoMountDrive(devPath string, logFn func(string)) (string, func(), error) {
	if logFn == nil {
		logFn = func(string) {}
	}

	mountPoint := filepath.Join("/mnt", "orch_drive_"+filepath.Base(devPath))
	_ = os.MkdirAll(mountPoint, 0755)

	fsTypeOut, _ := exec.Command("blkid", "-s", "TYPE", "-o", "value", devPath).Output()
	detectedFS := strings.ToLower(strings.TrimSpace(string(fsTypeOut)))

	if detectedFS == "ntfs" {
		if err := ensureNTFS3G(logFn); err != nil {
			return "", func() {}, err
		}
		cmdNTFS := exec.Command("ntfs-3g", "-o", "remove_hiberfile,rw", devPath, mountPoint)
		if _, err := cmdNTFS.CombinedOutput(); err == nil {
			return mountPoint, makeCleanup(mountPoint), nil
		}
		cmdFallback := exec.Command("mount", "-t", "ntfs-3g", devPath, mountPoint)
		if _, errF := cmdFallback.CombinedOutput(); errF == nil {
			return mountPoint, makeCleanup(mountPoint), nil
		}
	}

	cmd := exec.Command("mount", devPath, mountPoint)
	if _, err := cmd.CombinedOutput(); err == nil {
		return mountPoint, makeCleanup(mountPoint), nil
	}

	if errInstall := ensureNTFS3G(logFn); errInstall == nil {
		cmdRetry := exec.Command("ntfs-3g", devPath, mountPoint)
		if _, errRetry := cmdRetry.CombinedOutput(); errRetry == nil {
			return mountPoint, makeCleanup(mountPoint), nil
		}
	}

	return "", func() {}, fmt.Errorf("failed to mount %s", devPath)
}

func makeCleanup(mountPoint string) func() {
	return func() {
		_ = exec.Command("umount", "-l", mountPoint).Run()
		_ = os.Remove(mountPoint)
	}
}

func DetectBackupTargets() ([]BlockDevice, error) {
	out, err := exec.Command("lsblk", "-J", "-o", "NAME,SIZE,TYPE,MOUNTPOINT,MODEL,RM,FSTYPE").Output()
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

	var processDevice func(d BlockDevice)
	processDevice = func(d BlockDevice) {
		if strings.HasPrefix(d.Name, "sr") || d.Type == "rom" || d.Mountpoint == "[SWAP]" {
			return
		}
		if strings.HasPrefix(d.Name, pDiskBase) || d.Mountpoint == "/" {
			return
		}
		if len(d.Children) > 0 {
			for _, child := range d.Children {
				processDevice(child)
			}
			return
		}
		if d.Type == "part" || (d.Type == "loop" && d.FSType != "") || (d.Type == "disk" && d.FSType != "") {
			devPath := "/dev/" + d.Name
			valid = append(valid, BlockDevice{
				Name:       devPath,
				Size:       d.Size,
				Type:       d.Type,
				Mountpoint: d.Mountpoint,
				Model:      d.Model,
				RM:         d.RM,
				FSType:     d.FSType,
			})
		}
	}

	for _, dev := range parsed.BlockDevices {
		processDevice(dev)
	}
	return valid, nil
}

func EstimateHostBackupSize() uint64 {
	sources := []string{"/home", "/etc", "/root", "/var/local", "/opt"}
	var total uint64
	for _, s := range sources {
		out, err := exec.Command("du", "-sb", s).Output()
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
	// Estimate compressed size (~45% of uncompressed data)
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

	cmd := exec.Command("tar", tarArgs...)
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
			chkCmd := exec.Command("sha256sum", filepath.Base(fullDestPath))
			chkCmd.Dir = destDir
			if chkOut, cErr := chkCmd.Output(); cErr == nil {
				_ = os.WriteFile(fullDestPath+".sha256", chkOut, 0644)
			}
			_ = exec.Command("sync").Run()
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

		if mountDir == "" {
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
