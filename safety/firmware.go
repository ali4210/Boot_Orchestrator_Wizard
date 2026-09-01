// Package safety performs pre-flight checks (firmware mode, disk headroom,
// power status) that must pass before any destructive operation is allowed.
package safety

// FirmwareMode describes how the machine boots.
type FirmwareMode int

const (
	FirmwareUnknown FirmwareMode = iota
	FirmwareUEFI                 // pure UEFI, no CSM
	FirmwareUEFICSM              // UEFI with Compatibility Support Module active
	FirmwareLegacyBIOS
)

func (f FirmwareMode) String() string {
	switch f {
	case FirmwareUEFI:
		return "UEFI (native)"
	case FirmwareUEFICSM:
		return "UEFI with CSM (Compatibility Support Module)"
	case FirmwareLegacyBIOS:
		return "Legacy BIOS/MBR"
	default:
		return "Unknown"
	}
}

// FirmwareInfo is the result of a firmware mode check.
type FirmwareInfo struct {
	Mode     FirmwareMode
	Evidence []string
}

// DetectFirmware inspects the current machine's boot firmware mode.
// Platform-specific implementation lives in firmware_linux.go,
// firmware_windows.go, firmware_darwin.go.
func DetectFirmware() (FirmwareInfo, error) {
	return detectFirmwarePlatform()
}
