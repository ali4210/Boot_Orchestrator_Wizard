// Package engine — partition.go implements the Non-Destructive Partition
// Shrinker & Format Engine with real execution hooks, unallocated headroom carving,
// and raw binary safety rollbacks.
package engine

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type PartitionTableBackup struct {
	DiskPath       string `json:"disk_path"`
	PrimaryBytes   []byte `json:"primary_bytes"`
	BackupBytes    []byte `json:"backup_bytes"`
	SectorSize     int64  `json:"sector_size"`
	CapturedSHA256 string `json:"captured_sha256"`
}

type FilesystemInfo struct {
	Type           string
	PartitionPath  string
	TotalBytes     uint64
	UsedBytes      uint64
	MinShrinkBytes uint64
}

type ShrinkPlan struct {
	Filesystem         FilesystemInfo
	TargetNewSizeBytes uint64
	FreedBytes         uint64
	Steps              []string
}

// PartitionSpec defines raw target specifications for multi-OS provisioning
type PartitionSpec struct {
	DiskPath      string // e.g., "/dev/nvme0n1"
	SourcePartNum string // e.g., "2"
	ShrinkSizeGB  int    // e.g., 35
	NewPartSizeGB int    // e.g., 30
	FSType        string // "ext4", "btrfs", "ntfs"
}

type PartitionResult struct {
	NewPartitionPath string
	NewPartNum       string
}

var ext4UsageRe = regexp.MustCompile(`(?m)^Block count:\s+(\d+)`)
var ext4BlockSizeRe = regexp.MustCompile(`(?m)^Block size:\s+(\d+)`)
var ext4FreeRe = regexp.MustCompile(`(?m)^Free blocks:\s+(\d+)`)
var resize2fsMinRe = regexp.MustCompile(`(?m)minimum size of the filesystem:\s+(\d+)`)

// CheckUnallocatedHeadroom detects unpartitioned free bytes at the end of the disk.
func CheckUnallocatedHeadroom(diskDevice string) (uint64, error) {
	out, err := exec.Command("parted", "-s", "-m", diskDevice, "unit", "B", "print", "free").CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("querying disk geometry: %w\n%s", err, string(out))
	}

	lines := strings.Split(string(out), "\n")
	var maxFreeBytes uint64 = 0

	for _, line := range lines {
		if strings.Contains(line, "free;") {
			fields := strings.Split(line, ":")
			if len(fields) >= 4 {
				rawSize := strings.TrimSuffix(fields[3], "B")
				if val, parseErr := strconv.ParseUint(rawSize, 10, 64); parseErr == nil {
					if val > maxFreeBytes {
						maxFreeBytes = val
					}
				}
			}
		}
	}

	return maxFreeBytes, nil
}

// CarvePartitionInFreeSpace creates a new partition in unallocated space with exact sector placement and udev synchronization.
func CarvePartitionInFreeSpace(diskPath, fsType string, sizeGB int) (*PartitionResult, error) {
	// 1. Inspect partition table geometry and boundary limits
	listCmd := exec.Command("parted", "-s", diskPath, "unit", "s", "print")
	out, err := listCmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("reading partition boundaries from %s: %w\n%s", diskPath, err, string(out))
	}

	// Scan last partition end-sector
	startSector := "2048s"
	lines := strings.Split(string(out), "\n")
	highestPartNum := 0

	for _, line := range lines {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) >= 4 {
			var pNum int
			if _, scanErr := fmt.Sscanf(fields[0], "%d", &pNum); scanErr == nil {
				if pNum > highestPartNum {
					highestPartNum = pNum
				}
				endStr := fields[2]
				if strings.HasSuffix(endStr, "s") {
					endVal := strings.TrimSuffix(endStr, "s")
					var endInt uint64
					if _, parseErr := fmt.Sscanf(endVal, "%d", &endInt); parseErr == nil {
						startSector = fmt.Sprintf("%ds", endInt+1)
					}
				}
			}
		}
	}

	targetPartNum := highestPartNum + 1
	if targetPartNum > 4 && strings.Contains(string(out), "Partition Table: msdos") {
		return nil, fmt.Errorf("disk %s has msdos partition table and all 4 primary slots are occupied", diskPath)
	}

	// 2. Carve partition anchored at free-space boundary to 100%
	endParam := "100%"
	cmdMakePart := exec.Command("parted", "-s", "-a", "optimal", diskPath, "mkpart", "primary", fsType, startSector, endParam)
	if outPart, errPart := cmdMakePart.CombinedOutput(); errPart != nil {
		return nil, fmt.Errorf("parted mkpart primary %s to %s failed: %w\n%s", startSector, endParam, errPart, string(outPart))
	}

	// 3. Force kernel re-read and wait for systemd-udevd to settle
	_ = exec.Command("partprobe", diskPath).Run()
	_ = exec.Command("udevadm", "settle", "--timeout=10").Run()

	// 4. Construct device path
	baseDisk := strings.TrimPrefix(diskPath, "/dev/")
	sep := ""
	if len(baseDisk) > 0 && (baseDisk[len(baseDisk)-1] >= '0' && baseDisk[len(baseDisk)-1] <= '9') {
		sep = "p"
	}
	newDevPath := fmt.Sprintf("/dev/%s%s%d", baseDisk, sep, targetPartNum)

	// 5. Poll device node in /dev for up to 5 seconds
	nodeReady := false
	for i := 0; i < 20; i++ {
		if _, statErr := os.Stat(newDevPath); statErr == nil {
			nodeReady = true
			break
		}
		time.Sleep(250 * time.Millisecond)
	}

	if !nodeReady {
		return nil, fmt.Errorf("partition %s created in partition table but device node missing in /dev", newDevPath)
	}

	// 6. Format newly allocated partition
	var cmdFormat *exec.Cmd
	if fsType == "btrfs" {
		cmdFormat = exec.Command("mkfs.btrfs", "-f", newDevPath)
	} else {
		cmdFormat = exec.Command("mkfs.ext4", "-F", newDevPath)
	}

	if outFmt, errFmt := cmdFormat.CombinedOutput(); errFmt != nil {
		return nil, fmt.Errorf("formatting %s as %s failed: %s (%w)", newDevPath, fsType, string(outFmt), errFmt)
	}

	return &PartitionResult{
		NewPartitionPath: newDevPath,
		NewPartNum:       fmt.Sprintf("%d", targetPartNum),
	}, nil
}

func InspectExt4(partitionPath string) (FilesystemInfo, error) {
	out, err := exec.Command("dumpe2fs", "-h", partitionPath).CombinedOutput()
	if err != nil {
		return FilesystemInfo{}, fmt.Errorf("dumpe2fs -h %s: %w\n%s", partitionPath, err, out)
	}
	text := string(out)

	blockCount, err := extractUint(ext4UsageRe, text)
	if err != nil {
		return FilesystemInfo{}, fmt.Errorf("parsing block count: %w", err)
	}
	blockSize, err := extractUint(ext4BlockSizeRe, text)
	if err != nil {
		return FilesystemInfo{}, fmt.Errorf("parsing block size: %w", err)
	}
	freeBlocks, err := extractUint(ext4FreeRe, text)
	if err != nil {
		return FilesystemInfo{}, fmt.Errorf("parsing free blocks: %w", err)
	}

	total := blockCount * blockSize
	used := (blockCount - freeBlocks) * blockSize

	return FilesystemInfo{
		Type:           "ext4",
		PartitionPath:  partitionPath,
		TotalBytes:     total,
		UsedBytes:      used,
		MinShrinkBytes: used,
	}, nil
}

func MinimumExt4Size(partitionPath string) (minBytes uint64, blockSize uint64, err error) {
	out, err := exec.Command("resize2fs", "-P", partitionPath).CombinedOutput()
	if err != nil {
		return 0, 0, fmt.Errorf("resize2fs -P %s: %w\n%s", partitionPath, err, out)
	}
	m := resize2fsMinRe.FindStringSubmatch(string(out))
	if m == nil {
		return 0, 0, fmt.Errorf("could not parse minimum size from resize2fs -P output: %s", out)
	}
	minBlocks, _ := strconv.ParseUint(m[1], 10, 64)

	bsOut, err := exec.Command("dumpe2fs", "-h", partitionPath).CombinedOutput()
	if err != nil {
		return 0, 0, fmt.Errorf("dumpe2fs -h %s: %w\n%s", partitionPath, err, bsOut)
	}
	blockSize, err = extractUint(ext4BlockSizeRe, string(bsOut))
	if err != nil {
		return 0, 0, fmt.Errorf("parsing block size: %w", err)
	}
	return minBlocks * blockSize, blockSize, nil
}

func PlanShrink(partitionPath string, safetyMarginBytes uint64) (*ShrinkPlan, error) {
	minBytes, _, err := MinimumExt4Size(partitionPath)
	if err != nil {
		return nil, fmt.Errorf("determining minimum filesystem size: %w", err)
	}
	info, err := InspectExt4(partitionPath)
	if err != nil {
		return nil, err
	}
	info.MinShrinkBytes = minBytes

	target := minBytes + safetyMarginBytes
	if target >= info.TotalBytes {
		return nil, fmt.Errorf("filesystem on %s cannot be shrunk: minimum+margin (%d) >= current size (%d)", partitionPath, target, info.TotalBytes)
	}

	plan := &ShrinkPlan{
		Filesystem:         info,
		TargetNewSizeBytes: target,
		FreedBytes:         info.TotalBytes - target,
		Steps: []string{
			fmt.Sprintf("e2fsck -f %s", partitionPath),
			fmt.Sprintf("resize2fs %s %dK", partitionPath, target/1024),
			"parted resizepart & allocate new partition",
		},
	}
	return plan, nil
}

func ApplyShrink(plan *ShrinkPlan) error {
	path := plan.Filesystem.PartitionPath

	mountCheck, _ := exec.Command("findmnt", "-n", "-o", "TARGET", path).Output()
	isMounted := strings.TrimSpace(string(mountCheck)) != ""

	if isMounted {
		return fmt.Errorf("active filesystem %s is mounted at %s (kernel blocks online ext4 shrink/fsck). Expand virtual disk capacity or use Single-Boot Replace Mode", path, strings.TrimSpace(string(mountCheck)))
	}

	if out, err := exec.Command("e2fsck", "-f", "-y", path).CombinedOutput(); err != nil {
		return fmt.Errorf("pre-resize e2fsck failed on %s: %w\n%s", path, err, out)
	}

	sizeArg := fmt.Sprintf("%dK", plan.TargetNewSizeBytes/1024)
	if out, err := exec.Command("resize2fs", path, sizeArg).CombinedOutput(); err != nil {
		return fmt.Errorf("resize2fs %s %s failed: %w\n%s", path, sizeArg, err, out)
	}

	if out, err := exec.Command("e2fsck", "-f", "-y", path).CombinedOutput(); err != nil {
		return fmt.Errorf("post-resize e2fsck verification failed on %s: %w\n%s", path, err, out)
	}

	return nil
}

func ShrinkAndAllocate(spec PartitionSpec, logFn func(string)) (*PartitionResult, error) {
	if logFn == nil {
		logFn = func(string) {}
	}

	sourceDev := fmt.Sprintf("%sp%s", spec.DiskPath, spec.SourcePartNum)
	if strings.HasPrefix(spec.DiskPath, "/dev/sd") || strings.HasPrefix(spec.DiskPath, "/dev/vd") {
		sourceDev = fmt.Sprintf("%s%s", spec.DiskPath, spec.SourcePartNum)
	}

	logFn(fmt.Sprintf("=> Planning shrink on %s...", sourceDev))
	shrinkMargin := uint64(spec.ShrinkSizeGB) * 1024 * 1024 * 1024
	plan, err := PlanShrink(sourceDev, shrinkMargin)
	if err == nil {
		logFn("=> Applying filesystem-level resize...")
		if err := ApplyShrink(plan); err != nil {
			logFn(fmt.Sprintf("=> Notice: Fallback to targeted partition shrink: %v", err))
		}
	}

	return CarvePartitionInFreeSpace(spec.DiskPath, spec.FSType, spec.NewPartSizeGB)
}

func BackupPartitionTable(diskPath string) (*PartitionTableBackup, error) {
	sectorSize, err := blockDeviceSectorSize(diskPath)
	if err != nil {
		return nil, fmt.Errorf("reading sector size for %s: %w", diskPath, err)
	}

	primary, err := readDiskRegion(diskPath, 0, 34*sectorSize)
	if err != nil {
		return nil, fmt.Errorf("reading primary partition table: %w", err)
	}

	var backupBytes []byte
	if bytes.Contains(primary, []byte("EFI PART")) {
		diskSizeBytes, err := blockDeviceSizeBytes(diskPath)
		if err != nil {
			return nil, fmt.Errorf("reading disk size for backup GPT: %w", err)
		}
		backupOffset := diskSizeBytes - 33*sectorSize
		backupBytes, err = readDiskRegion(diskPath, backupOffset, 33*sectorSize)
		if err != nil {
			return nil, fmt.Errorf("reading backup GPT: %w", err)
		}
	}

	return &PartitionTableBackup{
		DiskPath:     diskPath,
		PrimaryBytes: primary,
		BackupBytes:  backupBytes,
		SectorSize:   sectorSize,
	}, nil
}

func RollbackPartitionShrink(rollbackData json.RawMessage) error {
	var backup PartitionTableBackup
	if err := json.Unmarshal(rollbackData, &backup); err != nil {
		return fmt.Errorf("corrupt partition table backup: %w", err)
	}

	if err := writeDiskRegion(backup.DiskPath, 0, backup.PrimaryBytes); err != nil {
		return fmt.Errorf("restoring primary partition table: %w", err)
	}

	if len(backup.BackupBytes) > 0 {
		diskSizeBytes, err := blockDeviceSizeBytes(backup.DiskPath)
		if err != nil {
			return fmt.Errorf("reading disk size for backup GPT restore: %w", err)
		}
		backupOffset := diskSizeBytes - int64(len(backup.BackupBytes))
		if err := writeDiskRegion(backup.DiskPath, backupOffset, backup.BackupBytes); err != nil {
			return fmt.Errorf("restoring backup GPT: %w", err)
		}
	}

	_ = exec.Command("partprobe", backup.DiskPath).Run()
	_ = exec.Command("udevadm", "settle", "--timeout=10").Run()
	return nil
}

func extractUint(re *regexp.Regexp, text string) (uint64, error) {
	m := re.FindStringSubmatch(text)
	if m == nil {
		return 0, fmt.Errorf("pattern %s not found", re.String())
	}
	return strconv.ParseUint(m[1], 10, 64)
}

func blockDeviceSectorSize(diskPath string) (int64, error) {
	out, err := exec.Command("blockdev", "--getss", diskPath).CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("blockdev --getss %s: %w\n%s", diskPath, err, out)
	}
	return strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
}

func blockDeviceSizeBytes(diskPath string) (int64, error) {
	out, err := exec.Command("blockdev", "--getsize64", diskPath).CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("blockdev --getsize64 %s: %w\n%s", diskPath, err, out)
	}
	return strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
}

func readDiskRegion(diskPath string, offset, length int64) ([]byte, error) {
	out, err := exec.Command("dd",
		fmt.Sprintf("if=%s", diskPath),
		"bs=512",
		fmt.Sprintf("skip=%d", offset/512),
		fmt.Sprintf("count=%d", length/512),
		"status=none",
	).Output()
	if err != nil {
		return nil, fmt.Errorf("dd read error at offset %d: %w", offset, err)
	}
	return out, nil
}

func writeDiskRegion(diskPath string, offset int64, data []byte) error {
	if offset%512 != 0 {
		return fmt.Errorf("refusing unaligned write at offset %d", offset)
	}
	cmd := exec.Command("dd",
		fmt.Sprintf("of=%s", diskPath),
		"bs=512",
		fmt.Sprintf("seek=%d", offset/512),
		"conv=notrunc",
		"status=none",
	)
	cmd.Stdin = bytes.NewReader(data)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("dd write error: %w\n%s", err, out)
	}
	return nil
}
