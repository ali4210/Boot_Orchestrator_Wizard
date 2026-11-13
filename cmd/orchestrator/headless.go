package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"boot-orchestrator/discovery"
	"boot-orchestrator/engine"
	"boot-orchestrator/hypervisor"
	"boot-orchestrator/safety"
)

// headlessConfig holds the flags accepted in --headless mode (Phase 1 item
// C.7). Password is deliberately never accepted directly: only
// --password-file is, so the plaintext never appears in argv (visible to
// any local user via `ps` or /proc/<pid>/cmdline).
type headlessConfig struct {
	OSID         string
	ISOPath      string
	TargetDisk   string
	Username     string
	PasswordFile string
	Yes          bool
}

// validate reports the first problem with cfg, or nil if headless
// provisioning may proceed. --yes is checked last so a user who forgot it
// still sees every other validation error at once, not just "pass --yes".
func (c headlessConfig) validate() error {
	if c.TargetDisk == "" {
		return fmt.Errorf("--headless requires --target-disk")
	}
	if c.Username == "" {
		return fmt.Errorf("--headless requires --username")
	}
	if c.PasswordFile == "" {
		return fmt.Errorf("--headless requires --password-file (the plaintext password is never accepted as a command-line argument)")
	}
	if c.OSID == "" && c.ISOPath == "" {
		return fmt.Errorf("--headless requires either --os-id (a catalog entry) or --iso-path (a local image)")
	}
	if c.OSID != "" && c.ISOPath != "" {
		return fmt.Errorf("--os-id and --iso-path are mutually exclusive; pick one")
	}
	if !c.Yes {
		return fmt.Errorf("refusing to run --headless without --yes (this flag exists so headless runs are never triggered by accident; re-run with --yes once you have confirmed the target disk, OS, and account)")
	}
	return nil
}

// readPasswordFile reads the password from path, stripping exactly one
// trailing newline (and, if present, the preceding carriage return) so a
// file saved by a text editor doesn't silently become part of the
// password. Internal whitespace is preserved verbatim.
func readPasswordFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading --password-file %s: %w", path, err)
	}
	s := string(data)
	s = strings.TrimSuffix(s, "\n")
	s = strings.TrimSuffix(s, "\r")
	return s, nil
}

// resolveHeadlessTarget decides, from targetDisk and whatever
// engine.DetectActiveRootDisk reported, whether this run is a dual-boot
// (shrinking the currently-active host OS) or single-boot (the target disk
// is not the machine's active root disk) provision, and which host
// partition/target partition to carry over in the dual-boot case. Kept as
// a pure function (no engine/Runner calls) so it is directly testable.
func resolveHeadlessTarget(targetDisk, activeRootDisk, activeHostNum, activeTargetPart string) (mode, hostNum, targetPart string) {
	if targetDisk != "" && targetDisk == activeRootDisk {
		return "dual-boot", activeHostNum, activeTargetPart
	}
	return "single-boot", "", ""
}

// resolveHeadlessImage resolves cfg's OS selection to the mirrors/sha/
// distro-name/entry quadruple RunProvision needs, exactly mirroring what
// the TUI's startProvisioning does with m.selectedOS (ui/model.go). Returns
// an error if --os-id was given but doesn't match any catalog entry.
func resolveHeadlessImage(cfg headlessConfig, catalog *discovery.Catalog) (primaryURL, sha256, distroName string, entry *discovery.Entry, isWindows bool, err error) {
	if cfg.OSID != "" {
		for _, e := range catalog.Entries {
			if strings.EqualFold(e.ID, cfg.OSID) {
				entryCopy := e
				entry = &entryCopy
				break
			}
		}
		if entry == nil {
			return "", "", "", nil, false, fmt.Errorf("--os-id %q does not match any catalog entry", cfg.OSID)
		}
		mirrors := entry.GetMirrors()
		if len(mirrors) == 0 {
			return "", "", "", nil, false, fmt.Errorf("catalog entry %q has no download URL or mirrors", cfg.OSID)
		}
		isWindows = strings.EqualFold(string(entry.Family), "windows")
		return mirrors[0], entry.SHA256, entry.Distro, entry, isWindows, nil
	}

	// --iso-path: a direct local image, same as the TUI's (currently
	// unwired — see AUDIT_REPORT.md / PROGRESS.md Phase 2 item H)
	// CustomISOPath concept, but actually consumed here. No catalog entry
	// means no automatic OS-family detection; this tool cannot yet inspect
	// an arbitrary ISO/image to tell Windows apart from Linux (that
	// detection work is Phase 2 item 19's ISO-picker fix), so Windows
	// autounattend injection is only available via --os-id.
	if _, statErr := os.Stat(cfg.ISOPath); statErr != nil {
		return "", "", "", nil, false, fmt.Errorf("--iso-path %s: %w", cfg.ISOPath, statErr)
	}
	return cfg.ISOPath, "", "custom-iso", nil, false, nil
}

// buildHeadlessRequest assembles the same engine.ProvisionRequest shape the
// TUI's startProvisioning builds (ui/model.go), from headless flags instead
// of TUI state, so headless mode runs through the identical engine path and
// safety gates (Phase 1 item C.7). It performs no provisioning itself.
func buildHeadlessRequest(cfg headlessConfig, catalog *discovery.Catalog, hvKind hypervisor.Kind, activeRootDisk, activeHostNum, activeTargetPart string) (engine.ProvisionRequest, error) {
	if err := cfg.validate(); err != nil {
		return engine.ProvisionRequest{}, err
	}
	if !engine.ValidUsername(cfg.Username) {
		return engine.ProvisionRequest{}, fmt.Errorf("--username %q is not a valid, non-reserved username", cfg.Username)
	}
	password, err := readPasswordFile(cfg.PasswordFile)
	if err != nil {
		return engine.ProvisionRequest{}, err
	}
	if !engine.ValidPassword(password) {
		return engine.ProvisionRequest{}, fmt.Errorf("password in %s must be at least %d characters", cfg.PasswordFile, engine.MinPasswordLength)
	}

	primaryURL, sha, distroName, entry, isWindows, err := resolveHeadlessImage(cfg, catalog)
	if err != nil {
		return engine.ProvisionRequest{}, err
	}

	mode, hostNum, targetPart := resolveHeadlessTarget(cfg.TargetDisk, activeRootDisk, activeHostNum, activeTargetPart)

	autounattendPath := ""
	if isWindows {
		if abs, absErr := filepath.Abs("discovery/manifests/autounattend.xml"); absErr == nil {
			if _, statErr := os.Stat(abs); statErr == nil {
				autounattendPath = abs
			}
		}
	}

	hash, err := (engine.Credentials{Password: password}).HashPassword()
	if err != nil {
		return engine.ProvisionRequest{}, fmt.Errorf("hashing password: %w", err)
	}

	return engine.ProvisionRequest{
		JournalPath:      "orchestrator_journal.json",
		ImageURL:         primaryURL,
		ImageSHA256:      sha,
		DownloadDestPath: "/var/tmp/os_image.payload",
		TargetDiskPath:   cfg.TargetDisk,
		TargetPartition:  targetPart,
		HostPartNum:      hostNum,
		ESPPartNum:       "1",
		IsWindowsImage:   isWindows,
		AutounattendPath: autounattendPath,
		BootLabel:        strings.ToLower(distroName),
		HypervisorKind:   hvKind,
		SelectedEntry:    entry,
		ProvisionMode:    mode,
		Credentials: engine.Credentials{
			Username:     cfg.Username,
			PasswordHash: hash,
			// AllowRootLogin is intentionally not exposed as a headless
			// flag; root stays locked by default exactly as in the TUI.
		},
	}, nil
}

// runHeadless is the --headless entry point: builds the request, then runs
// it through the exact same engine.RunProvision call the TUI uses
// (ui/provision.go's runProvisionWithCancel), printing step updates to
// stdout instead of a Bubble Tea view.
func runHeadless(cfg headlessConfig) error {
	if err := cfg.validate(); err != nil {
		return err
	}

	catalog, err := discovery.LoadEmbedded()
	if err != nil {
		return fmt.Errorf("loading OS catalog: %w", err)
	}

	hv, _ := hypervisor.Detect()

	activeRootDisk, _, activeHostNum, activeTargetPart, _ := engine.DetectActiveRootDisk()

	req, err := buildHeadlessRequest(cfg, catalog, hv.Kind, activeRootDisk, activeHostNum, activeTargetPart)
	if err != nil {
		return err
	}

	journal, jErr := safety.NewJournal(req.JournalPath, fmt.Sprintf("headless-%d", os.Getpid()))
	if jErr == nil {
		_ = journal.SetSessionState(safety.SessionState{
			TargetOSID:    req.BootLabel,
			DistroName:    req.BootLabel,
			ProvisionMode: req.ProvisionMode,
			TargetDisk:    req.TargetDiskPath,
			DownloadURL:   req.ImageURL,
		})
	}

	result := engine.RunProvision(context.Background(), req, "/etc/default/grub", func(u engine.StepUpdate) {
		if u.Err != nil {
			fmt.Fprintf(os.Stderr, "[FAILED] %s: %v\n", u.StepName, u.Err)
		} else {
			fmt.Printf("%s\n", u.StepName)
		}
	})

	if !result.Success {
		return fmt.Errorf("provisioning failed at step %q: %v (rolled back: %v)", result.FailedAtStep, result.OriginalError, result.RolledBack)
	}
	return nil
}
