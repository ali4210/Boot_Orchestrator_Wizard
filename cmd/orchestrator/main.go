package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"boot-orchestrator/engine"
	"boot-orchestrator/preflight"
	"boot-orchestrator/ui"
)

// CustomISOPath holds any direct image path provided via the command line
var CustomISOPath string

// dryRunBanner is printed to stderr once --dry-run actually enables
// simulation, so the operator can see at a glance that this run is
// intercepted rather than silently trusting the flag did something.
//
// Phase 5 (items 31-34) built real interception infrastructure
// (engine.Runner, engine.FS, engine.SetDryRun) but only migrated one
// proof-of-concept batch onto it, so --dry-run used to refuse to start
// rather than run a partial, misleading simulation — RunProvision's call
// sites interleaved covered and uncovered operations inside the same
// functions, so flipping the flag on would have let some real disk writes
// through while looking like protection. Item 45 closed that gap: every
// exec.Command/filesystem-write call site this tool can reach (excluding
// engine/rootfs.go, confirmed dead code — see docs/ROADMAP.md) is now
// routed through engine.Runner, engine.FS, or the newer execCommand var,
// all three switched together by engine.SetDryRun. --dry-run now runs the
// real flow with that switch on, instead of refusing.
//
// Two known, honest limitations remain, called out here rather than
// hidden:
//
//  1. A command whose real OUTPUT a later step depends on (a UUID from
//     blkid, a filesystem type, ...) reports empty output in dry-run,
//     since the disk state it would report on was never actually
//     created. A step whose own success check relies on that real output
//     (for example assertDefaultCredentials verifying an account was
//     actually created inside a chroot — see engine/dryrun_test.go) will
//     correctly report failure in dry-run rather than fabricate a result
//     it has no way to know.
//  2. The payload DOWNLOAD itself (engine/streamer.go's real net/http GET
//     plus os.Create/io.Copy to a local temp/cache file) is deliberately
//     NOT intercepted. It never touches the target disk/bootloader/NVRAM
//     — the actual safety concern 2.4 was about — and skipping it would
//     make the rest of a dry run less realistic (no real checksum to
//     verify, no real file size to compute free-space headroom against).
//     A dry run therefore still uses real bandwidth and real local
//     scratch disk space for this one step; everything after it that
//     would touch the TARGET disk is intercepted.
const dryRunBanner = `--dry-run: simulation mode enabled (engine.SetDryRun). Every exec.Command
and filesystem-write call site that could touch the target disk,
partition table, bootloader, or NVRAM is intercepted — each would-be
mutating operation is logged to stderr, prefixed "[DRY-RUN]", instead of
actually running.

Two things this does NOT change, deliberately: the payload download
itself still happens for real (real network bandwidth, real local
scratch/temp disk space — see docs/dryrun-inventory.md for why), and a
step whose own success check depends on a command's real output (a UUID,
a filesystem type, ...) may correctly report failure in dry-run, since
that output was never really produced.
`

// applyDryRunFlag enables engine.SetDryRun when --dry-run was passed and
// returns the banner to print; the caller then keeps running the real
// flow (headless or TUI) exactly as it would without the flag, with every
// reachable mutation now intercepted. Kept separate from main() so this
// is unit-testable without a subprocess.
func applyDryRunFlag(requested bool) (message string) {
	if !requested {
		return ""
	}
	_ = engine.SetDryRun(true)
	return dryRunBanner
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

// maybePrintBuildCommit checks for --print-build-commit as a bare
// os.Args scan, before flag.Parse() itself, so a launcher script can
// query it with zero risk of colliding with, or being blocked by, any
// other flag's parsing rules — this must stay usable even if every other
// flag on the command line is garbage. Prints BuildCommit alone (no
// banner, no other output) and reports true so the caller exits
// immediately: autorun.sh/.ps1 capture this verbatim and compare it
// against `git rev-parse HEAD` to decide whether the binary is stale
// (see version.go).
func maybePrintBuildCommit(args []string, w io.Writer) bool {
	for _, arg := range args {
		if arg == "--print-build-commit" {
			fmt.Fprintln(w, BuildCommit)
			return true
		}
	}
	return false
}

func main() {
	if maybePrintBuildCommit(os.Args[1:], os.Stdout) {
		return
	}

	isoFlag := flag.String("iso", "", "Direct local path to an ISO or raw disk image")
	dryRunFlag := flag.Bool("dry-run", false, "Run the real flow with every reachable exec.Command/filesystem-write call intercepted and logged instead of actually executed (see docs/dryrun-inventory.md for the one known limitation)")

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

	if message := applyDryRunFlag(*dryRunFlag); message != "" {
		fmt.Fprint(os.Stderr, message)
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
	// (headless or the TUI) gets going. --revert already returned above
	// by this point if requested, and doesn't need this check anyway
	// (revert doesn't download/write a new OS). --dry-run deliberately
	// still runs this gate: it's a read-only disk-headroom/catalog-load
	// check, not a mutation, and a dry run should surface a real
	// pre-flight problem the same way a real run would.
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

	ui.SetVersion(Version)

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
