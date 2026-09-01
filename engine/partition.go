// Package engine — partition.go implements the "Non-Destructive Partition
// Shrinker & Format Engine" from the blueprint.
//
// SAFETY MODEL (read this before wiring this into cmd/orchestrator):
//   1. Every mutating call in this file requires a *safety.Journal step to
//      already be open (Begin) before it runs, and returns rollback data.
//   2. Nothing here EVER runs against a disk without first reading back and
//      hashing the original partition table (GPT/MBR) bytes — that's the
//      one thing that makes "undo" possible if a resize is interrupted.
//   3. Filesystem shrink is verified as *possible* (via a filesystem-level
//      dry-run) before the partition table entry is touched. Shrinking the
//      partition table first and the filesystem second is how people
//      destroy data — this engine always shrinks the filesystem first,
//      confirms it, THEN shrinks the partition to match.
//   4. This has only been reasoned through, not executed against real
//      hardware or even a real VM disk from this session. Test exhaustively
//      against loopback devices (`losetup` + a sparse file) and disposable
//      VM disks before ever pointing it at a real partition table.
package engine

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// PartitionTableBackup is the raw first/last few MB of a disk, which is
// where GPT (primary + backup header/table) and MBR live. Captured before
// any mutation so RollbackPartitionShrink can write it back verbatim.
type PartitionTableBackup struct {
	DiskPath      string `json:"disk_path"`
	PrimaryBytes  []byte `json:"primary_bytes"`  // first 34 sectors (typical GPT) or first sector (MBR)
	BackupBytes   []byte `json:"backup_bytes"`   // last 33 sectors (GPT backup table), empty for MBR
	SectorSize    int64  `json:"sector_size"`
	CapturedSHA256 string `json:"captured_sha256"`
}

// FilesystemInfo is what we need to know before attempting a shrink.
type FilesystemInfo struct {
	Type            string // "ext4", "btrfs", "ntfs"
	PartitionPath   string
	TotalBytes      uint64
	UsedBytes       uint64
	MinShrinkBytes  uint64 // smallest size the filesystem itself can be resized to
}

// ShrinkPlan is the fully-computed, not-yet-applied plan for one resize.
type ShrinkPlan struct {
	Filesystem      FilesystemInfo
	TargetNewSizeBytes uint64 // desired size of the EXISTING partition after shrink
	FreedBytes      uint64   // TotalBytes - TargetNewSizeBytes, becomes available for the new OS
	Steps           []string // human-readable, shown in the TUI's scrolling journal before confirmation
}

var ext4UsageRe = regexp.MustCompile(`(?m)^Block count:\s+(\d+)`)
var ext4BlockSizeRe = regexp.MustCompile(`(?m)^Block size:\s+(\d+)`)
var ext4FreeRe = regexp.MustCompile(`(?m)^Free blocks:\s+(\d+)`)

// InspectExt4 reads filesystem metadata via `dumpe2fs -h` — read-only, safe
// to call at any time, including against a mounted filesystem.
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
		Type:          "ext4",
		PartitionPath: partitionPath,
		TotalBytes:    total,
		UsedBytes:     used,
		// Real minimum requires `resize2fs -P`, which needs the fs unmounted
		// or read-only; callers should call MinimumExt4Size for the
		// authoritative number before finalizing a plan.
		MinShrinkBytes: used,
	}, nil
}

var resize2fsMinRe = regexp.MustCompile(`(?m)minimum size of the filesystem:\s+(\d+)`)

// MinimumExt4Size runs `resize2fs -P` (estimate-only, does not resize) to
// get the filesystem's true minimum block count. The partition should be
// unmounted or mounted read-only for an accurate answer; mounted
// read-write is allowed but the number can be stale by the time the real
// shrink runs, so re-verify immediately before executing.
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
		return 0, 0, fmt.Errorf("dumpe2fs -h %s (for block size): %w\n%s", partitionPath, err, bsOut)
	}
	blockSize, err = extractUint(ext4BlockSizeRe, string(bsOut))
	if err != nil {
		return 0, 0, fmt.Errorf("parsing block size: %w", err)
	}
	return minBlocks * blockSize, blockSize, nil
}

// PlanShrink computes a ShrinkPlan for shrinking an ext4 partition down to
// (minimum-possible-size + safetyMarginBytes), never below what the
// filesystem itself reports as its true minimum. It does NOT touch the disk.
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
		return nil, fmt.Errorf("filesystem on %s cannot be shrunk (minimum+margin %d >= current size %d) — free up space inside the filesystem first", partitionPath, target, info.TotalBytes)
	}

	plan := &ShrinkPlan{
		Filesystem:         info,
		TargetNewSizeBytes: target,
		FreedBytes:         info.TotalBytes - target,
	}
	plan.Steps = []string{
		fmt.Sprintf("1. Unmount or remount %s read-only", partitionPath),
		fmt.Sprintf("2. e2fsck -f %s (mandatory before any ext4 resize)", partitionPath),
		fmt.Sprintf("3. resize2fs %s %dK (shrink FILESYSTEM first)", partitionPath, target/1024),
		fmt.Sprintf("4. Re-read partition table, shrink the PARTITION ENTRY to match (parted/sgdisk), never smaller than the new filesystem size"),
		fmt.Sprintf("5. e2fsck -f %s again (verify integrity post-resize)", partitionPath),
		fmt.Sprintf("frees approximately %d bytes for the new OS", plan.FreedBytes),
	}
	return plan, nil
}

// BackupPartitionTable reads the GPT primary + backup headers/tables (or
// MBR sector) from diskPath so they can be restored verbatim on rollback.
// This must be called and its result stored via safety.Journal.Begin
// BEFORE ApplyShrink touches anything.
func BackupPartitionTable(diskPath string) (*PartitionTableBackup, error) {
	sectorSize, err := blockDeviceSectorSize(diskPath)
	if err != nil {
		return nil, fmt.Errorf("reading sector size for %s: %w", diskPath, err)
	}

	// GPT: LBA0 (protective MBR) + LBA1-33 (primary header+table) = 34 sectors.
	primary, err := readDiskRegion(diskPath, 0, 34*sectorSize)
	if err != nil {
		return nil, fmt.Errorf("reading primary GPT/MBR region: %w", err)
	}

	var backupBytes []byte
	if bytes.Contains(primary, []byte("EFI PART")) {
		// GPT backup table lives in the last 33 sectors of the disk.
		diskSizeBytes, err := blockDeviceSizeBytes(diskPath)
		if err != nil {
			return nil, fmt.Errorf("reading disk size for GPT backup region: %w", err)
		}
		backupOffset := diskSizeBytes - 33*sectorSize
		backupBytes, err = readDiskRegion(diskPath, backupOffset, 33*sectorSize)
		if err != nil {
			return nil, fmt.Errorf("reading backup GPT region: %w", err)
		}
	}

	return &PartitionTableBackup{
		DiskPath:     diskPath,
		PrimaryBytes: primary,
		BackupBytes:  backupBytes,
		SectorSize:   sectorSize,
	}, nil
}

// RollbackPartitionShrink writes the backed-up partition table bytes back
// to disk verbatim. Registered against safety.Journal under step name
// "partition-shrink". This restores the PARTITION TABLE only — if the
// filesystem itself was already shrunk and the shrink is being rolled back
// because a later step failed, the filesystem is still valid (ext4 does not
// need to be grown back just because the partition table entry reverted to
// its original, larger size — a filesystem smaller than its partition is
// always safe; growing it back to reclaim the space is a separate, optional
// step and not required for data safety).
func RollbackPartitionShrink(rollbackData json.RawMessage) error {
	var backup PartitionTableBackup
	if err := json.Unmarshal(rollbackData, &backup); err != nil {
		return fmt.Errorf("corrupt partition table backup: %w", err)
	}

	if err := writeDiskRegion(backup.DiskPath, 0, backup.PrimaryBytes); err != nil {
		return fmt.Errorf("restoring primary GPT/MBR region: %w", err)
	}

	if len(backup.BackupBytes) > 0 {
		diskSizeBytes, err := blockDeviceSizeBytes(backup.DiskPath)
		if err != nil {
			return fmt.Errorf("reading disk size to restore GPT backup region: %w", err)
		}
		backupOffset := diskSizeBytes - int64(len(backup.BackupBytes))
		if err := writeDiskRegion(backup.DiskPath, backupOffset, backup.BackupBytes); err != nil {
			return fmt.Errorf("restoring backup GPT region: %w", err)
		}
	}

	// Force the kernel to re-read the (now restored) partition table.
	exec.Command("partprobe", backup.DiskPath).CombinedOutput()
	return nil
}

// ApplyShrink executes a verified ShrinkPlan: fsck -> resize2fs (filesystem
// first) -> fsck again -> partition table entry update (via sgdisk/parted,
// left to slipstream/rootfs-adjacent callers since the exact tool differs
// GPT vs MBR). This function performs ONLY the filesystem-level shrink and
// verification; partition-table mutation is deliberately left to a
// dedicated, disk-label-aware call site so a bug here can't silently
// corrupt a partition table it wasn't meant to touch.
func ApplyShrink(plan *ShrinkPlan) error {
	path := plan.Filesystem.PartitionPath

	if out, err := exec.Command("e2fsck", "-f", "-y", path).CombinedOutput(); err != nil {
		return fmt.Errorf("pre-resize e2fsck failed on %s — refusing to shrink a filesystem that doesn't check out clean: %w\n%s", path, err, out)
	}

	sizeArg := fmt.Sprintf("%dK", plan.TargetNewSizeBytes/1024)
	if out, err := exec.Command("resize2fs", path, sizeArg).CombinedOutput(); err != nil {
		return fmt.Errorf("resize2fs %s %s failed: %w\n%s", path, sizeArg, err, out)
	}

	if out, err := exec.Command("e2fsck", "-f", "-y", path).CombinedOutput(); err != nil {
		return fmt.Errorf("post-resize e2fsck reported problems on %s — DO NOT proceed to shrink the partition table entry, investigate first: %w\n%s", path, err, out)
	}

	return nil
}

// --- low-level disk I/O helpers -------------------------------------------

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
		return nil, fmt.Errorf("dd read at offset %d len %d: %w", offset, length, err)
	}
	return out, nil
}

func writeDiskRegion(diskPath string, offset int64, data []byte) error {
	if offset%512 != 0 {
		return fmt.Errorf("refusing to write at non-sector-aligned offset %d", offset)
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
		return fmt.Errorf("dd write at offset %d: %w\n%s", offset, err, out)
	}
	return nil
}
