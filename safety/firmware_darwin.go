//go:build darwin

package safety

import "os/exec"

// NOTE: cross-compiled from Linux, not executed on real macOS. Every Mac
// since 2006 boots via EFI, so FirmwareLegacyBIOS is not a real possibility
// here. The meaningful distinction on modern Macs is Apple Silicon's
// Secure/Full/Reduced/Permissive boot security policy, which behaves like a
// much stricter analogue of "CSM vs native UEFI" for dual-boot purposes:
// Apple Silicon Macs cannot dual-boot arbitrary x86 OSes at all, and third-
// party OS dual-boot is possible only via virtualization (see hypervisor
// package) or, for Linux, community bootloader shims like Asahi's u-boot.
func detectFirmwarePlatform() (FirmwareInfo, error) {
	info := FirmwareInfo{Mode: FirmwareUEFI}
	info.Evidence = append(info.Evidence, "all Macs since 2006 boot via EFI; native BIOS mode does not exist on this platform")

	if out, err := exec.Command("sysctl", "-n", "hw.optional.arm64").Output(); err == nil {
		if string(out) != "" && out[0] == '1' {
			info.Evidence = append(info.Evidence, "Apple Silicon detected: dual-boot of non-Apple OSes is NOT supported by this tool on this architecture; only virtualization (see hypervisor package) is viable")
		}
	}

	return info, nil
}
