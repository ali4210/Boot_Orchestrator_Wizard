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

// sourceAutorunAndRun sources the real autorun.sh from the repo root,
// redirecting SCRIPT_DIR to scriptDir via the explicit opt-in pair
// ORCH_AUTORUN_TESTING=1 / ORCH_TEST_SCRIPT_DIR (follow-up F10 — autorun.sh
// never honors a plain ambient "SCRIPT_DIR", only this narrowly-named
// pair), then runs body (typically a single function call such as
// "check_root_owned_files"). The `if [[ "${BASH_SOURCE[0]}" == "${0}" ]]`
// guard at the bottom of autorun.sh prevents its real build/launch
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
	cmd.Env = append(envWithoutScriptDir(),
		"ORCH_AUTORUN_TESTING=1",
		"ORCH_TEST_SCRIPT_DIR="+scriptDir,
	)
	out, runErr := cmd.CombinedOutput()
	return string(out), runErr
}

// envWithoutScriptDir returns the current process's environment with any
// SCRIPT_DIR variable stripped, so tests never accidentally rely on (or
// mask a bug via) an ambient SCRIPT_DIR the test process itself might
// happen to have set.
func envWithoutScriptDir() []string {
	var out []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "SCRIPT_DIR=") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

// TestCheckRootOwnedFiles_NoOffenders is the baseline: an ordinary scratch
// directory with a user-owned bin/ produces no warning or block.
func TestCheckRootOwnedFiles_NoOffenders(t *testing.T) {
	scriptDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(scriptDir, "bin"), 0755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	if err := os.WriteFile(filepath.Join(scriptDir, "bin", "boot-orchestrator"), []byte("fake"), 0755); err != nil {
		t.Fatalf("writing fixture binary: %v", err)
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
	if os.Geteuid() == 0 {
		t.Skip("check_root_owned_files intentionally returns 0 immediately when $EUID -eq 0 (autorun.sh: root can read/write/purge anything regardless of file ownership, so there is nothing to block) — this test's premise (a root-owned file blocks a non-root invocation) only applies when the test process itself is not root; skipping under `sudo go test`")
	}
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
		// hasPasswordlessSudo (sudo -n true) succeeding doesn't guarantee
		// -n chown specifically is also allowed — observed for real on
		// GitHub's macos-latest runner, where sudo -n true passes but
		// sudo -n chown root:root <fixture> fails outright (exit status
		// 1, no stderr captured). Skip rather than fail: this is an
		// environment-permission gap in the test's own setup, not a
		// failure of check_root_owned_files (the function under test is
		// never even reached). Linux is unaffected — this sudo call has
		// always succeeded there, so this branch stays effectively dead
		// on Linux and the real chown-blocks-it assertion below still
		// runs for real.
		t.Skipf("could not chown fixture to root (passwordless sudo allowed `sudo -n true` but not `sudo -n chown`): %v", err)
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
// TestAmbientSCRIPT_DIR_DoesNotRedirectScript is the regression test for
// follow-up F10: autorun.sh must never honor a plain "SCRIPT_DIR" that
// merely happens to be set in the environment — otherwise an accidental
// collision with another tool (or a hostile environment) could redirect
// --hard's purge_cache (rm -rf "${SCRIPT_DIR:?}/bin", `git gc
// --aggressive`, ...) at an arbitrary directory. This does not actually
// invoke purge_cache/--hard for real (that would be destructive against
// whatever directory it resolves to); it proves the underlying mechanism
// those destructive operations depend on — SCRIPT_DIR's own value — is
// computed from BASH_SOURCE and ignores the ambient variable, which is
// sufficient since purge_cache has no separate directory resolution of its
// own.
func TestAmbientSCRIPT_DIR_DoesNotRedirectScript(t *testing.T) {
	repoRoot, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("resolving repo root: %v", err)
	}
	autorunPath := filepath.Join(repoRoot, "autorun.sh")

	const evilDir = "/tmp/orch-test-evil-script-dir-should-never-be-used"
	script := "set -e\nsource " + shellQuote(autorunPath) + "\necho \"RESOLVED:$SCRIPT_DIR\"\n"
	cmd := exec.Command("bash", "-c", script)
	// Deliberately set a plain, ambient SCRIPT_DIR, WITHOUT the
	// ORCH_AUTORUN_TESTING/ORCH_TEST_SCRIPT_DIR opt-in pair — this is
	// exactly the case that must be ignored.
	cmd.Env = append(envWithoutScriptDir(), "SCRIPT_DIR="+evilDir)

	out, runErr := cmd.CombinedOutput()
	if runErr != nil {
		t.Fatalf("sourcing autorun.sh: %v\noutput:\n%s", runErr, out)
	}

	if strings.Contains(string(out), "RESOLVED:"+evilDir) {
		t.Fatalf("autorun.sh honored an ambient SCRIPT_DIR (%s) with no opt-in — --hard's purge would have operated on it, output:\n%s", evilDir, out)
	}
	if !strings.Contains(string(out), "RESOLVED:"+repoRoot) {
		t.Fatalf("expected SCRIPT_DIR to resolve to the real script location (%s), got:\n%s", repoRoot, out)
	}
}

// TestCheckRootOwnedFiles_RuntimeDirOwnedByRootBlocks is the regression
// test for follow-up F11: .runtime/ is populated by the root-run binary
// itself (not the unprivileged build step), and --hard's purge_cache does
// an unconditional `rm -rf` on it — exactly like bin/, a root-owned
// .runtime/ must hard-block with a clear message rather than letting that
// rm -rf fail with a raw permission error.
func TestCheckRootOwnedFiles_RuntimeDirOwnedByRootBlocks(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("check_root_owned_files intentionally returns 0 immediately when $EUID -eq 0 (autorun.sh: root can read/write/purge anything regardless of file ownership, so there is nothing to block) — this test's premise (a root-owned file blocks a non-root invocation) only applies when the test process itself is not root; skipping under `sudo go test`")
	}
	if !hasPasswordlessSudo(t) {
		t.Skip("passwordless sudo not available in this environment; skipping the real-root-ownership case")
	}

	scriptDir := t.TempDir()
	runtimeDir := filepath.Join(scriptDir, ".runtime")
	if err := os.MkdirAll(runtimeDir, 0755); err != nil {
		t.Fatalf("mkdir .runtime: %v", err)
	}
	statePath := filepath.Join(runtimeDir, "some_state_file")
	if err := os.WriteFile(statePath, []byte("fake"), 0644); err != nil {
		t.Fatalf("writing fixture state file: %v", err)
	}

	if err := exec.Command("sudo", "-n", "chown", "root:root", statePath).Run(); err != nil {
		// Same environment-permission gap as TestCheckRootOwnedFiles_BinOwnedByRootBlocks
		// above (observed for real on GitHub's macos-latest runner: sudo -n
		// true succeeds but sudo -n chown does not) — skip rather than
		// fail, since check_root_owned_files itself is never reached.
		// Linux is unaffected.
		t.Skipf("could not chown fixture to root (passwordless sudo allowed `sudo -n true` but not `sudo -n chown`): %v", err)
	}
	t.Cleanup(func() {
		_ = exec.Command("sudo", "-n", "chown", os.Getenv("USER")+":"+os.Getenv("USER"), statePath).Run()
	})

	out, err := sourceAutorunAndRun(t, scriptDir, "check_root_owned_files")
	if err == nil {
		t.Fatalf("expected check_root_owned_files to exit non-zero for a root-owned .runtime/ file, output:\n%s", out)
	}
	if !strings.Contains(out, "BLOCKED") {
		t.Fatalf("expected a [BLOCKED] message, got:\n%s", out)
	}
	if !strings.Contains(out, "chown") || !strings.Contains(out, ".runtime") {
		t.Fatalf("expected the chown command to mention .runtime/, got:\n%s", out)
	}
}

// Follow-up F12 removed the journal/installed-os-record ownership checks
// from check_root_owned_files entirely (both now live outside the repo by
// default, in a directory the invoking user can't write to at all), so the
// tests that used to cover "root-owned journal/record only warns" were
// removed along with that code — the behavior they tested no longer
// exists. See PROGRESS.md's F12 entry for the full rationale.
