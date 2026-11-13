package main

import (
	"flag"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"boot-orchestrator/ui"
)

// CustomISOPath holds any direct image path provided via the command line
var CustomISOPath string

// dryRunRefusalMessage is printed by --dry-run instead of a simulation.
//
// There is no real dry-run/simulation mode implemented: a repo-wide audit
// (2026) found roughly 350 direct exec.Command calls and 140 direct
// os.WriteFile/Symlink/Remove/RemoveAll/MkdirAll calls across ~28 files in
// engine/ and ui/ (partition, bootloader, USB, backup/revert, WIM/BCD,
// firmware/UEFI logic) that bypass the one Runner interception point
// (engine.Runner) added so far. Intercepting all of them so a dry run could
// safely log every would-be command/write instead of executing it is a
// large, repo-wide refactor — not something to half-do. Per the audit's own
// finding: a dry-run that only covers some of several hundred write paths
// is actively misleading (it would create false confidence while real
// commands still ran through the uncovered ones), so --dry-run refuses to
// start at all until that refactor is done, rather than simulate partially.
const dryRunRefusalMessage = `--dry-run requested, but refusing to start: no real dry-run mode exists yet.

This binary has ~350 direct exec.Command call sites and ~140 direct
filesystem-write call sites (os.WriteFile/Symlink/Remove/RemoveAll/MkdirAll)
across engine/ and ui/ that are NOT routed through any interception layer.
Only a small subset (engine.Runner, in engine/partition.go and
engine/bootloader.go) is currently mockable/interceptable.

Running with --dry-run today would NOT be safe: most partition, bootloader,
USB, backup/revert, and WIM/BCD operations would execute for real regardless
of this flag. Rather than simulate only part of what the tool does (which
would be misleading, not safer), this flag currently just refuses to start.

To get a real dry-run, the remaining call sites need to be routed through
engine.Runner (or an equivalent) first. Until then, test this tool only in a
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

func main() {
	isoFlag := flag.String("iso", "", "Direct local path to an ISO or raw disk image")
	dryRunFlag := flag.Bool("dry-run", false, "Refuse to start (see --help); full dry-run simulation is not implemented yet")
	flag.Parse()

	if refuse, message := checkDryRun(*dryRunFlag); refuse {
		fmt.Fprint(os.Stderr, message)
		os.Exit(1)
	}

	if *isoFlag != "" {
		CustomISOPath = *isoFlag
	}

	// Initialize TUI with Alternate Screen buffer and Mouse Cell Motion support
	p := tea.NewProgram(
		ui.NewModel(),
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)

	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Fatal error executing Orchestrator Hub: %v\n", err)
		os.Exit(1)
	}
}
