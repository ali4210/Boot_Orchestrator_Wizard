package ui

import (
	"testing"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"

	"boot-orchestrator/discovery"
)

func mustUIModel(t *testing.T, got tea.Model) Model {
	t.Helper()
	m, ok := got.(Model)
	if !ok {
		t.Fatalf("expected a ui.Model, got %T", got)
	}
	return m
}

func sendKey(t *testing.T, m Model, key string) Model {
	t.Helper()
	var msg tea.KeyMsg
	switch key {
	case "enter":
		msg = tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		msg = tea.KeyMsg{Type: tea.KeyEsc}
	default:
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
	}
	got, _ := m.Update(msg)
	return mustUIModel(t, got)
}

// TestOSSelect_UnverifiedChecksumEntry_RoutesThroughConfirmScreen is the
// regression test for audit follow-up F13: selecting a catalog entry with
// no SHA256 checksum must route through ScreenUnverifiedDownloadConfirm
// instead of proceeding straight into disk/USB selection.
func TestOSSelect_UnverifiedChecksumEntry_RoutesThroughConfirmScreen(t *testing.T) {
	m := NewModel("")
	m.screen = ScreenOSSelect
	m.isAdvancedMode = false
	m.osList.SetItems([]list.Item{
		osListItem{entry: discovery.Entry{ID: "no-checksum", Distro: "TestOS", Version: "1.0"}},
	})

	m = sendKey(t, m, "enter")

	if m.screen != ScreenUnverifiedDownloadConfirm {
		t.Fatalf("expected ScreenUnverifiedDownloadConfirm after selecting a checksum-less entry, got screen=%v", m.screen)
	}
	if m.selectedOS == nil || m.selectedOS.ID != "no-checksum" {
		t.Fatalf("expected selectedOS to be set to the chosen entry, got %+v", m.selectedOS)
	}
}

// TestOSSelect_VerifiedChecksumEntry_SkipsConfirmScreen proves an entry
// that DOES have a checksum proceeds directly, with no extra prompt.
func TestOSSelect_VerifiedChecksumEntry_SkipsConfirmScreen(t *testing.T) {
	m := NewModel("")
	m.screen = ScreenOSSelect
	m.isAdvancedMode = false
	m.osList.SetItems([]list.Item{
		osListItem{entry: discovery.Entry{ID: "has-checksum", Distro: "TestOS", Version: "1.0", SHA256: "deadbeef"}},
	})

	m = sendKey(t, m, "enter")

	if m.screen == ScreenUnverifiedDownloadConfirm {
		t.Fatalf("expected a checksummed entry to skip the unverified-download confirmation screen")
	}
	if !m.isScanning {
		t.Fatalf("expected disk-space detection to have started immediately")
	}
}

// TestUnverifiedDownloadConfirm_DeclineReturnsToOSSelect proves declining
// ("n"/"esc") goes back to OS selection and clears the pending selection,
// rather than leaving a half-confirmed state the user could stumble past.
func TestUnverifiedDownloadConfirm_DeclineReturnsToOSSelect(t *testing.T) {
	m := NewModel("")
	m.screen = ScreenUnverifiedDownloadConfirm
	entry := discovery.Entry{ID: "no-checksum", Distro: "TestOS"}
	m.selectedOS = &entry

	m = sendKey(t, m, "n")

	if m.screen != ScreenOSSelect {
		t.Fatalf("expected declining to return to ScreenOSSelect, got %v", m.screen)
	}
	if m.selectedOS != nil {
		t.Fatalf("expected selectedOS to be cleared after declining, got %+v", m.selectedOS)
	}
}

// TestUnverifiedDownloadConfirm_AcceptProceeds proves confirming ("y")
// continues into disk-space detection, same as a verified entry would
// (in non-advanced mode, proceedAfterOSSelected sets m.isScanning rather
// than changing m.screen — the async disk-scan result handler elsewhere
// in Update() is what eventually moves the screen on, matching the
// pre-existing behavior this was refactored from).
func TestUnverifiedDownloadConfirm_AcceptProceeds(t *testing.T) {
	m := NewModel("")
	m.screen = ScreenUnverifiedDownloadConfirm
	m.isAdvancedMode = false
	entry := discovery.Entry{ID: "no-checksum", Distro: "TestOS"}
	m.selectedOS = &entry

	m = sendKey(t, m, "y")

	if !m.isScanning {
		t.Fatalf("expected disk-space detection to have started after confirming")
	}
	if m.selectedOS == nil || m.selectedOS.ID != "no-checksum" {
		t.Fatalf("expected the selection to survive confirmation, got %+v", m.selectedOS)
	}
}

// TestUnverifiedDownloadConfirm_AcceptProceeds_AdvancedMode covers the
// other branch of proceedAfterOSSelected, where confirming DOES change
// m.screen (to ScreenUSBTargetSelect).
func TestUnverifiedDownloadConfirm_AcceptProceeds_AdvancedMode(t *testing.T) {
	m := NewModel("")
	m.screen = ScreenUnverifiedDownloadConfirm
	m.isAdvancedMode = true
	entry := discovery.Entry{ID: "no-checksum", Distro: "TestOS"}
	m.selectedOS = &entry

	m = sendKey(t, m, "y")

	if m.screen != ScreenUSBTargetSelect {
		t.Fatalf("expected confirming in advanced mode to move to ScreenUSBTargetSelect, got %v", m.screen)
	}
}

// TestViewUnverifiedDownloadConfirm_RendersWithoutPanic is a basic render
// smoke test for the new screen's View() path.
func TestViewUnverifiedDownloadConfirm_RendersWithoutPanic(t *testing.T) {
	m := NewModel("")
	m.screen = ScreenUnverifiedDownloadConfirm
	e := discovery.Entry{Distro: "TestOS", Version: "1.0", DownloadURL: "https://example.test/x.iso"}
	m.selectedOS = &e

	out := m.View()
	if out == "" {
		t.Fatalf("expected non-empty rendered output")
	}
}
