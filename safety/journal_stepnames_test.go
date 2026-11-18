package safety

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// repoRootForTest walks up from the current working directory (the
// package directory under `go test`) until it finds go.mod, so this test
// keeps working regardless of which directory `go test` is invoked from.
func repoRootForTest(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not find go.mod walking up from %s", dir)
		}
		dir = parent
	}
}

// findBeginCallStepNames statically scans every .go file under root
// (skipping .git and vendor) for a call of the form `X.Begin("name", ...)`
// and returns every literal string "name" it finds, along with the
// file:line it came from (for a readable failure message). It does not
// try to resolve X's type — any method literally named Begin, called with
// a string literal as its first argument, counts; that is deliberately
// broader than "only safety.Journal.Begin calls", so this test also
// catches a Begin call added against some future second type sharing the
// name, rather than silently ignoring it.
func findBeginCallStepNames(t *testing.T, root string) map[string]string {
	t.Helper()
	found := make(map[string]string)

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" {
			return nil
		}

		fset := token.NewFileSet()
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			// Not this test's job to validate syntax; a file that fails to
			// parse would already fail `go build`/`go vet`.
			return nil
		}

		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}

		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Begin" {
				return true
			}
			if len(call.Args) == 0 {
				return true
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			name, unquoteErr := strconv.Unquote(lit.Value)
			if unquoteErr != nil {
				return true
			}
			pos := fset.Position(call.Pos())
			found[name] = rel + ":" + strconv.Itoa(pos.Line)
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	return found
}

// TestAllJournalBeginCallSites_AreInAllowedStepNames is follow-up F17's
// completeness guard. allowedStepNames is the defense-in-depth allowlist
// validateLoadedJournal uses to reject a journal.json whose contents this
// engine's own code could not have produced (follow-up F12). If a new
// call site ever does journal.Begin("some-new-step", ...) without a
// matching allowedStepNames entry, that step would commit to disk just
// fine during the run that created it, but LoadJournal would then
// permanently refuse to load that journal ever again — it would look
// "unrecognized" to validateLoadedJournal, including on the very next run
// of the SAME binary that just wrote it. That is exactly the kind of
// silent mismatch a human reviewer could miss (the two lists live in
// different files, in different packages) but this static scan catches
// immediately: it finds every literal Begin("...") call across the whole
// repository and fails listing any step name used that isn't allowlisted.
func TestAllJournalBeginCallSites_AreInAllowedStepNames(t *testing.T) {
	root := repoRootForTest(t)
	used := findBeginCallStepNames(t, root)

	if len(used) == 0 {
		t.Fatalf("found zero journal.Begin(\"...\") call sites in the repo — this almost certainly means the scan itself is broken (wrong root, or the AST matcher stopped matching), not that the engine stopped using the journal")
	}

	for name, loc := range used {
		if !allowedStepNames[name] {
			t.Errorf("%s: journal.Begin(%q, ...) is not in safety.allowedStepNames — add it there (safety/journal.go) or this journal will fail to reload on its very next LoadJournal call", loc, name)
		}
	}
}

// TestAllowedStepNames_EveryEntryHasAUseOrIsDocumentedAsReserved is the
// complementary direction: allowedStepNames currently also lists six
// names ("docker-isolation", "uefi-set-boot-next", "uefi-set-boot-order",
// "bcd-mutation", "bcd-add-entry", "grub-set-default") that exist only as
// RollbackAll handler map keys in engine/orchestrator.go — no code path
// anywhere in the repo actually calls journal.Begin with any of them
// (confirmed by this same scan). That is not a security problem by itself
// (an overly permissive allowlist entry, unlike a missing one, can't let a
// tampered journal do anything new — RollbackAll still gates on a step
// actually being present), but it is dead allowlist surface worth keeping
// visible rather than silently accumulating more of over time. This test
// pins the exact current set of "reserved but unused" names so it fails
// loudly — forcing a conscious update to this list, not a silent drift —
// if that set ever grows or shrinks.
func TestAllowedStepNames_EveryEntryHasAUseOrIsDocumentedAsReserved(t *testing.T) {
	root := repoRootForTest(t)
	used := findBeginCallStepNames(t, root)

	reservedButUnused := map[string]bool{
		"docker-isolation":    true,
		"uefi-set-boot-next":  true,
		"uefi-set-boot-order": true,
		"bcd-mutation":        true,
		"bcd-add-entry":       true,
		"grub-set-default":    true,
	}

	for name := range allowedStepNames {
		_, isUsed := used[name]
		_, isReserved := reservedButUnused[name]
		if !isUsed && !isReserved {
			t.Errorf("allowedStepNames contains %q, which has no journal.Begin call site anywhere in the repo and is not in this test's reservedButUnused list — either it is now used (add it to a Begin call, or it already is and this test's AST matcher missed it) or it is newly dead and should be removed from allowedStepNames, or newly reserved and should be added to reservedButUnused with a reason", name)
		}
	}
	for name := range reservedButUnused {
		if !allowedStepNames[name] {
			t.Errorf("reservedButUnused lists %q but it is no longer in allowedStepNames — update this test's list to match", name)
		}
		if _, isUsed := used[name]; isUsed {
			t.Errorf("reservedButUnused lists %q as unused, but it now has a real journal.Begin call site — move it out of reservedButUnused", name)
		}
	}
}
