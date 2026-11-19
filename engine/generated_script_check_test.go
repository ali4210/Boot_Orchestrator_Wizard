package engine

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// scriptCheckT is the minimal subset of *testing.T checkGeneratedScript
// needs, so TestCheckGeneratedScript_CatchesABadScript can pass a
// recordingT (below) that observes failures instead of actually failing
// the outer test.
type scriptCheckT interface {
	Helper()
	Errorf(format string, args ...any)
	Fatalf(format string, args ...any)
	Skip(args ...any)
	Logf(format string, args ...any)
	TempDir() string
}

// checkGeneratedScript runs the standard set of checks Phase 4 item 26
// asks for against a script this engine generates and feeds to a real
// shell: bash -n (syntax only, never executed), shellcheck (best-effort;
// skipped if not installed; only ERROR-severity findings fail the test —
// shellcheck's style/warning-level opinions are not this check's concern
// and would make the test brittle against shellcheck version drift), a
// check for Go's own fmt-argument-mismatch marker ("%!", e.g.
// "%!s(MISSING)"/"%!(EXTRA ...)" — the reliable way to catch a
// forgotten/extra fmt.Sprintf verb, since a correctly-matched verb is
// consumed during formatting and can never appear literally in the
// output), and a check that no hardcoded "saleem" dev-account leftover
// (audit 2.3) is present.
func checkGeneratedScript(t scriptCheckT, name, script string) {
	t.Helper()

	if strings.Contains(script, "%!") {
		t.Errorf("%s: generated script contains %q — a fmt.Sprintf verb/argument mismatch (missing or extra %%-verb) leaked into the output instead of being substituted:\n%s", name, "%!", script)
	}
	if strings.Contains(script, "saleem") {
		t.Errorf("%s: generated script contains the hardcoded dev-account leftover %q (audit 2.3)", name, "saleem")
	}

	scriptPath := filepath.Join(t.TempDir(), "generated.sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0644); err != nil {
		t.Fatalf("%s: writing script to temp file: %v", name, err)
	}

	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available, skipping syntax check")
	}
	if out, err := exec.Command("bash", "-n", scriptPath).CombinedOutput(); err != nil {
		t.Errorf("%s: bash -n (syntax check) failed: %v\n%s\nscript:\n%s", name, err, out, script)
	}

	if _, err := exec.LookPath("shellcheck"); err != nil {
		t.Logf("%s: shellcheck not available, skipping lint check", name)
		return
	}
	out, err := exec.Command("shellcheck", "--severity=error", "--shell=bash", scriptPath).CombinedOutput()
	if err != nil {
		t.Errorf("%s: shellcheck found ERROR-severity issue(s): %v\n%s\nscript:\n%s", name, err, out, script)
	}
}

// TestGeneratedScripts_PassSyntaxAndContentChecks is Phase 4 item 26: runs
// checkGeneratedScript against every script-rendering function this
// engine has, with representative sample arguments.
func TestGeneratedScripts_PassSyntaxAndContentChecks(t *testing.T) {
	t.Run("renderAccountProvisioningScript_lockRoot", func(t *testing.T) {
		script := renderAccountProvisioningScript(
			"passwd -l root 2>/dev/null || true",
			"janedoe",
			shellSingleQuote("janedoe:$6$abcSalt$deadbeefhash"),
		)
		checkGeneratedScript(t, "renderAccountProvisioningScript (root locked)", script)
	})

	t.Run("renderAccountProvisioningScript_allowRoot", func(t *testing.T) {
		script := renderAccountProvisioningScript(
			"echo "+shellSingleQuote("root:$6$abcSalt$deadbeefhash")+" | chpasswd -e 2>/dev/null || true",
			"janedoe",
			shellSingleQuote("janedoe:$6$abcSalt$deadbeefhash"),
		)
		checkGeneratedScript(t, "renderAccountProvisioningScript (root allowed)", script)
	})

	t.Run("renderCalamaresCleanupScript", func(t *testing.T) {
		script := renderCalamaresCleanupScript("janedoe")
		checkGeneratedScript(t, "renderCalamaresCleanupScript", script)
	})

	t.Run("renderAutonomousInstallerScript", func(t *testing.T) {
		script := renderAutonomousInstallerScript(sampleScriptParams())
		checkGeneratedScript(t, "renderAutonomousInstallerScript", script)
	})

	t.Run("renderAutonomousInstallerScript_allowRootLogin", func(t *testing.T) {
		p := sampleScriptParams()
		p.RootCmd = `echo "root:$HASH" | chpasswd -e`
		script := renderAutonomousInstallerScript(p)
		checkGeneratedScript(t, "renderAutonomousInstallerScript (root allowed)", script)
	})
}

// TestCheckGeneratedScript_CatchesABadScript proves checkGeneratedScript
// itself actually catches real problems, rather than passing anything
// handed to it — a syntax error, a leaked fmt-verb marker, and a
// hardcoded "saleem" leftover, run against t in isolation via a nested
// *testing.T so a deliberately-failing sub-check doesn't fail THIS test.
func TestCheckGeneratedScript_CatchesABadScript(t *testing.T) {
	cases := []struct {
		name   string
		script string
	}{
		{"syntax error", "if [ -f /tmp/x ]; then\n  echo missing fi"},
		{"leaked fmt verb", "echo %!s(MISSING)"},
		{"hardcoded saleem", "useradd -m saleem"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := &recordingT{T: t}
			checkGeneratedScript(rec, c.name, c.script)
			if !rec.failed {
				t.Errorf("expected checkGeneratedScript to flag %q, but it reported no failure", c.name)
			}
		})
	}
}

// recordingT wraps *testing.T so checkGeneratedScript's own t.Errorf/
// t.Fatalf calls can be observed without actually failing the outer test
// — Errorf/Fatalf on the embedded *testing.T would otherwise fail
// TestCheckGeneratedScript_CatchesABadScript itself, which is backwards:
// that test's whole point is confirming a failure WAS reported.
type recordingT struct {
	*testing.T
	failed bool
}

func (r *recordingT) Errorf(format string, args ...any) {
	r.failed = true
	r.Logf("(expected) "+format, args...)
}

func (r *recordingT) Fatalf(format string, args ...any) {
	r.failed = true
	r.Logf("(expected) "+format, args...)
	r.SkipNow()
}
