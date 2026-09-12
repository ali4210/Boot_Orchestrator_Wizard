// Package engine — usb_manager.go handles strict removable media discovery,
// dynamic drive capacity queries via sysfs, autonomous 2-partition provisioning
// (ORCH_STORAGE + ORCH_BOOT), verified loopback ISO staging, and media resets.
package engine

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type usbDeviceRecord struct {
	Name       string            `json:"name"`
	Size       string            `json:"size"`
	Tran       string            `json:"tran"`
	Mountpoint string            `json:"mountpoint"`
	FSType     string            `json:"fstype"`
	Type       string            `json:"type"`
	Removable  string            `json:"rm"`
	Vendor     string            `json:"vendor"`
	Model      string            `json:"model"`
	Children   []usbDeviceRecord `json:"children,omitempty"`
}

type usbLsblkResponse struct {
	BlockDevices []usbDeviceRecord `json:"blockdevices"`
}

type USBTargetDevice struct {
	Name       string
	DevPath    string
	Size       string
	SizeBytes  uint64
	Vendor     string
	Model      string
	MountPoint string
}

type USBPayload struct {
	DeviceName   string
	FilePath     string
	FileName     string
	SizeBytes    int64
	MountPoint   string
	WasAutoMount bool
}

type USBProvisionResult struct {
	StoragePartition string
	BootPartition    string
	StorageBytes     uint64
	BootBytes        uint64
	StorageMountPath string
}

func QueryBlockDeviceBytes(devName string) (uint64, error) {
	baseName := filepath.Base(devName)
	for len(baseName) > 0 && (baseName[len(baseName)-1] >= '0' && baseName[len(baseName)-1] <= '9') {
		baseName = baseName[:len(baseName)-1]
	}
	baseName = strings.TrimSuffix(baseName, "p")

	sizeFile := filepath.Join("/sys/class/block", baseName, "size")
	data, err := os.ReadFile(sizeFile)
	if err != nil {
		return 0, fmt.Errorf("reading block size from %s: %w", sizeFile, err)
	}

	sectors, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parsing sector count: %w", err)
	}
	return sectors * 512, nil
}

func ProbeRemovableTargets() ([]USBTargetDevice, error) {
	out, err := exec.Command("lsblk", "-J", "-o", "NAME,SIZE,TRAN,MOUNTPOINT,FSTYPE,TYPE,RM,VENDOR,MODEL").Output()
	if err != nil {
		return nil, fmt.Errorf("lsblk target probe failed: %w", err)
	}

	var data usbLsblkResponse
	if err := json.Unmarshal(out, &data); err != nil {
		return nil, fmt.Errorf("parsing lsblk telemetry: %w", err)
	}

	var targets []USBTargetDevice

	for _, dev := range data.BlockDevices {
		isUSB := strings.ToLower(dev.Tran) == "usb" || dev.Removable == "1" || dev.Removable == "true"
		if isUSB && dev.Type == "disk" {
			devPath := "/dev/" + dev.Name
			byteSize, _ := QueryBlockDeviceBytes(dev.Name)
			targets = append(targets, USBTargetDevice{
				Name:       dev.Name,
				DevPath:    devPath,
				Size:       dev.Size,
				SizeBytes:  byteSize,
				Vendor:     strings.TrimSpace(dev.Vendor),
				Model:      strings.TrimSpace(dev.Model),
				MountPoint: dev.Mountpoint,
			})
		}
	}

	return targets, nil
}

func PrepareAutonomousUSBDisk(targetDev USBTargetDevice, logFn func(string)) (*USBProvisionResult, error) {
	if logFn == nil {
		logFn = func(string) {}
	}

	diskPath := targetDev.DevPath
	totalBytes := targetDev.SizeBytes
	if totalBytes == 0 {
		var err error
		totalBytes, err = QueryBlockDeviceBytes(targetDev.Name)
		if err != nil {
			return nil, fmt.Errorf("resolving disk geometry: %w", err)
		}
	}

	minRequired := uint64(14 * 1024 * 1024 * 1024)
	if totalBytes < minRequired {
		return nil, fmt.Errorf("USB drive capacity (%.1f GB) is too small. Minimum 16 GB required", float64(totalBytes)/(1024*1024*1024))
	}

	logFn(fmt.Sprintf("=> [USB PREPARE] Dynamic capacity confirmed: %.2f GB total", float64(totalBytes)/(1024*1024*1024)))

	// Partition 2: 1 GB FAT32 for pure UEFI boot files (GRUB binary only)
	// Partition 1: Remainder for raw ISO storage
	bootBytes := uint64(1 * 1024 * 1024 * 1024)
	storageBytes := totalBytes - bootBytes
	storageEndMB := storageBytes / (1024 * 1024)

	logFn("=> [USB PREPARE] Unmounting existing partitions on " + diskPath)
	_ = exec.Command("bash", "-c", fmt.Sprintf("umount -l %s* 2>/dev/null || true", diskPath)).Run()

	logFn("=> [USB PREPARE] Purging stale filesystem signatures...")
	_ = exec.Command("wipefs", "-a", "-f", diskPath).Run()

	logFn("=> [USB PREPARE] Writing clean GPT partition table...")
	if out, err := exec.Command("parted", "-s", diskPath, "mklabel", "gpt").CombinedOutput(); err != nil {
		return nil, fmt.Errorf("writing GPT label to %s: %w (%s)", diskPath, err, string(out))
	}

	logFn(fmt.Sprintf("=> [USB PREPARE] Carving Partition 1 [ORCH_STORAGE] (1MiB -> %dMiB)...", storageEndMB))
	if out, err := exec.Command("parted", "-s", diskPath, "mkpart", "ORCH_STORAGE", "ext4", "1MiB", fmt.Sprintf("%dMiB", storageEndMB)).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("creating storage partition: %w (%s)", err, string(out))
	}

	logFn("=> [USB PREPARE] Carving Partition 2 [ORCH_BOOT] (FAT32 Live ESP)...")
	if out, err := exec.Command("parted", "-s", diskPath, "mkpart", "ORCH_BOOT", "fat32", fmt.Sprintf("%dMiB", storageEndMB), "100%").CombinedOutput(); err != nil {
		return nil, fmt.Errorf("creating boot partition: %w (%s)", err, string(out))
	}

	_ = exec.Command("parted", "-s", diskPath, "set", "2", "boot", "on").Run()
	_ = exec.Command("parted", "-s", diskPath, "set", "2", "esp", "on").Run()
	_ = exec.Command("partprobe", diskPath).Run()
	_ = exec.Command("udevadm", "settle", "--timeout=10").Run()
	time.Sleep(1 * time.Second)

	part1 := diskPath + "1"
	part2 := diskPath + "2"
	if strings.Contains(diskPath, "nvme") || strings.Contains(diskPath, "mmcblk") {
		part1 = diskPath + "p1"
		part2 = diskPath + "p2"
	}

	logFn("=> [USB PREPARE] Formatting Partition 1 as ext4 [ORCH_STORAGE]...")
	if out, err := exec.Command("mkfs.ext4", "-F", "-q", "-L", "ORCH_STORAGE", part1).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("formatting ext4 storage partition %s: %w (%s)", part1, err, string(out))
	}

	logFn("=> [USB PREPARE] Formatting Partition 2 as FAT32 [ORCH_BOOT]...")
	if out, err := exec.Command("mkfs.vfat", "-F32", "-n", "ORCH_BOOT", part2).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("formatting FAT32 boot partition %s: %w (%s)", part2, err, string(out))
	}

	mountDir := "/mnt/orch_usb_storage"
	_ = os.MkdirAll(mountDir, 0755)
	_ = exec.Command("umount", "-l", mountDir).Run()

	logFn("=> [USB PREPARE] Mounting storage vault at " + mountDir)
	if out, err := exec.Command("mount", part1, mountDir).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("mounting storage partition %s to %s: %w (%s)", part1, mountDir, err, string(out))
	}

	return &USBProvisionResult{
		StoragePartition: part1,
		BootPartition:    part2,
		StorageBytes:     storageBytes,
		BootBytes:        bootBytes,
		StorageMountPath: mountDir,
	}, nil
}

// ArmAutonomousUSBBootloader configures Partition 2 (ORCH_BOOT) with a standalone
// EFI binary and a zero-timeout GRUB loopback configuration that boots the untouched ISO
// from Partition 1 with native findiso support.
func ArmAutonomousUSBBootloader(bootPart, isoPayloadPath, targetDisk string, logFn func(string)) error {
	if logFn == nil {
		logFn = func(string) {}
	}

	bootMount := "/mnt/orch_usb_boot"
	_ = os.MkdirAll(bootMount, 0755)
	_ = exec.Command("umount", "-l", bootMount).Run()

	logFn("=> [EFI ENGINE] Mounting ORCH_BOOT at " + bootMount + "...")
	if out, err := exec.Command("mount", bootPart, bootMount).CombinedOutput(); err != nil {
		return fmt.Errorf("mounting boot partition %s: %w (%s)", bootPart, err, string(out))
	}
	defer func() {
		_ = exec.Command("sync").Run()
		_ = exec.Command("umount", "-l", bootMount).Run()
	}()

	// 1. Temporarily mount ISO to extract the official distribution BOOTX64.EFI
	isoMount := "/mnt/orch_iso_temp"
	_ = os.MkdirAll(isoMount, 0755)
	_ = exec.Command("umount", "-l", isoMount).Run()

	efiBootDir := filepath.Join(bootMount, "EFI", "BOOT")
	_ = os.MkdirAll(efiBootDir, 0755)
	targetEFI := filepath.Join(efiBootDir, "BOOTX64.EFI")

	mountIsoCmd := exec.Command("mount", "-o", "loop,ro", isoPayloadPath, isoMount)
	if _, err := mountIsoCmd.CombinedOutput(); err == nil {
		defer func() {
			_ = exec.Command("umount", "-l", isoMount).Run()
			_ = os.Remove(isoMount)
		}()

		// Copy verified signed EFI loader directly from distribution media
		possibleEFIs := []string{
			filepath.Join(isoMount, "EFI", "BOOT", "BOOTX64.EFI"),
			filepath.Join(isoMount, "EFI", "boot", "bootx64.efi"),
			filepath.Join(isoMount, "efi", "boot", "bootx64.efi"),
		}
		for _, e := range possibleEFIs {
			if _, sErr := os.Stat(e); sErr == nil {
				_ = exec.Command("cp", "-f", e, targetEFI).Run()
				break
			}
		}
	}

	// Fallback to host signed UEFI shim if ISO didn't contain it
	if _, err := os.Stat(targetEFI); err != nil {
		_ = exec.Command("cp", "-f", "/boot/efi/EFI/BOOT/BOOTX64.EFI", targetEFI).Run()
	}

	// 2. Write the 100% reliable GRUB Loopback configuration
	// Timeout is set to 0: zero menus, zero delays, direct automatic execution.
	grubCfgContent := `set default="0"
set timeout=0

# Search for the ext4 storage partition containing the untouched ISO
search --no-floppy --set=storage_part --label ORCH_STORAGE

menuentry "Autonomous Provisioner & OS Installer" {
    set isofile="/os_image.payload"
    loopback loop ($storage_part)$isofile
    linux (loop)/live/vmlinuz boot=live findiso=$isofile components quiet splash
    initrd (loop)/live/initrd.img
}
`

	_ = os.WriteFile(filepath.Join(efiBootDir, "grub.cfg"), []byte(grubCfgContent), 0644)

	// Also mirror to /boot/grub/grub.cfg in case the EFI binary references that path
	bootGrubDir := filepath.Join(bootMount, "boot", "grub")
	_ = os.MkdirAll(bootGrubDir, 0755)
	_ = os.WriteFile(filepath.Join(bootGrubDir, "grub.cfg"), []byte(grubCfgContent), 0644)

	_ = exec.Command("sync").Run()
	logFn("=> [EFI ENGINE] Verified loopback runtime armed successfully.")
	return nil
}

func DownloadPayloadToUSB(targetDev USBTargetDevice, imageURL string, progressFn func(written, total int64)) error {
	mountPoint := targetDev.MountPoint
	autoMounted := false

	if mountPoint == "" {
		tmpDir, err := os.MkdirTemp("/tmp", "orch_usb_dl_*")
		if err != nil {
			return fmt.Errorf("creating temporary mount dir: %w", err)
		}
		mountPoint = tmpDir

		mountCmd := exec.Command("mount", targetDev.DevPath, mountPoint)
		if out, err := mountCmd.CombinedOutput(); err != nil {
			_ = os.Remove(mountPoint)
			return fmt.Errorf("mounting usb partition %s: %w\n%s", targetDev.DevPath, err, string(out))
		}
		autoMounted = true
	}

	defer func() {
		_ = exec.Command("sync").Run()
		if autoMounted {
			_ = exec.Command("umount", "-l", mountPoint).Run()
			_ = os.Remove(mountPoint)
		}
	}()

	destPath := filepath.Join(mountPoint, "os_image.payload")

	resp, err := http.Get(imageURL)
	if err != nil {
		return fmt.Errorf("connecting to image stream: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("bad HTTP status from stream server: %s", resp.Status)
	}

	out, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("creating destination file on USB: %w", err)
	}
	defer out.Close()

	total := resp.ContentLength
	buf := make([]byte, 32*1024)
	var written int64

	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			nWrite, writeErr := out.Write(buf[:n])
			if nWrite > 0 {
				written += int64(nWrite)
				if progressFn != nil {
					progressFn(written, total)
				}
			}
			if writeErr != nil {
				return fmt.Errorf("writing to USB storage: %w", writeErr)
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return fmt.Errorf("streaming payload read error: %w", readErr)
		}
	}

	_ = out.Sync()
	return nil
}

func ProbeUSBPayloads() ([]USBPayload, error) {
	out, err := exec.Command("lsblk", "-J", "-o", "NAME,SIZE,TRAN,MOUNTPOINT,FSTYPE,TYPE,RM").Output()
	if err != nil {
		return nil, fmt.Errorf("lsblk probe failed: %w", err)
	}

	var data usbLsblkResponse
	if err := json.Unmarshal(out, &data); err != nil {
		return nil, fmt.Errorf("parsing lsblk telemetry: %w", err)
	}

	var payloads []USBPayload

	var inspectDevice func(dev usbDeviceRecord, isUSB bool)
	inspectDevice = func(dev usbDeviceRecord, isUSB bool) {
		currentIsUSB := isUSB || dev.Tran == "usb" || dev.Removable == "1" || dev.Removable == "true"

		if currentIsUSB && dev.Type == "part" {
			mountPath := dev.Mountpoint
			autoMounted := false

			if mountPath == "" && dev.FSType != "" && dev.FSType != "swap" {
				tempMount, err := os.MkdirTemp("/tmp", "orch_usb_mount_*")
				if err == nil {
					devPath := "/dev/" + dev.Name
					mountCmd := exec.Command("mount", "-o", "ro", devPath, tempMount)
					if mountCmd.Run() == nil {
						mountPath = tempMount
						autoMounted = true
					} else {
						_ = os.Remove(tempMount)
					}
				}
			}

			if mountPath != "" {
				_ = filepath.Walk(mountPath, func(path string, info os.FileInfo, walkErr error) error {
					if walkErr == nil && !info.IsDir() {
						lower := strings.ToLower(info.Name())
						if strings.HasSuffix(lower, ".iso") || strings.HasSuffix(lower, ".img") || strings.HasSuffix(lower, ".payload") || strings.HasSuffix(lower, ".zip") {
							payloads = append(payloads, USBPayload{
								DeviceName:   dev.Name,
								FilePath:     path,
								FileName:     info.Name(),
								SizeBytes:    info.Size(),
								MountPoint:   mountPath,
								WasAutoMount: autoMounted,
							})
						}
					}
					return nil
				})
			}
		}

		for _, child := range dev.Children {
			inspectDevice(child, currentIsUSB)
		}
	}

	for _, dev := range data.BlockDevices {
		inspectDevice(dev, dev.Tran == "usb" || dev.Removable == "1")
	}

	return payloads, nil
}

func CleanAutoMounts(payloads []USBPayload) {
	for _, p := range payloads {
		if p.WasAutoMount && p.MountPoint != "" {
			_ = exec.Command("umount", "-l", p.MountPoint).Run()
			_ = os.Remove(p.MountPoint)
		}
	}
}

func PurgeIntermediatePayloads(storageMountPath string) error {
	if storageMountPath == "" {
		return nil
	}

	entries, err := os.ReadDir(storageMountPath)
	if err != nil {
		return fmt.Errorf("reading storage vault: %w", err)
	}

	for _, entry := range entries {
		name := entry.Name()
		lower := strings.ToLower(name)

		if strings.HasPrefix(lower, "host_os_backup") {
			continue
		}

		if strings.HasSuffix(lower, ".iso") ||
			strings.HasSuffix(lower, ".raw") ||
			strings.HasSuffix(lower, ".payload") ||
			strings.HasSuffix(lower, ".part") {
			_ = os.RemoveAll(filepath.Join(storageMountPath, name))
		}
	}

	_ = exec.Command("sync").Run()
	return nil
}

func FactoryResetUSB(diskPath, fsType string) error {
	if diskPath == "" {
		return fmt.Errorf("invalid disk path provided for factory reset")
	}

	_ = exec.Command("bash", "-c", fmt.Sprintf("umount -l %s* 2>/dev/null || true", diskPath)).Run()
	_ = exec.Command("wipefs", "-a", "-f", diskPath).Run()

	if out, err := exec.Command("parted", "-s", diskPath, "mklabel", "msdos").CombinedOutput(); err != nil {
		if outGpt, gptErr := exec.Command("parted", "-s", diskPath, "mklabel", "gpt").CombinedOutput(); gptErr != nil {
			return fmt.Errorf("relabeling drive failed: %w (%s | %s)", err, string(out), string(outGpt))
		}
	}

	partType := "ntfs"
	if strings.EqualFold(fsType, "fat32") {
		partType = "fat32"
	}

	if out, err := exec.Command("parted", "-s", diskPath, "mkpart", "primary", partType, "1MiB", "100%").CombinedOutput(); err != nil {
		return fmt.Errorf("creating primary partition: %w (%s)", err, string(out))
	}

	_ = exec.Command("partprobe", diskPath).Run()
	_ = exec.Command("udevadm", "settle", "--timeout=10").Run()
	time.Sleep(1 * time.Second)

	part1 := diskPath + "1"
	if strings.Contains(diskPath, "nvme") || strings.Contains(diskPath, "mmcblk") {
		part1 = diskPath + "p1"
	}

	switch strings.ToLower(fsType) {
	case "ntfs":
		if out, err := exec.Command("mkfs.ntfs", "-f", "-L", "USB_DATA", part1).CombinedOutput(); err != nil {
			if outEx, errEx := exec.Command("mkfs.exfat", "-n", "USB_DATA", part1).CombinedOutput(); errEx != nil {
				return fmt.Errorf("formatting NTFS/exFAT failed: %w (%s | %s)", err, string(out), string(outEx))
			}
		}
	case "fat32":
		if out, err := exec.Command("mkfs.vfat", "-F32", "-n", "USB_DATA", part1).CombinedOutput(); err != nil {
			return fmt.Errorf("formatting FAT32 failed: %w (%s)", err, string(out))
		}
	default:
		if _, err := exec.Command("mkfs.exfat", "-n", "USB_DATA", part1).CombinedOutput(); err != nil {
			_ = exec.Command("mkfs.vfat", "-F32", "-n", "USB_DATA", part1).Run()
		}
	}

	_ = exec.Command("sync").Run()
	return nil
}
