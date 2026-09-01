// Package engine — streamer.go implements the "Dual-Pipe HTTP Streamer &
// SHA-256 Hasher" from the blueprint: streams a download straight to disk
// while computing its hash in the same pass (no separate re-read of the
// whole file afterward), and reports live progress for ui/progress.go.
package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"
)

// Progress is a snapshot of an in-flight download, safe to read from
// another goroutine (e.g. the Bubble Tea update loop polling on a ticker).
type Progress struct {
	BytesRead   int64
	TotalBytes  int64 // 0 if server didn't send Content-Length
	StartedAt   time.Time
}

// Percent returns 0-100, or -1 if TotalBytes is unknown.
func (p Progress) Percent() float64 {
	if p.TotalBytes <= 0 {
		return -1
	}
	return float64(p.BytesRead) / float64(p.TotalBytes) * 100
}

// BytesPerSecond returns the average throughput since StartedAt.
func (p Progress) BytesPerSecond() float64 {
	elapsed := time.Since(p.StartedAt).Seconds()
	if elapsed <= 0 {
		return 0
	}
	return float64(p.BytesRead) / elapsed
}

// StreamResult is returned once a download completes.
type StreamResult struct {
	DestPath   string
	BytesTotal int64
	SHA256     string
	Duration   time.Duration
}

// counterWriter is an io.Writer that just tallies bytes atomically so the
// TUI can poll it from a separate goroutine without a data race.
type counterWriter struct {
	n *int64
}

func (c *counterWriter) Write(p []byte) (int, error) {
	atomic.AddInt64(c.n, int64(len(p)))
	return len(p), nil
}

// StreamDownload fetches url and writes it to destPath, computing a SHA-256
// of the content in the same read pass via io.MultiWriter (file + hasher +
// byte counter all fed from one io.Copy). progressFn, if non-nil, is called
// periodically (roughly every 250ms) from within this function's goroutine —
// callers wanting to read progress from elsewhere should instead poll the
// *Progress pointer this function keeps updated via atomic ops, exposed
// through GetProgress on the returned handle.
//
// If expectedSHA256 is non-empty, the download is verified against it and
// an error returned (with the partial file left in place, suffixed
// ".corrupt", for diagnostics) if it doesn't match — never silently accept
// a corrupt OS image.
func StreamDownload(ctx context.Context, url, destPath, expectedSHA256 string, onProgress func(Progress)) (*StreamResult, error) {
	start := time.Now()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching %s: unexpected status %s", url, resp.Status)
	}

	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return nil, fmt.Errorf("creating destination dir: %w", err)
	}

	tmpPath := destPath + ".part"
	f, err := os.Create(tmpPath)
	if err != nil {
		return nil, fmt.Errorf("creating temp file %s: %w", tmpPath, err)
	}
	defer f.Close()

	hasher := sha256.New()
	var bytesRead int64
	counter := &counterWriter{n: &bytesRead}
	mw := io.MultiWriter(f, hasher, counter)

	total := resp.ContentLength // -1 if unknown; treated as 0/"unknown" by Progress

	done := make(chan struct{})
	if onProgress != nil {
		go func() {
			ticker := time.NewTicker(250 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					t := total
					if t < 0 {
						t = 0
					}
					onProgress(Progress{
						BytesRead:  atomic.LoadInt64(&bytesRead),
						TotalBytes: t,
						StartedAt:  start,
					})
				case <-done:
					return
				}
			}
		}()
	}

	_, copyErr := io.Copy(mw, resp.Body)
	close(done)
	if closeErr := f.Close(); closeErr != nil && copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		os.Remove(tmpPath)
		return nil, fmt.Errorf("downloading %s: %w", url, copyErr)
	}

	actualSHA256 := hex.EncodeToString(hasher.Sum(nil))
	if expectedSHA256 != "" && !equalFoldHex(actualSHA256, expectedSHA256) {
		corruptPath := destPath + ".corrupt"
		os.Rename(tmpPath, corruptPath)
		return nil, fmt.Errorf("SHA-256 mismatch for %s: expected %s, got %s (partial file preserved at %s for inspection — do NOT use it)",
			url, expectedSHA256, actualSHA256, corruptPath)
	}

	if err := os.Rename(tmpPath, destPath); err != nil {
		return nil, fmt.Errorf("finalizing download to %s: %w", destPath, err)
	}

	if onProgress != nil {
		onProgress(Progress{BytesRead: bytesRead, TotalBytes: bytesRead, StartedAt: start})
	}

	return &StreamResult{
		DestPath:   destPath,
		BytesTotal: bytesRead,
		SHA256:     actualSHA256,
		Duration:   time.Since(start),
	}, nil
}

func equalFoldHex(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		ca, cb := a[i], b[i]
		if ca >= 'A' && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if cb >= 'A' && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}
