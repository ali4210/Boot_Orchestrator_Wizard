// Package rootscripts tests the repo-root shell launchers (autorun.sh)
// directly, by sourcing the real file rather than a pasted copy of its
// logic, so these tests catch drift between the script and whatever a
// hand-copied test fixture would assume.
//
// Audit follow-up (Windows CI): autorun.sh itself never runs on Windows —
// that platform has its own separate launchers (autorun.bat/autorun.cmd/
// autorun.ps1, see docs/ for which is current), so every test in this
// file (which all work by sourcing the real autorun.sh under a real
// POSIX shell) is unreachable there by construction, not just untested.
// Tagged !windows (not linux-only): macOS also uses autorun.sh and these
// tests already pass there for real.
//go:build !windows

package rootscripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// TestNeedsRebuild_NoBinary is the regression test for the reported bug:
// before this, autorun.sh only checked `[ ! -f "$BIN_PATH" ]` — a stale
// binary from an older commit was launched forever without ever
// rebuilding. needs_rebuild must report true whenever the binary doesn't
// exist yet at all.
func TestNeedsRebuild_NoBinary(t *testing.T) {
	scriptDir := t.TempDir()
	out, err := sourceAutorunAndRun(t, scriptDir, `needs_rebuild && echo "REBUILD:yes:$REBUILD_REASON" || echo "REBUILD:no"`)
	if err != nil {
		t.Fatalf("needs_rebuild: %v\noutput:\n%s", err, out)
	}
	if !strings.Contains(out, "REBUILD:yes:no existing binary") {
		t.Fatalf("expected needs_rebuild to report true with a reason naming the missing binary, got:\n%s", out)
	}
}

// TestNeedsRebuild_GoFileNewerThanBinary proves the first new staleness
// signal: a .go file modified after the binary was built.
func TestNeedsRebuild_GoFileNewerThanBinary(t *testing.T) {
	scriptDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(scriptDir, "bin"), 0755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	binPath := filepath.Join(scriptDir, "bin", "boot-orchestrator")
	writeFileWithTime(t, binPath, "fake-old-binary", time.Now().Add(-time.Hour))

	engineDir := filepath.Join(scriptDir, "engine")
	if err := os.MkdirAll(engineDir, 0755); err != nil {
		t.Fatalf("mkdir engine: %v", err)
	}
	writeFileWithTime(t, filepath.Join(engineDir, "example.go"), "package engine", time.Now())

	out, err := sourceAutorunAndRun(t, scriptDir, `needs_rebuild && echo "REBUILD:yes:$REBUILD_REASON" || echo "REBUILD:no"`)
	if err != nil {
		t.Fatalf("needs_rebuild: %v\noutput:\n%s", err, out)
	}
	if !strings.Contains(out, "REBUILD:yes:") || !strings.Contains(out, "example.go") {
		t.Fatalf("expected needs_rebuild to report true naming the newer .go file, got:\n%s", out)
	}
}

// TestNeedsRebuild_BannerTxtNewerThanBinary proves the embedded-asset
// signal specifically — this is the exact bug reported: the banner
// (ui/banner.txt, go:embed'd) changed but, before this fix, autorun.sh
// never noticed because nothing checked .txt files at all.
func TestNeedsRebuild_BannerTxtNewerThanBinary(t *testing.T) {
	scriptDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(scriptDir, "bin"), 0755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	binPath := filepath.Join(scriptDir, "bin", "boot-orchestrator")
	writeFileWithTime(t, binPath, "fake-old-binary", time.Now().Add(-time.Hour))

	uiDir := filepath.Join(scriptDir, "ui")
	if err := os.MkdirAll(uiDir, 0755); err != nil {
		t.Fatalf("mkdir ui: %v", err)
	}
	writeFileWithTime(t, filepath.Join(uiDir, "banner.txt"), "new banner art", time.Now())

	out, err := sourceAutorunAndRun(t, scriptDir, `needs_rebuild && echo "REBUILD:yes:$REBUILD_REASON" || echo "REBUILD:no"`)
	if err != nil {
		t.Fatalf("needs_rebuild: %v\noutput:\n%s", err, out)
	}
	if !strings.Contains(out, "REBUILD:yes:") || !strings.Contains(out, "banner.txt") {
		t.Fatalf("expected needs_rebuild to report true naming the newer banner.txt, got:\n%s", out)
	}
}

// TestNeedsRebuild_GoModNewerThanBinary proves the go.mod signal.
func TestNeedsRebuild_GoModNewerThanBinary(t *testing.T) {
	scriptDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(scriptDir, "bin"), 0755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	binPath := filepath.Join(scriptDir, "bin", "boot-orchestrator")
	writeFileWithTime(t, binPath, "fake-old-binary", time.Now().Add(-time.Hour))
	writeFileWithTime(t, filepath.Join(scriptDir, "go.mod"), "module boot-orchestrator", time.Now())

	out, err := sourceAutorunAndRun(t, scriptDir, `needs_rebuild && echo "REBUILD:yes:$REBUILD_REASON" || echo "REBUILD:no"`)
	if err != nil {
		t.Fatalf("needs_rebuild: %v\noutput:\n%s", err, out)
	}
	if !strings.Contains(out, "REBUILD:yes:") || !strings.Contains(out, "go.mod") {
		t.Fatalf("expected needs_rebuild to report true naming the newer go.mod, got:\n%s", out)
	}
}

// TestNeedsRebuild_EverythingOlderThanBinary_NoGitRepo is the clean
// baseline: nothing newer than the binary, and no .git directory at all
// (a source tarball, not a clone) — needs_rebuild must report false and
// skip the commit check gracefully rather than erroring.
func TestNeedsRebuild_EverythingOlderThanBinary_NoGitRepo(t *testing.T) {
	scriptDir := t.TempDir()
	writeFileWithTime(t, filepath.Join(scriptDir, "go.mod"), "module boot-orchestrator", time.Now().Add(-time.Hour))
	if err := os.MkdirAll(filepath.Join(scriptDir, "bin"), 0755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	writeFileWithTime(t, filepath.Join(scriptDir, "bin", "boot-orchestrator"), "fake-binary", time.Now())

	out, err := sourceAutorunAndRun(t, scriptDir, `needs_rebuild && echo "REBUILD:yes:$REBUILD_REASON" || echo "REBUILD:no"`)
	if err != nil {
		t.Fatalf("needs_rebuild: %v\noutput:\n%s", err, out)
	}
	if !strings.Contains(out, "REBUILD:no") {
		t.Fatalf("expected needs_rebuild to report false when nothing is newer and there's no .git directory, got:\n%s", out)
	}
}

// TestNeedsRebuild_CommitMismatch_RealGitRepo proves the second new
// staleness signal end to end against a REAL git repository and a real
// (fake, but executable and --print-build-commit-compatible) binary
// stub, not a mock of git itself — requires git on PATH, skipped
// otherwise.
func TestNeedsRebuild_CommitMismatch_RealGitRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found on PATH")
	}

	scriptDir := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = scriptDir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	runGit("init", "-q")
	if err := os.WriteFile(filepath.Join(scriptDir, "go.mod"), []byte("module boot-orchestrator"), 0644); err != nil {
		t.Fatalf("writing go.mod: %v", err)
	}
	runGit("add", "go.mod")
	runGit("commit", "-q", "-m", "initial")

	currentHead := strings.TrimSpace(gitRevParseHEAD(t, scriptDir))

	binDir := filepath.Join(scriptDir, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	binPath := filepath.Join(binDir, "boot-orchestrator")
	// A fake binary that only knows how to answer --print-build-commit
	// with a DIFFERENT commit than currentHead — everything else in this
	// test's filesystem is deliberately older than this binary, so this
	// is the only signal needs_rebuild has to go on.
	writeFakeBuildCommitBinary(t, binPath, "0000000000000000000000000000000000000000")
	futureTime := time.Now().Add(time.Hour)
	if err := os.Chtimes(binPath, futureTime, futureTime); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	out, err := sourceAutorunAndRun(t, scriptDir, `needs_rebuild && echo "REBUILD:yes:$REBUILD_REASON" || echo "REBUILD:no"`)
	if err != nil {
		t.Fatalf("needs_rebuild: %v\noutput:\n%s", err, out)
	}
	if !strings.Contains(out, "REBUILD:yes:") || !strings.Contains(out, currentHead) {
		t.Fatalf("expected needs_rebuild to report true naming the current HEAD (%s), got:\n%s", currentHead, out)
	}
}

// TestNeedsRebuild_CommitMatches_RealGitRepo is the matching positive
// control for the test above: same real repo, but the fake binary
// reports the SAME commit as HEAD, and nothing else is newer — must
// report false.
func TestNeedsRebuild_CommitMatches_RealGitRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found on PATH")
	}

	scriptDir := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = scriptDir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	runGit("init", "-q")
	if err := os.WriteFile(filepath.Join(scriptDir, "go.mod"), []byte("module boot-orchestrator"), 0644); err != nil {
		t.Fatalf("writing go.mod: %v", err)
	}
	runGit("add", "go.mod")
	runGit("commit", "-q", "-m", "initial")
	currentHead := strings.TrimSpace(gitRevParseHEAD(t, scriptDir))

	binDir := filepath.Join(scriptDir, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	binPath := filepath.Join(binDir, "boot-orchestrator")
	writeFakeBuildCommitBinary(t, binPath, currentHead)
	futureTime := time.Now().Add(time.Hour)
	if err := os.Chtimes(binPath, futureTime, futureTime); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	out, err := sourceAutorunAndRun(t, scriptDir, `needs_rebuild && echo "REBUILD:yes:$REBUILD_REASON" || echo "REBUILD:no"`)
	if err != nil {
		t.Fatalf("needs_rebuild: %v\noutput:\n%s", err, out)
	}
	if !strings.Contains(out, "REBUILD:no") {
		t.Fatalf("expected needs_rebuild to report false when the built commit matches HEAD and nothing else is newer, got:\n%s", out)
	}
}

func gitRevParseHEAD(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse HEAD: %v", err)
	}
	return string(out)
}

// writeFakeBuildCommitBinary writes a tiny real shell script (executable,
// standing in for the compiled binary) that answers
// --print-build-commit with commit and otherwise does nothing — enough
// for needs_rebuild's own `"$BIN_PATH" --print-build-commit` call to
// exercise the real code path, without needing an actual Go build in
// this test.
func writeFakeBuildCommitBinary(t *testing.T, path, commit string) {
	t.Helper()
	script := "#!/bin/sh\nif [ \"$1\" = \"--print-build-commit\" ]; then echo " + shellQuote(commit) + "; fi\n"
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatalf("writing fake binary at %s: %v", path, err)
	}
}

// writeFileWithTime writes content to path and then sets both its access
// and modification time to mtime, since find -newer compares mtimes and
// a plain os.WriteFile always uses "now" regardless of test ordering.
func writeFileWithTime(t *testing.T, path, content string, mtime time.Time) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
}
