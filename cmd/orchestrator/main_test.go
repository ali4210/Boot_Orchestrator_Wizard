package main

import (
	"strings"
	"testing"
)

// TestCheckDryRun_RefusesClosed is the regression test for Step 6's
// redesign: --dry-run must fail closed (refuse to start with a clear
// message) rather than silently run a partial, misleading simulation,
// since most of the engine's exec.Command/filesystem-write call sites are
// not routed through any interception layer yet.
func TestCheckDryRun_RefusesClosed(t *testing.T) {
	refuse, message := checkDryRun(true)
	if !refuse {
		t.Fatalf("expected --dry-run to refuse to start")
	}
	if !strings.Contains(message, "refusing to start") {
		t.Fatalf("expected a clear refusal message, got: %s", message)
	}
	if !strings.Contains(message, "exec.Command") || !strings.Contains(message, "engine.Runner") {
		t.Fatalf("expected the message to explain why (uncovered call sites), got: %s", message)
	}
}

func TestCheckDryRun_NoOpWhenNotRequested(t *testing.T) {
	if refuse, message := checkDryRun(false); refuse || message != "" {
		t.Fatalf("expected no refusal when --dry-run was not passed, got refuse=%v message=%q", refuse, message)
	}
}
