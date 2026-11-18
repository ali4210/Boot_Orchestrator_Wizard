package engine

import (
	"fmt"
	"os"
	"os/exec"
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

// writeStubBin writes an executable shell script named name into dir,
// containing body as its script body.
func writeStubBin(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name)
	content := "#!/usr/bin/env bash\n" + body + "\n"
	if err := os.WriteFile(path, []byte(content), 0755); err != nil {
		t.Fatalf("writing stub %s: %v", name, err)
	}
}

// runUSBStorageMountRetryBlock executes usbStorageMountRetryBlock's actual
// bash output for real under bash, with stub partprobe/udevadm/sleep
// binaries (always succeed, no real delay — these tests exercise retry
// COUNT, not wall-clock timing) and a caller-supplied mount stub, returning
// combined stdout+stderr and the process's exit error (nil on success).
// This is follow-up F15's "test with a fake mount that succeeds on the Nth
// try": rather than only asserting on the generated script's text, it
// actually runs the retry logic under a real shell against a fake mount
// command, so a logic bug in the loop (off-by-one, wrong exit status
// check, ...) would make the test fail for real, not just look right on
// inspection.
func runUSBStorageMountRetryBlock(t *testing.T, mountStubBody string, extraEnv ...string) (string, error) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available, skipping")
	}
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	writeStubBin(t, binDir, "partprobe", "exit 0")
	writeStubBin(t, binDir, "udevadm", "exit 0")
	writeStubBin(t, binDir, "sleep", "exit 0") // no real delay in tests
	writeStubBin(t, binDir, "mount", mountStubBody)

	vaultDir := filepath.Join(dir, "vault")
	scriptPath := filepath.Join(dir, "run.sh")
	script := fmt.Sprintf("#!/usr/bin/env bash\nset -u\n%s\necho \"MOUNTED_RESULT=$MOUNTED\"\n",
		usbStorageMountRetryBlock(vaultDir, "STORAGE_UUID"))
	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		t.Fatalf("writing script: %v", err)
	}

	cmd := exec.Command("bash", scriptPath)
	cmd.Env = append(append(os.Environ(), "PATH="+binDir+":"+os.Getenv("PATH"), "STORAGE_UUID="), extraEnv...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// TestUSBStorageMountRetryBlock_SucceedsOnThirdAttempt is the reproduction
// for the bug half of follow-up F15: before this fix, transient_resize.sh
// made exactly one mount attempt with no retry at all, so a USB storage
// partition that wasn't yet visible to the kernel (a very common timing
// window right after rebooting into a live USB environment) permanently
// failed the install. With the fix, a mount stub that fails twice and
// succeeds on its third call must be tolerated by the retry loop.
func TestUSBStorageMountRetryBlock_SucceedsOnThirdAttempt(t *testing.T) {
	dir := t.TempDir()
	counterFile := filepath.Join(dir, "mount_calls")
	mountStub := `
COUNT=0
[ -f "` + counterFile + `" ] && COUNT=$(cat "` + counterFile + `")
COUNT=$((COUNT+1))
echo "$COUNT" > "` + counterFile + `"
if [ "$COUNT" -ge 3 ]; then
    exit 0
fi
exit 1
`
	out, err := runUSBStorageMountRetryBlock(t, mountStub)
	if err != nil {
		t.Fatalf("expected the retry loop to eventually succeed, got error %v\noutput:\n%s", err, out)
	}
	if !strings.Contains(out, "MOUNTED_RESULT=1") {
		t.Fatalf("expected MOUNTED to be set after the retry succeeded, got:\n%s", out)
	}
	callsOut, readErr := os.ReadFile(counterFile)
	if readErr != nil {
		t.Fatalf("reading mount-call counter: %v", readErr)
	}
	if strings.TrimSpace(string(callsOut)) != "3" {
		t.Errorf("expected exactly 3 mount attempts (stopping as soon as it succeeded), got %s", strings.TrimSpace(string(callsOut)))
	}
}

// TestUSBStorageMountRetryBlock_FailsLoudlyAfterTenAttempts proves the
// loop gives up after exactly 10 attempts (not forever, and not silently
// like the pre-fix single `|| true` attempt) and reports a clear error
// instead of letting the caller script silently proceed with a
// non-existent payload path.
func TestUSBStorageMountRetryBlock_FailsLoudlyAfterTenAttempts(t *testing.T) {
	dir := t.TempDir()
	counterFile := filepath.Join(dir, "mount_calls")
	mountStub := `
COUNT=0
[ -f "` + counterFile + `" ] && COUNT=$(cat "` + counterFile + `")
COUNT=$((COUNT+1))
echo "$COUNT" > "` + counterFile + `"
exit 1
`
	out, err := runUSBStorageMountRetryBlock(t, mountStub)
	if err == nil {
		t.Fatalf("expected the retry loop to fail when mount never succeeds, got success:\n%s", out)
	}
	if !strings.Contains(out, "after 10 attempts") {
		t.Errorf("expected a clear 'after 10 attempts' error message, got:\n%s", out)
	}
	callsOut, readErr := os.ReadFile(counterFile)
	if readErr != nil {
		t.Fatalf("reading mount-call counter: %v", readErr)
	}
	if strings.TrimSpace(string(callsOut)) != "10" {
		t.Errorf("expected exactly 10 mount attempts, got %s", strings.TrimSpace(string(callsOut)))
	}
}

// TestUSBStorageMountRetryBlock_FallsBackToUUID proves the LABEL-fails/
// UUID-succeeds fallback path actually works: mount -L always fails, but
// mount -U with the expected UUID succeeds.
func TestUSBStorageMountRetryBlock_FallsBackToUUID(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available, skipping")
	}
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	writeStubBin(t, binDir, "partprobe", "exit 0")
	writeStubBin(t, binDir, "udevadm", "exit 0")
	writeStubBin(t, binDir, "sleep", "exit 0")
	writeStubBin(t, binDir, "mount", `
if [ "$1" = "-L" ]; then
    exit 1
fi
if [ "$1" = "-U" ] && [ "$2" = "deadbeef-uuid" ]; then
    exit 0
fi
exit 1
`)

	vaultDir := filepath.Join(dir, "vault")
	scriptPath := filepath.Join(dir, "run.sh")
	script := fmt.Sprintf("#!/usr/bin/env bash\nset -u\n%s\necho \"MOUNTED_RESULT=$MOUNTED\"\n",
		usbStorageMountRetryBlock(vaultDir, "STORAGE_UUID"))
	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		t.Fatalf("writing script: %v", err)
	}

	cmd := exec.Command("bash", scriptPath)
	cmd.Env = append(os.Environ(), "PATH="+binDir+":"+os.Getenv("PATH"), "STORAGE_UUID=deadbeef-uuid")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("expected the UUID fallback to succeed, got error %v\noutput:\n%s", err, string(out))
	}
	if !strings.Contains(string(out), "MOUNTED_RESULT=1") {
		t.Fatalf("expected MOUNTED to be set after the UUID fallback succeeded, got:\n%s", string(out))
	}
}

// TestRenderAutonomousInstallerScript_EmbedsUSBStorageMountRetryBlock
// proves the full script actually wires in usbStorageMountRetryBlock
// (rather than, say, forgetting to substitute it, or the fragment and the
// rendered script silently drifting apart) and declares STORAGE_UUID from
// the params.
func TestRenderAutonomousInstallerScript_EmbedsUSBStorageMountRetryBlock(t *testing.T) {
	p := sampleScriptParams()
	p.StorageUUID = "11112222-3333-4444-5555-666677778888"
	script := renderAutonomousInstallerScript(p)

	if !strings.Contains(script, `STORAGE_UUID="11112222-3333-4444-5555-666677778888"`) {
		t.Fatalf("expected STORAGE_UUID to be declared from params, got:\n%s", script)
	}
	if !strings.Contains(script, "for i in $(seq 1 10)") {
		t.Fatalf("expected the bounded 10-attempt retry loop to be embedded, got:\n%s", script)
	}
	if !strings.Contains(script, "partprobe") || !strings.Contains(script, "udevadm settle") {
		t.Fatalf("expected partprobe and udevadm settle before each mount attempt, got:\n%s", script)
	}
	if !strings.Contains(script, `mount -U "$STORAGE_UUID"`) {
		t.Fatalf("expected a UUID fallback mount attempt, got:\n%s", script)
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
