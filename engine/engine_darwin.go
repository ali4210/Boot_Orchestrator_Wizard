//go:build darwin

package engine

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

type DarwinStorageEngine struct{}

func NewStorageEngine() StorageEngine {
	return &DarwinStorageEngine{}
}

func (e *DarwinStorageEngine) EnumerateStorage() ([]UnifiedPartition, error) {
	cmd := exec.Command("diskutil", "list")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("running diskutil list: %w", err)
	}

	lines := strings.Split(string(out), "\n")
	var partitions []UnifiedPartition
	currentDisk := ""

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "/dev/disk") {
			parts := strings.Fields(trimmed)
			if len(parts) > 0 {
				currentDisk = parts[0]
			}
			continue
		}

		fields := strings.Fields(trimmed)
		if len(fields) >= 5 && strings.Contains(fields[len(fields)-1], "disk") {
			node := "/dev/" + fields[len(fields)-1]
			
			infoCmd := exec.Command("diskutil", "info", node)
			infoOut, infoErr := infoCmd.Output()
			if infoErr != nil {
				continue
			}

			infoStr := string(infoOut)
			fsType := parseDiskutilField(infoStr, "File System Personality:")
			if fsType == "" {
				fsType = parseDiskutilField(infoStr, "Type (Bundle):")
			}
			volName := parseDiskutilField(infoStr, "Volume Name:")
			mountPoint := parseDiskutilField(infoStr, "Mount Point:")
			sizeBytes := parseDiskutilSizeBytes(infoStr)

			isRoot := (mountPoint == "/")

			partitions = append(partitions, UnifiedPartition{
				DiskID:       currentDisk,
				PartitionID:  node,
				Label:        volName,
				FileSystem:   fsType,
				SizeBytes:    sizeBytes,
				MountPoint:   mountPoint,
				IsSystemRoot: isRoot,
				IsRemovable:  strings.Contains(infoStr, "Removable Media: Removable"),
			})
		}
	}

	return partitions, nil
}

func (e *DarwinStorageEngine) DetectActiveRoot() (UnifiedPartition, error) {
	partitions, err := e.EnumerateStorage()
	if err != nil {
		return UnifiedPartition{}, err
	}
	for _, p := range partitions {
		if p.IsSystemRoot {
			return p, nil
		}
	}
	return UnifiedPartition{}, fmt.Errorf("macOS root container / not found")
}

func (e *DarwinStorageEngine) ShrinkVolume(partitionID string, shrinkSizeBytes uint64, logFn func(string)) error {
	logFn(fmt.Sprintf("=> [MACOS ENGINE] Resizing APFS Container %s...", partitionID))

	infoCmd := exec.Command("diskutil", "info", partitionID)
	infoOut, err := infoCmd.Output()
	if err != nil {
		return fmt.Errorf("reading partition info: %w", err)
	}

	currSize := parseDiskutilSizeBytes(string(infoOut))
	if currSize <= shrinkSizeBytes {
		return fmt.Errorf("insufficient space to shrink APFS container")
	}

	targetBytes := currSize - shrinkSizeBytes
	targetGB := targetBytes / (1024 * 1024 * 1024)

	cmd := exec.Command("diskutil", "apfs", "resizeContainer", partitionID, fmt.Sprintf("%dg", targetGB))
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("resizing APFS container failed: %w (%s)", err, strings.TrimSpace(string(out)))
	}

	return nil
}

func (e *DarwinStorageEngine) CreatePartition(diskID string, sizeBytes uint64, fsType string, label string, logFn func(string)) (string, error) {
	logFn(fmt.Sprintf("=> [MACOS ENGINE] Allocating partition on %s (%s)...", diskID, fsType))

	sizeGB := sizeBytes / (1024 * 1024 * 1024)
	if sizeGB == 0 {
		sizeGB = 1
	}

	targetFormat := "MS-DOS FAT32"
	if strings.EqualFold(fsType, "apfs") {
		targetFormat = "APFS"
	} else if strings.EqualFold(fsType, "exfat") {
		targetFormat = "ExFAT"
	}

	cmd := exec.Command("diskutil", "addPartition", diskID, targetFormat, label, fmt.Sprintf("%dG", sizeGB))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("diskutil addPartition failed: %w (%s)", err, strings.TrimSpace(string(out)))
	}

	outStr := string(out)
	var createdNode string
	lines := strings.Split(outStr, "\n")
	for _, l := range lines {
		if strings.Contains(l, "/dev/disk") {
			fields := strings.Fields(l)
			for _, f := range fields {
				if strings.HasPrefix(f, "/dev/disk") {
					createdNode = f
				}
			}
		}
	}

	if createdNode == "" {
		createdNode = diskID + "s3"
	}

	return createdNode, nil
}

func (e *DarwinStorageEngine) MountVolume(devPath string, logFn func(string)) (string, func(), error) {
	logFn(fmt.Sprintf("=> [MACOS ENGINE] Mounting %s...", devPath))

	cmd := exec.Command("diskutil", "mount", devPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", func() {}, fmt.Errorf("mounting %s failed: %w (%s)", devPath, err, strings.TrimSpace(string(out)))
	}

	infoCmd := exec.Command("diskutil", "info", devPath)
	infoOut, _ := infoCmd.Output()
	mp := parseDiskutilField(string(infoOut), "Mount Point:")
	if mp == "" {
		mp = "/Volumes/" + devPath
	}

	cleanup := func() {
		_ = exec.Command("diskutil", "unmount", devPath).Run()
	}

	return mp, cleanup, nil
}

func (e *DarwinStorageEngine) RegisterBootEntry(title string, efiRelativePath string, diskID string, partNum uint, logFn func(string)) error {
	logFn(fmt.Sprintf("=> [MACOS BLESS] Registering EFI boot entry: %s...", title))

	targetEFI := fmt.Sprintf("%ss%d", diskID, partNum)
	_ = exec.Command("diskutil", "mount", targetEFI).Run()
	
	infoCmd := exec.Command("diskutil", "info", targetEFI)
	infoOut, _ := infoCmd.Output()
	mountPoint := parseDiskutilField(string(infoOut), "Mount Point:")
	if mountPoint == "" {
		mountPoint = "/Volumes/EFI"
	}

	fullEfiFile := mountPoint + "/" + strings.TrimPrefix(efiRelativePath, "\\")
	fullEfiFile = strings.ReplaceAll(fullEfiFile, "\\", "/")

	cmd := exec.Command("/usr/sbin/bless", "--mount", mountPoint, "--setBoot", "--file", fullEfiFile)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("bless failed: %w (%s)", err, strings.TrimSpace(string(out)))
	}

	logFn(fmt.Sprintf("=> [MACOS BLESS] Successfully blessed %s", fullEfiFile))
	return nil
}

func parseDiskutilField(infoText, fieldName string) string {
	lines := strings.Split(infoText, "\n")
	for _, l := range lines {
		if strings.Contains(l, fieldName) {
			parts := strings.Split(l, ":")
			if len(parts) >= 2 {
				return strings.TrimSpace(strings.Join(parts[1:], ":"))
			}
		}
	}
	return ""
}

func parseDiskutilSizeBytes(infoText string) uint64 {
	lines := strings.Split(infoText, "\n")
	for _, l := range lines {
		if strings.Contains(l, "Disk Size:") || strings.Contains(l, "Container Total Space:") || strings.Contains(l, "Volume Total Space:") {
			if idx := strings.Index(l, "("); idx != -1 {
				rest := l[idx+1:]
				if endIdx := strings.Index(rest, "Bytes"); endIdx != -1 {
					numStr := strings.TrimSpace(strings.ReplaceAll(rest[:endIdx], " ", ""))
					numStr = strings.ReplaceAll(numStr, ",", "")
					if val, err := strconv.ParseUint(numStr, 10, 64); err == nil {
						return val
					}
				}
			}
		}
	}
	return 0
}
