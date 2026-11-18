package main

import (
	"strings"
	"testing"
)

// TestCheckDryRun_RefusesClosed is the regression test for --dry-run's
// fail-closed design: it must refuse to start with a clear message rather
// than silently run a partial, misleading simulation. Phase 5 (items
// 31-34) built real interception infrastructure (engine.Runner,
// engine.FS, engine.SetDryRun) and migrated one batch onto it, but
// RunProvision's real call sites still interleave covered and uncovered
// operations within the same functions — so the refusal, and this test,
// remain in place; only the message's explanation of *why* was updated to
// reflect that real (if partial) progress instead of claiming nothing had
// been built at all.
func TestCheckDryRun_RefusesClosed(t *testing.T) {
	refuse, message := checkDryRun(true)
	if !refuse {
		t.Fatalf("expected --dry-run to refuse to start")
	}
	if !strings.Contains(message, "refusing to start") {
		t.Fatalf("expected a clear refusal message, got: %s", message)
	}
	if !strings.Contains(message, "engine.Runner") || !strings.Contains(message, "engine.FS") || !strings.Contains(message, "SetDryRun") {
		t.Fatalf("expected the message to reference the real interception infrastructure that now exists, got: %s", message)
	}
	if !strings.Contains(message, "call sites") {
		t.Fatalf("expected the message to explain why (uncovered call sites), got: %s", message)
	}
}

func TestCheckDryRun_NoOpWhenNotRequested(t *testing.T) {
	if refuse, message := checkDryRun(false); refuse || message != "" {
		t.Fatalf("expected no refusal when --dry-run was not passed, got refuse=%v message=%q", refuse, message)
	}
}
