// Package preflight composes the hypervisor/firmware/disk-headroom/
// catalog-load checks cmd/env-check already implemented in isolation
// (audit 2.14/2.15) into one function both cmd/env-check (a standalone
// diagnostic) and cmd/orchestrator (the real TUI/headless entry points)
// call, so the checks actually protect a real user instead of sitting in
// a diagnostic binary nothing ever invokes.
package preflight

import (
	"fmt"

	"boot-orchestrator/discovery"
	"boot-orchestrator/hypervisor"
	"boot-orchestrator/safety"
)

// Report is the result of running the standard pre-flight checks.
type Report struct {
	Hypervisor     hypervisor.Info
	Firmware       safety.FirmwareInfo
	Guard          safety.GuardReport
	CatalogEntries int

	// Warnings are informational — surfaced to the user but never block
	// continuation on their own (firmware mode, hypervisor detection,
	// battery level: all worth knowing, none of them this check's place
	// to refuse over, since the real target disk/OS isn't chosen yet).
	Warnings []string

	// Blocking are reasons Run itself judges serious enough that a
	// caller should refuse to continue by default (see
	// cmd/orchestrator/main.go's --skip-preflight for the explicit
	// override). Kept short deliberately: the precise, authoritative
	// safety gates (target-disk LVM/LUKS/BitLocker detection, the real
	// per-entry download-size check) already exist later in the real
	// flow once an actual target/OS is chosen — this is a fail-fast
	// sanity check before the user invests any time in the TUI, not a
	// re-implementation of those later gates.
	Blocking []string
}

// targetPathDiskFootprintBytes is 0 deliberately: at the point this runs
// (before the TUI has even launched), no OS has been picked yet, so there
// is no real image size to check disk headroom against. Passing 0 makes
// safety.CheckGuards's RequiredBytes equal to its own flat safety margin
// alone (25GB as of this writing) — a generic "do you have the baseline
// headroom this tool wants for ANYTHING" floor, not a promise that any
// specific catalog entry will fit. The real, size-specific check still
// runs later (engine.PreflightStorageSafetyCheck plus the download-time
// free-space check in engine/streamer.go) once a real target is chosen.
const targetPathDiskFootprintBytes = 0

// Run performs hypervisor detection, firmware-mode detection, a disk/
// power headroom check against targetPath, and an embedded-catalog
// load/sanity check. Never makes a network call — see RunLiveSuiteCheck
// for the separate, explicitly opt-in live scrape (audit 2.14).
func Run(targetPath string) (Report, error) {
	var report Report

	hv, hvErr := hypervisor.Detect()
	if hvErr != nil {
		report.Warnings = append(report.Warnings, fmt.Sprintf("hypervisor detection: %v", hvErr))
	} else {
		report.Hypervisor = hv
	}

	fw, fwErr := safety.DetectFirmware()
	if fwErr != nil {
		report.Warnings = append(report.Warnings, fmt.Sprintf("firmware detection: %v", fwErr))
	} else {
		report.Firmware = fw
		if fw.Mode == safety.FirmwareLegacyBIOS {
			report.Warnings = append(report.Warnings, "machine boots Legacy BIOS/MBR, not UEFI — some provisioning paths assume UEFI")
		}
	}

	guard, guardErr := safety.CheckGuards(targetPath, targetPathDiskFootprintBytes)
	if guardErr != nil {
		report.Blocking = append(report.Blocking, fmt.Sprintf("disk/power headroom check against %q failed: %v", targetPath, guardErr))
	} else {
		report.Guard = guard
		if !guard.HasEnoughSpace {
			report.Blocking = append(report.Blocking, fmt.Sprintf(
				"only %d bytes free at %q, below this tool's %d-byte baseline safety margin (no specific OS chosen yet — the exact per-entry check runs again once you pick one)",
				guard.AvailableBytes, targetPath, guard.RequiredBytes))
		}
		if !guard.PowerOK {
			report.Warnings = append(report.Warnings, fmt.Sprintf("power check: battery at %d%%, not on AC — provisioning can take a long time and must not be interrupted by a dead battery", guard.BatteryPercent))
		}
	}

	cat, catErr := discovery.LoadEmbedded()
	if catErr != nil {
		report.Blocking = append(report.Blocking, fmt.Sprintf("loading embedded OS catalog: %v", catErr))
	} else {
		report.CatalogEntries = len(cat.Entries)
		if report.CatalogEntries == 0 {
			report.Blocking = append(report.Blocking, "embedded OS catalog loaded but contains zero entries")
		}
	}

	return report, nil
}

// RunLiveSuiteCheck performs the separate, explicitly opt-in live network
// call to archive.ubuntu.com that cmd/env-check used to make
// unconditionally (audit 2.14): surprising for what's otherwise a local,
// offline-safe diagnostic, and breaks airgapped use entirely. Callers
// must ask for this explicitly (cmd/env-check's --live-scrape flag); it
// is never part of Run's own default checks.
func RunLiveSuiteCheck() ([]string, error) {
	return discovery.ScrapeUbuntuSuites(struct{}{})
}
