package engine

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"
)

// Phase 5 item 33: a ratchet, not a fix. Migrating all ~367 raw
// exec.Command call sites and ~149 filesystem-write call sites onto the
// Runner/FS interfaces (see docs/dryrun-inventory.md) is explicitly NOT
// attempted in one pass — too large, too risky to do without the same
// reproduce-first rigor this whole project otherwise holds every change
// to. This test instead stops the debt from growing silently: it counts
// the real, current number of raw call sites per file and fails if any
// file's count goes UP without a deliberate, visible change to the
// baseline map below (going down is always fine — no update needed to
// shrink debt). A brand new file with raw call sites and no baseline
// entry at all also fails, so a newly-added file can't quietly import
// the same debt this ratchet exists to stop.
//
// This is a file-level, not line-level, ratchet deliberately: line
// numbers shift on any unrelated edit above them, which would make a
// line-based baseline brittle and force constant unrelated updates. A
// file-level count still catches the real regression this guards
// against — someone adding a new unintercepted exec.Command/os.WriteFile
// call to an existing file — while tolerating normal code churn that
// doesn't change the count.

// execCommandBaseline is the real, current count of raw "exec.Command("
// call sites per file (engine/ui, non-test), as of the commit that added
// this test. Regenerate via:
//
//	grep -rln "exec\.Command(" engine/*.go ui/*.go | grep -v _test.go | \
//	  while read f; do echo "$(grep -c 'exec\.Command(' "$f") $(basename $f)"; done
var execCommandBaseline = map[string]int{
	"orchestrator.go":                93,
	"rootfs.go":                      47, // dead code (item 26) — still tracked so it can't grow either
	"revert_linux.go":                41,
	"usb_manager.go":                 40,
	"backup.go":                      38,
	"bcd_qemu.go":                    16,
	"engine_linux.go":                14,
	"uefi.go":                        11,
	"engine_darwin.go":               11,
	"wim_deployer.go":                10,
	"revert_darwin.go":               9,
	"revert_windows.go":              7,
	"isolate.go":                     5,
	"engine_windows.go":              5,
	"runner.go":                      2, // the Runner interception point itself (its own 2 doc comments mention "exec.Command(" as example text — a naive grep count would say 4; this baseline reflects the real, AST-parsed code count, same as what this test actually measures)
	"dryrun.go":                      2, // item 45: the execCommand interception point itself — dryRunExecCommand's two real, harmless stand-in commands ("true" and "cmd /c exit 0"), same exemption pattern as runner.go above
	"slipstream.go":                  3,
	"partition.go":                   3,
	"model.go":                       3, // ui/model.go
	"file_dialog.go":                 2,
	"autodiscover.go":                2,
	"efi_bootentry.go":               1,
	"credentials.go":                 1,
	"autonomous_installer_script.go": 1,
}

// fsWriteBaseline is the equivalent baseline for filesystem-write call
// sites (os.WriteFile/Remove/RemoveAll/MkdirAll/Symlink/Rename/Mkdir).
// Regenerate via:
//
//	grep -rlE "os\.(WriteFile|Remove|RemoveAll|MkdirAll|Symlink|Rename|Mkdir)\(" \
//	  engine/*.go ui/*.go | grep -v _test.go | while read f; do \
//	  echo "$(grep -cE 'os\.(WriteFile|Remove|RemoveAll|MkdirAll|Symlink|Rename|Mkdir)\(' "$f") $(basename $f)"; done
var fsWriteBaseline = map[string]int{
	"bootloader.go":         24, // was 26 — item 32 migrated 2 onto fsRunner
	"orchestrator.go":       22,
	"rootfs.go":             21, // dead code (item 26) — still tracked
	"usb_manager.go":        14,
	"revert_linux.go":       10,
	"backup.go":             8,
	"bcd_qemu.go":           6,
	"streamer.go":           5,
	"stream_decoder.go":     5,
	"scp_streamer.go":       5,
	"revert_windows.go":     5,
	"isolate.go":            5,
	"fs.go":                 6, // the FS interception point itself (item 45 extended it with Symlink/Rename, +2)
	"wim_deployer.go":       3,
	"resilient_streamer.go": 3,
	"uefi.go":               2,
	"provision.go":          2, // ui/provision.go
	"partition.go":          2,
	"model.go":              2, // ui/model.go
	"slipstream.go":         1,
	"revert_darwin.go":      1,
	"payload_normalizer.go": 1,
}

// countCallSites walks dir (engine/ or ui/) and returns, for each .go
// file (excluding _test.go), how many top-level calls match any of the
// given (package, function) pairs — e.g. {"exec", "Command"} for
// exec.Command(...), or {"os", "WriteFile"} for os.WriteFile(...).
func countCallSites(t *testing.T, dir string, targets [][2]string) map[string]int {
	t.Helper()
	counts := make(map[string]int)

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".go" {
			continue
		}
		name := e.Name()
		if len(name) > 8 && name[len(name)-8:] == "_test.go" {
			continue
		}

		fset := token.NewFileSet()
		path := filepath.Join(dir, name)
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			t.Fatalf("parsing %s: %v", path, parseErr)
		}

		n := 0
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkgIdent, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			for _, target := range targets {
				if pkgIdent.Name == target[0] && sel.Sel.Name == target[1] {
					n++
					return true
				}
			}
			return true
		})
		if n > 0 {
			counts[name] = n
		}
	}
	return counts
}

// checkRatchet compares actual per-file counts against baseline, failing
// on any increase or any new, un-baselined file with a nonzero count.
func checkRatchet(t *testing.T, label string, baseline, actual map[string]int) {
	t.Helper()
	for file, got := range actual {
		want, known := baseline[file]
		if !known {
			t.Errorf("%s: %s has %d call site(s) but no baseline entry at all — if this is legitimate new debt, add it to the baseline map deliberately (and say why); if it's avoidable, route it through the Runner/FS interception point instead", label, file, got)
			continue
		}
		if got > want {
			t.Errorf("%s: %s has %d call site(s), baseline allows %d — a new unintercepted call site was added. Either route it through the Runner/FS interception point, or bump the baseline deliberately with a reason", label, file, got, want)
		}
	}
}

// scannedDirs are the directories this ratchet covers. engine/ and ui/ are
// the two docs/dryrun-inventory.md's counts were drawn from; cmd/orchestrator
// was added in item 49 after a full-repo sweep for 2.4's closure found a
// real, unintercepted os.Remove call site there (cmd/orchestrator/revert.go)
// that this ratchet would not have caught otherwise — the ratchet itself
// hadn't been extended to look. Resolved relative to this test file's own
// package directory (engine/), so the test works regardless of which
// directory `go test` is invoked from.
var scannedDirs = []string{".", "../ui", "../cmd/orchestrator"}

func TestCallSiteRatchet_ExecCommand(t *testing.T) {
	actual := make(map[string]int)
	for _, dir := range scannedDirs {
		for file, n := range countCallSites(t, resolveDirForTest(t, dir), [][2]string{{"exec", "Command"}}) {
			actual[file] = n
		}
	}
	checkRatchet(t, "exec.Command", execCommandBaseline, actual)
}

func TestCallSiteRatchet_FilesystemWrites(t *testing.T) {
	targets := [][2]string{
		{"os", "WriteFile"}, {"os", "Remove"}, {"os", "RemoveAll"},
		{"os", "MkdirAll"}, {"os", "Symlink"}, {"os", "Rename"}, {"os", "Mkdir"},
	}
	actual := make(map[string]int)
	for _, dir := range scannedDirs {
		for file, n := range countCallSites(t, resolveDirForTest(t, dir), targets) {
			actual[file] = n
		}
	}
	checkRatchet(t, "filesystem-write", fsWriteBaseline, actual)
}

// resolveDirForTest resolves dir relative to this test file's own
// package directory (engine/).
func resolveDirForTest(t *testing.T, dir string) string {
	t.Helper()
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatalf("resolving %s: %v", dir, err)
	}
	return abs
}
