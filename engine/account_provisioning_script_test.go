package engine

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writeFakeBin writes a fake stub command named `name` into dir, executable,
// with the given POSIX sh body. Used to exercise the real admin-group
// detection and account-verification logic in renderAccountProvisioningScript
// via an actual `sh -c` invocation, without ever touching the real system's
// getent/useradd/usermod/chpasswd/id — those binary names are shadowed by
// putting dir first on PATH, so nothing here can create a real user or
// modify a real group.
func writeFakeBin(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0755); err != nil {
		t.Fatalf("writing fake %s: %v", name, err)
	}
}

// runAccountScript executes renderAccountProvisioningScript's output for
// real via `sh -c`, with fake getent/useradd/usermod/chpasswd/id stubs
// controlling the simulated chroot environment. fakeEnv configures those
// stubs (see writeFakeBin bodies below); no real disk, user, or group is
// ever touched.
func runAccountScript(t *testing.T, fakeEnv map[string]string) (stdout string, err error) {
	t.Helper()
	if runtime.GOOS == "windows" {
		// renderAccountProvisioningScript is only ever run via
		// assertDefaultCredentials's `cmdRunner.Run("chroot", rootPath,
		// "/bin/sh", "-c", script)` — chroot has no Windows equivalent
		// at all, so this is unreachable on a Windows host regardless of
		// what any individual test here asserts. (Two of the five tests
		// in this file currently don't fail on windows-latest, but only
		// because the fake useradd/getent/id stub mechanism this helper
		// relies on — shebang scripts with no real Windows executable
		// extension — doesn't work there either, making every run look
		// like "account missing" regardless of the fake env; that's a
		// coincidental pass, not a meaningful one, so all five are
		// skipped together rather than leaving two in a state that looks
		// green without actually testing anything.)
		t.Skip("renderAccountProvisioningScript is only reachable via assertDefaultCredentials's chroot call, which has no Windows equivalent")
	}
	binDir := t.TempDir()

	writeFakeBin(t, binDir, "getent", `
if [ "$2" = "sudo" ]; then
    [ "$FAKE_HAS_SUDO" = "1" ] && exit 0 || exit 2
fi
if [ "$2" = "wheel" ]; then
    [ "$FAKE_HAS_WHEEL" = "1" ] && exit 0 || exit 2
fi
exit 2
`)
	writeFakeBin(t, binDir, "useradd", `
if [ "$FAKE_USERADD_CREATES" = "1" ]; then
    touch "$FAKE_USER_MARKER"
fi
exit "${FAKE_USERADD_EXIT:-0}"
`)
	writeFakeBin(t, binDir, "usermod", `exit 0`)
	writeFakeBin(t, binDir, "chpasswd", `cat >/dev/null; exit 0`)
	writeFakeBin(t, binDir, "id", `
if [ -f "$FAKE_USER_MARKER" ]; then
    exit 0
else
    exit 1
fi
`)

	marker := filepath.Join(t.TempDir(), "user-exists-marker")

	rootCmd := "true"
	script := renderAccountProvisioningScript(rootCmd, "janedoe", "janedoe:somehash")

	cmd := exec.Command("sh", "-c", script)
	env := []string{
		"PATH=" + binDir + ":/usr/bin:/bin",
		"FAKE_USER_MARKER=" + marker,
	}
	for k, v := range fakeEnv {
		env = append(env, k+"="+v)
	}
	cmd.Env = env

	out, runErr := cmd.CombinedOutput()
	return string(out), runErr
}

// TestRenderAccountProvisioningScript_DetectsSudoGroup covers the
// Debian/Ubuntu case: "sudo" exists, "wheel" does not.
func TestRenderAccountProvisioningScript_DetectsSudoGroup(t *testing.T) {
	out, err := runAccountScript(t, map[string]string{
		"FAKE_HAS_SUDO":        "1",
		"FAKE_HAS_WHEEL":       "0",
		"FAKE_USERADD_CREATES": "1",
	})
	if err != nil {
		t.Fatalf("script failed: %v, output:\n%s", err, out)
	}
	if !strings.Contains(out, "ORCH_ACCOUNT_VERIFIED admin_group=sudo") {
		t.Fatalf("expected sudo to be selected as the admin group, got:\n%s", out)
	}
}

// TestRenderAccountProvisioningScript_DetectsWheelGroup covers the
// Fedora/RHEL/Arch case: "sudo" does not exist, "wheel" does.
func TestRenderAccountProvisioningScript_DetectsWheelGroup(t *testing.T) {
	out, err := runAccountScript(t, map[string]string{
		"FAKE_HAS_SUDO":        "0",
		"FAKE_HAS_WHEEL":       "1",
		"FAKE_USERADD_CREATES": "1",
	})
	if err != nil {
		t.Fatalf("script failed: %v, output:\n%s", err, out)
	}
	if !strings.Contains(out, "ORCH_ACCOUNT_VERIFIED admin_group=wheel") {
		t.Fatalf("expected wheel to be selected as the admin group, got:\n%s", out)
	}
}

// TestRenderAccountProvisioningScript_NoAdminGroupStillVerifiesAccount
// covers a target with neither "sudo" nor "wheel": the account must still
// be created (with no admin group) and the script must say so explicitly
// via admin_group=none rather than silently claiming full privileges.
func TestRenderAccountProvisioningScript_NoAdminGroupStillVerifiesAccount(t *testing.T) {
	out, err := runAccountScript(t, map[string]string{
		"FAKE_HAS_SUDO":        "0",
		"FAKE_HAS_WHEEL":       "0",
		"FAKE_USERADD_CREATES": "1",
	})
	if err != nil {
		t.Fatalf("script failed: %v, output:\n%s", err, out)
	}
	if !strings.Contains(out, "ORCH_ACCOUNT_VERIFIED admin_group=none") {
		t.Fatalf("expected admin_group=none to be reported, got:\n%s", out)
	}
}

// TestRenderAccountProvisioningScript_FailsLoudlyWhenAccountMissing is the
// regression test for audit 1A.3's "fail loudly if missing" requirement:
// if useradd reports success but the account does not actually exist
// afterward (id -u fails), the script must exit non-zero with
// ORCH_ACCOUNT_MISSING instead of silently continuing as if it succeeded.
func TestRenderAccountProvisioningScript_FailsLoudlyWhenAccountMissing(t *testing.T) {
	out, err := runAccountScript(t, map[string]string{
		"FAKE_HAS_SUDO":        "1",
		"FAKE_HAS_WHEEL":       "0",
		"FAKE_USERADD_EXIT":    "0", // useradd itself reports success...
		"FAKE_USERADD_CREATES": "0", // ...but does not actually create the account
	})
	if err == nil {
		t.Fatalf("expected the script to exit non-zero when the account was never actually created, output:\n%s", out)
	}
	if !strings.Contains(out, "ORCH_ACCOUNT_MISSING") {
		t.Fatalf("expected ORCH_ACCOUNT_MISSING in output, got:\n%s", out)
	}
}

// TestRenderAccountProvisioningScript_FailsLoudlyWhenUseraddErrors proves
// that a hard useradd failure (e.g. disk full, duplicate UID) propagates as
// a non-zero script exit under `set -e`, rather than being swallowed by a
// `|| true` as the pre-fix script did for every step in this block.
func TestRenderAccountProvisioningScript_FailsLoudlyWhenUseraddErrors(t *testing.T) {
	out, err := runAccountScript(t, map[string]string{
		"FAKE_HAS_SUDO":        "1",
		"FAKE_HAS_WHEEL":       "0",
		"FAKE_USERADD_EXIT":    "1",
		"FAKE_USERADD_CREATES": "0",
	})
	if err == nil {
		t.Fatalf("expected a useradd failure to abort the script, output:\n%s", out)
	}
}
