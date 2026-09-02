// Package engine — uefi.go implements the UEFI NVRAM / BCD / GRUB Bootloader Orchestrator.
package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

type BootEntry struct {
	ID     string
	Label  string
	Active bool
}

type NVRAMState struct {
	BootOrder []string    `json:"boot_order"`
	BootNext  string      `json:"boot_next"`
	Entries   []BootEntry `json:"entries"`
}

var efibootmgrEntryRe = regexp.MustCompile(`^Boot([0-9A-Fa-f]{4})(\*?)\s+(.+)$`)
var efibootmgrOrderRe = regexp.MustCompile(`^BootOrder:\s*(.+)$`)
var efibootmgrNextRe = regexp.MustCompile(`^BootNext:\s*([0-9A-Fa-f]{4})`)

func ReadNVRAMState() (*NVRAMState, error) {
	out, err := exec.Command("efibootmgr", "-v").CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("efibootmgr -v failed: %w\n%s", err, out)
	}

	state := &NVRAMState{}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimRight(line, "\r")
		if m := efibootmgrEntryRe.FindStringSubmatch(line); m != nil {
			state.Entries = append(state.Entries, BootEntry{
				ID:     m[1],
				Active: m[2] == "*",
				Label:  strings.TrimSpace(m[3]),
			})
			continue
		}
		if m := efibootmgrOrderRe.FindStringSubmatch(line); m != nil {
			state.BootOrder = strings.Split(m[1], ",")
			continue
		}
		if m := efibootmgrNextRe.FindStringSubmatch(line); m != nil {
			state.BootNext = m[1]
		}
	}
	if len(state.Entries) == 0 {
		return nil, fmt.Errorf("no boot entries parsed from efibootmgr output")
	}
	return state, nil
}

type SetBootNextRollbackData struct {
	PreviousBootNext string `json:"previous_boot_next"`
}

func SetBootNext(targetEntryID string) (rollbackData []byte, err error) {
	before, err := ReadNVRAMState()
	if err != nil {
		return nil, fmt.Errorf("reading current NVRAM state before mutating: %w", err)
	}
	rb := SetBootNextRollbackData{PreviousBootNext: before.BootNext}
	rollbackData, _ = json.Marshal(rb)

	found := false
	for _, e := range before.Entries {
		if e.ID == targetEntryID {
			found = true
			break
		}
	}
	if !found {
		return rollbackData, fmt.Errorf("boot entry %s not found in current NVRAM", targetEntryID)
	}

	out, err := exec.Command("efibootmgr", "-n", targetEntryID).CombinedOutput()
	if err != nil {
		return rollbackData, fmt.Errorf("efibootmgr -n %s failed: %w\n%s", targetEntryID, err, out)
	}

	after, err := ReadNVRAMState()
	if err != nil || after.BootNext != targetEntryID {
		return rollbackData, fmt.Errorf("efibootmgr reported success but BootNext verification failed")
	}

	return rollbackData, nil
}

func RollbackSetBootNext(rollbackData json.RawMessage) error {
	var rb SetBootNextRollbackData
	if err := json.Unmarshal(rollbackData, &rb); err != nil {
		return fmt.Errorf("corrupt rollback data: %w", err)
	}
	if rb.PreviousBootNext == "" {
		out, err := exec.Command("efibootmgr", "-N").CombinedOutput()
		if err != nil {
			return fmt.Errorf("efibootmgr -N failed: %w\n%s", err, out)
		}
		return nil
	}
	out, err := exec.Command("efibootmgr", "-n", rb.PreviousBootNext).CombinedOutput()
	if err != nil {
		return fmt.Errorf("restoring BootNext failed: %w\n%s", err, out)
	}
	return nil
}

type SetBootOrderRollbackData struct {
	PreviousBootOrder []string `json:"previous_boot_order"`
}

func SetBootOrder(newOrder []string) (rollbackData []byte, err error) {
	before, err := ReadNVRAMState()
	if err != nil {
		return nil, fmt.Errorf("reading current NVRAM state before mutating: %w", err)
	}
	rb := SetBootOrderRollbackData{PreviousBootOrder: before.BootOrder}
	rollbackData, _ = json.Marshal(rb)

	known := map[string]bool{}
	for _, e := range before.Entries {
		known[e.ID] = true
	}
	for _, id := range newOrder {
		if !known[id] {
			return rollbackData, fmt.Errorf("boot entry %s in requested order does not exist", id)
		}
	}

	out, err := exec.Command("efibootmgr", "-o", strings.Join(newOrder, ",")).CombinedOutput()
	if err != nil {
		return rollbackData, fmt.Errorf("efibootmgr -o failed: %w\n%s", err, out)
	}

	return rollbackData, nil
}

func RollbackSetBootOrder(rollbackData json.RawMessage) error {
	var rb SetBootOrderRollbackData
	if err := json.Unmarshal(rollbackData, &rb); err != nil {
		return fmt.Errorf("corrupt rollback data: %w", err)
	}
	out, err := exec.Command("efibootmgr", "-o", strings.Join(rb.PreviousBootOrder, ",")).CombinedOutput()
	if err != nil {
		return fmt.Errorf("restoring BootOrder failed: %w\n%s", err, out)
	}
	return nil
}

type BCDBackupRollbackData struct {
	ExportPath string `json:"export_path"`
}

func BackupBCD(exportPath string) (rollbackData []byte, err error) {
	out, err := exec.Command("bcdedit", "/export", exportPath).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("bcdedit /export failed: %w\n%s", err, out)
	}
	rb := BCDBackupRollbackData{ExportPath: exportPath}
	rollbackData, _ = json.Marshal(rb)
	return rollbackData, nil
}

func RollbackBCD(rollbackData json.RawMessage) error {
	var rb BCDBackupRollbackData
	if err := json.Unmarshal(rollbackData, &rb); err != nil {
		return fmt.Errorf("corrupt rollback data: %w", err)
	}
	out, err := exec.Command("bcdedit", "/import", rb.ExportPath).CombinedOutput()
	if err != nil {
		return fmt.Errorf("bcdedit /import failed: %w\n%s", err, out)
	}
	return nil
}

func AddWindowsBootEntry(description string) (guid string, rollbackData []byte, err error) {
	out, err := exec.Command("bcdedit", "/copy", "{current}", "/d", description).CombinedOutput()
	if err != nil {
		return "", nil, fmt.Errorf("bcdedit /copy failed: %w\n%s", err, out)
	}
	guid = extractBCDGuid(string(out))
	if guid == "" {
		return "", nil, fmt.Errorf("no GUID parsed from BCD output")
	}
	rb := struct {
		CreatedGuid string `json:"created_guid"`
	}{CreatedGuid: guid}
	rollbackData, _ = json.Marshal(rb)
	return guid, rollbackData, nil
}

func RollbackAddWindowsBootEntry(rollbackData json.RawMessage) error {
	var rb struct {
		CreatedGuid string `json:"created_guid"`
	}
	if err := json.Unmarshal(rollbackData, &rb); err != nil {
		return fmt.Errorf("corrupt rollback data: %w", err)
	}
	out, err := exec.Command("bcdedit", "/delete", rb.CreatedGuid).CombinedOutput()
	if err != nil {
		return fmt.Errorf("bcdedit /delete failed: %w\n%s", err, out)
	}
	return nil
}

var bcdGuidRe = regexp.MustCompile(`\{[0-9a-fA-F-]{36}\}`)

func extractBCDGuid(bcdCopyOutput string) string {
	return bcdGuidRe.FindString(bcdCopyOutput)
}

type GrubDefaultRollbackData struct {
	OriginalLine string `json:"original_line"`
	KeyWasAbsent bool   `json:"key_was_absent"`
}

func SetGrubDefaultEntry(grubDefaultPath, menuEntryTitle string) (rollbackData []byte, err error) {
	raw, err := os.ReadFile(grubDefaultPath)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", grubDefaultPath, err)
	}
	lines := strings.Split(string(raw), "\n")
	rb := GrubDefaultRollbackData{KeyWasAbsent: true}
	newLine := fmt.Sprintf(`GRUB_DEFAULT="%s"`, menuEntryTitle)
	replaced := false
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "GRUB_DEFAULT=") {
			rb.OriginalLine = line
			rb.KeyWasAbsent = false
			lines[i] = newLine
			replaced = true
			break
		}
	}
	if !replaced {
		lines = append(lines, newLine)
	}
	rollbackData, _ = json.Marshal(rb)

	if err := os.WriteFile(grubDefaultPath, []byte(strings.Join(lines, "\n")), 0644); err != nil {
		return rollbackData, fmt.Errorf("writing %s: %w", grubDefaultPath, err)
	}

	if err := RegenerateGrubConfig(); err != nil {
		return rollbackData, fmt.Errorf("regenerating grub config failed: %w", err)
	}
	return rollbackData, nil
}

func RollbackSetGrubDefaultEntry(grubDefaultPath string, rollbackData json.RawMessage) error {
	var rb GrubDefaultRollbackData
	if err := json.Unmarshal(rollbackData, &rb); err != nil {
		return fmt.Errorf("corrupt rollback data: %w", err)
	}
	raw, err := os.ReadFile(grubDefaultPath)
	if err != nil {
		return fmt.Errorf("reading %s: %w", grubDefaultPath, err)
	}
	lines := strings.Split(string(raw), "\n")
	var out []string
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "GRUB_DEFAULT=") {
			if rb.KeyWasAbsent {
				continue
			}
			out = append(out, rb.OriginalLine)
			continue
		}
		out = append(out, line)
	}
	if err := os.WriteFile(grubDefaultPath, []byte(strings.Join(out, "\n")), 0644); err != nil {
		return fmt.Errorf("writing restored %s: %w", grubDefaultPath, err)
	}
	return RegenerateGrubConfig()
}

// RegenerateGrubConfig refreshes GRUB via update-grub or grub2-mkconfig
func RegenerateGrubConfig() error {
	for _, tool := range []string{"update-grub", "grub2-mkconfig"} {
		if _, err := exec.LookPath(tool); err == nil {
			var args []string
			if tool == "grub2-mkconfig" {
				args = []string{"-o", "/boot/grub2/grub.cfg"}
			}
			out, err := exec.Command(tool, args...).CombinedOutput()
			if err != nil {
				return fmt.Errorf("%s failed: %w\n%s", tool, err, out)
			}
			return nil
		}
	}
	return fmt.Errorf("neither update-grub nor grub2-mkconfig found on PATH")
}

func FirmwareModeCheck() error {
	if _, err := os.Stat("/sys/firmware/efi"); err != nil {
		return fmt.Errorf("/sys/firmware/efi not present — system is not in UEFI mode")
	}
	return nil
}
