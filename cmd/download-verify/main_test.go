package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"boot-orchestrator/discovery"
)

func countFiles(dir string) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	return len(entries), nil
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func withFakeDiskSpace(t *testing.T, bytes uint64) {
	t.Helper()
	orig := availableSpaceFunc
	t.Cleanup(func() { availableSpaceFunc = orig })
	availableSpaceFunc = func(string) (uint64, error) { return bytes, nil }
}

// TestRunVerification_SkipsEntryTooLargeForAvailableSpace proves an entry
// whose ApproxSizeBytes exceeds the runner's free space is reported
// SKIPPED-SIZE rather than attempted (and so never downloaded at all).
func TestRunVerification_SkipsEntryTooLargeForAvailableSpace(t *testing.T) {
	withFakeDiskSpace(t, 100) // 100 bytes free

	entries := []discovery.Entry{
		{ID: "huge", DownloadURL: "https://example.test/should-never-be-fetched.iso", ApproxSizeBytes: 1_000_000_000},
	}

	reports := runVerification(context.Background(), entries, t.TempDir())
	if len(reports) != 1 {
		t.Fatalf("expected 1 report, got %d", len(reports))
	}
	if reports[0].Status != "SKIPPED-SIZE" {
		t.Fatalf("expected SKIPPED-SIZE, got %q (%s)", reports[0].Status, reports[0].Detail)
	}
}

// TestRunVerification_OKWhenChecksumMatches proves a successful download
// whose checksum matches the manifest is reported OK.
func TestRunVerification_OKWhenChecksumMatches(t *testing.T) {
	withFakeDiskSpace(t, 1_000_000_000)

	payload := []byte("fake-small-payload-for-testing")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(payload)
	}))
	defer srv.Close()

	entries := []discovery.Entry{
		{ID: "good", DownloadURL: srv.URL, SHA256: sha256Hex(payload)},
	}

	reports := runVerification(context.Background(), entries, t.TempDir())
	if len(reports) != 1 || reports[0].Status != "OK" {
		t.Fatalf("expected a single OK report, got: %+v", reports)
	}
}

// TestRunVerification_MismatchWhenChecksumWrong proves a downloaded
// payload that doesn't match the manifest's checksum is reported MISMATCH.
func TestRunVerification_MismatchWhenChecksumWrong(t *testing.T) {
	withFakeDiskSpace(t, 1_000_000_000)

	payload := []byte("fake-small-payload-for-testing")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(payload)
	}))
	defer srv.Close()

	entries := []discovery.Entry{
		{ID: "wrong-hash", DownloadURL: srv.URL, SHA256: "0000000000000000000000000000000000000000000000000000000000000"},
	}

	reports := runVerification(context.Background(), entries, t.TempDir())
	if len(reports) != 1 || reports[0].Status != "MISMATCH" {
		t.Fatalf("expected a single MISMATCH report, got: %+v", reports)
	}
}

// TestRunVerification_NoChecksumReportsComputedHash proves an entry with
// no manifest checksum at all (the current state of every real entry —
// see docs/MANIFEST_AUDIT.md) is reported NO-CHECKSUM with the computed
// hash included, rather than silently skipped or falsely marked OK.
func TestRunVerification_NoChecksumReportsComputedHash(t *testing.T) {
	withFakeDiskSpace(t, 1_000_000_000)

	payload := []byte("fake-small-payload-for-testing")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(payload)
	}))
	defer srv.Close()

	entries := []discovery.Entry{
		{ID: "no-checksum", DownloadURL: srv.URL},
	}

	reports := runVerification(context.Background(), entries, t.TempDir())
	if len(reports) != 1 || reports[0].Status != "NO-CHECKSUM" {
		t.Fatalf("expected a single NO-CHECKSUM report, got: %+v", reports)
	}
	if reports[0].ComputedSHA256 != sha256Hex(payload) {
		t.Fatalf("expected the computed hash to match the real payload hash, got %q", reports[0].ComputedSHA256)
	}
}

// TestRunVerification_FailedWhenNoMirrors proves an entry with no download
// URL or mirrors at all is reported FAILED rather than attempted.
func TestRunVerification_FailedWhenNoMirrors(t *testing.T) {
	entries := []discovery.Entry{{ID: "no-url"}}
	reports := runVerification(context.Background(), entries, t.TempDir())
	if len(reports) != 1 || reports[0].Status != "FAILED" {
		t.Fatalf("expected a single FAILED report, got: %+v", reports)
	}
}

// TestRunVerification_DeletesDownloadedFileAfterChecking proves each
// entry's downloaded file is removed before moving to the next, so disk
// usage across many entries never accumulates beyond one at a time.
func TestRunVerification_DeletesDownloadedFileAfterChecking(t *testing.T) {
	withFakeDiskSpace(t, 1_000_000_000)

	payload := []byte("fake-small-payload-for-testing")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(payload)
	}))
	defer srv.Close()

	dir := t.TempDir()
	entries := []discovery.Entry{
		{ID: "one", DownloadURL: srv.URL, SHA256: sha256Hex(payload)},
		{ID: "two", DownloadURL: srv.URL, SHA256: sha256Hex(payload)},
	}

	runVerification(context.Background(), entries, dir)

	leftoverFiles, err := countFiles(dir)
	if err != nil {
		t.Fatalf("listing scratch dir: %v", err)
	}
	if leftoverFiles != 0 {
		t.Fatalf("expected no leftover downloaded files in %s, found %d", dir, leftoverFiles)
	}
}
