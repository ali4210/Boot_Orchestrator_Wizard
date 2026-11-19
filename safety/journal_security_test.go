package safety

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// withScratchJournalCandidateDirs redirects journalUnixCandidateDirs to
// two directories under t.TempDir() for the duration of the test,
// restoring the real values afterward (follow-up F17) — so a test calling
// DefaultJournalPath() never really creates /var/lib/boot-orchestrator or
// /var/tmp/boot-orchestrator on the machine running the test. Confirmed
// this was a real, not just theoretical, problem: before this fix, running
// this package's own test suite left a real, root-owned
// /var/lib/boot-orchestrator directory sitting on the test machine
// (created by whichever test happened to run with elevated privileges)
// and a real /var/tmp/boot-orchestrator one even for an unprivileged run.
func withScratchJournalCandidateDirs(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	restore := SetJournalCandidateDirsForTesting([]string{filepath.Join(dir, "lib"), filepath.Join(dir, "tmp")})
	t.Cleanup(restore)
}

// TestSetJournalCandidateDirsForTesting_OverridesAndRestores is the
// regression test proving the follow-up F17 fix actually works. It
// deliberately never calls DefaultJournalPath() with the REAL candidate
// dirs in effect (that call is exactly the thing that creates
// /var/lib/boot-orchestrator or /var/tmp/boot-orchestrator for real) —
// instead it inspects journalUnixCandidateDirs directly, which this
// in-package test file can do without invoking the function at all.
func TestSetJournalCandidateDirsForTesting_OverridesAndRestores(t *testing.T) {
	original := journalUnixCandidateDirs
	scratch := []string{"/nonexistent-scratch-a", "/nonexistent-scratch-b"}

	restore := SetJournalCandidateDirsForTesting(scratch)
	if !reflect.DeepEqual(journalUnixCandidateDirs, scratch) {
		t.Fatalf("expected journalUnixCandidateDirs to be overridden to %v, got %v", scratch, journalUnixCandidateDirs)
	}

	restore()
	if !reflect.DeepEqual(journalUnixCandidateDirs, original) {
		t.Fatalf("expected restore() to put the original candidate dirs back; want %v, got %v", original, journalUnixCandidateDirs)
	}
}

// TestDefaultJournalPath_ResolvesOutsideTheCurrentDirectory is the
// regression test for follow-up F12's core fix: the journal must no
// longer default to a path inside whatever directory the tool happens to
// be invoked from (the git repo checkout), which an unprivileged user can
// write to. At least one of DefaultJournalPath's candidate directories
// (/var/lib, /var/tmp, or the user's home) should be creatable in any
// normal test environment, so the bare repo-relative fallback should never
// actually be hit here.
func TestDefaultJournalPath_ResolvesOutsideTheCurrentDirectory(t *testing.T) {
	withScratchJournalCandidateDirs(t)
	path := DefaultJournalPath()
	if path == "orchestrator_journal.json" {
		t.Fatalf("DefaultJournalPath fell all the way back to the repo-relative legacy path in a normal test environment — none of /var/lib, /var/tmp, or $HOME/.local/state were writable?")
	}
	if !filepath.IsAbs(path) {
		t.Fatalf("expected an absolute path, got %q", path)
	}
	if filepath.Base(path) != "journal.json" {
		t.Fatalf("expected the file to be named journal.json, got %q", path)
	}
}

// TestDefaultJournalPath_DirectoryIsNotWorldOrGroupWritable proves the
// resolved directory's permission bits actually restrict access on Unix,
// where os.MkdirAll's mode parameter is a real, enforced permission.
func TestDefaultJournalPath_DirectoryIsNotWorldOrGroupWritable(t *testing.T) {
	if runtime.GOOS == "windows" {
		// Windows has no POSIX permission bits at all: os.FileInfo.Mode()
		// there is synthesized from the read-only ATTRIBUTE bit only and
		// never reflects a real ACL, so this specific check (fi.Mode().
		// Perm()&0077) is testing the wrong thing on Windows regardless
		// of how the directory is actually protected — confirmed for
		// real on windows-latest CI, which failed this exact assertion
		// with "got 777" even once the directory's ACL was correctly
		// locked down by RestrictToAdministrators. The real
		// Windows equivalent is verified separately, via icacls output,
		// by TestRestrictJournalDirToAdministrators_LocksDownACL in
		// journal_acl_windows_test.go.
		t.Skip("os.FileInfo.Mode() on Windows never reflects a real ACL, only the read-only attribute bit — this check is meaningless there; see journal_acl_windows_test.go for the real Windows-specific verification")
	}
	withScratchJournalCandidateDirs(t)
	path := DefaultJournalPath()
	dir := filepath.Dir(path)
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat %s: %v", dir, err)
	}
	if fi.Mode().Perm()&0077 != 0 {
		t.Fatalf("expected %s to have no group/other permission bits, got %o", dir, fi.Mode().Perm())
	}
}

func validSession() SessionState {
	return SessionState{
		TargetOSID:      "ubuntu-24.04",
		TargetDisk:      "/dev/sdz",
		TargetPartition: "/dev/sdz2",
		ShrunkPartition: "/dev/sdz1",
	}
}

// TestValidateLoadedJournal_AcceptsOrdinarySession proves the baseline: a
// normal, well-formed journal produced by this engine's own code passes
// validation.
func TestValidateLoadedJournal_AcceptsOrdinarySession(t *testing.T) {
	j := &Journal{
		Session: validSession(),
		Steps: []*Step{
			{Name: "image-download", Status: StepCommitted},
			{Name: "partition-shrink", Status: StepCommitted},
		},
	}
	if err := validateLoadedJournal(j); err != nil {
		t.Fatalf("expected a well-formed journal to validate, got: %v", err)
	}
}

// TestValidateLoadedJournal_RejectsPathTraversal is the regression test
// for follow-up F12's "no path traversal" requirement: a tampered
// TargetDisk containing ".." must be rejected.
func TestValidateLoadedJournal_RejectsPathTraversal(t *testing.T) {
	cases := []string{
		"/dev/../etc/passwd",
		"../../etc/shadow",
		"/dev/sdz/../../etc/shadow",
	}
	for _, bad := range cases {
		sess := validSession()
		sess.TargetDisk = bad
		j := &Journal{Session: sess}
		if err := validateLoadedJournal(j); err == nil {
			t.Errorf("expected TargetDisk=%q (path traversal) to be rejected", bad)
		}
	}
}

// TestValidateLoadedJournal_RejectsNonDevicePath proves a field that
// doesn't even look like a Linux block device path (e.g. an attacker
// pointing it at an arbitrary file) is rejected, not just literal ".."
// traversal.
func TestValidateLoadedJournal_RejectsNonDevicePath(t *testing.T) {
	cases := []string{"/etc/passwd", "C:\\Windows\\System32", "not-a-path-at-all"}
	for _, bad := range cases {
		sess := validSession()
		sess.TargetPartition = bad
		j := &Journal{Session: sess}
		if err := validateLoadedJournal(j); err == nil {
			t.Errorf("expected TargetPartition=%q (not a device path) to be rejected", bad)
		}
	}
}

// TestValidateLoadedJournal_RejectsUnknownStepName is the regression test
// for follow-up F12's "step names from an allowlist" requirement: a step
// name this engine's own code never creates must be rejected, so a
// tampered journal can't make RollbackAll invoke an unexpected handler.
func TestValidateLoadedJournal_RejectsUnknownStepName(t *testing.T) {
	j := &Journal{
		Session: validSession(),
		Steps:   []*Step{{Name: "rm-rf-everything", Status: StepCommitted}},
	}
	if err := validateLoadedJournal(j); err == nil {
		t.Fatalf("expected an unrecognized step name to be rejected")
	}
}

// TestValidateLoadedJournal_EmptyFieldsAreAllowed proves empty optional
// fields (not every session populates every field) don't get rejected as
// if they were malformed paths.
func TestValidateLoadedJournal_EmptyFieldsAreAllowed(t *testing.T) {
	j := &Journal{Session: SessionState{}}
	if err := validateLoadedJournal(j); err != nil {
		t.Fatalf("expected an all-empty session to validate, got: %v", err)
	}
}

// TestLoadJournal_RejectsTamperedStepNameOnDisk is an end-to-end
// regression test proving the validation is actually wired into
// LoadJournal, not just reachable as a standalone function: a raw JSON
// file hand-edited to contain an unrecognized step name must fail to
// load.
func TestLoadJournal_RejectsTamperedStepNameOnDisk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.json")
	raw := `{
		"txn_id": "test",
		"session": {"target_disk": "/dev/sdz"},
		"steps": [{"id": "x-001", "name": "launch-reverse-shell", "status": "committed"}]
	}`
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	if _, err := LoadJournal(path); err == nil {
		t.Fatalf("expected LoadJournal to reject a tampered step name")
	} else if !strings.Contains(err.Error(), "validation") {
		t.Fatalf("expected the error to mention validation, got: %v", err)
	}
}

// TestLoadJournal_RejectsPathTraversalOnDisk is the same end-to-end check
// for a tampered device path.
func TestLoadJournal_RejectsPathTraversalOnDisk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.json")
	raw := `{
		"txn_id": "test",
		"session": {"target_disk": "/dev/../../../etc/shadow"},
		"steps": []
	}`
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	if _, err := LoadJournal(path); err == nil {
		t.Fatalf("expected LoadJournal to reject a path-traversal device path")
	}
}

// TestLoadJournal_AcceptsAWellFormedJournalRoundTrip proves validation
// doesn't produce false positives against the engine's own real output:
// create a journal via NewJournal, add a real step, and confirm it loads
// back cleanly.
func TestLoadJournal_AcceptsAWellFormedJournalRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.json")
	j, err := NewJournal(path, "test-txn")
	if err != nil {
		t.Fatalf("NewJournal: %v", err)
	}
	if err := j.SetSessionState(validSession()); err != nil {
		t.Fatalf("SetSessionState: %v", err)
	}
	if _, err := j.Begin("image-download", nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}

	loaded, err := LoadJournal(path)
	if err != nil {
		t.Fatalf("expected a well-formed, real journal to load cleanly, got: %v", err)
	}
	if loaded.Session.TargetDisk != "/dev/sdz" {
		t.Fatalf("unexpected loaded session: %+v", loaded.Session)
	}
}
