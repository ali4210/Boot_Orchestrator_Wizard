package ui

import (
	"os"
	"path/filepath"
	"testing"

	"boot-orchestrator/engine"
)

// TestClientDeploy_PickerSelectsISO_CarriesPathIntoProvisionRequest is
// follow-up F16's reproduction/regression test: drives real key messages
// through ScreenModeSelect -> ScreenAdvancedRoleSelect -> ScreenClientPanel
// -> ScreenFilePicker, selects a fake .iso, and asserts the result lands in
// the fields startProvisioning (ui/model.go) uses verbatim to build
// engine.ProvisionRequest.ImageURL — see the "if m.overridePayloadURL != ""
// { primaryURL = m.overridePayloadURL; mirrors = []string{m.overridePayloadURL}
// }" branch there. startProvisioning itself is not called here: it also
// calls safety.NewJournal/SetSessionState against the REAL, non-injectable
// safety.DefaultJournalPath() — the one real on-disk journal this whole
// project is never allowed to touch in a test — so asserting on
// m.overridePayloadURL/m.selectedOS.DownloadURL is the correct boundary,
// not a shortcut.
//
// Before follow-up F16, ScreenFilePicker's selection handler recognized
// only two destinations (SCP push / seeder dashboard — both server-role);
// a file picked from this client-deploy launch site silently fell through
// to the seeder-dashboard branch instead, so the picked .iso never reached
// ProvisionRequest at all.
func TestClientDeploy_PickerSelectsISO_CarriesPathIntoProvisionRequest(t *testing.T) {
	isoPath := filepath.Join(t.TempDir(), "custom-distro.iso")
	isoBytes := []byte("fake iso payload bytes for sizing")
	if err := os.WriteFile(isoPath, isoBytes, 0644); err != nil {
		t.Fatalf("staging fake iso: %v", err)
	}

	m := NewModel("")
	m.screen = ScreenModeSelect

	m = sendKey(t, m, "2") // advanced mode -> ScreenAdvancedRoleSelect
	if m.screen != ScreenAdvancedRoleSelect {
		t.Fatalf("expected ScreenAdvancedRoleSelect, got %v", m.screen)
	}

	m = sendKey(t, m, "1") // client role -> ScreenClientPanel
	if m.screen != ScreenClientPanel {
		t.Fatalf("expected ScreenClientPanel, got %v", m.screen)
	}

	m = sendKey(t, m, "2") // "Deploy from Local Payload"
	if m.hasStagedPayload {
		t.Skip("a staged payload exists at /var/tmp or /tmp on this machine, which short-circuits straight to ScreenCustomBootModeSelect instead of the picker this test exercises — not this test's concern")
	}
	if m.screen != ScreenFilePicker {
		t.Fatalf("expected ScreenFilePicker, got %v", m.screen)
	}
	if !m.pickerForClientDeploy {
		t.Fatalf("expected pickerForClientDeploy to be set when launched from ScreenClientPanel's local-payload option")
	}

	// Feed a synthetic directory listing directly (bypassing the real
	// filesystem read loadDirCmd would otherwise perform) containing our
	// fake .iso, exactly as the real dirContentsMsg handler expects.
	got, _ := m.Update(dirContentsMsg{
		dir: filepath.Dir(isoPath),
		items: []engine.FileItem{
			{Name: filepath.Base(isoPath), Path: isoPath, IsDir: false, Ext: ".iso"},
		},
	})
	m = mustUIModel(t, got)
	if len(m.pickerItems) != 1 {
		t.Fatalf("expected 1 picker item, got %d", len(m.pickerItems))
	}

	m = sendKey(t, m, "enter") // select the .iso

	if m.screen != ScreenCustomBootModeSelect {
		t.Fatalf("expected ScreenCustomBootModeSelect after picking the .iso, got %v", m.screen)
	}
	if m.overridePayloadURL != isoPath {
		t.Fatalf("expected overridePayloadURL %q, got %q", isoPath, m.overridePayloadURL)
	}
	if m.selectedOS == nil || m.selectedOS.DownloadURL != isoPath {
		t.Fatalf("expected selectedOS.DownloadURL %q, got %+v", isoPath, m.selectedOS)
	}
	if m.selectedOS.ApproxSizeBytes != uint64(len(isoBytes)) {
		t.Errorf("expected ApproxSizeBytes %d (from a real os.Stat of the picked file), got %d", len(isoBytes), m.selectedOS.ApproxSizeBytes)
	}
}

// TestClientDeploy_PickerEscape_ReturnsToClientPanel is the regression
// test for the other half of the same bug: before follow-up F16, pressing
// esc from this picker (launched from ScreenClientPanel) sent the user to
// ScreenSeederDashboard — a server-role screen with no way back into the
// client deploy flow — instead of back to ScreenClientPanel.
func TestClientDeploy_PickerEscape_ReturnsToClientPanel(t *testing.T) {
	m := NewModel("")
	m.screen = ScreenFilePicker
	m.pickerForClientDeploy = true
	m.pickerForSCP = false

	m = sendKey(t, m, "esc")

	if m.screen != ScreenClientPanel {
		t.Fatalf("expected esc from the client-deploy picker to return to ScreenClientPanel, got %v", m.screen)
	}
}

// TestNewModel_DirectISOPath_CarriesPathIntoProvisionRequest is follow-up
// F16's test for "direct .iso path entry": the --iso CLI flag's value
// (main.go's CustomISOPath) flowing straight into ui.NewModel. Before this
// fix, ui.NewModel took no arguments at all, so --iso set a package
// variable that nothing downstream ever read — no sequence of
// tea.KeyMsgs could ever make it reach ProvisionRequest, because the data
// never entered the Model in the first place.
func TestNewModel_DirectISOPath_CarriesPathIntoProvisionRequest(t *testing.T) {
	isoPath := filepath.Join(t.TempDir(), "direct-entry.iso")
	isoBytes := []byte("another fake iso payload")
	if err := os.WriteFile(isoPath, isoBytes, 0644); err != nil {
		t.Fatalf("staging fake iso: %v", err)
	}

	m := NewModel(isoPath)

	if m.fatalErr != nil {
		t.Fatalf("expected no fatal error for a valid --iso path, got %v", m.fatalErr)
	}
	if m.screen != ScreenCustomBootModeSelect {
		t.Fatalf("expected --iso to land directly on ScreenCustomBootModeSelect, got %v", m.screen)
	}
	if m.overridePayloadURL != isoPath {
		t.Fatalf("expected overridePayloadURL %q, got %q", isoPath, m.overridePayloadURL)
	}
	if m.selectedOS == nil || m.selectedOS.DownloadURL != isoPath {
		t.Fatalf("expected selectedOS.DownloadURL %q, got %+v", isoPath, m.selectedOS)
	}
	if m.selectedOS.ApproxSizeBytes != uint64(len(isoBytes)) {
		t.Errorf("expected ApproxSizeBytes %d, got %d", len(isoBytes), m.selectedOS.ApproxSizeBytes)
	}
}

// TestNewModel_DirectISOPath_MissingFileIsAFatalErrorNotSilentlyIgnored
// proves a nonexistent --iso path is surfaced loudly (m.fatalErr, rendered
// on every screen per ui/view.go) rather than silently falling back to
// the normal catalog flow with no explanation.
func TestNewModel_DirectISOPath_MissingFileIsAFatalErrorNotSilentlyIgnored(t *testing.T) {
	m := NewModel(filepath.Join(t.TempDir(), "does-not-exist.iso"))

	if m.fatalErr == nil {
		t.Fatalf("expected a fatal error for a nonexistent --iso path")
	}
	if m.screen != ScreenModeSelect {
		t.Fatalf("expected the screen to remain ScreenModeSelect on a bad --iso path, got %v", m.screen)
	}
	if m.overridePayloadURL != "" {
		t.Fatalf("expected overridePayloadURL to stay empty on a bad --iso path, got %q", m.overridePayloadURL)
	}
}

// TestNewModel_DirectISOPath_DirectoryIsAFatalError proves a --iso value
// that points at a directory (not a file) is also rejected loudly rather
// than, say, being passed straight through to ImageURL and failing much
// later and more confusingly inside the download/copy step.
func TestNewModel_DirectISOPath_DirectoryIsAFatalError(t *testing.T) {
	m := NewModel(t.TempDir())

	if m.fatalErr == nil {
		t.Fatalf("expected a fatal error for a --iso path that is a directory")
	}
	if m.screen != ScreenModeSelect {
		t.Fatalf("expected the screen to remain ScreenModeSelect, got %v", m.screen)
	}
}

// TestNewModel_NoISOPath_DoesNotAlterStartupScreen is a sanity check that
// the new --iso wiring is a no-op when the flag isn't passed: must not
// change NewModel's behavior for every other caller.
func TestNewModel_NoISOPath_DoesNotAlterStartupScreen(t *testing.T) {
	m := NewModel("")

	if m.fatalErr != nil {
		t.Fatalf("expected no fatal error with no --iso path, got %v", m.fatalErr)
	}
	if m.screen != ScreenModeSelect {
		t.Fatalf("expected ScreenModeSelect with no --iso path, got %v", m.screen)
	}
	if m.overridePayloadURL != "" {
		t.Fatalf("expected overridePayloadURL to stay empty with no --iso path, got %q", m.overridePayloadURL)
	}
}
