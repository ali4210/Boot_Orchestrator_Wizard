//go:build windows

package main

import (
	"path/filepath"
	"syscall"
	"unsafe"
)

// diskFreeBytes reports free space available to the current user on the
// volume containing path.
func diskFreeBytes(path string) (uint64, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return 0, err
	}
	ptr, err := syscall.UTF16PtrFromString(abs)
	if err != nil {
		return 0, err
	}

	var freeBytesAvailable, totalBytes, totalFreeBytes uint64
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	proc := kernel32.NewProc("GetDiskFreeSpaceExW")
	ret, _, callErr := proc.Call(
		uintptr(unsafe.Pointer(ptr)),
		uintptr(unsafe.Pointer(&freeBytesAvailable)),
		uintptr(unsafe.Pointer(&totalBytes)),
		uintptr(unsafe.Pointer(&totalFreeBytes)),
	)
	if ret == 0 {
		return 0, callErr
	}
	return freeBytesAvailable, nil
}
