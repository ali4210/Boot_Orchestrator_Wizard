//go:build windows

package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"boot-orchestrator/safety"
)

type DetectedGuestOS struct {
	DevicePath     string `json:"device_path"`      // e.g. "Disk 0, Partition 3"
	DiskPath       string `json:"disk_path"`        // e.g. "0"
	PartNum        string `json:"part_num"`         // e.g. "3"
	RestorePartNum string `json:"restore_part_num"` // e.g. "2"
	Name           string `json:"name"`             // e.g. "Linux Secondary OS"
	EFIDirName     string `json:"efi_dir_name"`     // e.g. "parrot", "ubuntu"
	TopologyMode   string `json:"topology_mode"`    // "dual-boot", "triple-boot"
	IsContiguous   bool   `json:"is_contiguous"`

	// OriginalSwapSizeBytes/OriginalSwapUUID (follow-up F14, Linux-only
	// feature): kept here only so this struct's shape matches
	// revert_linux.go's across platforms — ui/model.go has no build tag
	// and reads these fields unconditionally. Windows has no swap
	// partition concept to convert/restore, so these are always zero/
	// empty here.
	OriginalSwapSizeBytes int64  `json:"original_swap_size_bytes,omitempty"`
	OriginalSwapUUID      string `json:"original_swap_uuid,omitempty"`
}

// DiscoverSecondaryOSes dynamically checks journals, BCD records, and partition GUIDs.
func DiscoverSecondaryOSes() ([]DetectedGuestOS, error) {
	var results []DetectedGuestOS
	seenParts := make(map[string]bool)

	// 1. Journal Inspection: Inspect orchestrator records first. Checks
	// both the current default location (follow-up F12) and the legacy
	// repo-relative paths.
	defaultDir := filepath.Dir(safety.DefaultJournalPath())
	journalPaths := []string{
		"orchestrator_installed_os.json",
		"orchestrator_journal.json",
		filepath.Join(defaultDir, "orchestrator_installed_os.json"),
		safety.DefaultJournalPath(),
	}
	for _, jp := range journalPaths {
		if j, err := safety.LoadJournal(jp); err == nil && j != nil {
			sess := j.Session
			if sess.TargetDisk != "" && sess.TargetPartNum != "" {
				re := regexp.MustCompile(`\d+`)
				diskNum := re.FindString(sess.TargetDisk)
				partKey := fmt.Sprintf("%s:%s", diskNum, sess.TargetPartNum)

				if !seenParts[partKey] {
					distro := sess.DistroName
					if distro == "" {
						distro = sess.TargetOSID
					}
					if distro == "" {
						distro = "Linux Secondary OS"
					}

					efiDir := sess.EFIDirName
					if efiDir == "" {
						efiDir = inferWindowsEFIDir(distro)
					}

					results = append(results, DetectedGuestOS{
						DevicePath:     fmt.Sprintf("Disk %s, Partition %s", diskNum, sess.TargetPartNum),
						DiskPath:       diskNum,
						PartNum:        sess.TargetPartNum,
						RestorePartNum: sess.ShrunkPartNum,
						Name:           fmt.Sprintf("%s (from Journal)", distro),
						EFIDirName:     efiDir,
						TopologyMode:   "dual-boot",
						IsContiguous:   sess.ShrunkPartNum != "",
					})
					seenParts[partKey] = true
				}
			}
		}
	}

	// 2. Query Windows Partition subsystem for Linux GPT Type GUID
	psCmd := `Get-Partition | Where-Object { $_.Type -like "*Linux*" -or $_.GptType -eq "{0fc63daf-8483-4772-8e79-3d69d8477de4}" } | Select-Object DiskNumber, PartitionNumber | ConvertTo-Json`
	out, err := exec.Command("powershell.exe", "-NoProfile", "-Command", psCmd).Output()
	if err == nil && len(strings.TrimSpace(string(out))) > 0 {
		type winPart struct {
			DiskNumber      int `json:"DiskNumber"`
			PartitionNumber int `json:"PartitionNumber"`
		}

		var single winPart
		if jErr := json.Unmarshal(out, &single); jErr == nil {
			partKey := fmt.Sprintf("%d:%d", single.DiskNumber, single.PartitionNumber)
			if !seenParts[partKey] {
				results = append(results, DetectedGuestOS{
					DevicePath:     fmt.Sprintf("Disk %d, Partition %d", single.DiskNumber, single.PartitionNumber),
					DiskPath:       fmt.Sprintf("%d", single.DiskNumber),
					PartNum:        fmt.Sprintf("%d", single.PartitionNumber),
					RestorePartNum: fmt.Sprintf("%d", single.PartitionNumber-1),
					Name:           "Linux Secondary OS",
					EFIDirName:     "parrot",
					TopologyMode:   "dual-boot",
					IsContiguous:   true,
				})
				seenParts[partKey] = true
			}
		}

		var list []winPart
		if jErr := json.Unmarshal(out, &list); jErr == nil {
			for _, p := range list {
				partKey := fmt.Sprintf("%d:%d", p.DiskNumber, p.PartitionNumber)
				if !seenParts[partKey] {
					results = append(results, DetectedGuestOS{
						DevicePath:     fmt.Sprintf("Disk %d, Partition %d", p.DiskNumber, p.PartitionNumber),
						DiskPath:       fmt.Sprintf("%d", p.DiskNumber),
						PartNum:        fmt.Sprintf("%d", p.PartitionNumber),
						RestorePartNum: fmt.Sprintf("%d", p.PartitionNumber-1),
						Name:           fmt.Sprintf("Linux OS (Disk %d Part %d)", p.DiskNumber, p.PartitionNumber),
						EFIDirName:     "parrot",
						TopologyMode:   "dual-boot",
						IsContiguous:   true,
					})
					seenParts[partKey] = true
				}
			}
		}
	}

	return results, nil
}

func inferWindowsEFIDir(name string) string {
	lower := strings.ToLower(name)
	switch {
	case strings.Contains(lower, "parrot"):
		return "parrot"
	case strings.Contains(lower, "ubuntu"):
		return "ubuntu"
	case strings.Contains(lower, "fedora"):
		return "fedora"
	case strings.Contains(lower, "arch"):
		return "arch"
	case strings.Contains(lower, "debian"):
		return "debian"
	default:
		return "parrot"
	}
}

func ExecuteRealReversion(p RealRevertParams, logFn func(string)) error {
	return ExecuteDecommissionWithOptions(p, false, logFn, nil)
}

func ExecuteRealReversionWithProgress(p RealRevertParams, logFn func(string), progressFn func(step string, percent float64)) error {
	return ExecuteDecommissionWithOptions(p, false, logFn, progressFn)
}

// verifyWindowsTargetBeforeDestruction ensures we never touch the active Windows OS or ESP.
func verifyWindowsTargetBeforeDestruction(diskDigits, partNum string) error {
	psCheck := fmt.Sprintf(`
$p = Get-Partition -DiskNumber %s -PartitionNumber %s -ErrorAction SilentlyContinue
if (-not $p) {
    throw "Target partition does not exist on disk %s."
}
if ($p.IsSystem -or $p.IsBoot -or $p.DriveLetter -eq 'C') {
    throw "Safety abort: Partition %s is the active Windows Boot/System drive."
}
if ($p.GptType -eq "{c12a7328-f81f-11d2-ba4b-00a0c93ec93b}") {
    throw "Safety abort: Partition %s is the EFI System Partition (ESP)."
}
`, diskDigits, partNum, diskDigits, partNum, partNum)

	out, err := exec.Command("powershell.exe", "-NoProfile", "-Command", psCheck).CombinedOutput()
	if err != nil {
		return fmt.Errorf("safety check abort: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// ExecuteDecommissionWithOptions handles partition eradication and BCD/ESP cleanup on Windows.
func ExecuteDecommissionWithOptions(p RealRevertParams, keepSlice bool, logFn func(string), progressFn func(step string, percent float64)) error {
	if logFn == nil {
		logFn = func(string) {}
	}
	if progressFn == nil {
		progressFn = func(string, float64) {}
	}

	progressFn("=> [REVERT 10%] Parsing Windows storage partition table...", 0.10)
	logFn(fmt.Sprintf("=> [WINDOWS ROLLBACK] Target partition %s on disk %s...", p.PartitionNum, p.DiskDevice))

	re := regexp.MustCompile(`\d+`)
	diskDigits := re.FindString(p.DiskDevice)
	if diskDigits == "" {
		diskDigits = "0"
	}

	partNum := strings.TrimSpace(p.PartitionNum)
	hostPart := strings.TrimSpace(p.HostPartNum)

	if partNum != "" {
		// Run safety check before performing destructive disk modifications
		if err := verifyWindowsTargetBeforeDestruction(diskDigits, partNum); err != nil {
			return err
		}

		if keepSlice {
			progressFn("=> [REVERT 50%] Formatting partition slice as clean staging volume...", 0.50)
			logFn(fmt.Sprintf("=> Formatting partition %s on disk %s (slice preserved)...", partNum, diskDigits))
			psFormat := fmt.Sprintf(`
$p = Get-Partition -DiskNumber %s -PartitionNumber %s -ErrorAction SilentlyContinue
if ($p) {
    Format-Volume -Partition $p -FileSystem NTFS -NewFileSystemLabel "STAGING_SLICE" -Confirm:$false
}
`, diskDigits, partNum)
			_ = exec.Command("powershell.exe", "-NoProfile", "-Command", psFormat).Run()

		} else {
			progressFn("=> [REVERT 40%] Removing secondary partition from GPT table...", 0.40)
			logFn(fmt.Sprintf("=> Eradicating partition %s from disk %s...", partNum, diskDigits))
			psDel := fmt.Sprintf(`Get-Partition -DiskNumber %s -PartitionNumber %s -ErrorAction SilentlyContinue | Remove-Partition -Confirm:$false`, diskDigits, partNum)
			if out, err := exec.Command("powershell.exe", "-NoProfile", "-Command", psDel).CombinedOutput(); err != nil {
				return fmt.Errorf("failed to remove partition %s on disk %s: %s: %w", partNum, diskDigits, strings.TrimSpace(string(out)), err)
			}

			// Reclaim 100% capacity back into host partition
			if hostPart != "" {
				progressFn("=> [REVERT 70%] Expanding host partition boundary to 100%...", 0.70)
				logFn(fmt.Sprintf("=> Expanding host partition %s on disk %s to maximum capacity...", hostPart, diskDigits))
				psExpand := fmt.Sprintf(`
$max = (Get-PartitionSupportedSize -DiskNumber %s -PartitionNumber %s -ErrorAction SilentlyContinue).SizeMax
if ($max) {
    Resize-Partition -DiskNumber %s -PartitionNumber %s -Size $max -ErrorAction SilentlyContinue
}
`, diskDigits, hostPart, diskDigits, hostPart)
				_ = exec.Command("powershell.exe", "-NoProfile", "-Command", psExpand).Run()
			}
		}
	}

	// Purge EFI entry from Windows BCD store
	if p.EFIBootEntryNum != "" {
		progressFn("=> [REVERT 85%] Purging registered BCD boot target...", 0.85)
		logFn(fmt.Sprintf("=> Purging BCD boot target %s...", p.EFIBootEntryNum))
		_ = exec.Command("bcdedit.exe", "/delete", p.EFIBootEntryNum, "/cleanup").Run()
	}

	// Purge EFI directory using a collision-free temporary drive letter
	if p.EFIDirNameToPurge != "" {
		progressFn(fmt.Sprintf("=> [REVERT 90%%] Purging /EFI/%s from ESP...", p.EFIDirNameToPurge), 0.90)
		mountESP := fmt.Sprintf(`
$esp = (Get-Partition | Where-Object { $_.GptType -eq "{c12a7328-f81f-11d2-ba4b-00a0c93ec93b}" } | Select-Object -First 1)
if ($esp) {
    $letter = (67..90 | ForEach-Object { [char]$_ + ":" } | Where-Object { -not (Get-PSDrive -Name $_[0] -ErrorAction SilentlyContinue) } | Select-Object -First 1)
    if (-not $letter) { $letter = "S:" }
    Add-PartitionAccessPath -InputObject $esp -AccessPath $letter
    $targetPath = Join-Path $letter "EFI" "%s"
    if (Test-Path $targetPath) {
        Remove-Item -Recurse -Force $targetPath -ErrorAction SilentlyContinue
    }
    Remove-PartitionAccessPath -InputObject $esp -AccessPath $letter
}
`, p.EFIDirNameToPurge)
		_ = exec.Command("powershell.exe", "-NoProfile", "-Command", mountESP).Run()
	}

	// Purge journal files so state resets completely — both the current
	// default location (follow-up F12) and the legacy repo-relative paths.
	_ = os.Remove("orchestrator_journal.json")
	_ = os.Remove("orchestrator_installed_os.json")
	_ = os.Remove(filepath.Join(os.TempDir(), "orchestrator_journal.json"))
	_ = os.Remove(safety.DefaultJournalPath())
	_ = os.Remove(filepath.Join(filepath.Dir(safety.DefaultJournalPath()), "orchestrator_installed_os.json"))

	progressFn("=> [REVERT 100%] Windows host revert completed.", 1.0)
	logFn("=> [WINDOWS ROLLBACK] Completed cleanly.")
	return nil
}

func RollbackRevertHandler(rollbackData json.RawMessage) error {
	var params RealRevertParams
	if err := json.Unmarshal(rollbackData, &params); err != nil {
		return fmt.Errorf("unmarshaling windows revert rollback params: %w", err)
	}
	return ExecuteRealReversion(params, nil)
}
