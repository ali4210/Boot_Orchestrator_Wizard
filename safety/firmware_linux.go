//go:build linux

package safety

import (
	"os"
	"os/exec"
	"strings"
)

func detectFirmwarePlatform() (FirmwareInfo, error) {
	info := FirmwareInfo{}

	// If /sys/firmware/efi exists, the currently running OS was booted via
	// native UEFI (CSM was NOT used for this boot, since CSM boots produce a
	// legacy BIOS boot environment with no EFI runtime services exposed).
	if _, err := os.Stat("/sys/firmware/efi"); err == nil {
		info.Mode = FirmwareUEFI
		info.Evidence = append(info.Evidence, "/sys/firmware/efi exists: booted via native UEFI")

		if size, err := readFileTrim("/sys/firmware/efi/fw_platform_size"); err == nil {
			info.Evidence = append(info.Evidence, "EFI platform size: "+size+"-bit")
		}
		return info, nil
	}

	info.Evidence = append(info.Evidence, "/sys/firmware/efi absent: booted in legacy/BIOS-compatibility mode")

	// We can't tell from a legacy-mode boot alone whether the firmware is
	// capable of UEFI but is running with CSM enabled, or is truly legacy
	// BIOS-only hardware. dmidecode (if available and run as root) can
	// reveal a "UEFI is supported" BIOS characteristic, which is the
	// strongest available signal of CSM-mode operation.
	if out, err := exec.Command("dmidecode", "-t", "bios").Output(); err == nil {
		if strings.Contains(strings.ToLower(string(out)), "uefi is supported") {
			info.Mode = FirmwareUEFICSM
			info.Evidence = append(info.Evidence, "dmidecode reports firmware supports UEFI: booted in CSM/compatibility mode")
			return info, nil
		}
		info.Evidence = append(info.Evidence, "dmidecode ran but did not report UEFI capability")
	} else {
		info.Evidence = append(info.Evidence, "dmidecode unavailable or requires root; cannot distinguish CSM from true legacy BIOS")
	}

	info.Mode = FirmwareLegacyBIOS
	return info, nil
}

func readFileTrim(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}
