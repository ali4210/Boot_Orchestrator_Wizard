package engine

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sync"
)

// Phase 5 (items 32/34/45): dry-run interception. See
// docs/dryrun-inventory.md for the real scope this covers. As of item 45,
// this covers every exec.Command/os.WriteFile/os.MkdirAll/os.Remove/
// os.RemoveAll/os.Symlink/os.Rename call site in the engine and ui
// packages reachable from a real user-facing entry point (provision,
// revert, backup, autonomous USB staging) — engine/rootfs.go is the one
// deliberate exception (confirmed dead code, zero callers anywhere in the
// repo; see docs/ROADMAP.md) — via three mechanisms:
//
//  1. The ~73 exec.Command call sites already routed through the
//     injectable Runner interface (engine/runner.go) — covered for free,
//     no call-site changes, since SetDryRun swaps the same cmdRunner var
//     tests already use.
//  2. Filesystem writes routed through the FS interface (engine/fs.go) —
//     every remaining os.WriteFile/MkdirAll/Remove/RemoveAll/Symlink/
//     Rename call site was mechanically migrated onto fsRunner.
//  3. Every remaining raw exec.Command call site — including the ones
//     that set cmd.Stdin/Dir/Env after construction (credentials.go,
//     partition.go, engine_linux.go, backup.go), which Runner's two-method
//     interface cannot model at all, per its own doc comment — was
//     mechanically migrated onto the execCommand var below instead. This
//     is deliberately a *second*, simpler interception point rather than
//     forcing every site through Runner's Run/RunStdout split: execCommand
//     has the exact same signature as exec.Command itself (returns a real
//     *exec.Cmd), so the migration was a pure rename with no restructuring
//     — the caller can still set .Stdin/.Dir/.Env on the result exactly as
//     before, dry-run or not.
//
// cmd/orchestrator/main.go's --dry-run flag now runs the real flow under
// SetDryRun(true) instead of refusing to start — see that file's
// applyDryRunFlag/dryRunBanner and docs/ROADMAP.md's Phase 5 section for
// what's still a known, inherent limitation even with this in place (query
// commands return empty output in dry-run, since the disk state they'd
// report on was never actually created).

var dryRunMu sync.Mutex
var dryRunEnabled bool

// execCommand constructs an *exec.Cmd exactly like exec.Command — a
// package-level var (not a direct call) so SetDryRun can swap it. See the
// file-level doc comment above for why this exists alongside (not instead
// of) the older Runner interface.
var execCommand = exec.Command

// SetDryRun switches every Runner-, FS-, and execCommand-routed operation
// between the real implementation and one that logs what it would have
// done and returns synthetic success without touching anything. Returns a
// restore function so callers (and tests) can cleanly revert, mirroring
// the pattern safety.SetJournalCandidateDirsForTesting already uses for
// the same reason: these are shared, process-wide vars.
func SetDryRun(enabled bool) (restore func()) {
	dryRunMu.Lock()
	defer dryRunMu.Unlock()

	origRunner, origFS, origExec, origEnabled := cmdRunner, fsRunner, execCommand, dryRunEnabled
	dryRunEnabled = enabled
	if enabled {
		cmdRunner = DryRunRunner{}
		fsRunner = DryRunFS{}
		execCommand = dryRunExecCommand
	} else {
		cmdRunner = DefaultRunner
		fsRunner = DefaultFS{}
		execCommand = exec.Command
	}
	return func() {
		dryRunMu.Lock()
		defer dryRunMu.Unlock()
		cmdRunner, fsRunner, execCommand, dryRunEnabled = origRunner, origFS, origExec, origEnabled
	}
}

// dryRunExecCommand logs the command that would have run and returns a
// real, harmless, already-no-op *exec.Cmd instead — a genuine *exec.Cmd is
// required (not a stub) because callers are free to set .Stdin/.Dir/.Env
// on the result and then call .Run()/.Output()/.CombinedOutput(), exactly
// as they would on a real command. "true" (POSIX, present on both Linux
// and macOS) genuinely executes, touches nothing, and always exits 0;
// Windows has no equivalent single binary, so cmd.exe's own null command
// is used there instead — untested on real Windows hardware, consistent
// with this project's other Windows caveats (see docs/ARCH_SUPPORT.md).
func dryRunExecCommand(name string, args ...string) *exec.Cmd {
	logDryRun("exec", name, args...)
	if runtime.GOOS == "windows" {
		return exec.Command("cmd", "/c", "exit", "0")
	}
	return exec.Command("true")
}

// IsDryRun reports whether dry-run mode is currently active.
func IsDryRun() bool {
	dryRunMu.Lock()
	defer dryRunMu.Unlock()
	return dryRunEnabled
}

// FSRunner returns the currently active FS implementation (real or
// dry-run). cmdRunner/fsRunner/execCommand are deliberately package-private
// — SetDryRun is the only sanctioned way to change them — but the ui
// package has its own filesystem-write call sites that must also honor
// dry-run, so this (and ExecCommand below) is the narrow, read-only way
// for another package to reach the current value. Always call it fresh
// right before use rather than caching the result: SetDryRun can swap it
// at any time, and caching would silently go stale across that swap.
func FSRunner() FS { return fsRunner }

// ExecCommand builds a command through the currently active
// exec.Command-equivalent (real or dry-run), for the same reason
// FSRunner exists — see its doc comment.
func ExecCommand(name string, args ...string) *exec.Cmd { return execCommand(name, args...) }

// diskReadRunner is the Runner that genuinely read-only disk-geometry
// queries — blockDeviceSectorSize, blockDeviceSizeBytes, readDiskRegion
// (engine/partition.go) — should use instead of cmdRunner directly.
// These three never write anything regardless of SetDryRun, by
// construction (not by inspecting arguments): blockdev --getss/
// --getsize64 and a `dd if=... status=none` read. That matches
// docs/dryrun-inventory.md's own "read-only queries... already safe to
// run for real... do NOT need interception" principle — a principle the
// blanket cmdRunner/execCommand swap in SetDryRun doesn't actually honor
// on its own, since it has no way to tell a read-only invocation from a
// mutating one by command name alone (parted and dd are each used both
// ways elsewhere in this codebase, which is why this isn't a general
// allowlist of command names, only these three specific, unambiguous
// call sites).
//
// Found and added while building item 50 (audit follow-up G3)'s real
// dry-run proof: BackupPartitionTable (called unconditionally at the
// start of every provision/revert, dry-run or not) depends on these
// three for the disk's real sector size/byte count/GPT bytes — under the
// blanket swap, DryRunRunner returned empty output for all of them,
// which failed to parse, which made BackupPartitionTable error out, which
// made the whole operation abort at "partition-shrink" on literally every
// dry run, before any of the disk-mutating code this flag exists to
// protect against was ever reached at all.
//
// If cmdRunner has been replaced by a test's own mock (as
// TestReadDiskRegion_UsesStdoutOnly already does), that mock is still
// honored — only the dry-run-specific stand-in is bypassed.
func diskReadRunner() Runner {
	if _, ok := cmdRunner.(DryRunRunner); ok {
		return DefaultRunner
	}
	return cmdRunner
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
