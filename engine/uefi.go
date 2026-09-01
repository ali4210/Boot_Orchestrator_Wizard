// Package engine — uefi.go implements the "UEFI NVRAM / BCD / GRUB
// Bootloader Orchestrator" from the blueprint.
//
// THIS IS THE HIGHEST-RISK FILE IN THE PROJECT. A bad NVRAM write can leave
// a machine that only boots to a firmware setup screen; a bad BCD edit can
// leave Windows unbootable. Every exported mutating function here:
//   1. reads and returns the CURRENT state first, as rollback data, before
//      changing anything (mirrors safety.Journal's Begin/rollbackData model)
//   2. never deletes a boot entry — only reorders BootOrder / sets BootNext,
//      which are trivially reversible, or backs up bcdedit state via
//      `bcdedit /export` (a full binary snapshot) before any bcdedit /set
//   3. treats efibootmgr/bcdedit's own exit code as authoritative — never
//      assumes success from lack of a Go-level error alone where the tool
//      itself can silently no-op
//
// This has been reasoned through against efibootmgr/bcdedit documented
// behavior but not executed against real firmware. Test on a spare/VM
// machine with physical access for firmware recovery before relying on it
// on a machine you can't afford to lose boot access to.
package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

// BootEntry is one UEFI NVRAM boot entry as reported by efibootmgr.
type BootEntry struct {
	ID     string // 4 hex digits, e.g. "0001"
	Label  string
	Active bool
}

// NVRAMState is the full current NVRAM boot configuration, captured before
// any mutation so it can be restored exactly.
type NVRAMState struct {
	BootOrder []string    `json:"boot_order"` // ordered list of entry IDs
	BootNext  string      `json:"boot_next"`  // empty if unset
	Entries   []BootEntry `json:"entries"`
}

var efibootmgrEntryRe = regexp.MustCompile(`^Boot([0-9A-Fa-f]{4})(\*?)\s+(.+)$`)
var efibootmgrOrderRe = regexp.MustCompile(`^BootOrder:\s*(.+)$`)
var efibootmgrNextRe = regexp.MustCompile(`^BootNext:\s*([0-9A-Fa-f]{4})`)

// ReadNVRAMState runs `efibootmgr -v` and parses the current state.
// Read-only — safe to call at any time.
func ReadNVRAMState() (*NVRAMState, error) {
	out, err := exec.Command("efibootmgr", "-v").CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("efibootmgr -v failed (are you booted in UEFI mode, not legacy BIOS/CSM?): %w\n%s", err, out)
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
		return nil, fmt.Errorf("no boot entries parsed from efibootmgr output — output format may differ on this system, refusing to proceed blind")
	}
	return state, nil
}

// SetBootNextRollbackData is stored via safety.Journal.Begin before
// SetBootNext runs.
type SetBootNextRollbackData struct {
	PreviousBootNext string `json:"previous_boot_next"` // empty string means "unset"
}

// SetBootNext sets the one-time next-boot override to targetEntryID.
// This is the least risky NVRAM mutation available — it does not touch
// BootOrder at all and a single boot (successful or not) naturally clears
// it, so a bug here has a self-limiting blast radius.
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
		return rollbackData, fmt.Errorf("boot entry %s not found in current NVRAM — refusing to set BootNext to a nonexistent entry", targetEntryID)
	}

	out, err := exec.Command("efibootmgr", "-n", targetEntryID).CombinedOutput()
	if err != nil {
		return rollbackData, fmt.Errorf("efibootmgr -n %s failed: %w\n%s", targetEntryID, err, out)
	}

	after, err := ReadNVRAMState()
	if err != nil || after.BootNext != targetEntryID {
		return rollbackData, fmt.Errorf("efibootmgr reported success but BootNext does not read back as %s (got %q) — do not trust this boot override, verify manually", targetEntryID, after.BootNext)
	}

	return rollbackData, nil
}

// RollbackSetBootNext reverses SetBootNext. Registered under step name
// "uefi-set-boot-next".
func RollbackSetBootNext(rollbackData json.RawMessage) error {
	var rb SetBootNextRollbackData
	if err := json.Unmarshal(rollbackData, &rb); err != nil {
		return fmt.Errorf("corrupt rollback data: %w", err)
	}
	if rb.PreviousBootNext == "" {
		out, err := exec.Command("efibootmgr", "-N").CombinedOutput()
		if err != nil {
			return fmt.Errorf("efibootmgr -N (clear BootNext) failed: %w\n%s", err, out)
		}
		return nil
	}
	out, err := exec.Command("efibootmgr", "-n", rb.PreviousBootNext).CombinedOutput()
	if err != nil {
		return fmt.Errorf("restoring BootNext to %s failed: %w\n%s", rb.PreviousBootNext, err, out)
	}
	return nil
}

// SetBootOrderRollbackData is stored before SetBootOrder runs.
type SetBootOrderRollbackData struct {
	PreviousBootOrder []string `json:"previous_boot_order"`
}

// SetBootOrder permanently reorders BootOrder (unlike SetBootNext, this
// persists across every future boot until changed again — use SetBootNext
// instead unless a permanent change is actually what's wanted).
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
			return rollbackData, fmt.Errorf("boot entry %s in requested order does not exist — refusing partial/invalid BootOrder write", id)
		}
	}

	out, err := exec.Command("efibootmgr", "-o", strings.Join(newOrder, ",")).CombinedOutput()
	if err != nil {
		return rollbackData, fmt.Errorf("efibootmgr -o %s failed: %w\n%s", strings.Join(newOrder, ","), err, out)
	}

	after, err := ReadNVRAMState()
	if err != nil || strings.Join(after.BootOrder, ",") != strings.Join(newOrder, ",") {
		return rollbackData, fmt.Errorf("BootOrder did not read back as requested after write — verify manually before rebooting")
	}
	return rollbackData, nil
}

// RollbackSetBootOrder reverses SetBootOrder. Registered under step name
// "uefi-set-boot-order".
func RollbackSetBootOrder(rollbackData json.RawMessage) error {
	var rb SetBootOrderRollbackData
	if err := json.Unmarshal(rollbackData, &rb); err != nil {
		return fmt.Errorf("corrupt rollback data: %w", err)
	}
	out, err := exec.Command("efibootmgr", "-o", strings.Join(rb.PreviousBootOrder, ",")).CombinedOutput()
	if err != nil {
		return fmt.Errorf("restoring BootOrder to %v failed: %w\n%s", rb.PreviousBootOrder, err, out)
	}
	return nil
}

// --- Windows BCD ------------------------------------------------------

// BCDBackupRollbackData stores the path to a full bcdedit /export snapshot
// taken before any /set operation.
type BCDBackupRollbackData struct {
	ExportPath string `json:"export_path"`
}

// BackupBCD takes a full binary export of the current BCD store via
// `bcdedit /export`, the only reliable way to fully restore BCD state —
// individual /set commands are not reliably invertible because a single
// logical change can touch multiple elements.
func BackupBCD(exportPath string) (rollbackData []byte, err error) {
	out, err := exec.Command("bcdedit", "/export", exportPath).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("bcdedit /export %s failed: %w\n%s", exportPath, err, out)
	}
	if _, statErr := os.Stat(exportPath); statErr != nil {
		return nil, fmt.Errorf("bcdedit /export reported success but %s was not created: %w", exportPath, statErr)
	}
	rb := BCDBackupRollbackData{ExportPath: exportPath}
	rollbackData, _ = json.Marshal(rb)
	return rollbackData, nil
}

// RollbackBCD restores a BCD export taken by BackupBCD. Registered under
// step name "bcd-mutation". Requires running from Windows RE or an admin
// context outside the running OS whose BCD is being restored (you cannot
// reliably re-import the BCD store that is actively booted).
func RollbackBCD(rollbackData json.RawMessage) error {
	var rb BCDBackupRollbackData
	if err := json.Unmarshal(rollbackData, &rb); err != nil {
		return fmt.Errorf("corrupt rollback data: %w", err)
	}
	out, err := exec.Command("bcdedit", "/import", rb.ExportPath).CombinedOutput()
	if err != nil {
		return fmt.Errorf("bcdedit /import %s failed: %w\n%s", rb.ExportPath, err, out)
	}
	return nil
}

// AddWindowsBootEntry creates a new BCD entry for a Windows install found
// at targetPartition (e.g. after ApplyWim), copying the current OS loader
// entry as a template via `bcdedit /copy {current} /d <description>`,
// which is the documented-safe way to add an entry without hand-building
// one field by field.
//
// Returns the new entry's GUID and rollback data (the GUID itself — undone
// via `bcdedit /delete`, which is safe because this only ever deletes an
// entry this function itself just created, never a pre-existing one).
func AddWindowsBootEntry(description string) (guid string, rollbackData []byte, err error) {
	out, err := exec.Command("bcdedit", "/copy", "{current}", "/d", description).CombinedOutput()
	if err != nil {
		return "", nil, fmt.Errorf("bcdedit /copy failed: %w\n%s", err, out)
	}
	guid = extractBCDGuid(string(out))
	if guid == "" {
		return "", nil, fmt.Errorf("bcdedit /copy succeeded but no GUID could be parsed from output: %s", out)
	}
	rb := struct {
		CreatedGuid string `json:"created_guid"`
	}{CreatedGuid: guid}
	rollbackData, _ = json.Marshal(rb)
	return guid, rollbackData, nil
}

// RollbackAddWindowsBootEntry deletes the entry AddWindowsBootEntry
// created. Registered under step name "bcd-add-entry". Safe because it
// only ever targets a GUID this same run created, captured at creation
// time — never a GUID supplied fresh at rollback time.
func RollbackAddWindowsBootEntry(rollbackData json.RawMessage) error {
	var rb struct {
		CreatedGuid string `json:"created_guid"`
	}
	if err := json.Unmarshal(rollbackData, &rb); err != nil {
		return fmt.Errorf("corrupt rollback data: %w", err)
	}
	out, err := exec.Command("bcdedit", "/delete", rb.CreatedGuid).CombinedOutput()
	if err != nil {
		return fmt.Errorf("bcdedit /delete %s failed: %w\n%s", rb.CreatedGuid, err, out)
	}
	return nil
}

var bcdGuidRe = regexp.MustCompile(`\{[0-9a-fA-F-]{36}\}`)

func extractBCDGuid(bcdCopyOutput string) string {
	return bcdGuidRe.FindString(bcdCopyOutput)
}

// --- GRUB (Linux side of the "GRUB Bootloader Orchestrator") ----------

// GrubDefaultRollbackData stores the previous GRUB_DEFAULT line from
// /etc/default/grub before SetGrubDefaultEntry edits it.
type GrubDefaultRollbackData struct {
	OriginalLine string `json:"original_line"` // full original line, empty if key was absent
	KeyWasAbsent bool   `json:"key_was_absent"`
}

// SetGrubDefaultEntry edits /etc/default/grub's GRUB_DEFAULT and runs
// update-grub / grub2-mkconfig, so a Linux entry becomes the persistent
// default alongside the NVRAM-level BootOrder/BootNext functions above
// (GRUB's own menu default is a separate layer from UEFI NVRAM on
// GRUB-chainloaded setups).
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

	if err := regenerateGrubConfig(); err != nil {
		return rollbackData, fmt.Errorf("%s updated but regenerating grub.cfg failed — the change will not take effect until this is fixed: %w", grubDefaultPath, err)
	}
	return rollbackData, nil
}

// RollbackSetGrubDefaultEntry reverses SetGrubDefaultEntry. Registered
// under step name "grub-set-default". Needs grubDefaultPath supplied by
// the caller since it isn't stored in rollbackData (kept minimal/stable).
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
				continue // drop the line we added
			}
			out = append(out, rb.OriginalLine)
			continue
		}
		out = append(out, line)
	}
	if err := os.WriteFile(grubDefaultPath, []byte(strings.Join(out, "\n")), 0644); err != nil {
		return fmt.Errorf("writing restored %s: %w", grubDefaultPath, err)
	}
	return regenerateGrubConfig()
}

func regenerateGrubConfig() error {
	for _, tool := range []string{"update-grub", "grub2-mkconfig"} {
		if _, err := exec.LookPath(tool); err == nil {
			args := []string{}
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

// FirmwareModeCheck is a last-line-of-defense guard: several functions in
// this file are no-ops or actively harmful if called on a BIOS/CSM system
// (efibootmgr requires real UEFI variable support). Callers in
// cmd/orchestrator should call safety.CheckFirmwareMode (existing package)
// first, but this local check exists so this package doesn't silently
// assume it's always called correctly.
func FirmwareModeCheck() error {
	if _, err := os.Stat("/sys/firmware/efi"); err != nil {
		return fmt.Errorf("/sys/firmware/efi not present — this system is not booted in UEFI mode, NVRAM functions in this file will not work (legacy BIOS/CSM uses MBR boot code, not efibootmgr)")
	}
	return nil
}
