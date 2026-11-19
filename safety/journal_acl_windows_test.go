//go:build windows

package safety

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestRestrictToAdministrators_LocksDownACL is the real,
// Windows-specific verification RestrictToAdministrators
// needs: os.FileInfo.Mode() can't see a real ACL on Windows at all (see
// journal_security_test.go's TestDefaultJournalPath_DirectoryIsNotWorldOrGroupWritable,
// skipped here for exactly that reason), so this instead runs the real
// icacls.exe (the same way RestrictToAdministrators itself
// does) against a real temp directory and parses its actual output:
// Administrators and SYSTEM must each have an explicit grant, and
// broadly-scoped identities this directory must NOT be accessible to
// (Everyone, the built-in Users group, Authenticated Users — any of
// which would mean the lockdown silently failed to replace the
// inherited %ProgramData%-style default permissions) must not appear at
// all.
//
// Not run or verified against a real Windows machine from this (Linux)
// development sandbox — written to documented icacls output format;
// this project's own CI (windows-latest) is the first real execution.
func TestRestrictToAdministrators_LocksDownACL(t *testing.T) {
	if _, err := exec.LookPath("icacls.exe"); err != nil {
		t.Skip("icacls.exe not found on PATH, skipping")
	}

	dir := t.TempDir()
	if err := RestrictToAdministrators(dir); err != nil {
		t.Fatalf("RestrictToAdministrators: %v", err)
	}

	out, err := exec.Command("icacls.exe", dir).CombinedOutput()
	if err != nil {
		t.Fatalf("icacls %s (verifying): %v\n%s", dir, err, out)
	}
	report := string(out)

	for _, want := range []string{"Administrators", "SYSTEM"} {
		if !strings.Contains(report, want) {
			t.Errorf("expected icacls output to grant %q, got:\n%s", want, report)
		}
	}

	for _, forbidden := range []string{"Everyone", "BUILTIN\\Users", "Authenticated Users"} {
		if strings.Contains(report, forbidden) {
			t.Errorf("expected the inherited, broadly-scoped %q grant to have been removed by /inheritance:r, but it's still present:\n%s", forbidden, report)
		}
	}
}

// TestSetUpProtectedJournalDir_CreatesAndRestrictsOnWindows is the
// end-to-end proof that DefaultJournalPath's own call path actually
// wires the two steps together: creating the directory AND restricting
// its ACL, not just one or the other.
func TestSetUpProtectedJournalDir_CreatesAndRestrictsOnWindows(t *testing.T) {
	if _, err := exec.LookPath("icacls.exe"); err != nil {
		t.Skip("icacls.exe not found on PATH, skipping")
	}

	parent := t.TempDir()
	dir := filepath.Join(parent, "journal-acl-test")

	if !setUpProtectedJournalDir(dir) {
		t.Fatalf("setUpProtectedJournalDir(%s) reported failure", dir)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Fatalf("expected %s to exist as a directory, stat error: %v", dir, err)
	}

	out, err := exec.Command("icacls.exe", dir).CombinedOutput()
	if err != nil {
		t.Fatalf("icacls %s (verifying): %v\n%s", dir, err, out)
	}
	if !strings.Contains(string(out), "Administrators") {
		t.Errorf("expected the directory setUpProtectedJournalDir created to also have its ACL restricted, got:\n%s", out)
	}
}
