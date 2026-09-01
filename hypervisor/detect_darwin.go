//go:build darwin

package hypervisor

import (
	"os/exec"
	"strings"
)

// NOTE: This file is cross-compiled from Linux and has not been executed on
// real macOS hardware/VMs. Verify on real hardware and inside Parallels/UTM
// before relying on it. macOS dual-boot support is inherently limited by
// Apple Silicon's restricted boot process — this module identifies the
// environment only, it does not imply full macOS dual-boot orchestration.

func detectPlatform() (Info, error) {
	info := Info{Kind: KindBareMetal, IsVirtual: false}

	// sysctl hw.model reports "VirtualMac2,1", "Parallels..." etc. inside VMs
	// on Apple Silicon; on Intel Macs it may still reveal VMware Fusion/Parallels.
	if out, err := exec.Command("sysctl", "-n", "hw.model").Output(); err == nil {
		model := strings.ToLower(strings.TrimSpace(string(out)))
		info.RawVendor = strings.TrimSpace(string(out))
		if kind := classifyModelString(model); kind != KindUnknown {
			info.Kind = kind
			info.IsVirtual = true
			info.Evidence = append(info.Evidence, "sysctl hw.model matched: "+model)
			return info, nil
		}
	}

	// ioreg board-id / manufacturer often reveals VMware/Parallels on Intel Macs.
	if out, err := exec.Command("ioreg", "-l").Output(); err == nil {
		combined := strings.ToLower(string(out))
		switch {
		case strings.Contains(combined, "vmware"):
			info.Kind = KindVMware
			info.IsVirtual = true
			info.Evidence = append(info.Evidence, "ioreg contains VMware signature")
			return info, nil
		case strings.Contains(combined, "parallels"):
			info.Kind = KindParallels
			info.IsVirtual = true
			info.Evidence = append(info.Evidence, "ioreg contains Parallels signature")
			return info, nil
		}
	}

	info.Evidence = append(info.Evidence, "no virtualization signals found; assuming bare metal Mac")
	return info, nil
}

func classifyModelString(s string) Kind {
	switch {
	case strings.Contains(s, "vmware"):
		return KindVMware
	case strings.Contains(s, "parallels"):
		return KindParallels
	case strings.Contains(s, "virtualmac"):
		// Apple's own Virtualization.framework guest identifier (macOS-on-macOS)
		return KindKVM // closest generic bucket; treated as "virtualized, non-hardware"
	default:
		return KindUnknown
	}
}
