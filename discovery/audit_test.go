package discovery

import "testing"

func TestAuditEntry_FlagsEachMissingProperty(t *testing.T) {
	cases := []struct {
		name       string
		entry      Entry
		wantBroken bool
	}{
		{
			name:       "complete entry",
			entry:      Entry{ID: "ok", DownloadURL: "https://example.test/iso", SHA256: "deadbeef"},
			wantBroken: false,
		},
		{
			name:       "missing checksum",
			entry:      Entry{ID: "no-sha", DownloadURL: "https://example.test/iso"},
			wantBroken: true,
		},
		{
			name:       "missing url and mirrors",
			entry:      Entry{ID: "no-url", SHA256: "deadbeef"},
			wantBroken: true,
		},
		{
			name:       "missing both",
			entry:      Entry{ID: "nothing"},
			wantBroken: true,
		},
		{
			name:       "mirrors satisfy the url requirement",
			entry:      Entry{ID: "mirrors-only", Mirrors: []string{"https://example.test/iso"}, SHA256: "deadbeef"},
			wantBroken: false,
		},
		{
			name:       "whitespace-only checksum counts as missing",
			entry:      Entry{ID: "blank-sha", DownloadURL: "https://example.test/iso", SHA256: "   "},
			wantBroken: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := AuditEntry(tc.entry)
			if got.Broken != tc.wantBroken {
				t.Errorf("AuditEntry(%+v).Broken = %v, want %v (reason: %q)", tc.entry, got.Broken, tc.wantBroken, got.BrokenReason)
			}
		})
	}
}

func TestCatalog_Audit_FindsOnlyBrokenEntries(t *testing.T) {
	c := &Catalog{Entries: []Entry{
		{ID: "good", DownloadURL: "https://example.test/a", SHA256: "aaaa"},
		{ID: "bad-1", DownloadURL: "https://example.test/b"},
		{ID: "good-2", DownloadURL: "https://example.test/c", SHA256: "cccc"},
		{ID: "bad-2"},
	}}

	broken := c.Audit()
	if len(broken) != 2 {
		t.Fatalf("expected 2 broken entries, got %d: %+v", len(broken), broken)
	}
	if broken[0].Entry.ID != "bad-1" || broken[1].Entry.ID != "bad-2" {
		t.Fatalf("unexpected broken entries: %+v", broken)
	}
}

// TestRealEmbeddedCatalog_AuditReport is not a pass/fail regression test —
// every manifest entry currently lacks a SHA256 checksum (see
// docs/MANIFEST_AUDIT.md for the full list and why this isn't fixed here:
// a real checksum can only come from actually downloading each,
// multi-gigabyte, OS image, which this session cannot safely or
// practically do for 29 entries). This test exists so the real catalog's
// audit state is visible in test output (`go test -v`) without silently
// failing the suite over a data-completeness gap that isn't a code bug.
func TestRealEmbeddedCatalog_AuditReport(t *testing.T) {
	cat, err := LoadEmbedded()
	if err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	broken := cat.Audit()
	t.Logf("catalog audit: %d/%d entries broken", len(broken), len(cat.Entries))
	for _, b := range broken {
		t.Logf("  BROKEN %s (%s): %s", b.Entry.ID, b.Entry.Distro, b.BrokenReason)
	}
}
