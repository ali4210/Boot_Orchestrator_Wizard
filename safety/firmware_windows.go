//go:build windows

package safety

import (
	"os/exec"
	"strings"
)

// NOTE: cross-compiled from Linux, not executed on real Windows. Verify on a
// disposable Windows VM before use. This relies on the well-documented
// registry value HKLM\SYSTEM\CurrentControlSet\Control\SecureBoot\State,
// via the msinfo32-equivalent PowerShell cmdlet, which is the standard way
// Windows itself reports firmware type (avoids parsing bcdedit output).

func detectFirmwarePlatform() (FirmwareInfo, error) {
	info := FirmwareInfo{}

	psCmd := `(Get-CimInstance -ClassName Win32_ComputerSystem).PCSystemType; ` +
		`(Confirm-SecureBootUEFI) 2>$null`

	out, err := exec.Command("powershell", "-NoProfile", "-Command",
		`$fw = (Get-ItemProperty -Path 'HKLM:\SYSTEM\CurrentControlSet\Control' -Name PEFirmwareType -ErrorAction SilentlyContinue).PEFirmwareType; Write-Output $fw`,
	).Output()

	_ = psCmd // reserved for future secure-boot cross-check

	if err == nil {
		result := strings.TrimSpace(string(out))
		switch result {
		case "1": // PEFirmwareType 1 = BIOS/Legacy
			info.Mode = FirmwareLegacyBIOS
			info.Evidence = append(info.Evidence, "registry PEFirmwareType=1 (Legacy BIOS)")
			return info, nil
		case "2": // PEFirmwareType 2 = UEFI
			info.Mode = FirmwareUEFI
			info.Evidence = append(info.Evidence, "registry PEFirmwareType=2 (UEFI)")
			// Note: this cannot distinguish native UEFI from UEFI+CSM where
			// Windows itself was installed in UEFI mode; CSM ambiguity here
			// mainly matters for OTHER, not-yet-installed OSes on the same
			// disk, which this tool must check separately per-target.
			return info, nil
		}
	}

	info.Mode = FirmwareUnknown
	info.Evidence = append(info.Evidence, "could not read PEFirmwareType from registry: "+errString(err))
	return info, nil
}

func errString(err error) string {
	if err == nil {
		return "no error, but value was empty/unrecognized"
	}
	return err.Error()
}
