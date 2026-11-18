package engine

import (
	"fmt"
	"os"
	"sync"
)

// Phase 5 (items 32/34): dry-run interception. See docs/dryrun-inventory.md
// for the real scope this covers (and, just as importantly, doesn't yet
// cover) — this file is deliberately NOT a claim that every destructive
// operation in this engine is simulatable. It covers:
//
//  1. Every one of the ~73 exec.Command call sites already routed through
//     the injectable Runner interface (engine/runner.go) — for free, with
//     no call-site changes, since SetDryRun swaps the same cmdRunner var
//     tests already use.
//  2. The filesystem-write call sites explicitly migrated onto the new FS
//     interface below (currently: assertDefaultCredentials's sudoers.d
//     write — see that function in engine/bootloader.go). This is a
//     proof-of-concept batch, not an exhaustive migration; the bulk of
//     this engine's ~149 filesystem-write call sites still call os.*
//     directly and are NOT covered.
//
// cmd/orchestrator/main.go's --dry-run flag refuses to start rather than
// claim more than this actually simulates — see dryRunRefusalMessage
// there, and docs/ROADMAP.md's Phase 5 section for what's left before a
// real, honest --dry-run can be offered to users.

var dryRunMu sync.Mutex
var dryRunEnabled bool

// SetDryRun switches every Runner- and FS-routed operation between the
// real implementation and one that logs what it would have done and
// returns synthetic success without touching anything. Returns a restore
// function so callers (and tests) can cleanly revert, mirroring the
// pattern safety.SetJournalCandidateDirsForTesting already uses for the
// same reason: these are shared, process-wide vars.
func SetDryRun(enabled bool) (restore func()) {
	dryRunMu.Lock()
	defer dryRunMu.Unlock()

	origRunner, origFS, origEnabled := cmdRunner, fsRunner, dryRunEnabled
	dryRunEnabled = enabled
	if enabled {
		cmdRunner = DryRunRunner{}
		fsRunner = DryRunFS{}
	} else {
		cmdRunner = DefaultRunner
		fsRunner = DefaultFS{}
	}
	return func() {
		dryRunMu.Lock()
		defer dryRunMu.Unlock()
		cmdRunner, fsRunner, dryRunEnabled = origRunner, origFS, origEnabled
	}
}

// IsDryRun reports whether dry-run mode is currently active.
func IsDryRun() bool {
	dryRunMu.Lock()
	defer dryRunMu.Unlock()
	return dryRunEnabled
}

// DryRunRunner is a Runner that never actually executes anything: it logs
// the command it would have run (to stderr, prefixed so it's
// unmistakable in output) and returns an empty, successful result.
// Callers that parse RunStdout's output for a real value (a UUID, a
// filesystem type, a byte count) will see an empty string in dry-run mode
// — this is a known, inherent limitation of simulating a query whose real
// answer depends on a disk state that was never actually created; see
// docs/dryrun-inventory.md's notes on why read-only queries don't need
// (and currently don't get) interception at all, versus mutating commands
// which do.
type DryRunRunner struct{}

func (DryRunRunner) Run(name string, args ...string) ([]byte, error) {
	logDryRun("exec", name, args...)
	return nil, nil
}

func (DryRunRunner) RunStdout(name string, args ...string) ([]byte, error) {
	logDryRun("exec", name, args...)
	return nil, nil
}

// maxLoggedArgLen bounds how much of any single argument logDryRun prints.
// Found necessary, not theoretical: assertDefaultCredentials's chroot
// call passes a generated shell script as one argument, which embeds the
// account's password HASH — printing it in full to stderr on every
// dry-run would needlessly duplicate that hash into logs/terminal
// scrollback. Truncating every argument (not just ones that look
// sensitive — this helper has no way to know which ones are) keeps the
// log useful for seeing WHAT would run without ever printing a secret in
// full.
const maxLoggedArgLen = 60

func logDryRun(kind, name string, args ...string) {
	shown := make([]string, len(args))
	for i, a := range args {
		if len(a) > maxLoggedArgLen {
			shown[i] = a[:maxLoggedArgLen] + fmt.Sprintf("...[truncated, %d bytes total]", len(a))
		} else {
			shown[i] = a
		}
	}
	fmt.Fprintf(os.Stderr, "[DRY-RUN] would %s: %s %v\n", kind, name, shown)
}
