package engine

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// HostStorageProfile contains dynamically detected block geometry.
type HostStorageProfile struct {
	ParentDisk      string // e.g. /dev/sda or /dev/nvme0n1
	HostPartition   string // e.g. /dev/sda2 or /dev/nvme0n1p2
	HostPartNum     string // e.g. "2"
	TargetPartition string // e.g. /dev/sda3 or /dev/nvme0n1p3
	TargetPartNum   string // e.g. "3"
	ESPPartition    string // e.g. /dev/sda1
	FilesystemType  string // e.g. ext4, btrfs, xfs
}

// DiscoverHostStorage autonomously inspects the live host without user input.
func DiscoverHostStorage() (*HostStorageProfile, error) {
	// 1. Discover the block device mounted at root ("/")
	rootDev, fsType, err := findRootMount()
	if err != nil {
		return nil, fmt.Errorf("autonomous root discovery failed: %w", err)
	}

	// 2. Resolve symlinks (e.g. /dev/root or /dev/mapper/* -> real /dev/sdX)
	realPath, err := filepath.EvalSymlinks(rootDev)
	if err == nil {
		rootDev = realPath
	}

	// 3. Deconstruct partition number and parent disk. Uses the shared,
	// tested SplitDiskAndPartition (partition.go) rather than a second
	// hand-rolled parser — audit 1C.8 found this file's own copy mis-parsed
	// loop device partitions (/dev/loop0p1), exactly as partition.go's did
	// before being fixed; reusing one implementation keeps them from
	// drifting apart again.
	parentDisk, partNum, splitErr := SplitDiskAndPartition(rootDev)
	if splitErr != nil {
		return nil, fmt.Errorf("unable to determine parent disk for root device %s: %w", rootDev, splitErr)
	}

	// 4. Calculate next available target partition number
	hostNumInt, _ := strconv.Atoi(partNum)
	targetPartNum := strconv.Itoa(hostNumInt + 1)

	targetPart := JoinDiskAndPartition(parentDisk, targetPartNum)

	// 5. Autonomously locate the active EFI System Partition (ESP)
	espDev := findESPDevice(parentDisk)

	return &HostStorageProfile{
		ParentDisk:      parentDisk,
		HostPartition:   rootDev,
		HostPartNum:     partNum,
		TargetPartition: targetPart,
		TargetPartNum:   targetPartNum,
		ESPPartition:    espDev,
		FilesystemType:  fsType,
	}, nil
}

func findRootMount() (string, string, error) {
	// Query using findmnt if available
	out, err := exec.Command("findmnt", "-n", "-o", "SOURCE,FSTYPE", "/").Output()
	if err == nil {
		fields := strings.Fields(string(out))
		if len(fields) >= 2 {
			return fields[0], fields[1], nil
		}
		if len(fields) == 1 {
			return fields[0], "ext4", nil
		}
	}

	// Fallback to inspecting /proc/mounts
	f, err := os.Open("/proc/mounts")
	if err != nil {
		return "", "", err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 3 && fields[1] == "/" {
			return fields[0], fields[2], nil
		}
	}
	return "", "", fmt.Errorf("root mount / not found in /proc/mounts")
}

func findESPDevice(parentDisk string) string {
	// Look for /boot/efi or /boot mount in findmnt
	out, err := exec.Command("findmnt", "-n", "-o", "SOURCE", "/boot/efi").Output()
	if err == nil && len(strings.TrimSpace(string(out))) > 0 {
		return strings.TrimSpace(string(out))
	}

	// Fallback: partition 1 of parent disk.
	return JoinDiskAndPartition(parentDisk, "1")
}
