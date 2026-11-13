// Package rootscripts tests the repo-root shell launchers (autorun.sh)
// directly, by sourcing the real file rather than a pasted copy of its
// logic, so these tests catch drift between the script and whatever a
// hand-copied test fixture would assume.
package rootscripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// hasPasswordlessSudo reports whether `sudo -n true` succeeds, i.e. sudo
// can run without prompting for a password. Tests that need to create a
// root-owned fixture file skip themselves (rather than hang on a password
// prompt) when this is false, so this suite stays safe to run anywhere.
func hasPasswordlessSudo(t *testing.T) bool {
	t.Helper()
	return exec.Command("sudo", "-n", "true").Run() == nil
}

// sourceAutorunAndRun sources the real autorun.sh from the repo root with
// SCRIPT_DIR pre-set to scriptDir, then runs body (typically a single
// function call such as "check_root_owned_files"). SCRIPT_DIR's
// `: "${SCRIPT_DIR:=...}"` guard in autorun.sh respects this pre-set value
// instead of recomputing it, and the `if [[ "${BASH_SOURCE[0]}" == "${0}"
// ]]` guard at the bottom of autorun.sh prevents its real build/launch
// sequence (main) from running just because it was sourced.
func sourceAutorunAndRun(t *testing.T, scriptDir, body string) (string, error) {
	t.Helper()
	repoRoot, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("resolving repo root: %v", err)
	}
	autorunPath := filepath.Join(repoRoot, "autorun.sh")
	if _, statErr := os.Stat(autorunPath); statErr != nil {
		t.Fatalf("autorun.sh not found at %s: %v", autorunPath, statErr)
	}

	script := "set -e\nsource " + shellQuote(autorunPath) + "\n" + body + "\n"
	cmd := exec.Command("bash", "-c", script)
	cmd.Env = append(os.Environ(), "SCRIPT_DIR="+scriptDir)
	out, runErr := cmd.CombinedOutput()
	return string(out), runErr
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

// TestCheckRootOwnedFiles_NoOffenders is the baseline: an ordinary scratch
// directory with user-owned bin/ and journal produces no warning or block.
func TestCheckRootOwnedFiles_NoOffenders(t *testing.T) {
	scriptDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(scriptDir, "bin"), 0755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	if err := os.WriteFile(filepath.Join(scriptDir, "bin", "boot-orchestrator"), []byte("fake"), 0755); err != nil {
		t.Fatalf("writing fixture binary: %v", err)
	}
	if err := os.WriteFile(filepath.Join(scriptDir, "orchestrator_journal.json"), []byte("{}"), 0644); err != nil {
		t.Fatalf("writing fixture journal: %v", err)
	}

	out, err := sourceAutorunAndRun(t, scriptDir, "check_root_owned_files")
	if err != nil {
		t.Fatalf("check_root_owned_files: %v\noutput:\n%s", err, out)
	}
	if strings.Contains(out, "BLOCKED") || strings.Contains(out, "WARNING") {
		t.Fatalf("expected no blocked/warning output for an all-user-owned scratch dir, got:\n%s", out)
	}
}

// TestCheckRootOwnedFiles_BinOwnedByRootBlocks is the regression test for
// follow-up F6's first half: a root-owned file in bin/ must hard-block
// (non-zero exit) with a chown command naming bin/, exactly as before.
func TestCheckRootOwnedFiles_BinOwnedByRootBlocks(t *testing.T) {
	if !hasPasswordlessSudo(t) {
		t.Skip("passwordless sudo not available in this environment; skipping the real-root-ownership case")
	}

	scriptDir := t.TempDir()
	binDir := filepath.Join(scriptDir, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	binPath := filepath.Join(binDir, "boot-orchestrator")
	if err := os.WriteFile(binPath, []byte("fake"), 0755); err != nil {
		t.Fatalf("writing fixture binary: %v", err)
	}

	if err := exec.Command("sudo", "-n", "chown", "root:root", binPath).Run(); err != nil {
		t.Fatalf("chowning fixture to root: %v", err)
	}
	t.Cleanup(func() {
		_ = exec.Command("sudo", "-n", "chown", os.Getenv("USER")+":"+os.Getenv("USER"), binPath).Run()
	})

	out, err := sourceAutorunAndRun(t, scriptDir, "check_root_owned_files")
	if err == nil {
		t.Fatalf("expected check_root_owned_files to exit non-zero for a root-owned bin/ file, output:\n%s", out)
	}
	if !strings.Contains(out, "BLOCKED") {
		t.Fatalf("expected a [BLOCKED] message, got:\n%s", out)
	}
	if !strings.Contains(out, "chown") || !strings.Contains(out, "/bin") {
		t.Fatalf("expected the chown command to mention bin/, got:\n%s", out)
	}
}

// TestCheckRootOwnedFiles_JournalOwnedByRootOnlyWarns is the regression
// test for follow-up F6's second half: a root-owned journal must produce
// only a warning (exit zero, output mentions WARNING), never a hard block
// — the engine now fixes journal ownership itself after creating it, so
// this script no longer needs to treat a stale root-owned journal as fatal.
func TestCheckRootOwnedFiles_JournalOwnedByRootOnlyWarns(t *testing.T) {
	if !hasPasswordlessSudo(t) {
		t.Skip("passwordless sudo not available in this environment; skipping the real-root-ownership case")
	}

	scriptDir := t.TempDir()
	journalPath := filepath.Join(scriptDir, "orchestrator_journal.json")
	if err := os.WriteFile(journalPath, []byte("{}"), 0644); err != nil {
		t.Fatalf("writing fixture journal: %v", err)
	}

	if err := exec.Command("sudo", "-n", "chown", "root:root", journalPath).Run(); err != nil {
		t.Fatalf("chowning fixture to root: %v", err)
	}
	t.Cleanup(func() {
		_ = exec.Command("sudo", "-n", "chown", os.Getenv("USER")+":"+os.Getenv("USER"), journalPath).Run()
	})

	out, err := sourceAutorunAndRun(t, scriptDir, "check_root_owned_files")
	if err != nil {
		t.Fatalf("expected check_root_owned_files to exit zero for a root-owned journal (warning only), got err=%v output:\n%s", err, out)
	}
	if !strings.Contains(out, "WARNING") {
		t.Fatalf("expected a [WARNING] message about the journal, got:\n%s", out)
	}
	if strings.Contains(out, "BLOCKED") {
		t.Fatalf("expected no hard block for a root-owned journal alone, got:\n%s", out)
	}
}
