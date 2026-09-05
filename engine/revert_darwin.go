//go:build darwin

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

	logFn(fmt.Sprintf("=> [MACOS ROLLBACK] Erasing and restoring partition %s...", p.PartitionNum))

	if p.PartitionNum != "" {
		// Unmount first
		_ = exec.Command("diskutil", "unmountDisk", "force", p.PartitionNum).Run()

		// Erase or delete partition via diskutil
		cmdErase := exec.Command("diskutil", "eraseVolume", "Free Space", "%noformat%", p.PartitionNum)
		if out, err := cmdErase.CombinedOutput(); err != nil {
			logFn(fmt.Sprintf("=> Notice: eraseVolume output: %s", strings.TrimSpace(string(out))))
		}
	}

	// Purge EFI blessing if needed
	if p.EFIDirNameToPurge != "" {
		logFn(fmt.Sprintf("=> [MACOS ROLLBACK] Purging EFI directory %s...", p.EFIDirNameToPurge))
		// Mount EFI
		efiPart := p.DiskDevice + "s1"
		_ = exec.Command("diskutil", "mount", efiPart).Run()
		_ = exec.Command("rm", "-rf", "/Volumes/EFI/EFI/"+p.EFIDirNameToPurge).Run()
		_ = exec.Command("diskutil", "unmount", efiPart).Run()
	}

	return nil
}

func RollbackRevertHandler(rollbackData json.RawMessage) error {
	var params RealRevertParams
	if err := json.Unmarshal(rollbackData, &params); err != nil {
		return fmt.Errorf("unmarshaling darwin revert rollback params: %w", err)
	}
	return ExecuteRealReversion(params, nil)
}
