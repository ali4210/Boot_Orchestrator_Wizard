package main

import (
	"flag"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"boot-orchestrator/preflight"
	"boot-orchestrator/ui"
)

// CustomISOPath holds any direct image path provided via the command line
var CustomISOPath string

// dryRunRefusalMessage is printed by --dry-run instead of running the real
// provisioning flow in a simulated mode.
//
// Phase 5 (items 31-34) built real interception infrastructure —
// engine.Runner (pre-existing) plus the new engine.FS, both switchable via
// engine.SetDryRun(true) — and migrated one real batch onto it (see
// docs/dryrun-inventory.md, PROGRESS.md items 32/33). That is genuine
// progress, not nothing. It is still not safe to let --dry-run run the real
// RunProvision flow, for a reason more specific than "not enough call sites
// migrated yet": RunProvision's actual code path interleaves
// Runner/FS-routed calls with hundreds of raw, uncovered exec.Command/os.*
// calls throughout the SAME functions (partition, mount, bootloader, USB,
// backup/revert, WIM/BCD). Flipping SetDryRun(true) and then running that
// path for real would intercept only some of what each function does and
// let the rest execute for real — worse than refusing outright, because it
// would look like protection while still writing to the real disk partway
// through. A second, independent limitation surfaced while testing item
// 32's own migrated batch: a dry-run can't always even claim "success" for
// a fully-intercepted function, if that function's own correctness check
// depends on a real side effect the simulation never produced (see
// assertDefaultCredentials's documented dry-run behavior in
// engine/dryrun_test.go — it correctly returns an error in dry-run mode,
// not a fabricated success). Faking that away would mean inventing a
// result dry-run has no way to actually know. Until enough of the
// genuinely destructive call sites are migrated that running the real flow
// under SetDryRun(true) is accurate start-to-finish (not just "doesn't
// crash"), --dry-run keeps refusing rather than offer partial, misleading
// protection. See docs/ROADMAP.md's Phase 5 section for what's left.
const dryRunRefusalMessage = `--dry-run requested, but refusing to start: a full, honest dry-run isn't safe to offer yet.

Real interception infrastructure now exists (engine.Runner, engine.FS,
engine.SetDryRun — see docs/dryrun-inventory.md) and covers one real batch
end-to-end. It does NOT cover the hundreds of remaining partition,
bootloader, USB, backup/revert, and WIM/BCD call sites, and those are
interleaved with the covered ones inside the same functions RunProvision
calls — so running the real flow under dry-run today would intercept only
part of what happens and let the rest execute for real. That is worse than
refusing: it would look like protection while still touching the real disk.

Until enough of the remaining call sites are migrated that the real flow is
accurate under simulation from start to finish, this flag refuses to start
rather than offer partial, misleading protection. Test this tool only in a
disposable VM against a virtual disk you are prepared to lose.
`

// checkDryRun reports whether --dry-run was requested and, if so, the
// message that should be printed before refusing to start. Kept separate
// from main() so the fail-closed behavior is unit-testable without a
// subprocess.
func checkDryRun(requested bool) (refuse bool, message string) {
	if !requested {
		return false, ""
	}
	return true, dryRunRefusalMessage
}

// runPreflightGate decides whether to refuse to continue given an already-
// run preflight.Report, separately from main() so this decision is
// unit-testable without needing real hypervisor/firmware/disk detection
// (audit 2.15). message is non-empty whenever there was anything to tell
// the user, whether or not skip ends up overriding refuse.
func runPreflightGate(report preflight.Report, skip bool) (refuse bool, message string) {
	if len(report.Blocking) == 0 {
		return false, ""
	}
	msg := "Pre-flight check found blocking issue(s):\n"
	for _, b := range report.Blocking {
		msg += "  - " + b + "\n"
	}
	if skip {
		msg += "Continuing anyway because --skip-preflight was passed.\n"
		return false, msg
	}
	msg += "Refusing to start. Pass --skip-preflight to continue anyway (not recommended).\n"
	return true, msg
}

func main() {
	isoFlag := flag.String("iso", "", "Direct local path to an ISO or raw disk image")
	dryRunFlag := flag.Bool("dry-run", false, "Refuse to start (see --help); full dry-run simulation is not implemented yet")

	headlessFlag := flag.Bool("headless", false, "Run non-interactively instead of launching the TUI; requires --yes")
	osIDFlag := flag.String("os-id", "", "Headless: catalog entry ID to provision (mutually exclusive with --iso-path)")
	isoPathFlag := flag.String("iso-path", "", "Headless: direct local path to an ISO/image to provision (mutually exclusive with --os-id)")
	targetDiskFlag := flag.String("target-disk", "", "Headless: target disk device, e.g. /dev/sda")
	usernameFlag := flag.String("username", "", "Headless: account to create on the provisioned OS")
	passwordFileFlag := flag.String("password-file", "", "Headless: path to a file containing the account password (plaintext passwords are never accepted as a command-line argument)")
	yesFlag := flag.Bool("yes", false, "Headless/--revert: confirm you want to proceed; required for either to do anything")
	allowUnverifiedFlag := flag.Bool("allow-unverified", false, "Headless: provision a payload with no checksum to verify the download against (every --iso-path, and some --os-id catalog entries — see docs/MANIFEST_AUDIT.md); pass only after independently confirming the payload's integrity")
	revertFlag := flag.Bool("revert", false, "Run the dual-boot-to-single-boot revert non-interactively instead of launching the TUI; requires --yes")
	keepSliceFlag := flag.Bool("keep-slice", false, "--revert: reformat the added partition as a clean staging slice instead of deleting it and growing the host partition back")
	skipPreflightFlag := flag.Bool("skip-preflight", false, "Continue even if the pre-flight check (disk headroom, catalog load) finds a blocking issue; not recommended")
	preflightTargetFlag := flag.String("preflight-target-path", ".", "Path to check disk headroom against during the pre-flight check")
	flag.Parse()

	if refuse, message := checkDryRun(*dryRunFlag); refuse {
		fmt.Fprint(os.Stderr, message)
		os.Exit(1)
	}

	if *revertFlag {
		if err := runHeadlessRevert(*yesFlag, *keepSliceFlag); err != nil {
			fmt.Fprintf(os.Stderr, "headless revert failed: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// Audit 2.15: the pre-flight checks (hypervisor, firmware, disk
	// headroom, catalog load) used to live only in cmd/env-check, a
	// standalone diagnostic that nothing ever invoked — a genuinely
	// useful check that protected zero actual users. Now runs before
	// either real entry point that's about to provision something
	// (headless or the TUI) gets going. --revert and --dry-run already
	// returned above by this point if requested, and don't need this
	// check anyway (revert doesn't download/write a new OS; --dry-run
	// exits before doing anything regardless).
	{
		report, pfErr := preflight.Run(*preflightTargetFlag)
		if pfErr != nil {
			fmt.Fprintf(os.Stderr, "pre-flight check failed to run: %v\n", pfErr)
			os.Exit(1)
		}
		refuse, message := runPreflightGate(report, *skipPreflightFlag)
		if message != "" {
			fmt.Fprint(os.Stderr, message)
		}
		if refuse {
			os.Exit(1)
		}
	}

	if *headlessFlag {
		cfg := headlessConfig{
			OSID:            *osIDFlag,
			ISOPath:         *isoPathFlag,
			TargetDisk:      *targetDiskFlag,
			Username:        *usernameFlag,
			PasswordFile:    *passwordFileFlag,
			Yes:             *yesFlag,
			AllowUnverified: *allowUnverifiedFlag,
		}
		if err := runHeadless(cfg); err != nil {
			fmt.Fprintf(os.Stderr, "headless provisioning failed: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if *isoFlag != "" {
		CustomISOPath = *isoFlag
	}

	// Initialize TUI with Alternate Screen buffer and Mouse Cell Motion support
	p := tea.NewProgram(
		ui.NewModel(CustomISOPath),
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)

	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Fatal error executing Orchestrator Hub: %v\n", err)
		os.Exit(1)
	}
}
