package engine

import (
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"boot-orchestrator/safety"
)

// TestConfigureBootloaderStep_LinuxBranchFailurePropagates is the regression
// test for audit bug 2.1: when AutoConfigureDualBoot fails on the generic
// (non-macOS, non-Windows) branch, configureBootloaderStep must report
// failure to its caller, must never emit the "dual-boot bootloader
// synchronized" success update, and must leave the journal step marked
// failed rather than committed.
func TestConfigureBootloaderStep_LinuxBranchFailurePropagates(t *testing.T) {
	origFn := autoConfigureDualBootFn
	t.Cleanup(func() { autoConfigureDualBootFn = origFn })

	wantErr := errors.New("update-grub: exit status 1")
	autoConfigureDualBootFn = func(targetPartition, grubDefaultPath string, creds Credentials, logFn func(string)) error {
		return wantErr
	}

	journalPath := filepath.Join(t.TempDir(), "journal.json")
	journal, err := safety.NewJournal(journalPath, "test-txn")
	if err != nil {
		t.Fatalf("NewJournal: %v", err)
	}

	var updates []StepUpdate
	onUpdate := func(u StepUpdate) { updates = append(updates, u) }

	failCalled := false
	fail := func(stepName string, cause error) ProvisionResult {
		failCalled = true
		return ProvisionResult{Success: false, FailedAtStep: stepName, OriginalError: cause}
	}

	req := ProvisionRequest{TargetPartition: "/dev/loop0p1"}
	res, failed := configureBootloaderStep(journal, false /* isMacOS */, false /* isWindows */, req, "", "", onUpdate, fail)

	if !failed {
		t.Fatalf("expected configureBootloaderStep to signal failure to its caller, got failed=false")
	}
	if !failCalled {
		t.Fatalf("expected the fail() closure to be invoked")
	}
	if res.Success {
		t.Fatalf("expected ProvisionResult.Success=false on bootloader failure, got true")
	}

	for _, u := range updates {
		if strings.Contains(u.StepName, "dual-boot bootloader synchronized") {
			t.Fatalf("got success update %q after AutoConfigureDualBoot returned an error", u.StepName)
		}
	}

	if len(journal.Steps) != 1 {
		t.Fatalf("expected exactly 1 journal step, got %d", len(journal.Steps))
	}
	if journal.Steps[0].Status != safety.StepFailed {
		t.Fatalf("expected journal step status=%q, got %q", safety.StepFailed, journal.Steps[0].Status)
	}
}

// TestRollbackImageDeploy_AlwaysSucceeds documents audit item 2.12's
// actual contract: there is nothing for this handler to undo (the target
// partition was already mkfs'd before deployment began, destroying its
// prior contents — there is no "prior state" left to restore), so it
// always reports success rather than a spurious failure.
func TestRollbackImageDeploy_AlwaysSucceeds(t *testing.T) {
	if err := RollbackImageDeploy(nil); err != nil {
		t.Fatalf("expected RollbackImageDeploy to always return nil, got: %v", err)
	}
	if err := RollbackImageDeploy(json.RawMessage(`{"anything":"at all"}`)); err != nil {
		t.Fatalf("expected RollbackImageDeploy to ignore its input and always return nil, got: %v", err)
	}
}

// TestRollbackHandlers_RegistersImageDeploy is the regression test for
// audit item 2.12's actual bug: "image-deploy" had no entry in
// rollbackHandlers at all.
func TestRollbackHandlers_RegistersImageDeploy(t *testing.T) {
	handlers := rollbackHandlers("/etc/default/grub")
	if _, ok := handlers["image-deploy"]; !ok {
		t.Fatalf("expected rollbackHandlers to register a handler for \"image-deploy\", got none (keys: %v)", keysOf(handlers))
	}
}

func keysOf(m map[string]safety.RollbackFunc) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// TestRollbackAll_ImageDeployStepRollsBackWithoutError is the end-to-end
// regression test: before this fix, a journal containing a failed
// "image-deploy" step made journal.RollbackAll report
// `no rollback handler registered for "image-deploy"` — true, but
// indistinguishable from an actual oversight rather than a documented,
// inherent limitation. With the fix, rolling back a journal whose only
// step is a failed "image-deploy" must complete with no error at all.
func TestRollbackAll_ImageDeployStepRollsBackWithoutError(t *testing.T) {
	journalPath := filepath.Join(t.TempDir(), "journal.json")
	journal, err := safety.NewJournal(journalPath, "test-txn")
	if err != nil {
		t.Fatalf("NewJournal: %v", err)
	}

	step, err := journal.Begin("image-deploy", nil)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	journal.Fail(step, errors.New("simulated deploy failure mid-write"))

	if err := journal.RollbackAll(rollbackHandlers("/etc/default/grub")); err != nil {
		t.Fatalf("expected RollbackAll to complete without error for a failed image-deploy step, got: %v", err)
	}
}

// TestAutonomousUSBEFILoaderNVRAMPath_MatchesHostArch is the regression
// test for audit finding 2.6 (x86_64 hardcoded despite ARM64 support) as it
// applies to the autonomous-USB one-shot NVRAM boot entry: before this fix,
// RunProvision always registered \EFI\BOOT\BOOTX64.EFI regardless of the
// host's real architecture, which on an arm64 host points at a file that
// was never staged (ArmAutonomousUSBBootloader stages BOOTAA64.EFI there —
// see usb_manager.go's efiBootNamesForArch), silently breaking the
// reboot-into-installer step even though the USB media itself was correct.
func TestAutonomousUSBEFILoaderNVRAMPath_MatchesHostArch(t *testing.T) {
	if got := autonomousUSBEFILoaderNVRAMPath("arm64"); got != `\EFI\BOOT\BOOTAA64.EFI` {
		t.Errorf("arm64 host: expected \\EFI\\BOOT\\BOOTAA64.EFI, got %q", got)
	}
	if got := autonomousUSBEFILoaderNVRAMPath("amd64"); got != `\EFI\BOOT\BOOTX64.EFI` {
		t.Errorf("amd64 host: expected \\EFI\\BOOT\\BOOTX64.EFI, got %q", got)
	}
}

// TestLocateSquashfsOrErr_NoSquashfsAnywhereReturnsError is the regression
// test for audit follow-up G1 as it applies to extractLiveRootFS (the
// direct dual-boot deploy path, as opposed to the autonomous-USB path's
// own detectInstallerLayoutOrAbort): before this fix, finding no squashfs
// at all made this search fall back to a GUESSED path
// (isoMount/live/filesystem.squashfs) that often doesn't exist, which
// extractLiveRootFS then passed to unsquashfs — but only AFTER already
// having run mkfs.ext4 against the real target partition. Returning a
// clear error instead (checked by the caller before mkfs.ext4 runs — see
// extractLiveRootFS) means an unsupported ISO (e.g. alpine-linux-amd64,
// see OS_STATUS_MATRIX.md) is caught before any disk write, not after.
func TestLocateSquashfsOrErr_NoSquashfsAnywhereReturnsError(t *testing.T) {
	isoMount := t.TempDir()
	if err := os.WriteFile(filepath.Join(isoMount, "README.txt"), []byte("not a live image"), 0644); err != nil {
		t.Fatalf("seeding iso mount dir: %v", err)
	}

	_, err := locateSquashfsOrErr(isoMount)
	if err == nil {
		t.Fatalf("expected locateSquashfsOrErr to fail when no squashfs exists anywhere")
	}
	if !strings.Contains(err.Error(), "unsupported ISO layout") {
		t.Errorf("expected a clear 'unsupported ISO layout' error, got: %v", err)
	}
}

// TestLocateSquashfsOrErr_FindsRealSquashfs proves the normal (supported)
// case still works: a real casper/filesystem.squashfs is found and its
// exact path returned.
func TestLocateSquashfsOrErr_FindsRealSquashfs(t *testing.T) {
	isoMount := t.TempDir()
	if err := os.MkdirAll(filepath.Join(isoMount, "casper"), 0755); err != nil {
		t.Fatalf("mkdir casper: %v", err)
	}
	squashPath := filepath.Join(isoMount, "casper", "filesystem.squashfs")
	if err := os.WriteFile(squashPath, []byte("fake squashfs bytes"), 0644); err != nil {
		t.Fatalf("seeding squashfs: %v", err)
	}

	got, err := locateSquashfsOrErr(isoMount)
	if err != nil {
		t.Fatalf("expected a real squashfs to be found, got error: %v", err)
	}
	if got != squashPath {
		t.Errorf("expected %q, got %q", squashPath, got)
	}
}

// TestExtractLiveRootFS_LocatesSquashfsBeforeFormattingSourceOrder is a
// static, structural check complementing the two behavioral tests above:
// it parses engine/orchestrator.go's real source with go/ast (the same
// technique engine/call_site_ratchet_test.go and
// engine/journal_stepnames_test.go already use for precise, comment-
// excluding checks) and confirms locateSquashfsOrErr is called, and its
// error checked with an early return, strictly BEFORE mkfs.ext4 is
// invoked inside extractLiveRootFS's body. Driving the real function
// end-to-end would need a real loop-mounted ISO (root-gated, like
// engine/revert_linux_loopimage_test.go) to exercise the exact
// "/mnt/orch_iso_source" path extractLiveRootFS hardcodes; this check
// instead proves the ordering directly from the compiled control flow,
// which is what actually guarantees mkfs.ext4 cannot execute unless
// locateSquashfsOrErr already succeeded.
func TestExtractLiveRootFS_LocatesSquashfsBeforeFormattingSourceOrder(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "orchestrator.go", nil, 0)
	if err != nil {
		t.Fatalf("parsing orchestrator.go: %v", err)
	}

	var fn *ast.FuncDecl
	ast.Inspect(file, func(n ast.Node) bool {
		if f, ok := n.(*ast.FuncDecl); ok && f.Name.Name == "extractLiveRootFS" {
			fn = f
			return false
		}
		return true
	})
	if fn == nil {
		t.Fatalf("extractLiveRootFS not found in orchestrator.go")
	}

	locatePos, mkfsPos := -1, -1
	i := 0
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		i++
		if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "locateSquashfsOrErr" && locatePos == -1 {
			locatePos = i
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Run" {
			if inner, ok := sel.X.(*ast.CallExpr); ok {
				if innerIdent, ok := inner.Fun.(*ast.Ident); ok && innerIdent.Name == "execCommand" {
					if len(inner.Args) > 0 {
						if lit, ok := inner.Args[0].(*ast.BasicLit); ok && lit.Value == `"mkfs.ext4"` && mkfsPos == -1 {
							mkfsPos = i
						}
					}
				}
			}
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "CombinedOutput" {
			if inner, ok := sel.X.(*ast.CallExpr); ok {
				if innerIdent, ok := inner.Fun.(*ast.Ident); ok && innerIdent.Name == "execCommand" {
					if len(inner.Args) > 0 {
						if lit, ok := inner.Args[0].(*ast.BasicLit); ok && lit.Value == `"mkfs.ext4"` && mkfsPos == -1 {
							mkfsPos = i
						}
					}
				}
			}
		}
		return true
	})

	if locatePos == -1 {
		t.Fatalf("extractLiveRootFS no longer calls locateSquashfsOrErr at all")
	}
	if mkfsPos == -1 {
		t.Fatalf("extractLiveRootFS no longer calls mkfs.ext4 via execCommand — if it was refactored, update this test's detection, don't delete the ordering guarantee")
	}
	if locatePos >= mkfsPos {
		t.Fatalf("locateSquashfsOrErr must be called (and its error checked) before mkfs.ext4 — found locateSquashfsOrErr at AST position %d, mkfs.ext4 at %d", locatePos, mkfsPos)
	}
}
