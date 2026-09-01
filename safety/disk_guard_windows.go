//go:build windows

package safety

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"unsafe"
)

// NOTE: cross-compiled from Linux, not executed on real Windows. Verify on a
// disposable Windows VM before use.

func availableDiskBytes(targetPath string) (uint64, error) {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	proc := kernel32.NewProc("GetDiskFreeSpaceExW")

	pathPtr, err := syscall.UTF16PtrFromString(targetPath)
	if err != nil {
		return 0, fmt.Errorf("invalid path %q: %w", targetPath, err)
	}

	var freeBytesAvailable uint64
	ret, _, callErr := proc.Call(
		uintptr(unsafe.Pointer(pathPtr)),
		uintptr(unsafe.Pointer(&freeBytesAvailable)),
		0,
		0,
	)
	if ret == 0 {
		return 0, fmt.Errorf("GetDiskFreeSpaceExW failed: %w", callErr)
	}
	return freeBytesAvailable, nil
}

func powerStatus() (onAC bool, batteryPercent int, err error) {
	// Win32_Battery: BatteryStatus 2 = AC/charging (roughly), EstimatedChargeRemaining = %.
	out, cmdErr := exec.Command("powershell", "-NoProfile", "-Command",
		`Get-CimInstance -ClassName Win32_Battery | Select-Object -ExpandProperty EstimatedChargeRemaining`,
	).Output()
	if cmdErr != nil || strings.TrimSpace(string(out)) == "" {
		// No battery object at all == desktop, always "AC".
		return true, -1, nil
	}

	pct, perr := strconv.Atoi(strings.TrimSpace(string(out)))
	if perr != nil {
		return false, -1, fmt.Errorf("parsing battery percent: %w", perr)
	}

	acOut, acErr := exec.Command("powershell", "-NoProfile", "-Command",
		`(Get-CimInstance -ClassName Win32_Battery).BatteryStatus`,
	).Output()
	onAC = acErr == nil && strings.TrimSpace(string(acOut)) == "2"

	return onAC, pct, nil
}
