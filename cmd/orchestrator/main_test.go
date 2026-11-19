package main

import (
	"strings"
	"testing"

	"boot-orchestrator/engine"
)

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
