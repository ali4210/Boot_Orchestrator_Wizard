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

	// AllowUnverified opts into provisioning a payload with no checksum to
	// verify the download against (audit follow-up F13) — every --iso-path
	// is inherently unverified (no catalog checksum exists for an arbitrary
	// local file), and some --os-id catalog entries are too (see
	// docs/MANIFEST_AUDIT.md). Separate from --yes: --yes means "I confirm
	// the target disk/OS/account", this means "I specifically accept an
	// unverifiable download", and conflating the two would make it too easy
	// to pass once and forget what was actually being accepted.
	AllowUnverified bool
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

// isBlockDevice reports whether path names a real block device (disk or
// partition). Follow-up F8: this used to check for a same-named entry
// under /sys/class/block by basename alone, which neither resolves
// symlinks (a /dev/disk/by-id/... path's basename is never a kernel block
// device name, so it always failed closed there — safe, but useless for
// the common case of a by-id/by-path alias) nor actually inspects the
// target path itself (a regular file that happened to be named e.g. "sda"
// right next to a real disk would have been indistinguishable by basename
// alone in a differently-laid-out directory). Now resolves symlinks first,
// then stats the resolved path directly and requires the kernel's own
// device-type bits: os.ModeDevice set (a device node of some kind) AND
// os.ModeCharDevice NOT set (device nodes are either block or character;
// this must be block). A var (not a plain func) so tests can simulate
// "is/is not a block device" without a real one on the test machine.
var isBlockDevice = func(path string) bool {
	if path == "" {
		return false
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false
	}
	fi, err := os.Stat(resolved)
	if err != nil {
		return false
	}
	mode := fi.Mode()
	return mode&os.ModeDevice != 0 && mode&os.ModeCharDevice == 0
}

// procMountsPath is /proc/mounts, overridable in tests.
var procMountsPath = "/proc/mounts"

// isLikelyLiveRootDisk double-checks, independently of
// engine.DetectActiveRootDisk's own heuristics, whether diskDevice is (or
// contains the partition behind) the filesystem currently mounted at "/",
// by reading /proc/mounts directly. Defense in depth for follow-up F3: if
// DetectActiveRootDisk's own detection were ever to fail silently (e.g.
// returning "" instead of erroring), resolveHeadlessTarget would wrongly
// resolve the live root disk as "single-boot" — wiping the system this
// tool is currently running on. This catches that case independently.
//
// Follow-up F8: this used to accept any rootSrc with diskDevice as a
// string prefix, which wrongly matched e.g. diskDevice="/dev/sda1" against
// rootSrc="/dev/sda12" (a different partition) purely because
// "/dev/sda1" is a textual prefix of "/dev/sda12". Matching is now exact
// (rootSrc == diskDevice) or via engine.SplitDiskAndPartition's structural
// parse of rootSrc's own parent disk — so "sda" matches "sda1" or
// "nvme0n1p2"'s parent, but never "sdaa" (whose parent disk parses as
// "sdaa", not "sda").
func isLikelyLiveRootDisk(diskDevice string) bool {
	if diskDevice == "" {
		return false
	}
	data, err := os.ReadFile(procMountsPath)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[1] != "/" {
			continue
		}
		rootSrc := fields[0]
		if resolved, evalErr := filepath.EvalSymlinks(rootSrc); evalErr == nil {
			rootSrc = resolved
		}
		if rootSrc == diskDevice {
			return true
		}
		if parentDisk, _, splitErr := engine.SplitDiskAndPartition(rootSrc); splitErr == nil && parentDisk == diskDevice {
			return true
		}
	}
	return false
}

// findAutounattendNear looks for discovery/manifests/autounattend.xml
// relative to exePath — first alongside the executable itself, then in the
// executable's parent directory, which matches how this project's own
// autorun.sh/autorun.ps1 lay things out (the compiled binary lands in
// bin/, with discovery/ one level up at the repo root). Pure function (no
// os.Executable() call) so it is directly testable with fake directories.
func findAutounattendNear(exePath string) (string, error) {
	exeDir := filepath.Dir(exePath)
	candidates := []string{
		filepath.Join(exeDir, "discovery", "manifests", "autounattend.xml"),
		filepath.Join(filepath.Dir(exeDir), "discovery", "manifests", "autounattend.xml"),
	}
	for _, c := range candidates {
		if _, statErr := os.Stat(c); statErr == nil {
			return c, nil
		}
	}
	return "", fmt.Errorf("autounattend.xml not found near the orchestrator binary (checked %s)", strings.Join(candidates, ", "))
}

// resolveAutounattendPath is findAutounattendNear wired to the real running
// executable's path (not the process's current working directory, which
// can be anything depending on how the binary was launched).
func resolveAutounattendPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolving executable path to locate autounattend.xml: %w", err)
	}
	if resolved, evalErr := filepath.EvalSymlinks(exe); evalErr == nil {
		exe = resolved
	}
	return findAutounattendNear(exe)
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

	if !isBlockDevice(cfg.TargetDisk) {
		return engine.ProvisionRequest{}, fmt.Errorf("--target-disk %q is not a recognized block device (no /sys/class/block entry); refusing to provision a non-disk target", cfg.TargetDisk)
	}

	primaryURL, sha, distroName, entry, isWindows, err := resolveHeadlessImage(cfg, catalog)
	if err != nil {
		return engine.ProvisionRequest{}, err
	}

	// Audit follow-up F13: no payload may be silently provisioned without a
	// checksum to verify the download against. --iso-path is always
	// unverified (there is no catalog checksum for an arbitrary local
	// file); an --os-id catalog entry may also lack one — see
	// docs/MANIFEST_AUDIT.md, which currently lists every entry as such.
	if strings.TrimSpace(sha) == "" && !cfg.AllowUnverified {
		source := cfg.OSID
		if source == "" {
			source = cfg.ISOPath
		}
		return engine.ProvisionRequest{}, fmt.Errorf("%q has no checksum to verify the download against; refusing without --allow-unverified (pass it only if you have independently confirmed this payload's integrity)", source)
	}

	mode, hostNum, targetPart := resolveHeadlessTarget(cfg.TargetDisk, activeRootDisk, activeHostNum, activeTargetPart)
	if mode == "single-boot" && isLikelyLiveRootDisk(cfg.TargetDisk) {
		return engine.ProvisionRequest{}, fmt.Errorf("refusing: --target-disk %q contains this machine's live root filesystem (per /proc/mounts) but was resolved as single-boot, which would wipe it; this likely means root-disk auto-detection disagreed with the live mount table — investigate before proceeding", cfg.TargetDisk)
	}

	autounattendPath := ""
	if isWindows {
		resolved, aErr := resolveAutounattendPath()
		if aErr != nil {
			return engine.ProvisionRequest{}, fmt.Errorf("Windows target %q selected but no autounattend answer file is available: %w", distroName, aErr)
		}
		autounattendPath = resolved
	}

	hash, err := (engine.Credentials{Password: password}).HashPassword()
	if err != nil {
		return engine.ProvisionRequest{}, fmt.Errorf("hashing password: %w", err)
	}

	return engine.ProvisionRequest{
		JournalPath:      safety.DefaultJournalPath(),
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
