// Package engine — slipstream.go implements the "Legacy Windows Driver
// Slipstreamer" from the blueprint: injects NVMe, USB 3.0, and VirtIO/SCSI
// drivers into Windows 7 / Server 2008-era images that predate native
// support for that hardware, which otherwise BSOD (INACCESSIBLE_BOOT_DEVICE
// or similar) on first boot against modern NVMe/USB3 controllers or
// virtualized VirtIO disks.
package engine

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// DriverPackage is one driver to inject — a directory containing a .inf
// and its matching .sys/.cat files, as Windows driver packages ship.
type DriverPackage struct {
	Name    string // human label, e.g. "VirtIO SCSI (Red Hat)"
	InfPath string // path to the .inf inside the extracted driver package
	Reason  string // why it's needed, shown in the TUI ("required for boot from VirtIO disk")
}

// KnownLegacyGaps maps a Windows release + boot medium combination to the
// driver classes it's missing natively, per the blueprint's stated targets
// (Windows 7 / Server 2008 lacking NVMe, USB3, VirtIO/SCSI).
type BootMedium string

const (
	MediumNVMe   BootMedium = "nvme"
	MediumUSB3   BootMedium = "usb3"
	MediumVirtIO BootMedium = "virtio"
)

// RequiredDriverClasses returns which driver classes a given Windows
// release needs injected for the given boot medium, based on known native
// support cutoffs. Windows 8/8.1+ have native NVMe and USB3 support;
// VirtIO is never natively supported by any stock Windows image (it always
// needs injection when installing onto a VirtIO-backed VM disk).
func RequiredDriverClasses(windowsRelease string, medium BootMedium) []BootMedium {
	release := strings.ToLower(windowsRelease)
	isLegacy := strings.Contains(release, "windows 7") ||
		strings.Contains(release, "windows vista") ||
		strings.Contains(release, "windows xp") ||
		strings.Contains(release, "server 2008") ||
		strings.Contains(release, "server 2003")

	switch medium {
	case MediumVirtIO:
		return []BootMedium{MediumVirtIO} // always needed, regardless of release
	case MediumNVMe:
		if isLegacy {
			return []BootMedium{MediumNVMe}
		}
	case MediumUSB3:
		if isLegacy {
			return []BootMedium{MediumUSB3}
		}
	}
	return nil
}

// FindDriverPackage locates the correct .inf for a driver class + target
// architecture inside a directory of extracted vendor driver packages
// (e.g. downloaded VirtIO driver ISO, or Intel/Samsung NVMe driver zip).
// It does not download drivers itself — driver EULAs vary by vendor and
// this tool does not redistribute or silently fetch third-party drivers.
func FindDriverPackage(driverRootDir string, class BootMedium, arch string) (*DriverPackage, error) {
	var candidates []string
	err := filepath.WalkDir(driverRootDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.EqualFold(filepath.Ext(path), ".inf") {
			return nil
		}
		lowerPath := strings.ToLower(path)
		if !strings.Contains(lowerPath, strings.ToLower(arch)) {
			return nil
		}
		if classMatchesPath(class, lowerPath) {
			candidates = append(candidates, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scanning %s for drivers: %w", driverRootDir, err)
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no %s driver .inf found under %s for arch %s — download the vendor driver package and extract it there first", class, driverRootDir, arch)
	}

	name := driverPackageDisplayName(class)
	return &DriverPackage{
		Name:    name,
		InfPath: candidates[0],
		Reason:  fmt.Sprintf("required for boot: target image predates native %s support", class),
	}, nil
}

func classMatchesPath(class BootMedium, lowerPath string) bool {
	switch class {
	case MediumNVMe:
		return strings.Contains(lowerPath, "nvme")
	case MediumUSB3:
		return strings.Contains(lowerPath, "usb") && (strings.Contains(lowerPath, "xhci") || strings.Contains(lowerPath, "3.0") || strings.Contains(lowerPath, "3_0"))
	case MediumVirtIO:
		return strings.Contains(lowerPath, "virtio") || strings.Contains(lowerPath, "vioscsi") || strings.Contains(lowerPath, "viostor")
	}
	return false
}

func driverPackageDisplayName(class BootMedium) string {
	switch class {
	case MediumNVMe:
		return "NVMe Storage Driver"
	case MediumUSB3:
		return "USB 3.0 xHCI Host Controller Driver"
	case MediumVirtIO:
		return "VirtIO Block/SCSI Driver (Red Hat)"
	default:
		return string(class)
	}
}

// SlipstreamPlan is the not-yet-applied plan to inject one or more drivers
// into an offline WIM index or an already-applied Windows volume.
type SlipstreamPlan struct {
	TargetIsWim     bool   // true: inject into WIM via DISM offline mount; false: inject directly into applied volume via drvload/pnputil offline
	WimPath         string // set when TargetIsWim
	WimIndex        int
	MountDir        string // scratch dir DISM will mount the WIM image into
	TargetRoot      string // applied volume root, when !TargetIsWim
	Drivers         []DriverPackage
}

// PlanSlipstream builds the DISM command sequence for injecting drivers,
// preferring offline WIM injection (cleaner — survives image re-application)
// over injecting into an already-applied volume when both are possible.
func PlanSlipstream(wimPath string, wimIndex int, mountDir string, drivers []DriverPackage) *SlipstreamPlan {
	return &SlipstreamPlan{
		TargetIsWim: true,
		WimPath:     wimPath,
		WimIndex:    wimIndex,
		MountDir:    mountDir,
		Drivers:     drivers,
	}
}

// ApplySlipstream executes driver injection via DISM. Requires DISM (i.e.
// Windows host, or Windows PE); wimlib can apply images but does not
// support offline driver injection, so this step specifically needs a
// Windows environment even if earlier steps ran cross-platform.
//
// The mounted image is always committed-or-discarded explicitly (never
// left mounted) so a crash mid-injection doesn't leave the WIM file locked
// or in an inconsistent mounted state for the next run.
func ApplySlipstream(plan *SlipstreamPlan) (err error) {
	if _, err := exec.LookPath("dism.exe"); err != nil {
		return fmt.Errorf("dism.exe not found — driver slipstreaming requires running on Windows or WinPE: %w", err)
	}

	if err := os.MkdirAll(plan.MountDir, 0755); err != nil {
		return fmt.Errorf("creating mount dir %s: %w", plan.MountDir, err)
	}

	mountOut, err := exec.Command("dism.exe",
		"/Mount-Image",
		"/ImageFile:"+plan.WimPath,
		fmt.Sprintf("/Index:%d", plan.WimIndex),
		"/MountDir:"+plan.MountDir,
	).CombinedOutput()
	if err != nil {
		return fmt.Errorf("dism /Mount-Image failed: %w\n%s", err, mountOut)
	}

	// Always discard-or-commit on the way out — never leave the image mounted.
	defer func() {
		verb := "/Commit"
		if err != nil {
			verb = "/Discard"
		}
		unmountOut, uerr := exec.Command("dism.exe", "/Unmount-Image", "/MountDir:"+plan.MountDir, verb).CombinedOutput()
		if uerr != nil {
			// Surface the unmount failure even if the main operation
			// otherwise succeeded — a WIM stuck mounted will fail every
			// subsequent run until manually cleaned up with the same command.
			err = fmt.Errorf("dism /Unmount-Image (%s) failed after driver injection: %w\n%s (original error, if any: %v)", verb, uerr, unmountOut, err)
		}
	}()

	for _, drv := range plan.Drivers {
		out, derr := exec.Command("dism.exe",
			"/Image:"+plan.MountDir,
			"/Add-Driver",
			"/Driver:"+drv.InfPath,
		).CombinedOutput()
		if derr != nil {
			err = fmt.Errorf("dism /Add-Driver for %s (%s) failed: %w\n%s", drv.Name, drv.InfPath, derr, out)
			return err
		}
	}

	return nil
}
