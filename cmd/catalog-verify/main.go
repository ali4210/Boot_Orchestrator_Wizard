// Command catalog-verify implements Phase 3's "Tier 1" OS matrix check: for
// every catalog entry, confirms its download URL (and every mirror) is
// actually reachable right now, without downloading the real payload —
// an HTTP HEAD (falling back to a 1-byte ranged GET for servers that
// reject HEAD) only. This is deliberately cheap and safe enough to run on
// every invocation (unlike cmd/download-verify, which downloads real,
// multi-gigabyte files and only runs via workflow_dispatch/schedule): it
// catches a dead link, a moved release, or a mirror returning an error
// page, without the cost of a full download.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"boot-orchestrator/discovery"
)

// MirrorReport is the reachability result for one URL (a primary
// download_url or one of its mirrors).
type MirrorReport struct {
	URL           string `json:"url"`
	Reachable     bool   `json:"reachable"`
	StatusCode    int    `json:"status_code,omitempty"`
	ContentLength int64  `json:"content_length,omitempty"`
	SizeMismatch  bool   `json:"size_mismatch,omitempty"`
	Method        string `json:"method"` // "HEAD" or "GET-RANGE"
	Error         string `json:"error,omitempty"`
	LatencyMS     int64  `json:"latency_ms"`
}

// EntryReport is one catalog entry's full result across all its mirrors.
type EntryReport struct {
	ID           string         `json:"id"`
	Family       string         `json:"family"`
	Distro       string         `json:"distro"`
	Version      string         `json:"version"`
	AnyReachable bool           `json:"any_reachable"`
	Mirrors      []MirrorReport `json:"mirrors"`
}

// httpClientTimeout bounds each individual HEAD/ranged-GET request — a
// var so a test can shrink it instead of waiting out a real timeout
// against an unreachable httptest server.
var httpClientTimeout = 15 * time.Second

func main() {
	outPath := flag.String("out", "catalog-verify-report.json", "where to write the JSON report")
	flag.Parse()

	cat, err := discovery.LoadEmbedded()
	if err != nil {
		fmt.Fprintf(os.Stderr, "loading catalog: %v\n", err)
		os.Exit(1)
	}

	client := &http.Client{Timeout: httpClientTimeout}
	reports := runCatalogVerification(client, cat.Entries)

	data, err := json.MarshalIndent(reports, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "marshaling report: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(*outPath, data, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "writing report: %v\n", err)
		os.Exit(1)
	}

	failures := 0
	for _, r := range reports {
		status := "OK"
		if !r.AnyReachable {
			status = "UNREACHABLE"
			failures++
		}
		fmt.Printf("%-12s %-40s (%d/%d mirrors reachable)\n", status, r.ID, countReachable(r.Mirrors), len(r.Mirrors))
	}
	if failures > 0 {
		fmt.Fprintf(os.Stderr, "\n%d entr(ies) had NO reachable mirror\n", failures)
		os.Exit(1)
	}
}

func countReachable(mirrors []MirrorReport) int {
	n := 0
	for _, m := range mirrors {
		if m.Reachable {
			n++
		}
	}
	return n
}

// runCatalogVerification checks every entry's mirrors for reachability.
func runCatalogVerification(client *http.Client, entries []discovery.Entry) []EntryReport {
	reports := make([]EntryReport, 0, len(entries))
	for _, e := range entries {
		r := EntryReport{ID: e.ID, Family: string(e.Family), Distro: e.Distro, Version: e.Version}
		for _, url := range e.GetMirrors() {
			mr := checkMirror(client, url, int64(e.ApproxSizeBytes))
			if mr.Reachable {
				r.AnyReachable = true
			}
			r.Mirrors = append(r.Mirrors, mr)
		}
		reports = append(reports, r)
	}
	return reports
}

// checkMirror probes url with HTTP HEAD, falling back to a 1-byte ranged
// GET for servers that reject or mishandle HEAD (some mirrors do), closing
// the body immediately without reading the payload either way.
func checkMirror(client *http.Client, url string, expectedSizeBytes int64) MirrorReport {
	start := time.Now()
	mr := headRequest(client, url)
	if !mr.Reachable {
		mr = rangedGetRequest(client, url)
	}
	mr.URL = url
	mr.LatencyMS = time.Since(start).Milliseconds()

	if mr.Reachable && expectedSizeBytes > 0 && mr.ContentLength > 0 {
		diff := mr.ContentLength - expectedSizeBytes
		if diff < 0 {
			diff = -diff
		}
		// A ranged GET's Content-Length is 1 byte (the single byte
		// requested), not the full resource size, so only HEAD's
		// Content-Length is meaningful for this comparison.
		if mr.Method == "HEAD" {
			const tolerance = 256 * 1024 * 1024 // manifest sizes are approximate
			mr.SizeMismatch = diff > tolerance
		}
	}
	return mr
}

func headRequest(client *http.Client, url string) MirrorReport {
	req, err := http.NewRequest(http.MethodHead, url, nil)
	if err != nil {
		return MirrorReport{Method: "HEAD", Error: err.Error()}
	}
	resp, err := client.Do(req)
	if err != nil {
		return MirrorReport{Method: "HEAD", Error: err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return MirrorReport{Method: "HEAD", StatusCode: resp.StatusCode, Error: fmt.Sprintf("HTTP %d", resp.StatusCode)}
	}
	return MirrorReport{Method: "HEAD", Reachable: true, StatusCode: resp.StatusCode, ContentLength: resp.ContentLength}
}

func rangedGetRequest(client *http.Client, url string) MirrorReport {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return MirrorReport{Method: "GET-RANGE", Error: err.Error()}
	}
	req.Header.Set("Range", "bytes=0-0")
	resp, err := client.Do(req)
	if err != nil {
		return MirrorReport{Method: "GET-RANGE", Error: err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return MirrorReport{Method: "GET-RANGE", StatusCode: resp.StatusCode, Error: fmt.Sprintf("HTTP %d", resp.StatusCode)}
	}
	// 200 (Range ignored) or 206 (Partial Content honored) both mean the
	// resource is actually there.
	return MirrorReport{Method: "GET-RANGE", Reachable: true, StatusCode: resp.StatusCode}
}
