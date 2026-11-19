// Command download-verify implements audit item 14: it downloads every
// catalog entry FULLY using this project's own downloader
// (engine.StreamDownload), verifies the result against its manifest
// checksum when one is present, computes and reports one when it isn't
// (see docs/MANIFEST_AUDIT.md — every entry is currently missing one),
// deletes the downloaded file immediately afterward, and moves on to the
// next entry.
//
// This is meant to run only via .github/workflows/download-verify.yml
// (workflow_dispatch, plus a monthly schedule) — never on every push, and
// never invoked from normal development. It downloads real, multi-gigabyte
// files; do not run it casually.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"boot-orchestrator/discovery"
	"boot-orchestrator/engine"
)

// EntryReport is one row of the final report.
type EntryReport struct {
	ID             string `json:"id"`
	Distro         string `json:"distro"`
	Status         string `json:"status"` // OK, MISMATCH, NO-CHECKSUM, SKIPPED-SIZE, FAILED
	ExpectedSHA256 string `json:"expected_sha256,omitempty"`
	ComputedSHA256 string `json:"computed_sha256,omitempty"`
	BytesChecked   int64  `json:"bytes_checked,omitempty"`
	Detail         string `json:"detail,omitempty"`
}

// availableSpaceFunc is swappable for tests.
var availableSpaceFunc = diskFreeBytes

func main() {
	outPath := flag.String("out", "download-verify-report.json", "where to write the JSON report")
	workDir := flag.String("workdir", "", "scratch directory for downloads (defaults to a temp dir)")
	flag.Parse()

	cat, err := discovery.LoadEmbedded()
	if err != nil {
		fmt.Fprintf(os.Stderr, "loading catalog: %v\n", err)
		os.Exit(1)
	}

	dir := *workDir
	if dir == "" {
		d, err := os.MkdirTemp("", "download-verify-*")
		if err != nil {
			fmt.Fprintf(os.Stderr, "creating scratch dir: %v\n", err)
			os.Exit(1)
		}
		dir = d
		defer os.RemoveAll(dir)
	}

	reports := runVerification(context.Background(), cat.Entries, dir)

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
		fmt.Printf("%-12s %-40s %s\n", r.Status, r.ID, r.Detail)
		if r.Status == "MISMATCH" || r.Status == "FAILED" {
			failures++
		}
	}
	if failures > 0 {
		fmt.Fprintf(os.Stderr, "\n%d entr(ies) failed verification\n", failures)
		os.Exit(1)
	}
}

// runVerification downloads every entry (skipping those too large for the
// available space in dir) and reports the outcome, deleting each download
// before moving to the next so disk usage never exceeds one entry's size.
func runVerification(ctx context.Context, entries []discovery.Entry, dir string) []EntryReport {
	reports := make([]EntryReport, 0, len(entries))

	for _, e := range entries {
		r := EntryReport{ID: e.ID, Distro: e.Distro, ExpectedSHA256: e.SHA256}

		mirrors := e.GetMirrors()
		if len(mirrors) == 0 {
			r.Status = "FAILED"
			r.Detail = "no download URL or mirrors in manifest"
			reports = append(reports, r)
			continue
		}

		if e.ApproxSizeBytes > 0 {
			free, err := availableSpaceFunc(dir)
			if err == nil && e.ApproxSizeBytes > free {
				r.Status = "SKIPPED-SIZE"
				r.Detail = fmt.Sprintf("needs ~%d bytes, only %d available on the runner", e.ApproxSizeBytes, free)
				reports = append(reports, r)
				continue
			}
		}

		destPath := filepath.Join(dir, sanitizeFilename(e.ID)+".download")
		res, err := engine.StreamDownloadWithFailover(ctx, mirrors, destPath, "", nil)
		if err != nil {
			r.Status = "FAILED"
			r.Detail = err.Error()
			reports = append(reports, r)
			continue
		}
		r.BytesChecked = res.BytesTotal

		computed, hashErr := sha256File(destPath)
		_ = os.Remove(destPath)
		if hashErr != nil {
			r.Status = "FAILED"
			r.Detail = fmt.Sprintf("downloaded but could not hash: %v", hashErr)
			reports = append(reports, r)
			continue
		}
		r.ComputedSHA256 = computed

		switch {
		case e.SHA256 == "":
			r.Status = "NO-CHECKSUM"
			r.Detail = "manifest has no checksum to verify against; computed value included above for manual review before adding it"
		case strings.EqualFold(computed, e.SHA256):
			r.Status = "OK"
		default:
			r.Status = "MISMATCH"
			r.Detail = "downloaded content does not match the manifest's checksum"
		}
		reports = append(reports, r)
	}
	return reports
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func sanitizeFilename(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == 0 {
			return '_'
		}
		return r
	}, s)
}
