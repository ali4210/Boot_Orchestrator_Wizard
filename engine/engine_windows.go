//go:build windows

package engine

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

type WindowsStorageEngine struct{}

func NewStorageEngine() StorageEngine {
	return &WindowsStorageEngine{}
}

func runPowerShell(script string) ([]byte, error) {
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script)
	return cmd.CombinedOutput()
}

func (e *WindowsStorageEngine) EnumerateStorage() ([]UnifiedPartition, error) {
	psScript := `
Get-Partition | ForEach-Object {
    $part = $_
    $vol = Get-Volume -Partition $part -ErrorAction SilentlyContinue
    $disk = Get-Disk -Number $part.DiskNumber -ErrorAction SilentlyContinue
    [PSCustomObject]@{
        DiskID       = "Disk " + $part.DiskNumber
        PartitionID  = if ($part.DriveLetter) { $part.DriveLetter + ":" } else { "Part" + $part.PartitionNumber }
        Label        = if ($vol) { $vol.FileSystemLabel } else { "" }
        FileSystem   = if ($vol) { $vol.FileSystem } else { "" }
        SizeBytes    = $part.Size
        FreeBytes    = if ($vol) { $vol.SizeRemaining } else { 0 }
        MountPoint   = if ($part.DriveLetter) { $part.DriveLetter + ":\" } else { "" }
        IsSystemRoot = ($part.DriveLetter -eq "C")
        IsRemovable  = if ($disk) { ($disk.BusType -eq "USB") } else { $false }
    }
} | ConvertTo-Json -Depth 3
`
	out, err := runPowerShell(psScript)
	if err != nil {
		return nil, fmt.Errorf("powershell partition discovery failed: %w (%s)", err, strings.TrimSpace(string(out)))
	}

	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return nil, fmt.Errorf("no partitions returned by Windows PowerShell query")
	}

	var partitions []UnifiedPartition
	if strings.HasPrefix(trimmed, "[") {
		if err := json.Unmarshal([]byte(trimmed), &partitions); err != nil {
			return nil, fmt.Errorf("parsing windows storage array: %w", err)
		}
	} else {
		var single UnifiedPartition
		if err := json.Unmarshal([]byte(trimmed), &single); err != nil {
			return nil, fmt.Errorf("parsing single windows partition: %w", err)
		}
		partitions = append(partitions, single)
	}

	return partitions, nil
}

func (e *WindowsStorageEngine) DetectActiveRoot() (UnifiedPartition, error) {
	partitions, err := e.EnumerateStorage()
	if err != nil {
		return UnifiedPartition{}, err
	}
	for _, p := range partitions {
		if p.IsSystemRoot {
			return p, nil
		}
	}
	return UnifiedPartition{}, fmt.Errorf("windows C: system drive not found")
}

func (e *WindowsStorageEngine) ShrinkVolume(partitionID string, shrinkSizeBytes uint64, logFn func(string)) error {
	driveLetter := strings.TrimSuffix(strings.TrimSuffix(partitionID, ":\\"), ":")
	logFn(fmt.Sprintf("=> [WINDOWS ENGINE] Shrinking drive %s: by %d bytes...", driveLetter, shrinkSizeBytes))

	psScript := fmt.Sprintf(`
$drive = "%s"
$shrink = %d
$curr = (Get-Partition -DriveLetter $drive).Size
$target = $curr - $shrink
Resize-Partition -DriveLetter $drive -Size $target
`, driveLetter, shrinkSizeBytes)

	out, err := runPowerShell(psScript)
	if err != nil {
		return fmt.Errorf("failed to resize partition %s: %w (%s)", partitionID, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (e *WindowsStorageEngine) CreatePartition(diskID string, sizeBytes uint64, fsType string, label string, logFn func(string)) (string, error) {
	diskNumStr := strings.TrimPrefix(diskID, "Disk ")
	diskNum, err := strconv.Atoi(strings.TrimSpace(diskNumStr))
	if err != nil {
		diskNum = 0
	}

	logFn(fmt.Sprintf("=> [WINDOWS ENGINE] Carving staging partition on Disk %d (%s)...", diskNum, fsType))

	targetFS := "FAT32"
	if strings.EqualFold(fsType, "ntfs") {
		targetFS = "NTFS"
	}

	psScript := fmt.Sprintf(`
$part = New-Partition -DiskNumber %d -Size %d -AssignDriveLetter
$vol = Format-Volume -Partition $part -FileSystem %s -NewFileSystemLabel "%s" -Confirm:$false
$part.DriveLetter + ":"
`, diskNum, sizeBytes, targetFS, label)

	out, err := runPowerShell(psScript)
	if err != nil {
		return "", fmt.Errorf("windows partition creation failed: %w (%s)", err, strings.TrimSpace(string(out)))
	}

	newDrive := strings.TrimSpace(string(out))
	lines := strings.Split(newDrive, "\n")
	assignedDrive := strings.TrimSpace(lines[len(lines)-1])

	return assignedDrive, nil
}

func (e *WindowsStorageEngine) MountVolume(devPath string, logFn func(string)) (string, func(), error) {
	if strings.HasSuffix(devPath, ":") || strings.HasSuffix(devPath, ":\\") {
		return filepath.Clean(devPath) + "\\", func() {}, nil
	}
	return devPath, func() {}, nil
}

func (e *WindowsStorageEngine) RegisterBootEntry(title string, efiRelativePath string, diskID string, partNum uint, logFn func(string)) error {
	logFn(fmt.Sprintf("=> [WINDOWS BCD] Registering UEFI boot choice: %s...", title))

	cmdCreate := exec.Command("bcdedit.exe", "/create", "/d", title, "/application", "BOOTAPP")
	out, err := cmdCreate.CombinedOutput()
	if err != nil {
		return fmt.Errorf("bcdedit create failed: %w (%s)", err, strings.TrimSpace(string(out)))
	}

	outStr := string(out)
	startIdx := strings.Index(outStr, "{")
	endIdx := strings.Index(outStr, "}")
	if startIdx == -1 || endIdx == -1 || endIdx <= startIdx {
		return fmt.Errorf("unable to parse GUID from bcdedit output: %s", outStr)
	}
	guid := outStr[startIdx : endIdx+1]

	_ = exec.Command("bcdedit.exe", "/set", guid, "device", fmt.Sprintf("partition=\\Device\\HarddiskVolume%d", partNum)).Run()
	_ = exec.Command("bcdedit.exe", "/set", guid, "path", efiRelativePath).Run()
	_ = exec.Command("bcdedit.exe", "/displayorder", guid, "/addlast").Run()

	logFn(fmt.Sprintf("=> [WINDOWS BCD] Boot target %s registered successfully.", guid))
	return nil
}
