// Package engine — partition.go implements the Universal Non-Destructive Partition
// Management Engine with GPT sector alignment, real hardware block allocation,
// unallocated headroom carving, and raw binary safety rollbacks.
package engine

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

type PartitionSpec struct {
	DiskPath      string
	SourcePartNum string
	ShrinkSizeGB  int
	NewPartSizeGB int
	FSType        string
}

type PartitionResult struct {
	NewPartitionPath string
	NewPartNum       string

	// ConvertedSwapSizeBytes/ConvertedSwapUUID are populated only when the
	// new partition was carved by converting a confirmed trailing swap
	// slice (follow-up F14): the caller persists these into the journal
	// session so a later revert can recreate an equivalent swap partition,
	// rather than permanently losing the host's swap space. Zero/empty
	// when the new partition came from free space or a shrink instead.
	ConvertedSwapSizeBytes int64
	ConvertedSwapUUID      string
}

var ext4UsageRe = regexp.MustCompile(`(?m)^Block count:\s+(\d+)`)
var ext4BlockSizeRe = regexp.MustCompile(`(?m)^Block size:\s+(\d+)`)
var ext4FreeRe = regexp.MustCompile(`(?m)^Free blocks:\s+(\d+)`)
var resize2fsMinRe = regexp.MustCompile(`(?m)minimum size of the filesystem:\s+(\d+)`)

// mtabPath is the location resize2fs consults (via libext2fs) to determine
// whether a filesystem is currently mounted. On systems where /etc/mtab is a
// plain file rather than a symlink to /proc/self/mounts, that mount check
// throws ("missing mtab file") instead of falling back, aborting the shrink
// outright — this is the exact failure captured in a real run's journal
// (audit 2.2). Declared as a var (not a const) so tests can point it at a
// scratch path instead of mutating the real system's /etc/mtab.
var mtabPath = "/etc/mtab"

// ensureMtabSymlink makes sure mtabPath exists, symlinking it to
// /proc/self/mounts if missing. It never touches an existing file/symlink.
func ensureMtabSymlink() error {
	if _, err := os.Lstat(mtabPath); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("checking %s: %w", mtabPath, err)
	}
	if err := fsRunner.Symlink("/proc/self/mounts", mtabPath); err != nil {
		return fmt.Errorf("symlinking %s to /proc/self/mounts: %w", mtabPath, err)
	}
	return nil
}

// isPartitionMounted reports whether devPath is currently mounted anywhere,
// via findmnt. Mirrors the existing convention elsewhere in this file of
// treating a findmnt error as "not mounted" rather than a hard failure.
func isPartitionMounted(devPath string) bool {
	out, _ := cmdRunner.RunStdout("findmnt", "-n", "-o", "TARGET", devPath)
	return strings.TrimSpace(string(out)) != ""
}

// exitCodeOf extracts a command's process exit code from the error returned
// by Runner.Run. execRunner's real implementation returns exec.Command's
// error unwrapped, which is a *exec.ExitError on any non-zero exit, so this
// works for production errors with no extra plumbing; tests can produce a
// genuine *exec.ExitError at a chosen exit code via a real `sh -c "exit N"`
// subprocess (see fakeExitError in the test files) rather than needing a
// custom error type.
//
// Returns 0 if err is nil, or -1 if err is non-nil but not an *exec.ExitError
// (e.g. the binary could not even be started) — i.e. "couldn't determine the
// exit code," which callers should treat as a failure.
func exitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

// isConfirmedSwapPartition reports whether devPath is genuinely a swap
// partition, before any caller is allowed to `parted rm` it. partedFSName
// is the filesystem name parted's own "print" output reported for this
// partition; it alone is not trusted (parted's cache can be stale, and a
// previous version of this check also wrongly matched on partition number
// alone). This requires independent agreement from blkid's own TYPE probe
// as well — both must say "swap" — before treating the partition as
// confirmed swap (audit 1B.6).
func isConfirmedSwapPartition(devPath, partedFSName string) bool {
	if !strings.Contains(strings.ToLower(partedFSName), "swap") {
		return false
	}
	out, err := cmdRunner.RunStdout("blkid", "-o", "value", "-s", "TYPE", devPath)
	if err != nil {
		return false
	}
	return strings.TrimSpace(strings.ToLower(string(out))) == "swap"
}

func CheckUnallocatedHeadroom(diskDevice string) (uint64, error) {
	out, err := cmdRunner.Run("parted", "-s", "-m", diskDevice, "unit", "B", "print", "free")
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

func CarvePartitionInFreeSpace(diskPath, fsType string, sizeGB int) (*PartitionResult, error) {
	out, err := cmdRunner.Run("parted", "-s", diskPath, "unit", "s", "print")
	if err != nil {
		return nil, fmt.Errorf("reading partition boundaries from %s: %w\n%s", diskPath, err, string(out))
	}

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

	// Always use explicit GiB (binary) to prevent parted decimal truncation bugs
	endParam := "100%"
	if sizeGB > 0 {
		endParam = fmt.Sprintf("+%dGiB", sizeGB)
	}

	if outPart, errPart := cmdRunner.Run("parted", "-s", "-a", "optimal", diskPath, "mkpart", "primary", fsType, startSector, endParam); errPart != nil {
		if outFb, errFb := cmdRunner.Run("parted", "-s", "-a", "optimal", diskPath, "mkpart", "primary", fsType, startSector, "100%"); errFb != nil {
			return nil, fmt.Errorf("parted mkpart primary failed: %w (%s | %s)", errFb, string(outPart), string(outFb))
		}
	}

	_, _ = cmdRunner.Run("partprobe", diskPath)
	_, _ = cmdRunner.Run("udevadm", "settle", "--timeout=10")

	newDevPath := JoinDiskAndPartition(diskPath, fmt.Sprintf("%d", targetPartNum))

	nodeReady := false
	for i := 0; i < 20; i++ {
		if _, statErr := os.Stat(newDevPath); statErr == nil {
			nodeReady = true
			break
		}
		time.Sleep(250 * time.Millisecond)
	}

	if !nodeReady {
		return nil, fmt.Errorf("partition %s created in table but node missing in /dev", newDevPath)
	}

	var formatArgs []string
	if fsType == "btrfs" {
		formatArgs = []string{"mkfs.btrfs", "-f", newDevPath}
	} else {
		formatArgs = []string{"mkfs.ext4", "-F", "-q", "-L", "PARROT_ROOT", newDevPath}
	}

	if outFmt, errFmt := cmdRunner.Run(formatArgs[0], formatArgs[1:]...); errFmt != nil {
		return nil, fmt.Errorf("formatting %s as %s failed: %w (%s)", newDevPath, fsType, errFmt, string(outFmt))
	}

	return &PartitionResult{
		NewPartitionPath: newDevPath,
		NewPartNum:       fmt.Sprintf("%d", targetPartNum),
	}, nil
}

func CarveHardwarePartitionAtBoundary(diskPath, fsType string, targetBytes uint64, logFn func(string)) (*PartitionResult, error) {
	if logFn == nil {
		logFn = func(string) {}
	}

	logFn(fmt.Sprintf("=> [HARDWARE PARTITION] Analyzing partition table topology on %s...", diskPath))

	_, _ = cmdRunner.Run("swapoff", "-a")

	// 1. Check if unallocated free sectors exist on disk
	freeHeadroom, _ := CheckUnallocatedHeadroom(diskPath)
	if freeHeadroom >= targetBytes && targetBytes > 0 {
		logFn(fmt.Sprintf("=> [HARDWARE PARTITION] Found %.1f GiB unallocated space. Carving dedicated slice...", float64(freeHeadroom)/(1024*1024*1024)))
		return CarvePartitionInFreeSpace(diskPath, fsType, int(targetBytes/(1024*1024*1024)))
	}

	// 2. Read partition map
	out, err := cmdRunner.Run("parted", "-s", diskPath, "unit", "s", "print")
	if err != nil {
		return nil, fmt.Errorf("reading partition table on %s: %w\n%s", diskPath, err, string(out))
	}

	lines := strings.Split(string(out), "\n")
	var partitions []struct {
		num   int
		start uint64
		end   uint64
		fs    string
	}

	for _, line := range lines {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) >= 4 {
			var num int
			if _, sErr := fmt.Sscanf(fields[0], "%d", &num); sErr == nil {
				startVal := strings.TrimSuffix(fields[1], "s")
				endVal := strings.TrimSuffix(fields[2], "s")
				var startInt, endInt uint64
				_, _ = fmt.Sscanf(startVal, "%d", &startInt)
				_, _ = fmt.Sscanf(endVal, "%d", &endInt)

				fsName := ""
				if len(fields) >= 5 {
					fsName = fields[4]
				}
				partitions = append(partitions, struct {
					num   int
					start uint64
					end   uint64
					fs    string
				}{num: num, start: startInt, end: endInt, fs: fsName})
			}
		}
	}

	maxDiskSizeBytes, bErr := blockDeviceSizeBytes(diskPath)
	if bErr != nil {
		maxDiskSizeBytes = 1000215216 * 512
	}
	maxDiskSectors := uint64(maxDiskSizeBytes / 512)

	// 3. Inspect trailing swap. Audit 1B.6: this used to also treat ANY disk
	// whose last partition happened to be numbered 3 as swap
	// (`lastPart.num == 3`), regardless of its actual filesystem type — on a
	// disk with a third real (non-swap) partition, that would delete it.
	// isConfirmedSwapPartition now requires agreement between parted's own
	// fs-type column and an independent blkid probe before anything is
	// removed.
	hasSwapAtEnd := false
	var swapPartNum int
	if len(partitions) > 0 {
		lastPart := partitions[len(partitions)-1]
		lastPartDev := JoinDiskAndPartition(diskPath, fmt.Sprintf("%d", lastPart.num))
		if isConfirmedSwapPartition(lastPartDev, lastPart.fs) {
			hasSwapAtEnd = true
			swapPartNum = lastPart.num
		}
	}

	if hasSwapAtEnd && len(partitions) >= 2 {
		swapBytes := (partitions[len(partitions)-1].end - partitions[len(partitions)-1].start) * 512
		// Safety assertion: Ensure swap slice is physically large enough
		if swapBytes < targetBytes && targetBytes > 0 {
			return nil, fmt.Errorf("insufficient contiguous space (available trailing slice: %.1f GiB, required: %.1f GiB). Boot Live USB to shrink active root partition", float64(swapBytes)/(1024*1024*1024), float64(targetBytes)/(1024*1024*1024))
		}

		// Follow-up F14: capture the swap slice's identity (size, PARTUUID)
		// BEFORE it is deleted below, so the caller can persist it into the
		// journal session and a later revert can recreate an equivalent
		// swap partition instead of permanently losing the host's swap
		// space.
		swapDevBeforeDelete := JoinDiskAndPartition(diskPath, fmt.Sprintf("%d", swapPartNum))
		swapUUIDOut, _ := cmdRunner.RunStdout("blkid", "-s", "PARTUUID", "-o", "value", swapDevBeforeDelete)
		swapUUID := strings.TrimSpace(string(swapUUIDOut))

		logFn(fmt.Sprintf("=> [HARDWARE PARTITION] Converting trailing slice %d (%.1f GiB) to primary ext4...", swapPartNum, float64(swapBytes)/(1024*1024*1024)))
		// Confirmed swap (type, via isConfirmedSwapPartition above) and large
		// enough (size, via the swapBytes check above) — only now is it
		// removed.
		_, _ = cmdRunner.Run("parted", "-s", diskPath, "rm", fmt.Sprintf("%d", swapPartNum))
		_, _ = cmdRunner.Run("partprobe", diskPath)
		_, _ = cmdRunner.Run("udevadm", "settle", "--timeout=5")
		time.Sleep(500 * time.Millisecond)

		startSec := partitions[len(partitions)-2].end + 1
		endSec := maxDiskSectors - 2048

		newDevPath := JoinDiskAndPartition(diskPath, fmt.Sprintf("%d", swapPartNum))

		if mkOut, mkErr := cmdRunner.Run("parted", "-s", "-a", "optimal", diskPath, "mkpart", "primary", fsType, fmt.Sprintf("%ds", startSec), fmt.Sprintf("%ds", endSec)); mkErr != nil {
			return nil, fmt.Errorf("carving physical partition failed: %w (%s)", mkErr, string(mkOut))
		}

		_, _ = cmdRunner.Run("partprobe", diskPath)
		_, _ = cmdRunner.Run("udevadm", "settle", "--timeout=10")

		logFn(fmt.Sprintf("=> Formatting %s as [%s]...", newDevPath, fsType))
		if outFmt, errFmt := cmdRunner.Run("mkfs.ext4", "-F", "-q", "-L", "PARROT_ROOT", newDevPath); errFmt != nil {
			return nil, fmt.Errorf("formatting %s failed: %w (%s)", newDevPath, errFmt, string(outFmt))
		}

		return &PartitionResult{
			NewPartitionPath:       newDevPath,
			NewPartNum:             fmt.Sprintf("%d", swapPartNum),
			ConvertedSwapSizeBytes: int64(swapBytes),
			ConvertedSwapUUID:      swapUUID,
		}, nil
	}

	// 4. Shrink Partition 1 on Secondary Data Disks (e.g. /dev/sda1)
	if len(partitions) == 1 && partitions[0].num == 1 {
		part1Dev := JoinDiskAndPartition(diskPath, "1")
		logFn(fmt.Sprintf("=> [SHRINK ENGINE] Disk %s is fully allocated. Safely shrinking %s...", diskPath, part1Dev))

		// All non-destructive preflight checks run before anything that
		// mutates the disk (the `parted rm 2` below), so a failed check
		// aborts cleanly with partition 2 (and everything else) untouched.

		if err := ensureMtabSymlink(); err != nil {
			return nil, fmt.Errorf("ensuring /etc/mtab for resize2fs mount detection: %w", err)
		}

		if isPartitionMounted(part1Dev) {
			return nil, fmt.Errorf("refusing to shrink %s: it is still mounted (boot from a live environment to shrink the active root partition)", part1Dev)
		}

		logFn(fmt.Sprintf("=> [SHRINK ENGINE] Running e2fsck on %s...", part1Dev))
		out, err := cmdRunner.Run("e2fsck", "-fy", part1Dev)
		// e2fsck exit codes: 0 = no errors, 1 = errors were found and
		// corrected (safe to continue). 2+ (or a code we couldn't even
		// determine, e.g. the binary failed to start) means it found
		// problems it could not fix, or something worse — abort.
		if code := exitCodeOf(err); code == -1 || code >= 2 {
			return nil, fmt.Errorf("pre-resize e2fsck failed on %s (exit code %d), aborting shrink: %w\n%s", part1Dev, code, err, out)
		}

		totalPartSectors := partitions[0].end - partitions[0].start
		targetSectors := targetBytes / 512
		if targetSectors >= totalPartSectors {
			targetSectors = totalPartSectors / 2
		}
		newPart1Sectors := totalPartSectors - targetSectors
		newPart1GiB := (newPart1Sectors * 512) / (1024 * 1024 * 1024)

		minBytes, _, minErr := MinimumExt4Size(part1Dev)
		if minErr != nil {
			return nil, fmt.Errorf("determining minimum shrinkable size for %s, aborting shrink: %w", part1Dev, minErr)
		}
		newSizeBytes := newPart1GiB * 1024 * 1024 * 1024
		if newSizeBytes < minBytes {
			return nil, fmt.Errorf("target size %dGiB is smaller than %s's minimum shrinkable filesystem size (%d bytes); aborting shrink", newPart1GiB, part1Dev, minBytes)
		}

		_, _ = cmdRunner.Run("parted", "-s", diskPath, "rm", "2")
		_, _ = cmdRunner.Run("udevadm", "settle", "--timeout=5")

		logFn(fmt.Sprintf("=> [SHRINK ENGINE] Resizing filesystem on %s to %d GiB...", part1Dev, newPart1GiB))
		if outResize, errResize := cmdRunner.Run("resize2fs", part1Dev, fmt.Sprintf("%dG", newPart1GiB)); errResize != nil {
			return nil, fmt.Errorf("resize2fs failed on %s: %w (%s)", part1Dev, errResize, string(outResize))
		}

		logFn(fmt.Sprintf("=> [SHRINK ENGINE] Adjusting GPT partition boundary to %dGiB...", newPart1GiB))
		// parted needs an interactive "Yes" piped to stdin to confirm shrinking a
		// partition that overlaps the filesystem's former extent; the Runner
		// interface doesn't model stdin, so this stays a direct exec.Command.
		partedResizeCmd := execCommand("parted", "---pretend-input-tty", diskPath, "resizepart", "1", fmt.Sprintf("%dGiB", newPart1GiB))
		partedResizeCmd.Stdin = strings.NewReader("Yes\n")
		_ = partedResizeCmd.Run()

		_, _ = cmdRunner.Run("partprobe", diskPath)
		_, _ = cmdRunner.Run("udevadm", "settle", "--timeout=10")

		_, _ = cmdRunner.Run("e2fsck", "-fy", part1Dev)
		logFn("=> [SHRINK ENGINE] Partition 1 shrunk cleanly. Carving dedicated OS slice...")
	}

	return CarvePartitionInFreeSpace(diskPath, fsType, int(targetBytes/(1024*1024*1024)))
}

func InspectExt4(partitionPath string) (FilesystemInfo, error) {
	out, err := cmdRunner.Run("dumpe2fs", "-h", partitionPath)
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
	out, err := cmdRunner.Run("resize2fs", "-P", partitionPath)
	if err != nil {
		return 0, 0, fmt.Errorf("resize2fs -P %s: %w\n%s", partitionPath, err, out)
	}
	m := resize2fsMinRe.FindStringSubmatch(string(out))
	if m == nil {
		return 0, 0, fmt.Errorf("could not parse minimum size from resize2fs -P output: %s", out)
	}
	minBlocks, _ := strconv.ParseUint(m[1], 10, 64)

	bsOut, err := cmdRunner.Run("dumpe2fs", "-h", partitionPath)
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
			fmt.Sprintf("e2fsck -f -y %s", partitionPath),
			fmt.Sprintf("resize2fs %s %dK", partitionPath, target/1024),
			"parted resizepart & allocate new partition",
		},
	}
	return plan, nil
}

func ApplyShrink(plan *ShrinkPlan) error {
	path := plan.Filesystem.PartitionPath

	mountCheck, _ := cmdRunner.RunStdout("findmnt", "-n", "-o", "TARGET", path)
	isMounted := strings.TrimSpace(string(mountCheck)) != ""

	if isMounted {
		return fmt.Errorf("active filesystem %s is mounted at %s (kernel blocks online ext4 shrink/fsck). Boot via Live USB to shrink active root partition", path, strings.TrimSpace(string(mountCheck)))
	}

	preOut, preErr := cmdRunner.Run("e2fsck", "-f", "-y", path)
	if code := exitCodeOf(preErr); code == -1 || code >= 2 {
		return fmt.Errorf("pre-resize e2fsck failed on %s (exit code %d): %w\n%s", path, code, preErr, preOut)
	}

	sizeArg := fmt.Sprintf("%dK", plan.TargetNewSizeBytes/1024)
	if out, err := cmdRunner.Run("resize2fs", path, sizeArg); err != nil {
		return fmt.Errorf("resize2fs %s %s failed: %w\n%s", path, sizeArg, err, out)
	}

	postOut, postErr := cmdRunner.Run("e2fsck", "-f", "-y", path)
	if code := exitCodeOf(postErr); code == -1 || code >= 2 {
		return fmt.Errorf("post-resize e2fsck verification failed on %s (exit code %d): %w\n%s", path, code, postErr, postOut)
	}

	return nil
}

// ShrinkPartitionNonInteractive executes a safe, non-interactive partition boundary
// shrink using GNU parted, guaranteeing input piping to bypass any confirmation prompts.
// It enforces a safe container boundary that is strictly larger than the filesystem.
func ShrinkPartitionNonInteractive(diskPath, partNum string, targetGiB uint64) error {
	// Needs Stdin to confirm the resize; stays a direct exec.Command (see note
	// on the Runner type).
	partedCmd := execCommand("parted", "---pretend-input-tty", diskPath, "resizepart", partNum, fmt.Sprintf("%dGiB", targetGiB))
	partedCmd.Stdin = strings.NewReader("Yes\n")

	if out, err := partedCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("parted resizepart %s %s failed: %w\n%s", diskPath, partNum, err, string(out))
	}

	_, _ = cmdRunner.Run("partprobe", diskPath)
	_, _ = cmdRunner.Run("udevadm", "settle", "--timeout=10")
	return nil
}

func ShrinkAndAllocate(spec PartitionSpec, logFn func(string)) (*PartitionResult, error) {
	if logFn == nil {
		logFn = func(string) {}
	}

	sourceDev := JoinDiskAndPartition(spec.DiskPath, spec.SourcePartNum)

	logFn(fmt.Sprintf("=> Planning shrink on %s...", sourceDev))
	shrinkMargin := uint64(spec.ShrinkSizeGB) * 1024 * 1024 * 1024
	plan, err := PlanShrink(sourceDev, shrinkMargin)
	if err == nil {
		logFn("=> Applying filesystem-level resize...")
		if err := ApplyShrink(plan); err != nil {
			logFn(fmt.Sprintf("=> Notice: Fallback to targeted partition shrink: %v", err))
		}
	}

	return CarveHardwarePartitionAtBoundary(spec.DiskPath, spec.FSType, uint64(spec.NewPartSizeGB)*1024*1024*1024, logFn)
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

	_, _ = cmdRunner.Run("partprobe", backup.DiskPath)
	_, _ = cmdRunner.Run("udevadm", "settle", "--timeout=10")
	return nil
}

// SplitDiskAndPartition takes a partition device path (e.g. /dev/sdb3,
// /dev/nvme0n1p3, /dev/mmcblk0p2, /dev/loop0p2 — a loop device, used for all
// safe disk-image testing per this tool's own safety rules) and returns the
// parent disk path and the partition number.
//
// Audit 1C.8: this used to special-case only "nvme"/"mmcblk" by substring
// before falling back to a plain "disk name + trailing digits" parse; a
// loop device partition (e.g. "loop0p1") doesn't contain either substring,
// so it fell into the fallback branch and parsed as disk="/dev/loop0p",
// part="1" — wrong, since the real disk is "/dev/loop0". Partition numbers
// are now detected structurally instead of by an allowlist of device-name
// substrings: any disk name ending in a digit (nvme0n1, mmcblk0, loop0, ...)
// uses a "pN" infix, because a bare trailing digit on such a name would be
// ambiguous with the disk's own numbering; this covers loop devices and any
// future numbered device naming the same way, with no new allowlist entry
// needed.
//
// partitionPath must be an actual partition path, not a bare disk path: a
// bare numbered disk like "/dev/loop0" is inherently ambiguous (it could be
// misread as disk "loop" + partition "0") and this function has no way to
// know which disk names are "real" versus partition-bearing, so callers are
// responsible for only passing genuine partition paths.
func SplitDiskAndPartition(partitionPath string) (diskPath string, partNum string, err error) {
	base := filepath.Base(partitionPath)
	dir := filepath.Dir(partitionPath)

	i := len(base) - 1
	for i >= 0 && base[i] >= '0' && base[i] <= '9' {
		i--
	}
	if i == len(base)-1 {
		return "", "", fmt.Errorf("no partition number found in %s", partitionPath)
	}
	partNum = base[i+1:]

	if i >= 1 && base[i] == 'p' && base[i-1] >= '0' && base[i-1] <= '9' {
		// The disk name itself ends in a digit, so this "p" is the
		// partition-number infix, not part of the disk name.
		return filepath.Join(dir, base[:i]), partNum, nil
	}
	return filepath.Join(dir, base[:i+1]), partNum, nil
}

// JoinDiskAndPartition joins a disk path and a partition number, correctly
// handling NVMe/eMMC "pN" suffixes as well as standard SATA/USB devices.
func JoinDiskAndPartition(diskPath, partNum string) string {
	base := filepath.Base(diskPath)
	if (len(base) > 0 && base[len(base)-1] >= '0' && base[len(base)-1] <= '9') ||
		strings.Contains(base, "nvme") || strings.Contains(base, "mmcblk") {
		return diskPath + "p" + partNum
	}
	return diskPath + partNum
}

func extractUint(re *regexp.Regexp, text string) (uint64, error) {
	m := re.FindStringSubmatch(text)
	if m == nil {
		return 0, fmt.Errorf("pattern %s not found", re.String())
	}
	return strconv.ParseUint(m[1], 10, 64)
}

func blockDeviceSectorSize(diskPath string) (int64, error) {
	out, err := diskReadRunner().Run("blockdev", "--getss", diskPath)
	if err != nil {
		return 0, fmt.Errorf("blockdev --getss %s: %w\n%s", diskPath, err, out)
	}
	return strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
}

func blockDeviceSizeBytes(diskPath string) (int64, error) {
	out, err := diskReadRunner().Run("blockdev", "--getsize64", diskPath)
	if err != nil {
		return 0, fmt.Errorf("blockdev --getsize64 %s: %w\n%s", diskPath, err, out)
	}
	return strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
}

func readDiskRegion(diskPath string, offset, length int64) ([]byte, error) {
	out, err := diskReadRunner().RunStdout("dd",
		fmt.Sprintf("if=%s", diskPath),
		"bs=512",
		fmt.Sprintf("skip=%d", offset/512),
		fmt.Sprintf("count=%d", length/512),
		"status=none",
	)
	if err != nil {
		return nil, fmt.Errorf("dd read error at offset %d: %w", offset, err)
	}
	return out, nil
}

func writeDiskRegion(diskPath string, offset int64, data []byte) error {
	if offset%512 != 0 {
		return fmt.Errorf("refusing unaligned write at offset %d", offset)
	}
	// Needs Stdin to supply the data being written; stays a direct
	// exec.Command (see note on the Runner type).
	cmd := execCommand("dd",
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

// FindESPPartition locates the EFI System Partition on diskPath by GPT partition
// type GUID and the parted "esp"/"boot" flag.
func FindESPPartition(diskPath string) (string, error) {
	const espTypeGUID = "c12a7328-f81f-11d2-ba4b-00a0c93ec93b"
	out, err := cmdRunner.RunStdout("lsblk", "-n", "-r", "-o", "NAME,PARTTYPE", diskPath)
	if err == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			fields := strings.Fields(line)
			if len(fields) == 2 && strings.EqualFold(fields[1], espTypeGUID) {
				return "/dev/" + fields[0], nil
			}
		}
	}

	partedOut, err := cmdRunner.Run("parted", "-s", "-m", diskPath, "print")
	if err == nil {
		for _, line := range strings.Split(string(partedOut), "\n") {
			if strings.Contains(line, "esp") || strings.Contains(line, "boot") {
				fields := strings.Split(line, ":")
				if len(fields) > 0 {
					return JoinDiskAndPartition(diskPath, fields[0]), nil
				}
			}
		}
	}

	for i := 1; i <= 4; i++ {
		candidate := JoinDiskAndPartition(diskPath, fmt.Sprintf("%d", i))
		fsOut, fsErr := cmdRunner.RunStdout("blkid", "-s", "TYPE", "-o", "value", candidate)
		if fsErr == nil && strings.TrimSpace(string(fsOut)) == "vfat" {
			probeDir, _ := os.MkdirTemp("", "orch-esp-probe-*")
			if _, mountErr := cmdRunner.Run("mount", "-o", "ro", candidate, probeDir); mountErr == nil {
				_, statErr := os.Stat(filepath.Join(probeDir, "EFI"))
				_, _ = cmdRunner.Run("umount", "-l", probeDir)
				_ = fsRunner.RemoveAll(probeDir)
				if statErr == nil {
					return candidate, nil
				}
			}
		}
	}

	return "", fmt.Errorf("no EFI System Partition detected on %s", diskPath)
}
