//go:build linux

package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type LinuxStorageEngine struct{}

func NewStorageEngine() StorageEngine {
	return &LinuxStorageEngine{}
}

// EnsureRequiredPackage dynamically resolves the host distribution package manager.
func EnsureRequiredPackage(pkg string, logFn func(string)) error {
	if _, err := exec.LookPath(pkg); err == nil {
		return nil
	}

	logFn(fmt.Sprintf("=> [PACKAGE MANAGER] Missing required dependency '%s'. Resolving...", pkg))

	// Debian / Ubuntu / Kali
	if _, err := exec.LookPath("apt-get"); err == nil {
		cmd := exec.Command("apt-get", "install", "-y", "-qq", pkg)
		cmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("apt-get failed: %w (%s)", err, strings.TrimSpace(string(out)))
		}
		return nil
	}

	// Fedora / RHEL 8+ / Rocky / AlmaLinux
	if _, err := exec.LookPath("dnf"); err == nil {
		cmd := exec.Command("dnf", "install", "-y", "-q", pkg)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("dnf failed: %w (%s)", err, strings.TrimSpace(string(out)))
		}
		return nil
	}

	// CentOS 7 / Older RHEL
	if _, err := exec.LookPath("yum"); err == nil {
		cmd := exec.Command("yum", "install", "-y", "-q", pkg)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("yum failed: %w (%s)", err, strings.TrimSpace(string(out)))
		}
		return nil
	}

	// Arch Linux
	if _, err := exec.LookPath("pacman"); err == nil {
		cmd := exec.Command("pacman", "-S", "--noconfirm", "--needed", pkg)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("pacman failed: %w (%s)", err, strings.TrimSpace(string(out)))
		}
		return nil
	}

	return fmt.Errorf("no supported package manager found to install %s", pkg)
}

func (e *LinuxStorageEngine) EnumerateStorage() ([]UnifiedPartition, error) {
	out, err := exec.Command("lsblk", "-J", "-b", "-o", "NAME,SIZE,TYPE,MOUNTPOINT,MODEL,RM,FSTYPE,LABEL").Output()
	if err != nil {
		return nil, fmt.Errorf("lsblk execution failed: %w", err)
	}

	type lsblkDevice struct {
		Name       string        `json:"name"`
		Size       uint64        `json:"size"`
		Type       string        `json:"type"`
		MountPoint string        `json:"mountpoint"`
		Model      string        `json:"model"`
		RM         bool          `json:"rm"`
		FSType     string        `json:"fstype"`
		Label      string        `json:"label"`
		Children   []lsblkDevice `json:"children,omitempty"`
	}

	var parsed struct {
		BlockDevices []lsblkDevice `json:"blockdevices"`
	}

	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, fmt.Errorf("parsing lsblk output: %w", err)
	}

	var results []UnifiedPartition
	var walk func(d lsblkDevice, parentDisk string)

	walk = func(d lsblkDevice, parentDisk string) {
		currentDisk := parentDisk
		if d.Type == "disk" {
			currentDisk = "/dev/" + d.Name
		}

		if len(d.Children) > 0 {
			for _, child := range d.Children {
				walk(child, currentDisk)
			}
			return
		}

		if d.Type == "part" || (d.Type == "disk" && d.FSType != "") {
			devPath := "/dev/" + d.Name
			results = append(results, UnifiedPartition{
				DiskID:       currentDisk,
				PartitionID:  devPath,
				Label:        d.Label,
				FileSystem:   d.FSType,
				SizeBytes:    d.Size,
				MountPoint:   d.MountPoint,
				IsSystemRoot: d.MountPoint == "/",
				IsRemovable:  d.RM,
			})
		}
	}

	for _, dev := range parsed.BlockDevices {
		walk(dev, "/dev/"+dev.Name)
	}

	return results, nil
}

func (e *LinuxStorageEngine) DetectActiveRoot() (UnifiedPartition, error) {
	partitions, err := e.EnumerateStorage()
	if err != nil {
		return UnifiedPartition{}, err
	}
	for _, p := range partitions {
		if p.IsSystemRoot {
			return p, nil
		}
	}
	return UnifiedPartition{}, fmt.Errorf("unable to locate root mount point")
}

func (e *LinuxStorageEngine) ShrinkVolume(partitionID string, shrinkSizeBytes uint64, logFn func(string)) error {
	logFn(fmt.Sprintf("=> [LINUX ENGINE] Shrinking partition %s by %d bytes...", partitionID, shrinkSizeBytes))

	cmdCheck := exec.Command("e2fsck", "-f", "-y", partitionID)
	_ = cmdCheck.Run()

	logFn("=> Resizing filesystem boundaries...")
	cmdResize := exec.Command("resize2fs", partitionID)
	if out, err := cmdResize.CombinedOutput(); err != nil {
		return fmt.Errorf("filesystem resize failed: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (e *LinuxStorageEngine) CreatePartition(diskID string, sizeBytes uint64, fsType string, label string, logFn func(string)) (string, error) {
	logFn(fmt.Sprintf("=> [LINUX ENGINE] Partitioning disk %s (Filesystem: %s)...", diskID, fsType))
	_ = exec.Command("partprobe", diskID).Run()

	sizeMiB := sizeBytes / (1024 * 1024)
	cmdPart := exec.Command("parted", "-s", "-a", "optimal", diskID, "mkpart", "primary", fsType, "0%", fmt.Sprintf("%dMiB", sizeMiB))
	if out, err := cmdPart.CombinedOutput(); err != nil {
		return "", fmt.Errorf("parted failed: %w (%s)", err, strings.TrimSpace(string(out)))
	}

	_ = exec.Command("partprobe", diskID).Run()
	time.Sleep(1 * time.Second)

	targetPart := diskID + "2"
	if strings.Contains(diskID, "nvme") {
		targetPart = diskID + "p2"
	}

	mkfsCmd := "mkfs.ext4"
	if fsType == "vfat" || fsType == "fat32" {
		mkfsCmd = "mkfs.vfat"
	}

	cmdFmt := exec.Command(mkfsCmd, "-F", targetPart)
	if out, err := cmdFmt.CombinedOutput(); err != nil {
		return "", fmt.Errorf("formatting %s failed: %w (%s)", targetPart, err, strings.TrimSpace(string(out)))
	}

	return targetPart, nil
}

func (e *LinuxStorageEngine) MountVolume(devPath string, logFn func(string)) (string, func(), error) {
	return AutoMountDrive(devPath, logFn)
}

func (e *LinuxStorageEngine) RegisterBootEntry(title string, efiRelativePath string, diskID string, partNum uint, logFn func(string)) error {
	logFn(fmt.Sprintf("=> [LINUX ENGINE] Registering UEFI NVRAM boot entry '%s'...", title))

	if _, err := exec.LookPath("efibootmgr"); err != nil {
		if installErr := EnsureRequiredPackage("efibootmgr", logFn); installErr != nil {
			return installErr
		}
	}

	cmd := exec.Command("efibootmgr", "-c", "-d", diskID, "-p", strconv.Itoa(int(partNum)), "-L", title, "-l", efiRelativePath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("efibootmgr failed: %w (%s)", err, strings.TrimSpace(string(out)))
	}

	grubCfgCandidates := []string{
		"/boot/grub/grub.cfg",
		"/boot/grub2/grub.cfg",
		"/boot/efi/EFI/fedora/grub.cfg",
		"/boot/efi/EFI/centos/grub.cfg",
		"/boot/efi/EFI/redhat/grub.cfg",
	}

	for _, cfg := range grubCfgCandidates {
		if _, err := os.Stat(cfg); err == nil {
			logFn(fmt.Sprintf("=> Updating bootloader config at %s...", cfg))
			if _, lookErr := exec.LookPath("update-grub"); lookErr == nil {
				_ = exec.Command("update-grub").Run()
			} else if _, lookErr2 := exec.LookPath("grub2-mkconfig"); lookErr2 == nil {
				_ = exec.Command("grub2-mkconfig", "-o", cfg).Run()
			}
			break
		}
	}

	return nil
}
