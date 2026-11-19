//go:build linux

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

	"boot-orchestrator/safety"
)

type DetectedGuestOS struct {
	DevicePath     string `json:"device_path"`      // e.g. "/dev/sda2", "/dev/sdb3"
	DiskPath       string `json:"disk_path"`        // e.g. "/dev/sda", "/dev/sdb"
	PartNum        string `json:"part_num"`         // e.g. "3"
	RestorePartNum string `json:"restore_part_num"` // e.g. "2" (the host partition to expand)
	Name           string `json:"name"`             // e.g. "Windows Boot Manager", "Parrot OS"
	EFIDirName     string `json:"efi_dir_name"`     // e.g. "Microsoft", "parrot"
	TopologyMode   string `json:"topology_mode"`    // "dual-boot", "triple-boot"
	IsContiguous   bool   `json:"is_contiguous"`

	// OriginalSwapSizeBytes/OriginalSwapUUID (follow-up F14): populated
	// from the journal when install-time partitioning converted a
	// trailing swap slice into this guest's partition, so a revert can
	// recreate an equivalent swap partition. Only ever known for the
	// journal-sourced guest entry (os-prober/lsblk discovery has no way to
	// know what a partition used to be before install).
	OriginalSwapSizeBytes int64  `json:"original_swap_size_bytes,omitempty"`
	OriginalSwapUUID      string `json:"original_swap_uuid,omitempty"`
}

// DiscoverSecondaryOSes dynamically scans journal state, os-prober, and raw block devices.
func DiscoverSecondaryOSes() ([]DetectedGuestOS, error) {
	var guests []DetectedGuestOS
	activeDisk, activeHostPart, hostNum, _, _ := DetectActiveRootDisk()
	seenDevices := make(map[string]bool)

	// 1. Dynamic Journal Inspection. Checks both the current default
	// location (follow-up F12: a dedicated, non-world-writable state
	// directory outside the repo) and the legacy repo-relative paths, so a
	// record written by a version of this tool from before that change is
	// still found.
	defaultDir := filepath.Dir(safety.DefaultJournalPath())
	journalPaths := []string{
		"orchestrator_installed_os.json",
		"orchestrator_journal.json",
		filepath.Join(defaultDir, "orchestrator_installed_os.json"),
		safety.DefaultJournalPath(),
	}
	var j *safety.Journal
	for _, jp := range journalPaths {
		if loaded, err := safety.LoadJournal(jp); err == nil && loaded != nil {
			j = loaded
			break
		}
	}
	if j != nil {
		sess := j.Session
		targetDev := sess.TargetPartition
		targetDisk := sess.TargetDisk
		targetNum := sess.TargetPartNum
		hostRestoreNum := sess.ShrunkPartNum

		if targetDev != "" && targetDev != activeHostPart {
			if targetDisk == "" || targetNum == "" {
				targetDisk, targetNum, _ = SplitDiskAndPartition(targetDev)
			}
			if hostRestoreNum == "" {
				hostRestoreNum = findPrecedingPartition(targetDisk, targetNum)
			}

			if partitionExists(targetDisk, targetNum) {
				distroName := sess.DistroName
				if distroName == "" {
					distroName = sess.TargetOSID
				}
				if distroName == "" {
					distroName = "Target Provisioned OS"
				}

				guests = append(guests, DetectedGuestOS{
					DevicePath:            targetDev,
					DiskPath:              targetDisk,
					PartNum:               targetNum,
					RestorePartNum:        hostRestoreNum,
					Name:                  fmt.Sprintf("%s (from Journal)", distroName),
					EFIDirName:            sess.EFIDirName,
					IsContiguous:          hostRestoreNum != "",
					OriginalSwapSizeBytes: sess.OriginalSwapSizeBytes,
					OriginalSwapUUID:      sess.OriginalSwapUUID,
				})
				seenDevices[targetDev] = true
			}
		}
	}

	// 2. Discover via os-prober across all connected physical disks
	out, err := execCommand("os-prober").Output()
	if err == nil && len(out) > 0 {
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		for _, line := range lines {
			parts := strings.Split(line, ":")
			if len(parts) >= 3 {
				dev := strings.TrimSpace(parts[0])
				if dev == activeHostPart || seenDevices[dev] {
					continue
				}

				osName := strings.TrimSpace(parts[1])
				disk, partNum, splitErr := SplitDiskAndPartition(dev)
				if splitErr != nil {
					continue
				}

				restoreNum := findPrecedingPartition(disk, partNum)
				efiDir := inferEFIDir(osName)

				guests = append(guests, DetectedGuestOS{
					DevicePath:     dev,
					DiskPath:       disk,
					PartNum:        partNum,
					RestorePartNum: restoreNum,
					Name:           osName,
					EFIDirName:     efiDir,
					IsContiguous:   restoreNum != "",
				})
				seenDevices[dev] = true
			}
		}
	}

	// 3. Comprehensive Block Scan across all disks
	lsblkOut, err := execCommand("lsblk", "-rno", "NAME,TYPE,FSTYPE,LABEL,MOUNTPOINT").Output()
	if err == nil {
		lines := strings.Split(strings.TrimSpace(string(lsblkOut)), "\n")
		for _, line := range lines {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			devName := fields[0]
			devType := fields[1]

			if devType != "part" {
				continue
			}

			fullDev := "/dev/" + devName
			if fullDev == activeHostPart || seenDevices[fullDev] {
				continue
			}

			disk, partNum, splitErr := SplitDiskAndPartition(fullDev)
			if splitErr != nil {
				continue
			}

			fsType := ""
			label := ""
			mountPoint := ""
			if len(fields) >= 3 {
				fsType = fields[2]
			}
			if len(fields) >= 4 {
				label = fields[3]
			}
			if len(fields) >= 5 {
				mountPoint = fields[4]
			}

			// Do not touch boot/system partitions
			if fsType == "vfat" && (label == "EFI" || label == "BOOT" || mountPoint == "/boot/efi") {
				continue
			}

			isGuest := false
			detectedName := label

			// Condition A: Explicit secondary types
			if fsType == "ntfs" {
				isGuest = true
				if detectedName == "" {
					detectedName = "Windows Operating System"
				}
			} else if label == "STAGING_SLICE" {
				isGuest = true
				detectedName = "Staging Slice (Pending Eradication)"
			} else if (fsType == "ext4" || fsType == "btrfs" || fsType == "xfs") && label != "" {
				lowerLbl := strings.ToLower(label)
				if !strings.Contains(lowerLbl, "storage") && !strings.Contains(lowerLbl, "data") && !strings.Contains(lowerLbl, "backup") {
					isGuest = true
				}
			}

			// Condition B: Any extra partition on the active root disk (e.g. /dev/sdb3)
			if !isGuest && disk == activeDisk && fullDev != activeHostPart && mountPoint != "/boot/efi" {
				isGuest = true
				if detectedName == "" {
					detectedName = fmt.Sprintf("Unmanaged Partition (%s)", devName)
				}
			}

			if isGuest {
				restoreNum := findPrecedingPartition(disk, partNum)
				efiDir := inferEFIDir(detectedName)

				guests = append(guests, DetectedGuestOS{
					DevicePath:     fullDev,
					DiskPath:       disk,
					PartNum:        partNum,
					RestorePartNum: restoreNum,
					Name:           detectedName,
					EFIDirName:     efiDir,
					IsContiguous:   restoreNum != "",
				})
				seenDevices[fullDev] = true
			}
		}
	}

	// 4. Populate Topology Mode dynamically
	totalGuests := len(guests)
	for i := range guests {
		if totalGuests >= 2 {
			guests[i].TopologyMode = "triple-boot"
		} else {
			guests[i].TopologyMode = "dual-boot"
		}
		if guests[i].DiskPath == activeDisk && guests[i].RestorePartNum == "" {
			guests[i].RestorePartNum = hostNum
			guests[i].IsContiguous = true
		}
	}

	return guests, nil
}

func findPrecedingPartition(disk, currentPartNum string) string {
	cNum, err := strconv.Atoi(currentPartNum)
	if err != nil || cNum <= 1 {
		return ""
	}

	for candidate := cNum - 1; candidate >= 1; candidate-- {
		candStr := strconv.Itoa(candidate)
		if partitionExists(disk, candStr) {
			return candStr
		}
	}
	return ""
}

func inferEFIDir(osName string) string {
	lower := strings.ToLower(osName)
	espMount := findActiveESPMountFunc()

	candidates := []string{}
	switch {
	case strings.Contains(lower, "windows"):
		candidates = []string{"Microsoft"}
	case strings.Contains(lower, "parrot"):
		candidates = []string{"parrot", "Parrot"}
	case strings.Contains(lower, "ubuntu"):
		candidates = []string{"ubuntu"}
	case strings.Contains(lower, "fedora"):
		candidates = []string{"fedora"}
	case strings.Contains(lower, "arch"):
		candidates = []string{"arch"}
	case strings.Contains(lower, "debian") && !strings.Contains(lower, "kali"):
		candidates = []string{"debian"}
	default:
		tokens := strings.Fields(lower)
		if len(tokens) > 0 {
			candidates = []string{tokens[0]}
		}
	}

	for _, c := range candidates {
		target := filepath.Join(espMount, "EFI", c)
		if _, err := os.Stat(target); err == nil {
			return c
		}
	}
	return ""
}

func ExecuteRealReversion(p RealRevertParams, logFn func(string)) error {
	return ExecuteDecommissionWithOptions(p, false, logFn, nil)
}

func ExecuteRealReversionWithProgress(p RealRevertParams, logFn func(string), progressFn func(step string, percent float64)) error {
	return ExecuteDecommissionWithOptions(p, false, logFn, progressFn)
}

// grubCustomPath, grubDropinPaths, updateGrubFunc, and fstabPath are
// injectable (vars, not consts/inline calls) so a test can redirect
// ExecuteDecommissionWithOptions's GRUB-regeneration and fstab-cleanup
// steps to a scratch directory instead of the real /etc/grub.d,
// /etc/default/grub.d, and /etc/fstab. Bug F's investigation found this
// function had NO testability isolation at all — every real-system side
// effect was a literal system path, making it unsafe to exercise
// end-to-end in an automated test without this.
var grubCustomPath = "/etc/grub.d/40_custom"
var grubDropinPaths = []string{
	"/etc/default/grub.d/50-dualboot-menu.cfg",
	"/etc/default/grub.d/99-orchestrator-prober.cfg",
}
var updateGrubFunc = func() error { return execCommand("update-grub").Run() }
var fstabPath = "/etc/fstab"

// findActiveESPMountFunc and efibootmgrRemoveFunc complete that isolation
// for the two remaining real-system side effects bug F's investigation
// flagged as still hardcoded (follow-up F14): ESP-directory discovery and
// UEFI NVRAM boot-entry removal. A test points findActiveESPMountFunc at a
// scratch directory standing in for the ESP and replaces
// efibootmgrRemoveFunc with a spy to assert the boot entry number it would
// have removed, without ever touching the real ESP or real NVRAM.
var findActiveESPMountFunc = func() string {
	candidates := []string{"/boot/efi", "/efi", "/boot"}
	for _, c := range candidates {
		out, err := execCommand("findmnt", "-n", "-o", "FSTYPE", c).Output()
		if err == nil {
			fstype := strings.TrimSpace(string(out))
			if fstype == "vfat" || fstype == "msdos" {
				return c
			}
		}
	}
	return "/boot/efi"
}

var efibootmgrRemoveFunc = func(bootNum string) error {
	return execCommand("efibootmgr", "-b", bootNum, "-B").Run()
}

func ExecuteDecommissionWithOptions(p RealRevertParams, keepSlice bool, logFn func(string), progressFn func(step string, percent float64)) error {
	if logFn == nil {
		logFn = func(string) {}
	}
	if progressFn == nil {
		progressFn = func(string, float64) {}
	}

	progressFn("=> [REVERT 10%] Checking elevated privileges and verifying disk access...", 0.10)
	logFn("=> [REVERT ENGINE] Initializing multi-disk decommissioning pipeline...")
	if os.Geteuid() != 0 {
		return fmt.Errorf("root privileges required for disk and NVRAM restoration")
	}

	targetDev := JoinDiskAndPartition(p.DiskDevice, p.PartitionNum)

	// Re-resolve the live device by its stable PARTUUID when available,
	// since device letters/numbers can shift between install and revert
	// time (a USB plugged in a different order, an NVMe enumeration
	// change, ...). Bug F's investigation found this was never done on
	// Linux (unlike revert_darwin.go's equivalent path), so a shifted
	// device naming would have made this function blindly trust a stale
	// DiskDevice/PartitionNum and risk deleting/resizing the wrong
	// partition on the wrong disk.
	if p.TargetPartUUID != "" {
		if liveDev, resolveErr := resolveDeviceByPartUUID(p.TargetPartUUID); resolveErr == nil {
			targetDev = liveDev
			if disk, num, splitErr := SplitDiskAndPartition(liveDev); splitErr == nil {
				p.DiskDevice = disk
				p.PartitionNum = num
			}
			logFn(fmt.Sprintf("=> Resolved target by PARTUUID -> %s", targetDev))
		} else {
			logFn(fmt.Sprintf("=> PARTUUID resolve note: %s (falling back to stored device path)", resolveErr.Error()))
		}
	}

	// Last safety gate right before anything destructive: confirm the
	// device about to be deleted/resized still matches what was recorded
	// at install time.
	if verifyErr := verifyLinuxTargetBeforeDestruction(targetDev, p.ExpectedFSType, p.ExpectedSizeBytes); verifyErr != nil {
		return verifyErr
	}

	// 1. Force unmount target filesystem
	logFn(fmt.Sprintf("=> Force unmounting target filesystem on %s...", targetDev))
	_ = execCommand("umount", "-f", "-l", targetDev).Run()

	// 2. Wipe filesystem metadata & signatures
	logFn(fmt.Sprintf("=> Sanitizing signatures and unlocking %s...", targetDev))
	_ = execCommand("wipefs", "-a", "-f", targetDev).Run()

	// 3. UEFI NVRAM Cleanup
	if isUEFIModeFunc() && p.EFIBootEntryNum != "" {
		progressFn("=> [REVERT 25%] Purging UEFI NVRAM boot entry...", 0.25)
		cleanNum := strings.TrimPrefix(p.EFIBootEntryNum, "Boot")
		cleanNum = strings.TrimSpace(cleanNum)
		logFn(fmt.Sprintf("=> Removing UEFI NVRAM entry: Boot%s...", cleanNum))
		if err := efibootmgrRemoveFunc(cleanNum); err != nil {
			logFn(fmt.Sprintf("=> [WARNING] efibootmgr removal note: %v", err))
		}
	}

	// 4. ESP Directory Cleanup
	if isUEFIModeFunc() && p.EFIDirNameToPurge != "" {
		progressFn(fmt.Sprintf("=> [REVERT 35%%] Purging /EFI/%s from ESP...", p.EFIDirNameToPurge), 0.35)
		espBase := findActiveESPMountFunc()
		targetEFIDir := filepath.Join(espBase, "EFI", strings.TrimSpace(p.EFIDirNameToPurge))
		if _, err := os.Stat(targetEFIDir); err == nil {
			_ = fsRunner.RemoveAll(targetEFIDir)
			logFn(fmt.Sprintf("=> Successfully deleted EFI payload: %s", targetEFIDir))
		}
	}

	// 5. Branch Execution: Slice Preservation vs Full Eradication & Host Expansion
	if keepSlice {
		progressFn(fmt.Sprintf("=> [REVERT 60%%] Formatting %s as clean staging slice...", targetDev), 0.60)
		out, err := execCommand("mkfs.ext4", "-F", "-q", "-L", "STAGING_SLICE", targetDev).CombinedOutput()
		if err != nil {
			return fmt.Errorf("formatting clean partition slice: %s: %w", string(out), err)
		}
		_ = execCommand("sync").Run()
		logFn(fmt.Sprintf("=> Partition %s wiped and sanitized. Ready for next OS deployment.", targetDev))

	} else {
		progressFn(fmt.Sprintf("=> [REVERT 50%%] Eradicating partition %s on %s...", p.PartitionNum, p.DiskDevice), 0.50)
		logFn(fmt.Sprintf("=> Removing partition %s from GPT table on %s...", p.PartitionNum, p.DiskDevice))

		// Fix backup GPT header to end of disk so parted/sgdisk will not fail
		_ = execCommand("sgdisk", "-e", p.DiskDevice).Run()

		// Delete partition dynamically via sgdisk (-d)
		delCmd := execCommand("sgdisk", "-d", p.PartitionNum, p.DiskDevice)
		if out, err := delCmd.CombinedOutput(); err != nil {
			logFn(fmt.Sprintf("=> sgdisk delete failed: %s; running parted fallback...", strings.TrimSpace(string(out))))
			if out2, err2 := execCommand("parted", "-s", p.DiskDevice, "rm", p.PartitionNum).CombinedOutput(); err2 != nil {
				return fmt.Errorf("failed to delete partition %s on %s: sgdisk: %s | parted: %s",
					p.PartitionNum, p.DiskDevice, strings.TrimSpace(string(out)), strings.TrimSpace(string(out2)))
			}
		}

		_ = execCommand("sync").Run()

		// Tell the kernel directly to drop this partition from memory even if the root disk is busy
		_ = execCommand("delpart", p.DiskDevice, p.PartitionNum).Run()
		_ = execCommand("partx", "-d", "--nr", p.PartitionNum, p.DiskDevice).Run()
		_ = execCommand("partprobe", p.DiskDevice).Run()
		_ = execCommand("udevadm", "settle", "--timeout=5").Run()
		time.Sleep(300 * time.Millisecond)

		if p.HostPartNum != "" {
			progressFn(fmt.Sprintf("=> [REVERT 70%%] Expanding partition %s to 100%% of %s...", p.HostPartNum, p.DiskDevice), 0.70)
			logFn(fmt.Sprintf("=> Expanding boundary of partition %s on disk %s...", p.HostPartNum, p.DiskDevice))

			// Follow-up F14: if install-time partitioning converted a
			// trailing swap slice into the partition just removed, the host
			// must NOT grow all the way to 100% — that would consume the
			// space needed to recreate an equivalent swap partition below.
			// growpart always grows to use all following free space (it has
			// no "stop short" mode), so it is skipped entirely in that case
			// in favor of parted resizepart with an explicit end.
			hostResizeEnd := "100%"
			leaveSwapRoom := p.OriginalSwapSizeBytes > 0
			if leaveSwapRoom {
				if end, ok := hostResizeEndLeavingSwap(p.DiskDevice, p.OriginalSwapSizeBytes); ok {
					hostResizeEnd = end
				} else {
					logFn("=> [WARNING] could not determine disk geometry to leave room for the original swap slice; host will reclaim that space instead")
					leaveSwapRoom = false
				}
			}

			growWorked := false
			if !leaveSwapRoom {
				if _, err := exec.LookPath("growpart"); err == nil {
					if out, gErr := execCommand("growpart", p.DiskDevice, p.HostPartNum).CombinedOutput(); gErr == nil {
						growWorked = true
					} else {
						logFn(fmt.Sprintf("=> growpart note: %s", strings.TrimSpace(string(out))))
					}
				}
			}

			if !growWorked {
				if out, err := execCommand("parted", "-s", p.DiskDevice, "resizepart", p.HostPartNum, hostResizeEnd).CombinedOutput(); err != nil {
					logFn(fmt.Sprintf("=> parted resizepart failed: %s", strings.TrimSpace(string(out))))
				}
			}

			_ = execCommand("partx", "-u", "--nr", p.HostPartNum, p.DiskDevice).Run()
			_ = execCommand("udevadm", "settle", "--timeout=5").Run()
			time.Sleep(300 * time.Millisecond)

			restoredDev := JoinDiskAndPartition(p.DiskDevice, p.HostPartNum)
			logFn(fmt.Sprintf("=> Resizing filesystem online on %s...", restoredDev))

			fsTypeOut, _ := execCommand("blkid", "-s", "TYPE", "-o", "value", restoredDev).Output()
			fsType := strings.ToLower(strings.TrimSpace(string(fsTypeOut)))

			switch fsType {
			case "ntfs":
				_ = execCommand("ntfsresize", "-f", "-b", restoredDev).Run()
			case "btrfs":
				_ = execCommand("btrfs", "filesystem", "resize", "max", restoredDev).Run()
			default: // ext4
				cmdResizeFS := execCommand("resize2fs", restoredDev)
				if out, err := cmdResizeFS.CombinedOutput(); err != nil {
					logFn(fmt.Sprintf("=> resize2fs note: %s", strings.TrimSpace(string(out))))
				} else {
					logFn("=> Host filesystem successfully expanded to fill partition.")
				}
			}

			if leaveSwapRoom {
				progressFn("=> [REVERT 78%] Recreating the original swap partition...", 0.78)
				if swapErr := recreateSwapPartition(p.DiskDevice, p.HostPartNum, p.OriginalSwapSizeBytes, p.OriginalSwapUUID, logFn); swapErr != nil {
					logFn(fmt.Sprintf("=> [WARNING] swap recreation failed: %v (the host's original swap space was NOT restored)", swapErr))
				}
			}
		}
	}

	// 6. Clean Custom GRUB Stanzas & Regenerate GRUB
	progressFn("=> [REVERT 85%] Synchronizing bootloader and refreshing host GRUB...", 0.85)
	for _, dropin := range grubDropinPaths {
		_ = fsRunner.Remove(dropin)
	}

	defaultCustomGrub := `#!/bin/sh
exec tail -n +3 $0
# This file provides an easy way to add custom menu entries. Simply type the
# menu entries you want to add after this comment. Be careful not to change
# the 'exec tail' line above.
`
	_ = fsRunner.WriteFile(grubCustomPath, []byte(defaultCustomGrub), 0755)
	_ = execCommand("chmod", "+x", grubCustomPath).Run()

	logFn("=> Refreshing host GRUB configuration...")
	if err := updateGrubFunc(); err != nil {
		logFn(fmt.Sprintf("=> [WARNING] update-grub note: %v", err))
	}

	// 6b. fstab cleanup (audit bug F): if the deleted partition — or any
	// swap slice converted into it during install — is still referenced in
	// /etc/fstab by UUID or device path, that entry now points at nothing.
	// A dangling fstab entry for a filesystem can delay boot (systemd waits
	// on it) and a dangling swap entry fails silently; a missing root entry
	// is more serious. This does not attempt to recreate a swap slice that
	// install-time conversion may have consumed (this tool does not
	// currently record enough about the original layout — size, UUID — to
	// safely rebuild one; see PROGRESS.md's bug F entry), but it does stop
	// the host's own fstab from referencing a partition that no longer
	// exists after this revert.
	if !keepSlice {
		if changed, removed, err := cleanDanglingFstabEntries(fstabPath); err != nil {
			logFn(fmt.Sprintf("=> [WARNING] fstab cleanup note: %v", err))
		} else if changed {
			logFn(fmt.Sprintf("=> Commented out %d dangling /etc/fstab entr(ies) referencing the removed partition: %s", len(removed), strings.Join(removed, ", ")))
		}
	}

	// 7. Purge Journal State Files — both the current default location
	// (follow-up F12) and the legacy repo-relative paths.
	_ = fsRunner.Remove("orchestrator_journal.json")
	_ = fsRunner.Remove("orchestrator_installed_os.json")
	_ = fsRunner.Remove("/var/tmp/orchestrator_journal.json")
	_ = fsRunner.Remove(safety.DefaultJournalPath())
	_ = fsRunner.Remove(filepath.Join(filepath.Dir(safety.DefaultJournalPath()), "orchestrator_installed_os.json"))

	progressFn("=> [REVERT COMPLETE 100%] Decommissioning completed successfully.", 1.0)
	logFn("=> [SUCCESS] Decommissioning transaction completed cleanly.")
	time.Sleep(500 * time.Millisecond)
	return nil
}

// trailingFreeSectorRange runs `parted -m unit s print free` against
// diskDevice and returns the start/end sectors (inclusive) of the LAST
// "free;" region it reports — the trailing free space immediately after
// the current final partition. Used instead of hand-computed sector math
// so the boundaries handed to mkpart are always ones parted itself already
// considers valid (avoiding the GPT-backup-area off-by-a-few-sectors
// mismatch a manual "total sectors - N" calculation can produce).
func trailingFreeSectorRange(diskDevice string) (startSector, endSector int64, err error) {
	out, err := execCommand("parted", "-s", "-m", diskDevice, "unit", "s", "print", "free").Output()
	if err != nil {
		return 0, 0, err
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var lastStart, lastEnd int64
	found := false
	for _, line := range lines {
		fields := strings.Split(line, ":")
		if len(fields) < 5 || fields[len(fields)-1] != "free;" {
			continue
		}
		start, startErr := strconv.ParseInt(strings.TrimSuffix(fields[1], "s"), 10, 64)
		end, endErr := strconv.ParseInt(strings.TrimSuffix(fields[2], "s"), 10, 64)
		if startErr != nil || endErr != nil {
			continue
		}
		lastStart, lastEnd = start, end
		found = true
	}
	if !found {
		return 0, 0, fmt.Errorf("no free space reported on %s", diskDevice)
	}
	return lastStart, lastEnd, nil
}

// hostResizeEndLeavingSwap computes a parted resizepart END expression (in
// sectors) that stops the host partition swapSizeBytes short of the actual
// end of diskDevice's free space, leaving exactly that much trailing free
// space for recreateSwapPartition to carve a replacement swap slice from
// (follow-up F14). It anchors on parted's own reported usable last sector
// (via trailingFreeSectorRange) rather than the disk's raw sector count
// from blockdev, since GPT reserves a trailing region (its backup header
// and partition array) that raw sector count includes but parted will
// never actually let a partition use — subtracting swap sectors from the
// raw count instead would leave slightly LESS free space than
// swapSizeBytes, causing recreateSwapPartition to reject it as
// undersized. Returns ok=false if the disk's geometry can't be read or the
// swap size doesn't sanely fit, in which case the caller falls back to
// growing to 100% rather than blocking the whole revert on it.
func hostResizeEndLeavingSwap(diskDevice string, swapSizeBytes int64) (string, bool) {
	_, usableLastSector, freeErr := trailingFreeSectorRange(diskDevice)
	if freeErr != nil {
		return "", false
	}
	sectorSizeOut, err := execCommand("blockdev", "--getss", diskDevice).Output()
	if err != nil {
		return "", false
	}
	sectorSize, err := strconv.ParseInt(strings.TrimSpace(string(sectorSizeOut)), 10, 64)
	if err != nil || sectorSize <= 0 {
		return "", false
	}
	// Round the sector count up (not down) so the trailing free space left
	// behind is never smaller than swapSizeBytes even when it isn't an
	// exact multiple of the sector size.
	swapSectors := (swapSizeBytes + sectorSize - 1) / sectorSize
	if swapSectors <= 0 || swapSectors >= usableLastSector {
		return "", false
	}
	return fmt.Sprintf("%ds", usableLastSector-swapSectors), true
}

// recreateSwapPartition carves a new trailing swap partition of
// swapSizeBytes immediately after hostPartNum — which must already have
// been resized (via hostResizeEndLeavingSwap) to leave exactly this much
// trailing free space — restores its original GPT PARTUUID when known (so
// an /etc/fstab entry referencing it by PARTUUID keeps working across the
// revert), and formats it with mkswap. This restores the swap space that
// install-time partition conversion consumed (follow-up F14) instead of
// leaving the host permanently short that space after a revert.
func recreateSwapPartition(diskDevice, hostPartNum string, swapSizeBytes int64, originalPartUUID string, logFn func(string)) error {
	// Find the exact trailing free region parted itself reports, rather
	// than computing sector boundaries by hand from blockdev's raw sector
	// count: GPT reserves a tail region for its backup header/partition
	// array (commonly 33-34 sectors, but the exact count is not a
	// constant this code should hardcode), so a hand-computed "100%"/"-1s"
	// endpoint lands past parted's actual usable last sector and parted's
	// script mode refuses to auto-round a doubly-pinned start+end rather
	// than silently shrinking the request. Reading the boundary parted
	// already computed sidesteps that entirely.
	startSector, endSector, freeErr := trailingFreeSectorRange(diskDevice)
	if freeErr != nil {
		return fmt.Errorf("finding free space on %s: %w", diskDevice, freeErr)
	}
	sectorSizeOut, err := execCommand("blockdev", "--getss", diskDevice).Output()
	if err != nil {
		return fmt.Errorf("reading sector size of %s: %w", diskDevice, err)
	}
	sectorSize, err := strconv.ParseInt(strings.TrimSpace(string(sectorSizeOut)), 10, 64)
	if err != nil || sectorSize <= 0 {
		return fmt.Errorf("could not parse sector size of %s: %q", diskDevice, strings.TrimSpace(string(sectorSizeOut)))
	}
	if freeBytes := (endSector - startSector + 1) * sectorSize; freeBytes < swapSizeBytes {
		return fmt.Errorf("trailing free space on %s (%d bytes) is smaller than the recorded swap size (%d bytes)", diskDevice, freeBytes, swapSizeBytes)
	}

	hostPartNumInt, err := strconv.Atoi(hostPartNum)
	if err != nil {
		return fmt.Errorf("invalid host partition number %q: %w", hostPartNum, err)
	}
	newSwapPartNum := strconv.Itoa(hostPartNumInt + 1)

	if out, err := execCommand("parted", "-s", "-a", "none", diskDevice, "mkpart", "primary", "linux-swap",
		fmt.Sprintf("%ds", startSector), fmt.Sprintf("%ds", endSector)).CombinedOutput(); err != nil {
		return fmt.Errorf("creating swap partition: %s: %w", strings.TrimSpace(string(out)), err)
	}
	_ = execCommand("partprobe", diskDevice).Run()
	_ = execCommand("udevadm", "settle", "--timeout=5").Run()
	time.Sleep(300 * time.Millisecond)

	if strings.TrimSpace(originalPartUUID) != "" {
		if out, err := execCommand("sgdisk", "-u", newSwapPartNum+":"+strings.TrimSpace(originalPartUUID), diskDevice).CombinedOutput(); err != nil {
			logFn(fmt.Sprintf("=> [WARNING] could not restore original swap PARTUUID: %s", strings.TrimSpace(string(out))))
		}
		_ = execCommand("partprobe", diskDevice).Run()
		_ = execCommand("udevadm", "settle", "--timeout=5").Run()
		time.Sleep(300 * time.Millisecond)
	}

	swapDev := JoinDiskAndPartition(diskDevice, newSwapPartNum)
	if out, err := execCommand("mkswap", swapDev).CombinedOutput(); err != nil {
		return fmt.Errorf("mkswap %s: %s: %w", swapDev, strings.TrimSpace(string(out)), err)
	}
	logFn(fmt.Sprintf("=> Recreated swap partition %s and formatted it with mkswap.", swapDev))

	if err := ensureSwapFstabEntry(fstabPath, originalPartUUID); err != nil {
		logFn(fmt.Sprintf("=> [WARNING] could not add swap entry to fstab: %v", err))
	}
	return nil
}

// ensureSwapFstabEntry appends a "PARTUUID=... none swap sw 0 0" line to
// path if one referencing partUUID isn't already present, so the recreated
// swap partition is actually used on the next boot rather than sitting
// unused because nothing references it. A no-op if partUUID is empty (the
// original swap slice's PARTUUID wasn't recorded) or the file can't be
// read, since fstab is an optional nicety here, not a safety gate.
func ensureSwapFstabEntry(path, partUUID string) error {
	partUUID = strings.TrimSpace(partUUID)
	if partUUID == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			data = nil
		} else {
			return err
		}
	}
	if strings.Contains(strings.ToLower(string(data)), strings.ToLower(partUUID)) {
		return nil
	}
	line := fmt.Sprintf("PARTUUID=%s none swap sw 0 0\n", partUUID)
	out := string(data)
	if out != "" && !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	out += line
	return fsRunner.WriteFile(path, []byte(out), 0644)
}

// partitionExists checks for the partition's /sys/class/block entry. Audit
// 1C.8: this used to special-case only "nvme"/"mmcblk" by substring before
// assuming a bare "name+number" join, which mis-parses loop device
// partitions (loop0p1); now built via the same structural
// JoinDiskAndPartition the rest of the engine uses.
func partitionExists(diskDevice, partNum string) bool {
	partPath := JoinDiskAndPartition(diskDevice, partNum)
	partName := filepath.Base(partPath)
	_, err := os.Stat(filepath.Join("/sys/class/block", partName))
	return err == nil
}

// cleanDanglingFstabEntries comments out any non-comment, non-blank
// /etc/fstab line whose device specification (UUID=, PARTUUID=, LABEL=, or
// a raw /dev/... path) no longer resolves to a real block device. This
// catches both the just-deleted partition itself and any swap slice that
// install-time partition conversion consumed, without needing to have
// tracked which UUID belonged to which beforehand — the check is a
// generic "does this still exist", run after the deletion/grow/resize
// steps above have already happened.
func cleanDanglingFstabEntries(path string) (changed bool, removedSpecs []string, err error) {
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		if os.IsNotExist(readErr) {
			return false, nil, nil
		}
		return false, nil, readErr
	}

	lines := strings.Split(string(data), "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			out = append(out, line)
			continue
		}
		fields := strings.Fields(trimmed)
		spec := fields[0]
		if fstabDeviceExists(spec) {
			out = append(out, line)
			continue
		}
		changed = true
		removedSpecs = append(removedSpecs, spec)
		out = append(out, "# removed by boot-orchestrator revert (device no longer exists): "+line)
	}

	if !changed {
		return false, nil, nil
	}
	return true, removedSpecs, fsRunner.WriteFile(path, []byte(strings.Join(out, "\n")), 0644)
}

// fstabDeviceExists reports whether spec (an /etc/fstab first-field device
// specification) still resolves to a real block device. Non-block-device
// specs (tmpfs, swap files, network mounts, "none", cgroup2, ...) are not
// this function's concern and are always reported as "existing" so they're
// never touched.
func fstabDeviceExists(spec string) bool {
	switch {
	case strings.HasPrefix(spec, "UUID="):
		out, err := execCommand("blkid", "-U", strings.TrimPrefix(spec, "UUID=")).Output()
		return err == nil && strings.TrimSpace(string(out)) != ""
	case strings.HasPrefix(spec, "PARTUUID="):
		out, err := execCommand("blkid", "-t", "PARTUUID="+strings.TrimPrefix(spec, "PARTUUID="), "-o", "device").Output()
		return err == nil && strings.TrimSpace(string(out)) != ""
	case strings.HasPrefix(spec, "LABEL="):
		out, err := execCommand("blkid", "-L", strings.TrimPrefix(spec, "LABEL=")).Output()
		return err == nil && strings.TrimSpace(string(out)) != ""
	case strings.HasPrefix(spec, "/dev/"):
		_, statErr := os.Stat(spec)
		return statErr == nil
	default:
		return true
	}
}

// resolveDeviceByPartUUID re-resolves the live /dev path for a partition
// by its stable PARTUUID. Mirrors revert_darwin.go's
// resolveDeviceByPartitionUUID.
func resolveDeviceByPartUUID(partUUID string) (string, error) {
	if partUUID == "" {
		return "", fmt.Errorf("no partition UUID recorded — cannot safely resolve device")
	}
	out, err := execCommand("blkid", "-t", "PARTUUID="+partUUID, "-o", "device").Output()
	if err != nil || strings.TrimSpace(string(out)) == "" {
		return "", fmt.Errorf("no live partition currently has PARTUUID %s (drive may be unplugged, or already reverted)", partUUID)
	}
	return strings.TrimSpace(string(out)), nil
}

// verifyLinuxTargetBeforeDestruction mirrors revert_darwin.go's
// verifyDarwinTargetBeforeDestruction: a last safety gate right before any
// destructive operation, confirming devicePath still matches what the
// install-time record recorded (filesystem type, and size within a
// generous margin for sector/alignment rounding) before anything is
// deleted or resized. A failed query (can't read blkid/size) does not
// block — it can't verify, so it doesn't invent a reason to abort — but an
// actual mismatch does.
func verifyLinuxTargetBeforeDestruction(devicePath, expectedFSType string, expectedSizeBytes int64) error {
	if expectedFSType == "" && expectedSizeBytes == 0 {
		return nil
	}
	if expectedFSType != "" {
		out, err := execCommand("blkid", "-s", "TYPE", "-o", "value", devicePath).Output()
		if err == nil {
			actual := strings.ToLower(strings.TrimSpace(string(out)))
			if actual != "" && actual != strings.ToLower(expectedFSType) {
				return fmt.Errorf("safety abort: %s has filesystem %q but the install record recorded %q — refusing to delete", devicePath, actual, expectedFSType)
			}
		}
	}
	if expectedSizeBytes > 0 {
		if actualSize, err := blockDeviceSizeBytes(devicePath); err == nil && actualSize > 0 {
			diff := actualSize - expectedSizeBytes
			if diff < 0 {
				diff = -diff
			}
			const marginBytes = 64 * 1024 * 1024 // 64MiB: sector/alignment rounding tolerance
			if diff > marginBytes {
				return fmt.Errorf("safety abort: %s is %d bytes but the install record recorded %d bytes (diff %d exceeds the %d byte margin) — refusing to delete", devicePath, actualSize, expectedSizeBytes, diff, marginBytes)
			}
		}
	}
	return nil
}

// isUEFIModeFunc is injectable (follow-up F14) so a test can force the
// UEFI-mode gate on without needing real EFI firmware underneath the test
// process (a loop-image test runs on whatever hardware/VM hosts it, which
// may itself be BIOS-booted).
var isUEFIModeFunc = func() bool {
	_, err := os.Stat("/sys/firmware/efi")
	return err == nil
}

func RollbackRevertHandler(rollbackData json.RawMessage) error {
	var params RealRevertParams
	if err := json.Unmarshal(rollbackData, &params); err != nil {
		return fmt.Errorf("unmarshaling revert rollback params: %w", err)
	}
	return ExecuteRealReversion(params, nil)
}
