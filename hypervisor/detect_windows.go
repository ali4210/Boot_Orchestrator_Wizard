//go:build windows

package hypervisor

import (
	"os/exec"
	"strings"
)

// NOTE: This file is cross-compiled from Linux and has not been executed on
// real Windows hardware/VMs. Verify behavior on a disposable Windows VM
// before relying on it for anything destructive.

func detectPlatform() (Info, error) {
	info := Info{Kind: KindBareMetal, IsVirtual: false}

	// Query manufacturer/model via WMIC (present on Windows 7 through most of
	// Windows 10; on Windows 11 systems where wmic is removed, fall back to
	// PowerShell's Get-CimInstance).
	if out, err := exec.Command("wmic", "computersystem", "get", "manufacturer,model").Output(); err == nil {
		combined := strings.ToLower(string(out))
		info.RawVendor = strings.TrimSpace(string(out))
		if kind := classifyDMIString(combined); kind != KindUnknown {
			info.Kind = kind
			info.IsVirtual = true
			info.Evidence = append(info.Evidence, "wmic computersystem manufacturer/model matched")
			return info, nil
		}
	}

	// Fallback for Windows 11+ (wmic deprecated): PowerShell CIM query.
	psCmd := `(Get-CimInstance -ClassName Win32_ComputerSystem | Select-Object -ExpandProperty Manufacturer) + " " + (Get-CimInstance -ClassName Win32_ComputerSystem | Select-Object -ExpandProperty Model)`
	if out, err := exec.Command("powershell", "-NoProfile", "-Command", psCmd).Output(); err == nil {
		combined := strings.ToLower(string(out))
		if info.RawVendor == "" {
			info.RawVendor = strings.TrimSpace(string(out))
		}
		if kind := classifyDMIString(combined); kind != KindUnknown {
			info.Kind = kind
			info.IsVirtual = true
			info.Evidence = append(info.Evidence, "PowerShell Win32_ComputerSystem matched")
			return info, nil
		}
	}

	info.Evidence = append(info.Evidence, "no virtualization signals found via WMI; assuming bare metal")
	return info, nil
}

func classifyDMIString(s string) Kind {
	switch {
	case strings.Contains(s, "virtualbox"):
		return KindVirtualBox
	case strings.Contains(s, "vmware"):
		return KindVMware
	case strings.Contains(s, "microsoft corporation") && strings.Contains(s, "virtual"):
		return KindHyperV
	case strings.Contains(s, "qemu"):
		return KindQEMU
	case strings.Contains(s, "xen"):
		return KindXen
	case strings.Contains(s, "parallels"):
		return KindParallels
	default:
		return KindUnknown
	}
}
