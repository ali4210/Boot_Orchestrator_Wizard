package engine

import (
	"os/exec"
	"strings"
)

// ResolveBootEntryByEFIDir finds the UEFI NVRAM boot entry number (e.g.
// "0003" for "Boot0003") whose description mentions efiDir, by parsing
// `efibootmgr`'s output. Used to populate RealRevertParams.EFIBootEntryNum
// before calling ExecuteDecommissionWithOptions. Deliberately not under a
// linux-only build tag: `efibootmgr` doesn't exist on macOS/Windows, but
// exec.Command simply fails to start there and this returns "" gracefully,
// the same as "no matching entry found" — so this is safe to call,
// unconditionally, from cross-platform callers like ui/model.go and
// cmd/orchestrator's headless --revert.
func ResolveBootEntryByEFIDir(efiDir string) string {
	if efiDir == "" {
		return ""
	}
	out, err := exec.Command("efibootmgr").Output()
	if err != nil {
		return ""
	}
	lines := strings.Split(string(out), "\n")
	for _, line := range lines {
		if strings.Contains(strings.ToLower(line), strings.ToLower(efiDir)) && strings.HasPrefix(line, "Boot") {
			parts := strings.Fields(line)
			if len(parts) > 0 {
				raw := strings.TrimPrefix(parts[0], "Boot")
				return strings.TrimSuffix(raw, "*")
			}
		}
	}
	return ""
}
