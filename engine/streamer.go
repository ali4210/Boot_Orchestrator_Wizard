// Package engine — streamer.go implements the Resilient Multi-Mirror HTTP
// Streamer with automated failover, HTTP Range pause/resume capabilities,
// local file payload support, single-pass SHA-256 verification, and autonomous
// HTTP 416 range auto-recovery.
package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

// ErrDownloadPaused indicates the download stopped gracefully due to a pause request.
var ErrDownloadPaused = errors.New("download paused by user")

// errStalled indicates an attempt was aborted because no bytes were
// received for stallTimeout, distinct from a user-requested pause/cancel
// so retry logic can tell the two apart.
var errStalled = errors.New("download stalled: no data received")

// errPermanentStatus wraps an HTTP status a mirror returned that retrying
// against the SAME mirror cannot possibly fix (403 Forbidden, 404 Not
// Found, 401, ...). Without this, downloadFromSingleMirrorWithRetry's
// generic "retry transient failures" logic would otherwise retry a
// mirror that will deterministically return the same error every time,
// burning downloadMaxAttempts worth of exponential backoff (tens of
// seconds) before finally moving on to the next mirror.
type errPermanentStatus struct {
	status string
}

func (e *errPermanentStatus) Error() string {
	return fmt.Sprintf("unexpected HTTP status: %s", e.status)
}

// stallTimeout is how long downloadFromSingleMirror waits for ANY forward
// progress (new bytes read from the response body) before concluding the
// connection has stalled and aborting that attempt so it can be retried.
// Audit item 11/E: previously there was no such watchdog at all — a
// connection that stayed open but stopped sending data (a true network
// "stall", as opposed to a clean close or reset) would block on Read()
// forever, since neither client.Timeout (disabled) nor the transport's
// ResponseHeaderTimeout/IdleConnTimeout cover an already-in-progress body
// read. A var so tests can shrink it instead of waiting out the real
// duration.
var stallTimeout = 30 * time.Second

// downloadMaxAttempts and downloadRetryBaseDelay control per-mirror retry
// backoff for a stalled/reset/truncated attempt. Audit item 11/E:
// previously a single failed attempt against a mirror moved straight to
// the next mirror (or straight to "all mirrors exhausted" when there was
// only one, the common case for a single ImageURL) — a single transient
// blip on a multi-gigabyte download killed the whole download with no
// retry at all. Vars so tests don't have to wait out real backoff delays.
var downloadMaxAttempts = 5
var downloadRetryBaseDelay = 2 * time.Second

// Progress holds a thread-safe snapshot of download telemetry. Status, when
// non-empty, is a human-readable transient note (e.g. "stalled, retrying
// 2/5") for the caller to surface in its UI; it is not cumulative state.
type Progress struct {
	BytesRead  int64
	TotalBytes int64 // 0 if unknown
	StartedAt  time.Time
	Status     string
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

// StreamDownloadWithFailover handles local paths, iterates redundant mirrors,
// runs active preflight sentinel checks, auto-decompresses archives,
// and validates SHA-256 integrity.
//
// Deliberately NOT routed through fsRunner/execCommand, found and fixed
// while building item 50 (audit follow-up G3)'s real dry-run proof: this
// function only ever writes to a LOCAL destPath (never the target disk),
// and the project's own documented design (docs/dryrun-inventory.md) is
// that the payload download stays real under --dry-run so the rest of a
// dry run (checksum verification, free-space headroom) stays meaningful.
// An earlier pass had migrated this file's os.MkdirAll/Rename/Remove onto
// fsRunner for blanket consistency with every other file — which broke
// that documented design in a subtle way: the real HTTP/local-file copy
// still happened for real (raw os.Create/io.Copy, never touched), but the
// FINAL os.Rename committing the result to destPath got silently faked
// under dry-run, so callers downstream (checksum verification against
// streamResult.DestPath) would find no file there at all. Reverted this
// file (and stream_decoder.go/resilient_streamer.go/scp_streamer.go,
// the same download/local-cache family) back to raw os.* so the whole
// download path is consistently real, not half-intercepted.
func StreamDownloadWithFailover(ctx context.Context, mirrors []string, destPath, expectedSHA256 string, onProgress func(Progress)) (*StreamResult, error) {
	if len(mirrors) == 0 {
		return nil, fmt.Errorf("no download mirrors provided")
	}

	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return nil, fmt.Errorf("creating destination directory: %w", err)
	}

	start := time.Now()

	// 1. FAST-PATH: If target destination already exists and passes checksum, use it immediately
	if stat, err := os.Stat(destPath); err == nil && stat.Size() > 0 {
		calcSHA, hashErr := computeFileSHA256(destPath)
		if hashErr == nil && (expectedSHA256 == "" || equalFoldHex(calcSHA, expectedSHA256)) {
			return &StreamResult{
				DestPath:     destPath,
				BytesTotal:   stat.Size(),
				SHA256:       calcSHA,
				Duration:     time.Since(start),
				MirrorUsed:   "local-cache-hit",
				ResumedBytes: stat.Size(),
			}, nil
		}
	}

	// 2. HEALTH SENTINEL: Pre-sort mirror pool by liveliness & throughput
	_, prioritizedMirrors := SelectBestMirror(ctx, mirrors)
	if len(prioritizedMirrors) == 0 {
		prioritizedMirrors = mirrors
	}

	tmpPath := destPath + ".part"
	var lastErr error

	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ResponseHeaderTimeout: 35 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   25 * time.Second,
		ExpectContinueTimeout: 5 * time.Second,
	}

	client := &http.Client{
		Transport: transport,
		Timeout:   0, // Keep stream open as long as data transfers
	}

	for mirrorIndex, mirrorURL := range prioritizedMirrors {
		cleanMirror := strings.TrimSpace(mirrorURL)
		if cleanMirror == "" {
			continue
		}

		select {
		case <-ctx.Done():
			return nil, ErrDownloadPaused
		default:
		}

		// Local path ingest
		if strings.HasPrefix(cleanMirror, "/") || strings.HasPrefix(cleanMirror, "~/") || !strings.Contains(cleanMirror, "://") {
			localPath := cleanMirror
			if strings.HasPrefix(localPath, "~/") {
				if home, err := os.UserHomeDir(); err == nil {
					localPath = filepath.Join(home, localPath[2:])
				}
			}

			if fileInfo, err := os.Stat(localPath); err == nil && !fileInfo.IsDir() {
				res, err := copyFromLocalFile(ctx, localPath, tmpPath, fileInfo.Size(), start, onProgress)
				if err != nil {
					lastErr = fmt.Errorf("local file ingest failed: %w", err)
					continue
				}

				calculatedSHA, hashErr := computeFileSHA256(tmpPath)
				if hashErr != nil {
					lastErr = fmt.Errorf("verifying checksum of local payload: %w", hashErr)
					continue
				}

				if expectedSHA256 != "" && !equalFoldHex(calculatedSHA, expectedSHA256) {
					lastErr = fmt.Errorf("checksum mismatch on local file %s (expected: %s, calculated: %s)", localPath, expectedSHA256, calculatedSHA)
					continue
				}

				if err := os.Rename(tmpPath, destPath); err != nil {
					return nil, fmt.Errorf("committing payload to %s: %w", destPath, err)
				}

				return &StreamResult{
					DestPath:   destPath,
					BytesTotal: res.BytesTotal,
					SHA256:     calculatedSHA,
					Duration:   time.Since(start),
					MirrorUsed: "local://" + localPath,
				}, nil
			}
		}

		// Remote mirror execution. Retries transient failures (stall,
		// reset) against this same mirror internally (audit item 11/E —
		// see downloadFromSingleMirrorWithRetry); a checksum mismatch gets
		// exactly one additional clean restart (full re-download from
		// scratch) before this mirror is abandoned, since the mismatch
		// could stem from a stale/corrupt .part file rather than the
		// mirror itself.
		var existingOffset int64
		if stat, err := os.Stat(tmpPath); err == nil && stat.Size() > 0 {
			existingOffset = stat.Size()
		}

		var res *StreamResult
		var calculatedSHA string
		var attemptErr error
		for cleanRestart := 0; cleanRestart < 2; cleanRestart++ {
			if cleanRestart == 1 {
				_ = os.Remove(tmpPath)
			}

			res, attemptErr = downloadFromSingleMirrorWithRetry(ctx, client, cleanMirror, tmpPath, start, onProgress)
			if attemptErr != nil {
				break
			}

			calculatedSHA, attemptErr = computeFileSHA256(tmpPath)
			if attemptErr != nil {
				break
			}

			if expectedSHA256 == "" || equalFoldHex(calculatedSHA, expectedSHA256) {
				attemptErr = nil
				break
			}

			attemptErr = fmt.Errorf("checksum mismatch (expected: %s, calculated: %s)", expectedSHA256, calculatedSHA)
			if onProgress != nil {
				onProgress(Progress{StartedAt: start, Status: "checksum mismatch, restarting download from scratch..."})
			}
		}

		if attemptErr != nil {
			if errors.Is(attemptErr, context.Canceled) || errors.Is(attemptErr, ErrDownloadPaused) {
				return nil, ErrDownloadPaused
			}
			if strings.Contains(attemptErr.Error(), "checksum mismatch") {
				corruptPath := destPath + ".corrupt"
				_ = os.Rename(tmpPath, corruptPath)
			}
			lastErr = fmt.Errorf("mirror [%d/%d] %s failed: %w", mirrorIndex+1, len(prioritizedMirrors), cleanMirror, attemptErr)
			continue
		}

		if err := os.Rename(tmpPath, destPath); err != nil {
			return nil, fmt.Errorf("finalizing download commit to %s: %w", destPath, err)
		}

		return &StreamResult{
			DestPath:     destPath,
			BytesTotal:   res.BytesTotal,
			SHA256:       calculatedSHA,
			Duration:     time.Since(start),
			MirrorUsed:   cleanMirror,
			ResumedBytes: existingOffset,
		}, nil
	}

	return nil, fmt.Errorf("all mirrors exhausted. Last error: %w", lastErr)
}

func copyFromLocalFile(
	ctx context.Context,
	srcPath, tmpPath string,
	totalBytes int64,
	startTime time.Time,
	onProgress func(Progress),
) (*StreamResult, error) {
	src, err := os.Open(srcPath)
	if err != nil {
		return nil, err
	}
	defer src.Close()

	decoded, err := InspectAndDecodeStream(src)
	if err != nil {
		return nil, fmt.Errorf("stream decoder failed: %w", err)
	}
	defer decoded.Cleanup()

	dst, err := os.Create(tmpPath)
	if err != nil {
		return nil, err
	}
	defer dst.Close()

	var currentBytes int64
	done := make(chan struct{})

	if onProgress != nil {
		go func() {
			ticker := time.NewTicker(100 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					onProgress(Progress{
						BytesRead:  atomic.LoadInt64(&currentBytes),
						TotalBytes: totalBytes,
						StartedAt:  startTime,
					})
				case <-done:
					return
				}
			}
		}()
	}

	buffer := make([]byte, 1024*1024)
	for {
		select {
		case <-ctx.Done():
			close(done)
			return nil, ErrDownloadPaused
		default:
		}

		n, readErr := decoded.Reader.Read(buffer)
		if n > 0 {
			if _, writeErr := dst.Write(buffer[:n]); writeErr != nil {
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

// downloadFromSingleMirrorWithRetry wraps downloadFromSingleMirror with
// exponential-backoff-with-jitter retries (audit item 11/E): a stalled,
// reset, or otherwise transient failure retries the SAME mirror up to
// downloadMaxAttempts times (re-reading whatever bytes already landed in
// tmpPath via Range, same as a fresh resume) before giving up on it,
// instead of a single failed attempt immediately exhausting that mirror.
// A context cancellation/pause is never retried — it propagates straight
// through.
func downloadFromSingleMirrorWithRetry(ctx context.Context, client *http.Client, url, tmpPath string, startTime time.Time, onProgress func(Progress)) (*StreamResult, error) {
	var lastErr error
	for attempt := 1; attempt <= downloadMaxAttempts; attempt++ {
		var resumeOffset int64
		if stat, statErr := os.Stat(tmpPath); statErr == nil && stat.Size() > 0 {
			resumeOffset = stat.Size()
		}

		res, err := downloadFromSingleMirror(ctx, client, url, tmpPath, resumeOffset, startTime, onProgress)
		if err == nil {
			return res, nil
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, ErrDownloadPaused) {
			return nil, err
		}
		var permErr *errPermanentStatus
		if errors.As(err, &permErr) {
			// A deterministic HTTP error status (403, 404, ...) will not
			// change on retry — fail fast instead of burning
			// downloadMaxAttempts worth of exponential backoff against a
			// mirror that will never succeed.
			return nil, err
		}
		lastErr = err
		if attempt == downloadMaxAttempts {
			break
		}

		reason := "connection error"
		if errors.Is(err, errStalled) {
			reason = "stalled"
		}

		// Exponential backoff with jitter: base * 2^(attempt-1), plus up to
		// one more base-delay-worth of random jitter so many clients
		// retrying the same mirror at once don't all hammer it in lockstep.
		delay := downloadRetryBaseDelay * time.Duration(1<<uint(attempt-1))
		delay += time.Duration(rand.Int63n(int64(downloadRetryBaseDelay) + 1))

		if onProgress != nil {
			onProgress(Progress{
				StartedAt: startTime,
				Status:    fmt.Sprintf("%s, retrying %d/%d in %s...", reason, attempt, downloadMaxAttempts, delay.Round(time.Second)),
			})
		}

		select {
		case <-ctx.Done():
			return nil, ErrDownloadPaused
		case <-time.After(delay):
		}
	}
	return nil, fmt.Errorf("after %d attempts: %w", downloadMaxAttempts, lastErr)
}

func downloadFromSingleMirror(
	ctx context.Context,
	client *http.Client,
	url, tmpPath string,
	resumeOffset int64,
	startTime time.Time,
	onProgress func(Progress),
) (*StreamResult, error) {
	// A per-attempt cancellable context, derived from ctx, lets the stall
	// watchdog below abort an in-progress read (by cancelling this context,
	// which aborts the underlying request) without needing SetReadDeadline
	// on resp.Body, which net/http does not expose.
	attemptCtx, cancelAttempt := context.WithCancel(ctx)
	defer cancelAttempt()

	req, err := http.NewRequestWithContext(attemptCtx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) Boot-Orchestrator-Universal/2.0")

	useRange := false
	if resumeOffset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", resumeOffset))
		useRange = true
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}

	// Range Sanity Fallback: If remote returned HTTP 416 (offset >= file size), truncate & retry from 0
	if useRange && resp.StatusCode == http.StatusRequestedRangeNotSatisfiable {
		resp.Body.Close()
		_ = os.Truncate(tmpPath, 0)
		return downloadFromSingleMirror(ctx, client, url, tmpPath, 0, startTime, onProgress)
	}
	defer resp.Body.Close()

	var file *os.File
	var currentBytes int64 = resumeOffset
	var totalBytes int64

	if useRange && resp.StatusCode == http.StatusPartialContent {
		file, err = os.OpenFile(tmpPath, os.O_APPEND|os.O_WRONLY, 0644)
		if err != nil {
			return nil, err
		}
		totalBytes = resumeOffset + resp.ContentLength
	} else if resp.StatusCode == http.StatusOK {
		file, err = os.Create(tmpPath)
		if err != nil {
			return nil, err
		}
		currentBytes = 0
		totalBytes = resp.ContentLength
	} else {
		return nil, &errPermanentStatus{status: resp.Status}
	}
	defer file.Close()

	// Free-disk-space pre-check (audit item 12/E): once the server has told
	// us how many bytes remain, confirm the destination filesystem actually
	// has room before starting the write loop, rather than filling the disk
	// partway through a multi-gigabyte transfer. Best-effort: if the space
	// can't be determined (unsupported platform/filesystem), skip silently
	// rather than blocking a download that might otherwise succeed.
	if totalBytes > 0 {
		remaining := totalBytes - currentBytes
		if remaining > 0 {
			if free, spaceErr := freeDiskSpaceBytes(filepath.Dir(tmpPath)); spaceErr == nil && int64(free) < remaining {
				return nil, fmt.Errorf("insufficient disk space: need %d more bytes for this download, only %d available on %s", remaining, free, filepath.Dir(tmpPath))
			}
		}
	}

	// Stall watchdog (audit item 11/E): if no new bytes arrive for
	// stallTimeout, cancel attemptCtx so a blocked Read() unblocks, rather
	// than waiting on a connection that has gone silent forever. net/http
	// doesn't expose a per-read deadline on resp.Body, so cancelling the
	// request's own context is the mechanism available.
	//
	// Started here, BEFORE InspectAndDecodeStream, not just around the main
	// read loop below: InspectAndDecodeStream does its own blocking
	// io.ReadFull of the first 512 bytes to sniff the stream format, and a
	// server that accepts the connection, sends headers, then sends zero
	// body bytes and goes silent would otherwise hang there completely
	// unprotected, before the main loop (and the watchdog that used to only
	// guard it) ever starts.
	done := make(chan struct{})
	lastProgressAt := int64(0)
	atomic.StoreInt64(&lastProgressAt, time.Now().UnixNano())
	var stalled int32
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				last := atomic.LoadInt64(&lastProgressAt)
				if time.Since(time.Unix(0, last)) > stallTimeout {
					atomic.StoreInt32(&stalled, 1)
					cancelAttempt()
					return
				}
			}
		}
	}()

	decoded, err := InspectAndDecodeStream(resp.Body)
	if err != nil {
		close(done)
		if atomic.LoadInt32(&stalled) == 1 {
			return nil, fmt.Errorf("%w for %s (while identifying the stream format)", errStalled, stallTimeout)
		}
		return nil, fmt.Errorf("failed decoding stream payload: %w", err)
	}
	defer decoded.Cleanup()

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

	buffer := make([]byte, 128*1024)
	for {
		select {
		case <-attemptCtx.Done():
			close(done)
			if atomic.LoadInt32(&stalled) == 1 {
				return nil, fmt.Errorf("%w for %s", errStalled, stallTimeout)
			}
			return nil, ErrDownloadPaused
		default:
		}

		n, readErr := decoded.Reader.Read(buffer)
		if n > 0 {
			if _, writeErr := file.Write(buffer[:n]); writeErr != nil {
				close(done)
				return nil, writeErr
			}
			atomic.AddInt64(&currentBytes, int64(n))
			atomic.StoreInt64(&lastProgressAt, time.Now().UnixNano())
		}

		if readErr != nil {
			close(done)
			if errors.Is(readErr, io.EOF) {
				break
			}
			// A Read() already in flight when the watchdog cancels
			// attemptCtx surfaces here as a transport-level error (not
			// caught by the attemptCtx.Done() select above, which only
			// catches cancellation discovered BETWEEN reads) — relabel it
			// as a stall if that's what actually happened.
			if atomic.LoadInt32(&stalled) == 1 {
				return nil, fmt.Errorf("%w for %s", errStalled, stallTimeout)
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
	buffer := make([]byte, 1024*1024)
	if _, err := io.CopyBuffer(hasher, f, buffer); err != nil {
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
