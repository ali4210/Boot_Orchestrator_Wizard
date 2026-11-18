package main

import (
	"strings"
	"testing"

	"boot-orchestrator/preflight"
)

// TestRunPreflightGate_NoBlockingIssues proves the common case: nothing
// blocking means no message and no refusal, regardless of --skip-preflight.
func TestRunPreflightGate_NoBlockingIssues(t *testing.T) {
	report := preflight.Report{Warnings: []string{"some informational warning"}}

	refuse, message := runPreflightGate(report, false)
	if refuse {
		t.Errorf("expected no refusal when there are no blocking issues")
	}
	if message != "" {
		t.Errorf("expected no message when there are no blocking issues, got %q", message)
	}
}

// TestRunPreflightGate_BlockingIssueRefusesByDefault is the actual gate
// audit 2.15 asks for: a blocking issue (e.g. catalog failed to load, or
// not enough disk headroom) must refuse to continue by default.
func TestRunPreflightGate_BlockingIssueRefusesByDefault(t *testing.T) {
	report := preflight.Report{Blocking: []string{"embedded OS catalog loaded but contains zero entries"}}

	refuse, message := runPreflightGate(report, false)
	if !refuse {
		t.Fatalf("expected a blocking issue to refuse to continue by default")
	}
	if !strings.Contains(message, "zero entries") {
		t.Errorf("expected the blocking reason to appear in the message, got %q", message)
	}
	if !strings.Contains(message, "--skip-preflight") {
		t.Errorf("expected the message to mention the override flag, got %q", message)
	}
}

// TestRunPreflightGate_SkipOverridesRefusalButStillWarns proves
// --skip-preflight actually works (continues anyway) while still telling
// the user what it overrode, rather than silently hiding the issue.
func TestRunPreflightGate_SkipOverridesRefusalButStillWarns(t *testing.T) {
	report := preflight.Report{Blocking: []string{"only 100 bytes free"}}

	refuse, message := runPreflightGate(report, true)
	if refuse {
		t.Fatalf("expected --skip-preflight to override the refusal")
	}
	if !strings.Contains(message, "only 100 bytes free") {
		t.Errorf("expected the original blocking reason to still be shown, got %q", message)
	}
	if !strings.Contains(message, "Continuing anyway") {
		t.Errorf("expected the message to say it's continuing anyway, got %q", message)
	}
}
