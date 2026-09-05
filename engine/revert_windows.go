//go:build windows

package engine

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

func ExecuteRealReversion(p RealRevertParams, logFn func(string)) error {
	if logFn == nil {
		logFn = func(string) {}
	}

	logFn(fmt.Sprintf("=> [WINDOWS ROLLBACK] Removing staging partition %s on %s...", p.PartitionNum, p.DiskDevice))

	// If a partition number was created, delete via PowerShell
	if p.PartitionNum != "" {
		psDel := fmt.Sprintf(`
$part = Get-Partition -DriveLetter "%s" -ErrorAction SilentlyContinue
if ($part) {
    Remove-Partition -DriveLetter "%s" -Confirm:$false
}
`, strings.TrimSuffix(p.PartitionNum, ":"), strings.TrimSuffix(p.PartitionNum, ":"))
		_, _ = exec.Command("powershell.exe", "-NoProfile", "-Command", psDel).CombinedOutput()
	}

	// Purge BCD entry if registered
	if p.EFIBootEntryNum != "" {
		logFn(fmt.Sprintf("=> [WINDOWS ROLLBACK] Purging BCD boot target %s...", p.EFIBootEntryNum))
		_ = exec.Command("bcdedit.exe", "/delete", p.EFIBootEntryNum, "/cleanup").Run()
	}

	return nil
}

func RollbackRevertHandler(rollbackData json.RawMessage) error {
	var params RealRevertParams
	if err := json.Unmarshal(rollbackData, &params); err != nil {
		return fmt.Errorf("unmarshaling windows revert rollback params: %w", err)
	}
	return ExecuteRealReversion(params, nil)
}
