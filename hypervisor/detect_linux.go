//go:build linux

package hypervisor

import (
	"os/exec"
	"strings"
)

// dmiPaths are the standard sysfs locations exposing firmware-provided
// identification strings. These are populated by the kernel from ACPI/SMBIOS
// tables and require no special privileges to read.
var dmiPaths = []string{
	"/sys/class/dmi/id/product_name",
	"/sys/class/dmi/id/sys_vendor",
	"/sys/class/dmi/id/board_vendor",
	"/sys/class/dmi/id/bios_vendor",
}

func detectPlatform() (Info, error) {
	info := Info{Kind: KindBareMetal, IsVirtual: false}

	// 1. Prefer systemd-detect-virt if present: it is purpose-built for this
	// and handles containers vs VMs vs bare metal correctly.
	if out, err := exec.Command("systemd-detect-virt").Output(); err == nil {
		result := strings.TrimSpace(string(out))
		if result != "none" {
			info.Evidence = append(info.Evidence, "systemd-detect-virt reported: "+result)
			info.IsVirtual = true
			info.Kind = classifyVirtString(result)
			return info, nil
		}
		info.Evidence = append(info.Evidence, "systemd-detect-virt reported: none")
	}

	// 2. Fallback: inspect DMI strings directly (works even in minimal
	// environments without systemd, e.g. live/rescue images).
	combined := ""
	for _, p := range dmiPaths {
		if data, err := readFileTrim(p); err == nil && data != "" {
			combined += " " + strings.ToLower(data)
			if info.RawVendor == "" {
				info.RawVendor = data
			}
		}
	}

	if combined != "" {
		if kind := classifyDMIString(combined); kind != KindUnknown {
			info.Kind = kind
			info.IsVirtual = true
			info.Evidence = append(info.Evidence, "DMI strings matched: "+strings.TrimSpace(combined))
			return info, nil
		}
	}

	// 3. Last resort: check for the "hypervisor" CPU flag in /proc/cpuinfo.
	if data, err := readFileTrim("/proc/cpuinfo"); err == nil {
		if strings.Contains(data, "hypervisor") {
			info.IsVirtual = true
			info.Kind = KindUnknown
			info.Evidence = append(info.Evidence, "/proc/cpuinfo contains 'hypervisor' flag but vendor could not be classified")
			return info, nil
		}
	}

	info.Evidence = append(info.Evidence, "no virtualization signals found; assuming bare metal")
	return info, nil
}

func classifyVirtString(s string) Kind {
	switch strings.ToLower(s) {
	case "oracle", "virtualbox":
		return KindVirtualBox
	case "vmware":
		return KindVMware
	case "kvm":
		return KindKVM
	case "microsoft":
		return KindHyperV
	case "qemu":
		return KindQEMU
	case "xen":
		return KindXen
	case "parallels":
		return KindParallels
	default:
		return KindUnknown
	}
}

func classifyDMIString(s string) Kind {
	switch {
	case strings.Contains(s, "virtualbox"):
		return KindVirtualBox
	case strings.Contains(s, "vmware"):
		return KindVMware
	case strings.Contains(s, "kvm"):
		return KindKVM
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
