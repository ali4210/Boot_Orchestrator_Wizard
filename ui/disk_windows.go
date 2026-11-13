//go:build windows

package ui

import (
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

func getFreeDiskSpace(path string) (uint64, error) {
	root := os.Getenv("SystemDrive")
	if root == "" {
		root = "C:"
	}
	root += `\`

	pUTF16, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return 0, err
	}

	var freeBytesAvailable uint64
	var totalBytes uint64
	var totalFreeBytes uint64

	kernel32 := windows.NewLazySystemDLL("kernel32.dll")
	procGetDiskFreeSpace := kernel32.NewProc("GetDiskFreeSpaceExW")

	r1, _, errCall := procGetDiskFreeSpace.Call(
		uintptr(unsafe.Pointer(pUTF16)),
		uintptr(unsafe.Pointer(&freeBytesAvailable)),
		uintptr(unsafe.Pointer(&totalBytes)),
		uintptr(unsafe.Pointer(&totalFreeBytes)),
	)
	if r1 == 0 {
		return 0, errCall
	}
	return freeBytesAvailable, nil
}
