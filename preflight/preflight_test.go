package preflight

import "testing"

// TestRun_SucceedsAgainstARealTempDir runs the real check (real
// hypervisor/firmware/disk detection on whatever machine runs the test,
// against a safe, writable t.TempDir() rather than any real target disk
// path) and confirms it reports a plausible catalog entry count and
// never panics. This is the real function, not a mock — the point is
// proving it actually runs end to end.
func TestRun_SucceedsAgainstARealTempDir(t *testing.T) {
	report, err := Run(t.TempDir())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.CatalogEntries == 0 {
		t.Errorf("expected the real embedded catalog to load with a nonzero entry count, got 0")
	}
	for _, b := range report.Blocking {
		t.Logf("blocking (informational, not a test failure by itself): %s", b)
	}
}
