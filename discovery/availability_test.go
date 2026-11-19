package discovery

import "testing"

// TestGetEntriesByCategory_HidesUnavailableEntries is the regression test
// for audit follow-up G1's catalog-availability gate: an entry marked
// Unavailable (e.g. gentoo-stage3-amd64, both macOS recovery entries —
// see OS_STATUS_MATRIX.md for why) must never be offered by the TUI's
// category listing, unlike EOL entries which are still listed (just
// tagged) since EOL is a "proceed anyway" choice and Unavailable isn't.
func TestGetEntriesByCategory_HidesUnavailableEntries(t *testing.T) {
	c := &Catalog{Entries: []Entry{
		{ID: "ok", Category: CategoryArchOther, Family: FamilyLinux},
		{ID: "dead", Category: CategoryArchOther, Family: FamilyLinux, Unavailable: true, UnavailableReason: "mirrors 404"},
	}}

	got := c.GetEntriesByCategory(CategoryArchOther)
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 entry (the unavailable one hidden), got %d: %+v", len(got), got)
	}
	if got[0].ID != "ok" {
		t.Fatalf("expected the available entry 'ok', got %q", got[0].ID)
	}
}

// TestListCategories_OmitsCategoryWhoseOnlyEntryIsUnavailable proves the
// category itself disappears, not just the entry within it, when every
// entry in that category is unavailable — otherwise the TUI would offer
// an empty folder.
func TestListCategories_OmitsCategoryWhoseOnlyEntryIsUnavailable(t *testing.T) {
	c := &Catalog{Entries: []Entry{
		{ID: "dead-macos", Category: CategoryMacOS, Family: FamilyMacOS, Unavailable: true},
		{ID: "ok-kali", Category: CategoryKali, Family: FamilyLinux},
	}}

	cats := c.ListCategories()
	for _, cat := range cats {
		if cat == CategoryMacOS {
			t.Fatalf("expected CategoryMacOS to be omitted (its only entry is unavailable), got categories: %v", cats)
		}
	}
	found := false
	for _, cat := range cats {
		if cat == CategoryKali {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected CategoryKali (a real, available entry) to still be listed, got: %v", cats)
	}
}

// TestFilter_ExcludesUnavailableRegardlessOfOtherCriteria proves Filter
// (used by cmd/env-check) excludes Unavailable entries unconditionally —
// there is no "includeUnavailable" override the way includeEOL is one,
// since an unavailable entry's download is confirmed not to work at all,
// not merely a judgment call about using old software.
func TestFilter_ExcludesUnavailableRegardlessOfOtherCriteria(t *testing.T) {
	c := &Catalog{Entries: []Entry{
		{ID: "dead", Family: FamilyLinux, Flavor: FlavorTTY, Unavailable: true},
		{ID: "alive", Family: FamilyLinux, Flavor: FlavorTTY},
	}}

	got := c.Filter(FamilyLinux, FlavorTTY, "", true)
	if len(got) != 1 || got[0].ID != "alive" {
		t.Fatalf("expected only 'alive', got %+v", got)
	}
}
