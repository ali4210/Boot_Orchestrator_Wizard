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
					DevicePath:     targetDev,
					DiskPath:       targetDisk,
					PartNum:        targetNum,
					RestorePartNum: hostRestoreNum,
					Name:           fmt.Sprintf("%s (from Journal)", distroName),
					EFIDirName:     sess.EFIDirName,
					IsContiguous:   hostRestoreNum != "",
				})
				seenDevices[targetDev] = true
			}
		}
	}

	// 2. Discover via os-prober across all connected physical disks
	out, err := exec.Command("os-prober").Output()
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
	lsblkOut, err := exec.Command("lsblk", "-rno", "NAME,TYPE,FSTYPE,LABEL,MOUNTPOINT").Output()
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
	espMount := findActiveESPMount()

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
// effect (these, plus findActiveESPMount and efibootmgr, which remain
// hardcoded) was a literal system path, making it unsafe to exercise
// end-to-end in an automated test without this.
var grubCustomPath = "/etc/grub.d/40_custom"
var grubDropinPaths = []string{
	"/etc/default/grub.d/50-dualboot-menu.cfg",
	"/etc/default/grub.d/99-orchestrator-prober.cfg",
}
var updateGrubFunc = func() error { return exec.Command("update-grub").Run() }
var fstabPath = "/etc/fstab"

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
	_ = exec.Command("umount", "-f", "-l", targetDev).Run()

	// 2. Wipe filesystem metadata & signatures
	logFn(fmt.Sprintf("=> Sanitizing signatures and unlocking %s...", targetDev))
	_ = exec.Command("wipefs", "-a", "-f", targetDev).Run()

	// 3. UEFI NVRAM Cleanup
	if isUEFIMode() && p.EFIBootEntryNum != "" {
		progressFn("=> [REVERT 25%] Purging UEFI NVRAM boot entry...", 0.25)
		cleanNum := strings.TrimPrefix(p.EFIBootEntryNum, "Boot")
		cleanNum = strings.TrimSpace(cleanNum)
		logFn(fmt.Sprintf("=> Removing UEFI NVRAM entry: Boot%s...", cleanNum))
		_ = exec.Command("efibootmgr", "-b", cleanNum, "-B").Run()
	}

	// 4. ESP Directory Cleanup
	if isUEFIMode() && p.EFIDirNameToPurge != "" {
		progressFn(fmt.Sprintf("=> [REVERT 35%%] Purging /EFI/%s from ESP...", p.EFIDirNameToPurge), 0.35)
		espBase := findActiveESPMount()
		targetEFIDir := filepath.Join(espBase, "EFI", strings.TrimSpace(p.EFIDirNameToPurge))
		if _, err := os.Stat(targetEFIDir); err == nil {
			_ = os.RemoveAll(targetEFIDir)
			logFn(fmt.Sprintf("=> Successfully deleted EFI payload: %s", targetEFIDir))
		}
	}

	// 5. Branch Execution: Slice Preservation vs Full Eradication & Host Expansion
	if keepSlice {
		progressFn(fmt.Sprintf("=> [REVERT 60%%] Formatting %s as clean staging slice...", targetDev), 0.60)
		out, err := exec.Command("mkfs.ext4", "-F", "-q", "-L", "STAGING_SLICE", targetDev).CombinedOutput()
		if err != nil {
			return fmt.Errorf("formatting clean partition slice: %s: %w", string(out), err)
		}
		_ = exec.Command("sync").Run()
		logFn(fmt.Sprintf("=> Partition %s wiped and sanitized. Ready for next OS deployment.", targetDev))

	} else {
		progressFn(fmt.Sprintf("=> [REVERT 50%%] Eradicating partition %s on %s...", p.PartitionNum, p.DiskDevice), 0.50)
		logFn(fmt.Sprintf("=> Removing partition %s from GPT table on %s...", p.PartitionNum, p.DiskDevice))

		// Fix backup GPT header to end of disk so parted/sgdisk will not fail
		_ = exec.Command("sgdisk", "-e", p.DiskDevice).Run()

		// Delete partition dynamically via sgdisk (-d)
		delCmd := exec.Command("sgdisk", "-d", p.PartitionNum, p.DiskDevice)
		if out, err := delCmd.CombinedOutput(); err != nil {
			logFn(fmt.Sprintf("=> sgdisk delete failed: %s; running parted fallback...", strings.TrimSpace(string(out))))
			if out2, err2 := exec.Command("parted", "-s", p.DiskDevice, "rm", p.PartitionNum).CombinedOutput(); err2 != nil {
				return fmt.Errorf("failed to delete partition %s on %s: sgdisk: %s | parted: %s",
					p.PartitionNum, p.DiskDevice, strings.TrimSpace(string(out)), strings.TrimSpace(string(out2)))
			}
		}

		_ = exec.Command("sync").Run()

		// Tell the kernel directly to drop this partition from memory even if the root disk is busy
		_ = exec.Command("delpart", p.DiskDevice, p.PartitionNum).Run()
		_ = exec.Command("partx", "-d", "--nr", p.PartitionNum, p.DiskDevice).Run()
		_ = exec.Command("partprobe", p.DiskDevice).Run()
		_ = exec.Command("udevadm", "settle", "--timeout=5").Run()
		time.Sleep(300 * time.Millisecond)

		if p.HostPartNum != "" {
			progressFn(fmt.Sprintf("=> [REVERT 70%%] Expanding partition %s to 100%% of %s...", p.HostPartNum, p.DiskDevice), 0.70)
			logFn(fmt.Sprintf("=> Expanding boundary of partition %s on disk %s...", p.HostPartNum, p.DiskDevice))

			growWorked := false
			if _, err := exec.LookPath("growpart"); err == nil {
				if out, gErr := exec.Command("growpart", p.DiskDevice, p.HostPartNum).CombinedOutput(); gErr == nil {
					growWorked = true
				} else {
					logFn(fmt.Sprintf("=> growpart note: %s", strings.TrimSpace(string(out))))
				}
			}

			if !growWorked {
				if out, err := exec.Command("parted", "-s", p.DiskDevice, "resizepart", p.HostPartNum, "100%").CombinedOutput(); err != nil {
					logFn(fmt.Sprintf("=> parted resizepart failed: %s", strings.TrimSpace(string(out))))
				}
			}

			_ = exec.Command("partx", "-u", "--nr", p.HostPartNum, p.DiskDevice).Run()
			_ = exec.Command("udevadm", "settle", "--timeout=5").Run()
			time.Sleep(300 * time.Millisecond)

			restoredDev := JoinDiskAndPartition(p.DiskDevice, p.HostPartNum)
			logFn(fmt.Sprintf("=> Resizing filesystem online on %s...", restoredDev))

			fsTypeOut, _ := exec.Command("blkid", "-s", "TYPE", "-o", "value", restoredDev).Output()
			fsType := strings.ToLower(strings.TrimSpace(string(fsTypeOut)))

			switch fsType {
			case "ntfs":
				_ = exec.Command("ntfsresize", "-f", "-b", restoredDev).Run()
			case "btrfs":
				_ = exec.Command("btrfs", "filesystem", "resize", "max", restoredDev).Run()
			default: // ext4
				cmdResizeFS := exec.Command("resize2fs", restoredDev)
				if out, err := cmdResizeFS.CombinedOutput(); err != nil {
					logFn(fmt.Sprintf("=> resize2fs note: %s", strings.TrimSpace(string(out))))
				} else {
					logFn("=> Host filesystem successfully expanded to fill partition.")
				}
			}
		}
	}

	// 6. Clean Custom GRUB Stanzas & Regenerate GRUB
	progressFn("=> [REVERT 85%] Synchronizing bootloader and refreshing host GRUB...", 0.85)
	for _, dropin := range grubDropinPaths {
		_ = os.Remove(dropin)
	}

	defaultCustomGrub := `#!/bin/sh
exec tail -n +3 $0
# This file provides an easy way to add custom menu entries. Simply type the
# menu entries you want to add after this comment. Be careful not to change
# the 'exec tail' line above.
`
	_ = os.WriteFile(grubCustomPath, []byte(defaultCustomGrub), 0755)
	_ = exec.Command("chmod", "+x", grubCustomPath).Run()

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
	_ = os.Remove("orchestrator_journal.json")
	_ = os.Remove("orchestrator_installed_os.json")
	_ = os.Remove("/var/tmp/orchestrator_journal.json")
	_ = os.Remove(safety.DefaultJournalPath())
	_ = os.Remove(filepath.Join(filepath.Dir(safety.DefaultJournalPath()), "orchestrator_installed_os.json"))

	progressFn("=> [REVERT COMPLETE 100%] Decommissioning completed successfully.", 1.0)
	logFn("=> [SUCCESS] Decommissioning transaction completed cleanly.")
	time.Sleep(500 * time.Millisecond)
	return nil
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
	return true, removedSpecs, os.WriteFile(path, []byte(strings.Join(out, "\n")), 0644)
}

// fstabDeviceExists reports whether spec (an /etc/fstab first-field device
// specification) still resolves to a real block device. Non-block-device
// specs (tmpfs, swap files, network mounts, "none", cgroup2, ...) are not
// this function's concern and are always reported as "existing" so they're
// never touched.
func fstabDeviceExists(spec string) bool {
	switch {
	case strings.HasPrefix(spec, "UUID="):
		out, err := exec.Command("blkid", "-U", strings.TrimPrefix(spec, "UUID=")).Output()
		return err == nil && strings.TrimSpace(string(out)) != ""
	case strings.HasPrefix(spec, "PARTUUID="):
		out, err := exec.Command("blkid", "-t", "PARTUUID="+strings.TrimPrefix(spec, "PARTUUID="), "-o", "device").Output()
		return err == nil && strings.TrimSpace(string(out)) != ""
	case strings.HasPrefix(spec, "LABEL="):
		out, err := exec.Command("blkid", "-L", strings.TrimPrefix(spec, "LABEL=")).Output()
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
	out, err := exec.Command("blkid", "-t", "PARTUUID="+partUUID, "-o", "device").Output()
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
		out, err := exec.Command("blkid", "-s", "TYPE", "-o", "value", devicePath).Output()
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

func isUEFIMode() bool {
	_, err := os.Stat("/sys/firmware/efi")
	return err == nil
}

func findActiveESPMount() string {
	candidates := []string{"/boot/efi", "/efi", "/boot"}
	for _, c := range candidates {
		out, err := exec.Command("findmnt", "-n", "-o", "FSTYPE", c).Output()
		if err == nil {
			fstype := strings.TrimSpace(string(out))
			if fstype == "vfat" || fstype == "msdos" {
				return c
			}
		}
	}
	return "/boot/efi"
}

func RollbackRevertHandler(rollbackData json.RawMessage) error {
	var params RealRevertParams
	if err := json.Unmarshal(rollbackData, &params); err != nil {
		return fmt.Errorf("unmarshaling revert rollback params: %w", err)
	}
	return ExecuteRealReversion(params, nil)
}
