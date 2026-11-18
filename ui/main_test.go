package ui

import (
	"os"
	"path/filepath"
	"testing"

	"boot-orchestrator/safety"
)

// TestMain redirects the journal's default candidate directories to a
// scratch directory for this whole test binary run (follow-up F17).
// NewModel() unconditionally calls safety.LoadJournal(safety.DefaultJournalPath())
// to pick up any unfinished session, so without this every ui package
// test that constructs a Model — directly or via sendKey/mustUIModel
// helpers — really created /var/lib/boot-orchestrator or
// /var/tmp/boot-orchestrator (mode 0700) on whatever machine ran
// `go test ./ui/...`, not just this package's own journal-specific tests.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "boot-orchestrator-ui-test-journal-*")
	if err != nil {
		panic(err)
	}
	restore := safety.SetJournalCandidateDirsForTesting([]string{filepath.Join(dir, "lib"), filepath.Join(dir, "tmp")})

	code := m.Run()

	restore()
	os.RemoveAll(dir)
	os.Exit(code)
}
