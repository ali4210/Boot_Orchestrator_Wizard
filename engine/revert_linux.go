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
	_ = os.Remove("/etc/default/grub.d/50-dualboot-menu.cfg")
	_ = os.Remove("/etc/default/grub.d/99-orchestrator-prober.cfg")

	defaultCustomGrub := `#!/bin/sh
exec tail -n +3 $0
# This file provides an easy way to add custom menu entries. Simply type the
# menu entries you want to add after this comment. Be careful not to change
# the 'exec tail' line above.
`
	_ = os.WriteFile("/etc/grub.d/40_custom", []byte(defaultCustomGrub), 0755)
	_ = exec.Command("chmod", "+x", "/etc/grub.d/40_custom").Run()

	logFn("=> Refreshing host GRUB configuration...")
	_ = exec.Command("update-grub").Run()

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
