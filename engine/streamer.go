// Package engine — streamer.go implements the Resilient Multi-Mirror HTTP
// Streamer with automated failover, HTTP Range pause/resume capabilities,
// and single-pass SHA-256 verification.
package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

// ErrDownloadPaused indicates the download stopped gracefully due to a pause request.
var ErrDownloadPaused = errors.New("download paused by user")

// Progress holds a thread-safe snapshot of download telemetry.
type Progress struct {
	BytesRead  int64
	TotalBytes int64 // 0 if unknown
	StartedAt  time.Time
}

// Percent returns 0-100 or -1 if total is unknown.
func (p Progress) Percent() float64 {
	if p.TotalBytes <= 0 {
		return -1
	}
	pct := float64(p.BytesRead) / float64(p.TotalBytes) * 100
	if pct > 100 {
		return 100
	}
	return pct
}

// BytesPerSecond returns the throughput rate.
func (p Progress) BytesPerSecond() float64 {
	elapsed := time.Since(p.StartedAt).Seconds()
	if elapsed <= 0 {
		return 0
	}
	return float64(p.BytesRead) / elapsed
}

// StreamResult is returned when a payload finishes downloading and passes verification.
type StreamResult struct {
	DestPath     string
	BytesTotal   int64
	SHA256       string
	Duration     time.Duration
	MirrorUsed   string
	ResumedBytes int64
}

// StreamDownload maintains backward-compatibility by wrapping StreamDownloadWithFailover.
func StreamDownload(ctx context.Context, url, destPath, expectedSHA256 string, onProgress func(Progress)) (*StreamResult, error) {
	return StreamDownloadWithFailover(ctx, []string{url}, destPath, expectedSHA256, onProgress)
}

// StreamDownloadWithFailover iterates through redundant mirrors, supports resuming
// from partial .part files, and validates SHA-256 integrity upon completion.
func StreamDownloadWithFailover(ctx context.Context, mirrors []string, destPath, expectedSHA256 string, onProgress func(Progress)) (*StreamResult, error) {
	if len(mirrors) == 0 {
		return nil, fmt.Errorf("no download mirrors provided")
	}

	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return nil, fmt.Errorf("creating destination directory: %w", err)
	}

	tmpPath := destPath + ".part"
	start := time.Now()
	var lastErr error

	// Determine existing offset if resuming
	var existingOffset int64
	if stat, err := os.Stat(tmpPath); err == nil && stat.Size() > 0 {
		existingOffset = stat.Size()
	}

	client := &http.Client{
		Timeout: 45 * time.Second,
	}

	for mirrorIndex, mirrorURL := range mirrors {
		if strings.TrimSpace(mirrorURL) == "" {
			continue
		}

		select {
		case <-ctx.Done():
			return nil, ErrDownloadPaused
		default:
		}

		res, err := downloadFromSingleMirror(ctx, client, mirrorURL, tmpPath, existingOffset, start, onProgress)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, ErrDownloadPaused) {
				return nil, ErrDownloadPaused
			}
			lastErr = fmt.Errorf("mirror [%d/%d] %s failed: %w", mirrorIndex+1, len(mirrors), mirrorURL, err)
			continue
		}

		// Calculate SHA-256 of completed .part payload
		calculatedSHA, hashErr := computeFileSHA256(tmpPath)
		if hashErr != nil {
			lastErr = fmt.Errorf("verifying checksum of downloaded payload: %w", hashErr)
			continue
		}

		if expectedSHA256 != "" && !equalFoldHex(calculatedSHA, expectedSHA256) {
			corruptPath := destPath + ".corrupt"
			_ = os.Rename(tmpPath, corruptPath)
			lastErr = fmt.Errorf("checksum mismatch from mirror %s (expected: %s, calculated: %s)", mirrorURL, expectedSHA256, calculatedSHA)
			existingOffset = 0 // Reset offset for next mirror
			continue
		}

		// Atomically commit verified file
		if err := os.Rename(tmpPath, destPath); err != nil {
			return nil, fmt.Errorf("finalizing download commit to %s: %w", destPath, err)
		}

		return &StreamResult{
			DestPath:     destPath,
			BytesTotal:   res.BytesTotal,
			SHA256:       calculatedSHA,
			Duration:     time.Since(start),
			MirrorUsed:   mirrorURL,
			ResumedBytes: existingOffset,
		}, nil
	}

	return nil, fmt.Errorf("all mirrors exhausted. Last error: %w", lastErr)
}

func downloadFromSingleMirror(
	ctx context.Context,
	client *http.Client,
	url, tmpPath string,
	resumeOffset int64,
	startTime time.Time,
	onProgress func(Progress),
) (*StreamResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	useRange := false
	if resumeOffset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", resumeOffset))
		useRange = true
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var file *os.File
	var currentBytes int64 = resumeOffset
	var totalBytes int64

	if useRange && resp.StatusCode == http.StatusPartialContent {
		// Server accepted partial content range
		file, err = os.OpenFile(tmpPath, os.O_APPEND|os.O_WRONLY, 0644)
		if err != nil {
			return nil, err
		}
		totalBytes = resumeOffset + resp.ContentLength
	} else if resp.StatusCode == http.StatusOK {
		// Server does not support ranges or this is a clean start
		file, err = os.Create(tmpPath)
		if err != nil {
			return nil, err
		}
		currentBytes = 0
		totalBytes = resp.ContentLength
	} else {
		return nil, fmt.Errorf("unexpected HTTP status: %s", resp.Status)
	}
	defer file.Close()

	done := make(chan struct{})
	if onProgress != nil {
		go func() {
			ticker := time.NewTicker(250 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					t := atomic.LoadInt64(&totalBytes)
					if t < 0 {
						t = 0
					}
					onProgress(Progress{
						BytesRead:  atomic.LoadInt64(&currentBytes),
						TotalBytes: t,
						StartedAt:  startTime,
					})
				case <-done:
					return
				}
			}
		}()
	}

	buffer := make([]byte, 64*1024) // 64 KB buffer
	for {
		select {
		case <-ctx.Done():
			close(done)
			return nil, ErrDownloadPaused
		default:
		}

		n, readErr := resp.Body.Read(buffer)
		if n > 0 {
			if _, writeErr := file.Write(buffer[:n]); writeErr != nil {
				close(done)
				return nil, writeErr
			}
			atomic.AddInt64(&currentBytes, int64(n))
		}

		if readErr != nil {
			close(done)
			if errors.Is(readErr, io.EOF) {
				break
			}
			return nil, readErr
		}
	}

	return &StreamResult{
		DestPath:   tmpPath,
		BytesTotal: currentBytes,
	}, nil
}

func computeFileSHA256(filePath string) (string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
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
