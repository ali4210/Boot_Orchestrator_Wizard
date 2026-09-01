// Package hypervisor determines whether the current machine is running on
// bare metal or inside a virtual machine, and if so, which hypervisor.
package hypervisor

// Kind identifies the execution environment of the host.
type Kind int

const (
	KindUnknown Kind = iota
	KindBareMetal
	KindVirtualBox
	KindVMware
	KindKVM
	KindHyperV
	KindQEMU
	KindXen
	KindParallels
)

func (k Kind) String() string {
	switch k {
	case KindBareMetal:
		return "Bare Metal"
	case KindVirtualBox:
		return "Oracle VirtualBox"
	case KindVMware:
		return "VMware"
	case KindKVM:
		return "KVM"
	case KindHyperV:
		return "Microsoft Hyper-V"
	case KindQEMU:
		return "QEMU"
	case KindXen:
		return "Xen"
	case KindParallels:
		return "Parallels"
	default:
		return "Unknown"
	}
}

// Info is the full result of a hypervisor detection pass.
type Info struct {
	Kind       Kind
	IsVirtual  bool
	Evidence   []string // human-readable list of signals that led to the verdict
	RawVendor  string   // raw DMI / firmware vendor string, if available
}

// Detect inspects the current machine and returns what it's running on.
// Implementation is platform-specific (see detect_linux.go, detect_windows.go,
// detect_darwin.go) and is selected automatically via Go build tags.
func Detect() (Info, error) {
	return detectPlatform()
}
