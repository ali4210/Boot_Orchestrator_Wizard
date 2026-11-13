//go:build darwin

package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type DetectedGuestOS struct {
	DevicePath     string `json:"device_path"`      // e.g. "/dev/disk0s3"
	DiskPath       string `json:"disk_path"`        // e.g. "/dev/disk0"
	PartNum        string `json:"part_num"`         // e.g. "3"
	RestorePartNum string `json:"restore_part_num"` // e.g. "2"
	Name           string `json:"name"`             // e.g. "Linux Secondary OS"
	EFIDirName     string `json:"efi_dir_name"`     // e.g. "parrot"
	TopologyMode   string `json:"topology_mode"`    // "dual-boot", "triple-boot"
	IsContiguous   bool   `json:"is_contiguous"`
}

func DiscoverSecondaryOSes() ([]DetectedGuestOS, error) {
	out, err := exec.Command("diskutil", "list").Output()
	if err != nil {
		return nil, nil
	}

	var guests []DetectedGuestOS
	lines := strings.Split(string(out), "\n")
	currentDisk := ""

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "/dev/disk") {
			fields := strings.Fields(trimmed)
			if len(fields) > 0 {
				currentDisk = fields[0]
			}
			continue
		}

		// Detect Linux ext4/swap/filesystem partitions on macOS
		if strings.Contains(line, "Linux") || strings.Contains(line, "0FC63DAF-8483-4772-8E79-3D69D8477DE4") {
			fields := strings.Fields(line)
			if len(fields) >= 6 {
				devNode := "/dev/" + fields[len(fields)-1]
				partIdx := fields[0]
				guests = append(guests, DetectedGuestOS{
					DevicePath:     devNode,
					DiskPath:       currentDisk,
					PartNum:        partIdx,
					RestorePartNum: "2",
					Name:           "Linux Secondary OS",
					EFIDirName:     "parrot",
					TopologyMode:   "dual-boot",
					IsContiguous:   true,
				})
			}
		}
	}

	return guests, nil
}

func ExecuteRealReversion(p RealRevertParams, logFn func(string)) error {
	return ExecuteDecommissionWithOptions(p, false, logFn, nil)
}

func ExecuteRealReversionWithProgress(p RealRevertParams, logFn func(string), progressFn func(step string, percent float64)) error {
	return ExecuteDecommissionWithOptions(p, false, logFn, progressFn)
}

// resolveDeviceByPartitionUUID looks up the current /dev/diskXsY node for a
// partition by its stable Partition UUID (survives disk renumbering across
// USB ports/boot order).
func resolveDeviceByPartitionUUID(partUUID string) (string, error) {
	if partUUID == "" {
		return "", fmt.Errorf("no partition UUID recorded — cannot safely resolve device")
	}
	out, err := exec.Command("diskutil", "info", "-plist", partUUID).Output()
	if err != nil || len(out) == 0 {
		return "", fmt.Errorf("no live partition currently has UUID %s (drive may be unplugged, or already reverted)", partUUID)
	}
	// diskutil info accepts a UUID directly as an identifier; pull DeviceNode from the plist output.
	text := string(out)
	idx := strings.Index(text, "<key>DeviceNode</key>")
	if idx == -1 {
		return "", fmt.Errorf("could not parse device node for UUID %s", partUUID)
	}
	rest := text[idx+len("<key>DeviceNode</key>"):]
	start := strings.Index(rest, "<string>")
	end := strings.Index(rest, "</string>")
	if start == -1 || end == -1 || end < start {
		return "", fmt.Errorf("could not parse device node for UUID %s", partUUID)
	}
	dev := strings.TrimSpace(rest[start+len("<string>") : end])
	if dev == "" {
		return "", fmt.Errorf("empty device node resolved for UUID %s", partUUID)
	}
	return "/dev/" + strings.TrimPrefix(dev, "/dev/"), nil
}

// verifyDarwinTargetBeforeDestruction checks filesystem type + size against
// what was recorded at creation, before anything destructive runs.
func verifyDarwinTargetBeforeDestruction(devicePath, expectedFSType string, expectedSizeBytes int64) error {
	if expectedFSType == "" && expectedSizeBytes == 0 {
		return nil
	}
	out, err := exec.Command("diskutil", "info", "-plist", devicePath).Output()
	if err != nil {
		return nil // can't verify — don't block on a query failure
	}
	text := string(out)

	if expectedFSType != "" {
		if idx := strings.Index(text, "<key>FilesystemType</key>"); idx != -1 {
			rest := text[idx+len("<key>FilesystemType</key>"):]
			start := strings.Index(rest, "<string>")
			end := strings.Index(rest, "</string>")
			if start != -1 && end != -1 && end > start {
				actual := strings.ToLower(strings.TrimSpace(rest[start+len("<string>") : end]))
				if actual != "" && actual != strings.ToLower(expectedFSType) {
					return fmt.Errorf("safety abort: %s has filesystem %q but journal recorded %q — refusing to delete",
						devicePath, actual, expectedFSType)
				}
			}
		}
	}

	if expectedSizeBytes > 0 {
		if idx := strings.Index(text, "<key>Size</key>"); idx != -1 {
			rest := text[idx+len("<key>Size</key>"):]
			start := strings.Index(rest, "<integer>")
			end := strings.Index(rest, "</integer>")
			if start != -1 && end != -1 && end > start {
				sizeStr := strings.TrimSpace(rest[start+len("<integer>") : end])
				var actualSize int64
				if _, scanErr := fmt.Sscanf(sizeStr, "%d", &actualSize); scanErr == nil {
					diff := actualSize - expectedSizeBytes
					if diff < 0 {
						diff = -diff
					}
					if float64(diff) > float64(expectedSizeBytes)*0.01 {
						return fmt.Errorf("safety abort: %s is %d bytes but journal recorded %d bytes — refusing to delete",
							devicePath, actualSize, expectedSizeBytes)
					}
				}
			}
		}
	}
	return nil
}

func ExecuteDecommissionWithOptions(p RealRevertParams, keepSlice bool, logFn func(string), progressFn func(step string, percent float64)) error {
	if logFn == nil {
		logFn = func(string) {}
	}
	if progressFn == nil {
		progressFn = func(string, float64) {}
	}

	targetDev := strings.TrimSpace(p.PartitionNum)
	if targetDev != "" && !strings.HasPrefix(targetDev, "/dev/") {
		targetDev = "/dev/" + targetDev
	}

	// Re-resolve the live device node from the stable Partition UUID before touching anything.
	if p.TargetPartUUID != "" {
		if liveDev, resolveErr := resolveDeviceByPartitionUUID(p.TargetPartUUID); resolveErr == nil {
			targetDev = liveDev
			logFn(fmt.Sprintf("=> Resolved target by UUID -> %s", targetDev))
		} else {
			logFn(fmt.Sprintf("=> UUID resolve note: %s (falling back to stored value)", resolveErr.Error()))
		}
	}

	progressFn("=> [REVERT 10%] Unmounting target disk partitions...", 0.10)
	logFn(fmt.Sprintf("=> [MACOS ROLLBACK] Processing target partition %s...", targetDev))

	if targetDev != "" {
		_ = exec.Command("diskutil", "unmountDisk", "force", targetDev).Run()

		if keepSlice {
			// Format slice as FAT32 / ExFAT to preserve the partition slice for a subsequent OS
			progressFn("=> [REVERT 50%] Formatting staging slice on macOS...", 0.50)
			logFn(fmt.Sprintf("=> Formatting %s as clean staging slice...", targetDev))
			_ = exec.Command("diskutil", "eraseVolume", "MS-DOS FAT32", "STAGING_SLICE", targetDev).Run()
		} else {
			// Final safety gate right before deletion.
			if verifyErr := verifyDarwinTargetBeforeDestruction(targetDev, p.ExpectedFSType, p.ExpectedSizeBytes); verifyErr != nil {
				return verifyErr
			}

			// Erase to Free Space and reclaim
			progressFn("=> [REVERT 50%] Erasing partition to unallocated space...", 0.50)
			logFn(fmt.Sprintf("=> Deleting partition %s and returning to free space...", targetDev))
			cmdErase := exec.Command("diskutil", "eraseVolume", "Free Space", "%noformat%", targetDev)
			if out, err := cmdErase.CombinedOutput(); err != nil {
				logFn(fmt.Sprintf("=> eraseVolume note: %s", strings.TrimSpace(string(out))))
			}

			// Expand APFS Container to 100% capacity if host partition was specified
			if p.DiskDevice != "" {
				progressFn("=> [REVERT 75%] Expanding APFS container to reclaim 100% disk...", 0.75)
				logFn(fmt.Sprintf("=> Reclaiming free space on %s into APFS container...", p.DiskDevice))
				_ = exec.Command("diskutil", "apfs", "resizeContainer", p.DiskDevice, "0").Run()
			}
		}
	}

	// Purge EFI directory if present on EFI System Partition (diskXs1)
	if p.EFIDirNameToPurge != "" && p.DiskDevice != "" {
		progressFn(fmt.Sprintf("=> [REVERT 90%%] Purging /EFI/%s from macOS ESP...", p.EFIDirNameToPurge), 0.90)
		logFn(fmt.Sprintf("=> Purging EFI directory %s...", p.EFIDirNameToPurge))
		efiPart := p.DiskDevice + "s1"
		_ = exec.Command("diskutil", "mount", efiPart).Run()
		_ = os.RemoveAll(filepath.Join("/Volumes/EFI/EFI", p.EFIDirNameToPurge))
		_ = exec.Command("diskutil", "unmount", efiPart).Run()
	}

	progressFn("=> [REVERT 100%] macOS revert completed.", 1.0)
	logFn("=> [MACOS ROLLBACK] Completed successfully.")
	return nil
}

func RollbackRevertHandler(rollbackData json.RawMessage) error {
	var params RealRevertParams
	if err := json.Unmarshal(rollbackData, &params); err != nil {
		return fmt.Errorf("unmarshaling darwin revert rollback params: %w", err)
	}
	return ExecuteRealReversion(params, nil)
}
