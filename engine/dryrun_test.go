package engine

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestDryRunRunner_NeverExecutes proves DryRunRunner.Run genuinely never
// runs the real command — not just that it returns success, which a
// broken implementation that silently executed anyway could also do by
// coincidence. Points it at a real binary (`touch`) that would leave a
// detectable, provable side effect (a file on disk) if actually executed,
// and confirms that file does not exist afterward.
func TestDryRunRunner_NeverExecutes(t *testing.T) {
	if _, err := exec.LookPath("touch"); err != nil {
		t.Skip("touch not available, skipping")
	}
	marker := filepath.Join(t.TempDir(), "dry-run-should-not-create-this")

	out, err := DryRunRunner{}.Run("touch", marker)
	if err != nil {
		t.Fatalf("expected DryRunRunner.Run to report success, got: %v", err)
	}
	if out != nil {
		t.Errorf("expected nil output, got %q", out)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatalf("DryRunRunner actually executed the command — %s was created for real", marker)
	}
}

// TestDryRunFS_NeverWrites proves DryRunFS's four methods genuinely never
// touch the real filesystem, the same way TestDryRunRunner_NeverExecutes
// proves it for commands.
func TestDryRunFS_NeverWrites(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "subdir", "file.txt")
	dirPath := filepath.Join(dir, "newdir")

	if err := (DryRunFS{}).WriteFile(filePath, []byte("content"), 0644); err != nil {
		t.Errorf("WriteFile: expected nil error, got %v", err)
	}
	if _, err := os.Stat(filePath); err == nil {
		t.Fatalf("DryRunFS.WriteFile actually wrote %s", filePath)
	}

	if err := (DryRunFS{}).MkdirAll(dirPath, 0755); err != nil {
		t.Errorf("MkdirAll: expected nil error, got %v", err)
	}
	if _, err := os.Stat(dirPath); err == nil {
		t.Fatalf("DryRunFS.MkdirAll actually created %s", dirPath)
	}

	// Remove/RemoveAll: prove they don't touch a REAL existing file/dir
	// either (not just that they don't create one).
	realFile := filepath.Join(dir, "real.txt")
	if err := os.WriteFile(realFile, []byte("x"), 0644); err != nil {
		t.Fatalf("staging real file: %v", err)
	}
	realDir := filepath.Join(dir, "realdir")
	if err := os.MkdirAll(realDir, 0755); err != nil {
		t.Fatalf("staging real dir: %v", err)
	}

	if err := (DryRunFS{}).Remove(realFile); err != nil {
		t.Errorf("Remove: expected nil error, got %v", err)
	}
	if _, err := os.Stat(realFile); err != nil {
		t.Fatalf("DryRunFS.Remove actually removed %s (real file is now gone)", realFile)
	}

	if err := (DryRunFS{}).RemoveAll(realDir); err != nil {
		t.Errorf("RemoveAll: expected nil error, got %v", err)
	}
	if _, err := os.Stat(realDir); err != nil {
		t.Fatalf("DryRunFS.RemoveAll actually removed %s (real dir is now gone)", realDir)
	}
}

// TestLogDryRun_TruncatesLongArguments is the regression test for a real
// issue found while writing this batch's own tests:
// assertDefaultCredentials's chroot call passes a generated shell script
// as one argument, which embeds the account's password HASH — the
// unmodified log line printed that whole script, hash included, to
// stderr. logDryRun must never print more than maxLoggedArgLen bytes of
// any single argument, regardless of what it contains.
func TestLogDryRun_TruncatesLongArguments(t *testing.T) {
	secretLookingArg := "echo 'janedoe:$6$abcSalt$" + strings.Repeat("deadbeef", 20) + "' | chpasswd -e"
	if len(secretLookingArg) <= maxLoggedArgLen {
		t.Fatalf("test setup bug: secretLookingArg (%d bytes) must exceed maxLoggedArgLen (%d)", len(secretLookingArg), maxLoggedArgLen)
	}

	origStderr := os.Stderr
	r, w, pipeErr := os.Pipe()
	if pipeErr != nil {
		t.Fatalf("creating pipe: %v", pipeErr)
	}
	os.Stderr = w
	logDryRun("exec", "chroot", "/mnt/target", "/bin/sh", "-c", secretLookingArg)
	_ = w.Close()
	os.Stderr = origStderr

	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	logged := buf.String()

	if strings.Contains(logged, secretLookingArg) {
		t.Fatalf("logDryRun printed the full, untruncated argument (%d bytes) — a real password hash would leak into logs this way:\n%s", len(secretLookingArg), logged)
	}
	if !strings.Contains(logged, "truncated") {
		t.Errorf("expected the log line to note truncation, got:\n%s", logged)
	}
}

// TestSetDryRun_SwapsBothRunnerAndFSAndRestores proves the single toggle
// actually switches cmdRunner, fsRunner, AND execCommand together, and
// that the returned restore function puts the real implementations back
// — mirroring safety.SetJournalCandidateDirsForTesting's own contract.
func TestSetDryRun_SwapsBothRunnerAndFSAndRestores(t *testing.T) {
	origRunner, origFS, origExec := cmdRunner, fsRunner, execCommand
	t.Cleanup(func() { cmdRunner, fsRunner, execCommand = origRunner, origFS, origExec })

	if IsDryRun() {
		t.Fatalf("expected dry-run to be off before this test touches it")
	}

	restore := SetDryRun(true)
	if !IsDryRun() {
		t.Errorf("expected IsDryRun() to report true after SetDryRun(true)")
	}
	if _, ok := cmdRunner.(DryRunRunner); !ok {
		t.Errorf("expected cmdRunner to be DryRunRunner, got %T", cmdRunner)
	}
	if _, ok := fsRunner.(DryRunFS); !ok {
		t.Errorf("expected fsRunner to be DryRunFS, got %T", fsRunner)
	}

	marker := filepath.Join(t.TempDir(), "should-not-exist")
	_ = fsRunner.WriteFile(marker, []byte("x"), 0644)
	if _, err := os.Stat(marker); err == nil {
		t.Fatalf("a write through fsRunner during dry-run actually touched disk")
	}

	execMarker := filepath.Join(t.TempDir(), "exec-should-not-create-this")
	if _, err := execCommand("touch", execMarker).CombinedOutput(); err != nil {
		t.Errorf("expected execCommand's dry-run stand-in to report success, got: %v", err)
	}
	if _, statErr := os.Stat(execMarker); statErr == nil {
		t.Fatalf("execCommand during dry-run actually ran the real command — %s was created", execMarker)
	}

	restore()
	if IsDryRun() {
		t.Errorf("expected IsDryRun() to report false after restore()")
	}
	if cmdRunner != DefaultRunner {
		t.Errorf("expected cmdRunner to be restored to DefaultRunner, got %T", cmdRunner)
	}
	if _, ok := fsRunner.(DefaultFS); !ok {
		t.Errorf("expected fsRunner to be restored to DefaultFS, got %T", fsRunner)
	}
}

// TestDryRunExecCommand_NeverExecutesButSatisfiesStdinDirEnv proves
// dryRunExecCommand both (a) never runs the real command, the same way
// TestDryRunRunner_NeverExecutes proves it for Runner, and (b) returns a
// genuine *exec.Cmd that callers can still set .Stdin/.Dir/.Env on and
// then call .Run()/.Output()/.CombinedOutput() without panicking — the
// exact capability engine.Runner's own doc comment says it cannot offer,
// and the reason this second interception mechanism exists at all
// (credentials.go, partition.go, engine_linux.go, backup.go all set one of
// these fields on a raw exec.Command result).
func TestDryRunExecCommand_NeverExecutesButSatisfiesStdinDirEnv(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "dry-run-exec-should-not-create-this")

	cmd := dryRunExecCommand("touch", marker)
	cmd.Stdin = strings.NewReader("irrelevant stdin")
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(), "ORCH_TEST=1")

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("expected dryRunExecCommand's stand-in to succeed even with Stdin/Dir/Env set, got: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("expected empty output, got %q", out)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatalf("dryRunExecCommand actually executed touch — %s was created for real", marker)
	}
}

// TestAssertDefaultCredentials_DryRunDoesNotWriteSudoersFile proves item
// 32's actual migrated call site: with dry-run enabled,
// assertDefaultCredentials's sudoers.d write (now routed through
// fsRunner — see engine/bootloader.go) does not touch the real
// filesystem.
//
// Updated for the PR #2 full-dry-run-flow follow-up: this function's own
// account-existence verification used to legitimately fail in dry-run,
// since the chroot command that would create the account never really
// ran — which meant RunProvision could never get past account
// provisioning under --dry-run at all, stopping well before mkfs, the
// bootloader, or NVRAM registration. Per that follow-up's explicit
// instruction, this specific verification (which checks for a REAL side
// effect of an intercepted command, not something dry-run could ever
// honestly determine either way) is now simulated instead, with a
// logDryRunSimulated line recording exactly that — see
// assertDefaultCredentials's own comment. A nil error here is no longer
// a false claim: it is the documented, deliberate simulation.
func TestAssertDefaultCredentials_DryRunDoesNotWriteSudoersFile(t *testing.T) {
	restore := SetDryRun(true)
	defer restore()

	rootPath := t.TempDir()
	creds := Credentials{Username: "janedoe", Password: "a-fine-password"}

	err := assertDefaultCredentials(rootPath, creds, nil)
	if err != nil {
		t.Fatalf("expected assertDefaultCredentials to simulate success in dry-run (chroot's real verification is unreachable by construction, see logDryRunSimulated), got: %v", err)
	}

	sudoersPath := filepath.Join(rootPath, "etc", "sudoers.d", "janedoe")
	if _, statErr := os.Stat(sudoersPath); statErr == nil {
		t.Fatalf("dry-run mode actually wrote a real sudoers.d file at %s", sudoersPath)
	}
}

// TestDiskReadRunner_BypassesDryRunStandIn is the regression test for the
// real bug found while building item 50 (audit follow-up G3)'s dry-run
// proof: blockDeviceSectorSize/blockDeviceSizeBytes/readDiskRegion
// (engine/partition.go) going through cmdRunner directly meant
// SetDryRun(true) made them all return empty output (DryRunRunner's
// stand-in), which then failed to parse as a number — making
// BackupPartitionTable, which every provision/revert calls unconditionally
// near the start, fail immediately on every single dry run, before any
// of the disk-mutating code --dry-run exists to protect against was ever
// reached. diskReadRunner() must return the real DefaultRunner while
// dry-run is active (these three are read-only by construction, not by
// argument-inspection, so this is safe) — but still return a test's own
// mock when one is in place, not silently ignore it.
func TestDiskReadRunner_BypassesDryRunStandIn(t *testing.T) {
	origRunner := cmdRunner
	t.Cleanup(func() { cmdRunner = origRunner })

	cmdRunner = DefaultRunner
	if diskReadRunner() != DefaultRunner {
		t.Errorf("expected diskReadRunner() to return cmdRunner unchanged when it's already DefaultRunner")
	}

	mock := &mockRunner{Handler: func(name string, args []string) ([]byte, error) { return nil, nil }}
	cmdRunner = mock
	if got := diskReadRunner(); got != Runner(mock) {
		t.Errorf("expected diskReadRunner() to honor a test's own mock cmdRunner, got %T", got)
	}

	cmdRunner = DryRunRunner{}
	if got := diskReadRunner(); got != DefaultRunner {
		t.Errorf("expected diskReadRunner() to bypass DryRunRunner and return the real DefaultRunner, got %T", got)
	}
}

