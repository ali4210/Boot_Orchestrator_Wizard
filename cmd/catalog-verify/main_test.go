package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"boot-orchestrator/discovery"
)

func fastClient() *http.Client {
	return &http.Client{Timeout: 2 * time.Second}
}

func TestCheckMirror_HEADReachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead {
			t.Errorf("expected a HEAD request, got %s", r.Method)
		}
		w.Header().Set("Content-Length", "1048576")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	mr := checkMirror(fastClient(), srv.URL, 1048576)
	if !mr.Reachable {
		t.Fatalf("expected reachable, got %+v", mr)
	}
	if mr.Method != "HEAD" {
		t.Errorf("expected HEAD to succeed directly, got method %q", mr.Method)
	}
	if mr.SizeMismatch {
		t.Errorf("expected no size mismatch when sizes match exactly, got %+v", mr)
	}
}

// TestCheckMirror_FallsBackToRangedGETWhenHEADRejected proves the fallback
// path: a mirror that returns 405 for HEAD (some real mirrors do this)
// must still be reported reachable via the ranged GET fallback, and must
// never cause the full resource to be read.
func TestCheckMirror_FallsBackToRangedGETWhenHEADRejected(t *testing.T) {
	var sawRangeHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		sawRangeHeader = r.Header.Get("Range")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("x"))
	}))
	defer srv.Close()

	mr := checkMirror(fastClient(), srv.URL, 0)
	if !mr.Reachable {
		t.Fatalf("expected the ranged-GET fallback to report reachable, got %+v", mr)
	}
	if mr.Method != "GET-RANGE" {
		t.Errorf("expected method GET-RANGE, got %q", mr.Method)
	}
	if sawRangeHeader != "bytes=0-0" {
		t.Errorf("expected a 1-byte Range request, got %q", sawRangeHeader)
	}
}

// TestCheckMirror_ReportsUnreachableOn404 proves a dead link is reported,
// not silently treated as reachable.
func TestCheckMirror_ReportsUnreachableOn404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	mr := checkMirror(fastClient(), srv.URL, 0)
	if mr.Reachable {
		t.Fatalf("expected a 404 to be reported unreachable, got %+v", mr)
	}
	if mr.Error == "" {
		t.Errorf("expected a non-empty error message, got %+v", mr)
	}
}

// TestCheckMirror_FlagsGrossSizeMismatch proves a manifest's
// approx_size_bytes wildly disagreeing with the server's real
// Content-Length is flagged, not silently ignored (catches a manifest
// entry pointing at a since-replaced, differently-sized release).
func TestCheckMirror_FlagsGrossSizeMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1024")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	mr := checkMirror(fastClient(), srv.URL, 10*1024*1024*1024) // manifest claims 10GiB, server says 1KB
	if !mr.Reachable {
		t.Fatalf("expected reachable despite the size mismatch, got %+v", mr)
	}
	if !mr.SizeMismatch {
		t.Errorf("expected a gross size mismatch to be flagged, got %+v", mr)
	}
}

// TestRunCatalogVerification_AnyReachableTrueIfOneMirrorWorks proves an
// entry with multiple mirrors is reported reachable if even one works,
// matching GetMirrors' own multi-mirror-failover semantics.
func TestRunCatalogVerification_AnyReachableTrueIfOneMirrorWorks(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer dead.Close()
	alive := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer alive.Close()

	entries := []discovery.Entry{
		{ID: "test-entry", Mirrors: []string{dead.URL, alive.URL}},
	}
	reports := runCatalogVerification(fastClient(), entries)
	if len(reports) != 1 {
		t.Fatalf("expected 1 report, got %d", len(reports))
	}
	if !reports[0].AnyReachable {
		t.Fatalf("expected AnyReachable=true when one of two mirrors works, got %+v", reports[0])
	}
	if len(reports[0].Mirrors) != 2 {
		t.Errorf("expected both mirrors to be checked and recorded, got %d", len(reports[0].Mirrors))
	}
}

// TestRunCatalogVerification_AnyReachableFalseWhenAllMirrorsDead proves
// the negative case isn't silently reported as success.
func TestRunCatalogVerification_AnyReachableFalseWhenAllMirrorsDead(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer dead.Close()

	entries := []discovery.Entry{
		{ID: "test-entry", Mirrors: []string{dead.URL}},
	}
	reports := runCatalogVerification(fastClient(), entries)
	if reports[0].AnyReachable {
		t.Fatalf("expected AnyReachable=false when the only mirror is dead, got %+v", reports[0])
	}
}
