package main

import (
	"strings"
	"testing"

	"boot-orchestrator/engine"
)

// TestMaybePrintBuildCommit is the regression test for autorun.sh's
// staleness check (item A, banner follow-up): autorun.sh compares
// `git rev-parse HEAD` against this binary's own embedded BuildCommit to
// decide whether to rebuild, and needs a way to query that value from
// the already-built binary without launching the whole TUI/preflight
// gate. --print-build-commit must work regardless of flag.Parse (it is
// checked before it), and must print BuildCommit verbatim with nothing
// else mixed in, since autorun.sh captures it directly into a shell
// variable.
func TestMaybePrintBuildCommit(t *testing.T) {
	origCommit := BuildCommit
	t.Cleanup(func() { BuildCommit = origCommit })
	BuildCommit = "abc123deadbeef"

	var buf strings.Builder
	if handled := maybePrintBuildCommit([]string{"--print-build-commit"}, &buf); !handled {
		t.Fatalf("expected maybePrintBuildCommit to report handled=true")
	}
	if got := strings.TrimSpace(buf.String()); got != "abc123deadbeef" {
		t.Fatalf("expected exactly the build commit printed, got %q", got)
	}
}

func TestMaybePrintBuildCommit_IgnoresOtherArgs(t *testing.T) {
	var buf strings.Builder
	if handled := maybePrintBuildCommit([]string{"--headless", "--yes", "--os-id=foo"}, &buf); handled {
		t.Fatalf("expected maybePrintBuildCommit to report handled=false for unrelated args")
	}
	if buf.Len() != 0 {
		t.Fatalf("expected nothing written when the flag wasn't present, got %q", buf.String())
	}
}

func TestMaybePrintBuildCommit_WorksAlongsideOtherFlags(t *testing.T) {
	var buf strings.Builder
	if handled := maybePrintBuildCommit([]string{"--headless", "--print-build-commit", "--yes"}, &buf); !handled {
		t.Fatalf("expected maybePrintBuildCommit to find the flag regardless of position among other args")
	}
}

// TestApplyDryRunFlag_EnablesSimulationAndReturnsBanner is the regression
// test for item 45's closure of audit 2.4: --dry-run used to refuse to
// start outright (Phase 5 left RunProvision's call sites only partially
// migrated onto engine.Runner/engine.FS). Now that every reachable
// exec.Command/filesystem-write call site is intercepted (see
// docs/dryrun-inventory.md), --dry-run instead enables engine.SetDryRun
// and lets the real flow run — this proves the flag actually flips that
// switch and returns a banner explaining what's happening, rather than
// silently doing nothing or silently refusing.
func TestApplyDryRunFlag_EnablesSimulationAndReturnsBanner(t *testing.T) {
	defer engine.SetDryRun(false)

	message := applyDryRunFlag(true)
	if !engine.IsDryRun() {
		t.Fatalf("expected --dry-run to enable engine.SetDryRun")
	}
	if !strings.Contains(message, "DRY-RUN") {
		t.Fatalf("expected the banner to mention the [DRY-RUN] log prefix, got: %s", message)
	}
	if !strings.Contains(message, "intercepted") {
		t.Fatalf("expected the banner to explain interception, got: %s", message)
	}
}

func TestApplyDryRunFlag_NoOpWhenNotRequested(t *testing.T) {
	defer engine.SetDryRun(false)

	if engine.IsDryRun() {
		t.Fatalf("test setup bug: dry-run should be off before this assertion")
	}
	if message := applyDryRunFlag(false); message != "" {
		t.Fatalf("expected no banner when --dry-run was not passed, got %q", message)
	}
	if engine.IsDryRun() {
		t.Fatalf("expected dry-run to remain off when not requested")
	}
}
