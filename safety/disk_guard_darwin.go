//go:build darwin

package safety

import (
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"syscall"
)

// NOTE: cross-compiled from Linux, not executed on real macOS.

func availableDiskBytes(targetPath string) (uint64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(targetPath, &stat); err != nil {
		return 0, fmt.Errorf("statfs %q: %w", targetPath, err)
	}
	return uint64(stat.Bavail) * uint64(stat.Bsize), nil
}

var pmsetPercentRe = regexp.MustCompile(`(\d+)%`)

func powerStatus() (onAC bool, batteryPercent int, err error) {
	out, cmdErr := exec.Command("pmset", "-g", "batt").Output()
	if cmdErr != nil {
		return false, -1, fmt.Errorf("pmset: %w", cmdErr)
	}
	text := string(out)

	onAC = strings.Contains(text, "AC Power")

	batteryPercent = -1
	if m := pmsetPercentRe.FindStringSubmatch(text); len(m) == 2 {
		if pct, perr := strconv.Atoi(m[1]); perr == nil {
			batteryPercent = pct
		}
	}
	if batteryPercent == -1 && !strings.Contains(text, "InternalBattery") {
		// Desktop Mac (Mac mini/Studio/Pro) with no battery at all.
		return true, -1, nil
	}

	return onAC, batteryPercent, nil
}
