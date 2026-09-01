//go:build linux

package safety

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

func availableDiskBytes(targetPath string) (uint64, error) {
	// statfs requires the path to exist; walk up to the nearest existing
	// ancestor (e.g. the target dir doesn't exist yet but its parent does).
	path := targetPath
	for {
		if _, err := os.Stat(path); err == nil {
			break
		}
		parent := filepath.Dir(path)
		if parent == path {
			return 0, fmt.Errorf("no existing ancestor directory found for %q", targetPath)
		}
		path = parent
	}

	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, fmt.Errorf("statfs %q: %w", path, err)
	}
	return stat.Bavail * uint64(stat.Bsize), nil
}

func powerStatus() (onAC bool, batteryPercent int, err error) {
	const base = "/sys/class/power_supply"
	entries, err := os.ReadDir(base)
	if err != nil {
		return false, -1, fmt.Errorf("reading %s: %w", base, err)
	}

	batteryPercent = -1
	foundBattery := false
	foundAC := false

	for _, e := range entries {
		name := e.Name()
		typePath := filepath.Join(base, name, "type")
		t, terr := readFileTrim(typePath)
		if terr != nil {
			continue
		}
		switch t {
		case "Mains":
			online, oerr := readFileTrim(filepath.Join(base, name, "online"))
			if oerr == nil && strings.TrimSpace(online) == "1" {
				onAC = true
			}
			foundAC = true
		case "Battery":
			foundBattery = true
			if cap, cerr := readFileTrim(filepath.Join(base, name, "capacity")); cerr == nil {
				if pct, perr := strconv.Atoi(cap); perr == nil {
					batteryPercent = pct
				}
			}
		}
	}

	// Desktop with no battery and no explicit AC node visible: treat as
	// permanently-powered (there's nothing to unplug).
	if !foundBattery && !foundAC {
		return true, -1, nil
	}
	if !foundBattery {
		// AC-only system (desktop with a UPS reporting as Mains, etc.)
		return onAC, -1, nil
	}
	return onAC, batteryPercent, nil
}
