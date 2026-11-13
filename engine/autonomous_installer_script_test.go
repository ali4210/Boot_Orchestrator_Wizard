package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sampleScriptParams() autonomousInstallerScriptParams {
	return autonomousInstallerScriptParams{
		Disk:          "/dev/sdz",
		HostPartDev:   "/dev/sdz1",
		TargetPartDev: "/dev/sdz2",
		HostESP:       "/dev/sdz15",
		HostPartNum:   "1",
		TargetPartNum: "2",
		DistroLabel:   "UBUNTU",
		Username:      "janedoe",
		PasswordHash:  "$6$abcSalt$deadbeefhash",
		RootCmd:       "passwd -l root 2>/dev/null || true",
	}
}

// TestWriteScriptSecurely_UsesOwnerOnlyMode is the regression test for
// Phase 1 item A.2: transient_resize.sh embeds the password hash in its own
// text (see renderAutonomousInstallerScript) and must never be
// group/world-readable. Before this fix it was written with os.OpenFile(...,
// 0755) and re-chmod'd to 0755 — both group- and world-readable.
func TestWriteScriptSecurely_UsesOwnerOnlyMode(t *testing.T) {
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "transient_resize.sh")

	if err := writeScriptSecurely(scriptPath, "#!/usr/bin/env bash\necho hi\n"); err != nil {
		t.Fatalf("writeScriptSecurely: %v", err)
	}

	fi, err := os.Stat(scriptPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := fi.Mode().Perm(); got != transientScriptMode {
		t.Fatalf("expected mode %o, got %o (world/group readable: %v)", transientScriptMode, got, got&0077 != 0)
	}
}

// TestWriteScriptSecurely_DoesNotNarrowModeOnExistingFile documents real
// open(2) behavior that orchestrator.go's caller depends on: O_CREAT does
// NOT change the mode of a file that already exists (e.g. left over from a
// prior run written before this fix, at the old 0755). writeScriptSecurely's
// OpenFile mode argument only applies to newly created files, so the caller
// in RunProvision additionally calls os.Chmod(scriptPath, transientScriptMode)
// after every write — this test is the regression guard for why that extra
// chmod is required, not optional.
func TestWriteScriptSecurely_DoesNotNarrowModeOnExistingFile(t *testing.T) {
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "transient_resize.sh")

	if err := os.WriteFile(scriptPath, []byte("stale"), 0755); err != nil {
		t.Fatalf("seeding stale file: %v", err)
	}

	if err := writeScriptSecurely(scriptPath, "fresh"); err != nil {
		t.Fatalf("writeScriptSecurely: %v", err)
	}

	fi, err := os.Stat(scriptPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0755 {
		t.Fatalf("expected open(2) to leave the pre-existing mode (0755) untouched, got %o — if this changed, the explicit os.Chmod call after writeScriptSecurely in RunProvision may be redundant but is still required as a safety net", got)
	}
	content, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}
	if string(content) != "fresh" {
		t.Fatalf("expected O_TRUNC to replace stale content, got %q", string(content))
	}
}

// TestRenderAutonomousInstallerScript_CleansUpOnExit is the regression test
// for the other half of item A.2: both the embedded password-hash file
// (/mnt/target/tmp/.orch_pwhash) and the script's own on-disk path must be
// removed on every exit path, including early failures under `set -e`, not
// just the success path at the end of the script.
func TestRenderAutonomousInstallerScript_CleansUpOnExit(t *testing.T) {
	script := renderAutonomousInstallerScript(sampleScriptParams())

	if !strings.Contains(script, "trap cleanup EXIT") {
		t.Fatalf("expected an EXIT trap so cleanup runs on every exit path, got:\n%s", script)
	}
	if !strings.Contains(script, `rm -f /mnt/target/tmp/.orch_pwhash`) {
		t.Fatalf("expected the cleanup trap to remove the password-hash file, got:\n%s", script)
	}
	if !strings.Contains(script, `rm -f "$SELF_PATH"`) {
		t.Fatalf("expected the cleanup trap to remove the script's own path, got:\n%s", script)
	}
	// The trap must be installed before anything that can fail under set -e
	// (i.e. near the top), otherwise an early failure skips cleanup entirely.
	trapIdx := strings.Index(script, "trap cleanup EXIT")
	firstRiskyCmd := strings.Index(script, "swapoff -a")
	if trapIdx == -1 || firstRiskyCmd == -1 || trapIdx > firstRiskyCmd {
		t.Fatalf("expected the cleanup trap to be installed before the first command that can fail")
	}
}

// TestRenderAutonomousInstallerScript_HashNeverLoggedToStdout confirms the
// hash reaches the target filesystem only via the quoted heredoc (no shell
// expansion), and is not echoed anywhere a log-tee could capture it.
func TestRenderAutonomousInstallerScript_HashNeverLoggedToStdout(t *testing.T) {
	p := sampleScriptParams()
	script := renderAutonomousInstallerScript(p)

	for _, line := range strings.Split(script, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.Contains(line, "echo") && strings.Contains(line, p.PasswordHash) {
			t.Fatalf("found an echo of the raw password hash: %q", line)
		}
	}
}
