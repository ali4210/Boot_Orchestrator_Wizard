package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// shrinkDownloadTimings shrinks the package-level stall/retry timing vars
// for the duration of a test, so these tests run in milliseconds instead
// of waiting out real-world stall/backoff durations, and restores them
// afterward.
func shrinkDownloadTimings(t *testing.T, stall time.Duration, maxAttempts int, baseDelay time.Duration) {
	t.Helper()
	origStall, origAttempts, origDelay := stallTimeout, downloadMaxAttempts, downloadRetryBaseDelay
	stallTimeout, downloadMaxAttempts, downloadRetryBaseDelay = stall, maxAttempts, baseDelay
	t.Cleanup(func() {
		stallTimeout, downloadMaxAttempts, downloadRetryBaseDelay = origStall, origAttempts, origDelay
	})
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// hijackableHandler lets a test take over the raw TCP connection to produce
// behaviors net/http's ResponseWriter can't (e.g. advertising a
// Content-Length then sending fewer bytes and closing — the server-side
// half of a dropped/truncated transfer).
func hijackableHandler(fn func(conn net.Conn)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "hijack not supported", http.StatusInternalServerError)
			return
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		fn(conn)
	}
}

// TestStreamDownload_StallMidBody_EventuallyFailsFast is the regression
// test for audit item 11/E's core bug: before the stall watchdog, a
// connection that stayed open but stopped sending data would block
// forever (neither client.Timeout, which is disabled, nor the transport's
// header/idle timeouts cover an in-progress body read). With the watchdog,
// a permanently stalled server now fails within a bounded time instead of
// hanging.
func TestStreamDownload_StallMidBody_EventuallyFailsFast(t *testing.T) {
	shrinkDownloadTimings(t, 150*time.Millisecond, 2, 10*time.Millisecond)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000000")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("partial-data-then-silence"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// Never write again; hold the connection open until the test's
		// http.Client gives up or the test itself tears down the server.
		<-r.Context().Done()
	}))
	defer srv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "payload.bin")

	done := make(chan error, 1)
	go func() {
		_, err := StreamDownload(context.Background(), srv.URL, dest, "", nil)
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatalf("expected the permanently stalled download to fail")
		}
		if !strings.Contains(err.Error(), "stalled") {
			t.Fatalf("expected the error to mention a stall, got: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("download did not fail within 5s — the stall watchdog did not fire (this is exactly the pre-fix hang-forever bug)")
	}
}

// TestStreamDownload_StallThenRecovers_SucceedsOnRetry proves the retry
// wrapper actually recovers from a transient stall: the first connection
// attempt stalls and gets abandoned, but the download succeeds overall
// because the retry resumes against a server that behaves normally on the
// next attempt.
func TestStreamDownload_StallThenRecovers_SucceedsOnRetry(t *testing.T) {
	shrinkDownloadTimings(t, 150*time.Millisecond, 3, 10*time.Millisecond)

	payload := []byte(strings.Repeat("ok-data-", 4096)) // 32KB
	var attempts int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&attempts, 1)
		if n == 1 {
			// First attempt: accept, then go silent forever.
			w.Header().Set("Content-Length", fmt.Sprintf("%d", len(payload)))
			w.WriteHeader(http.StatusOK)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			<-r.Context().Done()
			return
		}
		// Second attempt onward: behave normally.
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(payload)))
		w.WriteHeader(http.StatusOK)
		w.Write(payload)
	}))
	defer srv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "payload.bin")

	res, err := StreamDownload(context.Background(), srv.URL, dest, sha256Hex(payload), nil)
	if err != nil {
		t.Fatalf("expected the download to succeed after one stalled attempt, got: %v", err)
	}
	if res.BytesTotal != int64(len(payload)) {
		t.Fatalf("expected %d bytes, got %d", len(payload), res.BytesTotal)
	}
	if atomic.LoadInt32(&attempts) < 2 {
		t.Fatalf("expected at least 2 connection attempts, got %d", attempts)
	}
}

// TestStreamDownload_TruncatedWithDeclaredContentLength_Fails proves a
// connection that advertises a Content-Length but closes after sending
// fewer bytes is correctly treated as a real failure (io.ErrUnexpectedEOF,
// not io.EOF) — not silently accepted as a complete download. This was
// already correct before this audit's fix (net/http's own body wrapper
// makes this distinction), so this test locks in that existing behavior
// rather than reproducing a bug.
func TestStreamDownload_TruncatedWithDeclaredContentLength_Fails(t *testing.T) {
	shrinkDownloadTimings(t, 2*time.Second, 1, 10*time.Millisecond)

	srv := httptest.NewServer(hijackableHandler(func(conn net.Conn) {
		fmt.Fprintf(conn, "HTTP/1.1 200 OK\r\nContent-Length: 1000\r\nConnection: close\r\n\r\n")
		conn.Write([]byte("only-100-bytes-of-a-1000-byte-promise..."))
		// Close without sending the remaining declared bytes.
	}))
	defer srv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "payload.bin")

	_, err := StreamDownload(context.Background(), srv.URL, dest, "", nil)
	if err == nil {
		t.Fatalf("expected a declared-but-undelivered Content-Length to fail, not succeed")
	}
}

// TestStreamDownload_NoContentLengthEarlyClose_CaughtByChecksum
// demonstrates the genuinely ambiguous case the HTTP protocol itself
// cannot resolve: a response with NO Content-Length and not
// chunked-encoded is framed by "read until the connection closes" — a
// server that sends only part of the payload and then closes looks
// identical, at the transport layer, to one that intentionally finished
// early. The transport layer reports success either way; only an
// end-to-end checksum (via StreamDownloadWithFailover, not
// downloadFromSingleMirror alone) can catch this. This is exactly why
// audit item 13 (every manifest entry must have a checksum) matters.
func TestStreamDownload_NoContentLengthEarlyClose_CaughtByChecksum(t *testing.T) {
	shrinkDownloadTimings(t, 2*time.Second, 1, 10*time.Millisecond)

	fullPayload := []byte(strings.Repeat("X", 10000))
	truncated := fullPayload[:100]

	srv := httptest.NewServer(hijackableHandler(func(conn net.Conn) {
		// No Content-Length, no Transfer-Encoding: chunked — HTTP/1.0-style
		// "close signals end of body" framing.
		fmt.Fprintf(conn, "HTTP/1.1 200 OK\r\nConnection: close\r\n\r\n")
		conn.Write(truncated)
	}))
	defer srv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "payload.bin")

	// Without a checksum, this "succeeds" with truncated content — the
	// transport layer has no way to know it was supposed to get more.
	res, err := StreamDownload(context.Background(), srv.URL, dest, "", nil)
	if err != nil {
		t.Fatalf("expected the transport layer to report success (it cannot detect this truncation on its own), got: %v", err)
	}
	if res.BytesTotal != int64(len(truncated)) {
		t.Fatalf("expected the truncated byte count %d, got %d", len(truncated), res.BytesTotal)
	}

	// With the correct checksum of the FULL file, StreamDownloadWithFailover
	// must catch the mismatch and fail instead of accepting the corrupt
	// payload.
	dest2 := filepath.Join(dir, "payload2.bin")
	_, err = StreamDownload(context.Background(), srv.URL, dest2, sha256Hex(fullPayload), nil)
	if err == nil {
		t.Fatalf("expected a checksum mismatch against the truncated payload to be caught")
	}
	if !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("expected the error to mention a checksum mismatch, got: %v", err)
	}
}

// TestStreamDownload_HTMLErrorPageServedAs200_CaughtByChecksum covers a
// mirror that returns 200 OK with an HTML error/placeholder page instead
// of the real payload (a common CDN/expired-URL failure mode) — the
// transport layer sees a perfectly normal, complete 200 response, so only
// the checksum catches it.
func TestStreamDownload_HTMLErrorPageServedAs200_CaughtByChecksum(t *testing.T) {
	shrinkDownloadTimings(t, 2*time.Second, 1, 10*time.Millisecond)

	realPayload := []byte(strings.Repeat("REAL-ISO-BYTES-", 1000))
	htmlErrorPage := []byte("<html><body>404 - mirror expired</body></html>")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		w.Write(htmlErrorPage)
	}))
	defer srv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "payload.bin")

	_, err := StreamDownload(context.Background(), srv.URL, dest, sha256Hex(realPayload), nil)
	if err == nil {
		t.Fatalf("expected an HTML error page served as 200 to fail checksum verification")
	}
	if !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("expected a checksum-mismatch error, got: %v", err)
	}
}

// TestStreamDownload_RedirectChain_Succeeds proves a multi-hop redirect
// chain is followed transparently (via net/http's default client
// behavior) all the way to a server that serves the real payload.
func TestStreamDownload_RedirectChain_Succeeds(t *testing.T) {
	payload := []byte("the-actual-payload-after-two-redirects")

	final := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(payload)
	}))
	defer final.Close()

	hop2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, final.URL, http.StatusFound)
	}))
	defer hop2.Close()

	hop1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, hop2.URL, http.StatusFound)
	}))
	defer hop1.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "payload.bin")

	res, err := StreamDownload(context.Background(), hop1.URL, dest, sha256Hex(payload), nil)
	if err != nil {
		t.Fatalf("expected a redirect chain to be followed to the real payload, got: %v", err)
	}
	if res.BytesTotal != int64(len(payload)) {
		t.Fatalf("unexpected byte count: %d", res.BytesTotal)
	}
}

// TestStreamDownload_Mirror403FailsOverToWorkingMirror proves a mirror
// that returns an error status is skipped in favor of the next one,
// rather than aborting the whole download.
func TestStreamDownload_Mirror403FailsOverToWorkingMirror(t *testing.T) {
	payload := []byte("served-by-the-good-mirror")

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer bad.Close()

	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(payload)
	}))
	defer good.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "payload.bin")

	res, err := StreamDownloadWithFailover(context.Background(), []string{bad.URL, good.URL}, dest, sha256Hex(payload), nil)
	if err != nil {
		t.Fatalf("expected failover to the working mirror to succeed, got: %v", err)
	}
	if res.MirrorUsed != good.URL {
		t.Fatalf("expected the good mirror to be used, got %q", res.MirrorUsed)
	}
}

// TestStreamDownload_ServerIgnoresRange_RestartsCleanly proves that when
// a resume attempt's Range header is ignored by the server (it answers
// 200 with the full body instead of 206 with the partial range), the
// existing fallback path (os.Create truncates and restarts from 0) still
// produces a correct, complete file rather than a corrupted
// partial-plus-full-again concatenation.
func TestStreamDownload_ServerIgnoresRange_RestartsCleanly(t *testing.T) {
	payload := []byte(strings.Repeat("FULL-PAYLOAD-", 500))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Always answers 200 with the full body, regardless of any Range
		// header — i.e. a server that does not support Range at all.
		w.WriteHeader(http.StatusOK)
		w.Write(payload)
	}))
	defer srv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "payload.bin")
	tmpPath := dest + ".part"

	// Seed a stale/partial .part file, simulating an interrupted prior
	// attempt, to force a resume attempt with a Range header.
	if err := os.WriteFile(tmpPath, []byte("STALE-PARTIAL-DATA"), 0644); err != nil {
		t.Fatalf("seeding stale .part file: %v", err)
	}

	res, err := StreamDownload(context.Background(), srv.URL, dest, sha256Hex(payload), nil)
	if err != nil {
		t.Fatalf("expected a clean restart when the server ignores Range, got: %v", err)
	}
	if res.BytesTotal != int64(len(payload)) {
		t.Fatalf("expected exactly the full payload length %d, got %d (stale data was not discarded correctly)", len(payload), res.BytesTotal)
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("reading final file: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("final file content does not match the expected payload exactly")
	}
}

// TestStreamDownload_ChunkedTransferEncoding_Succeeds is a sanity check
// that a normal chunked-encoded response (no Content-Length at all, the
// common case for dynamically generated or compressed responses) downloads
// correctly.
func TestStreamDownload_ChunkedTransferEncoding_Succeeds(t *testing.T) {
	payload := []byte(strings.Repeat("chunked-data-", 2000))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Not setting Content-Length and flushing mid-write forces Go's
		// http server to use chunked transfer encoding.
		fw := w.(http.Flusher)
		half := len(payload) / 2
		w.Write(payload[:half])
		fw.Flush()
		w.Write(payload[half:])
	}))
	defer srv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "payload.bin")

	res, err := StreamDownload(context.Background(), srv.URL, dest, sha256Hex(payload), nil)
	if err != nil {
		t.Fatalf("expected a chunked response to download correctly, got: %v", err)
	}
	if res.BytesTotal != int64(len(payload)) {
		t.Fatalf("unexpected byte count: %d", res.BytesTotal)
	}
}

// TestStreamDownload_StallDuringInitialFormatSniff_StillCaught is the
// regression test proving the watchdog protects InspectAndDecodeStream's
// own initial blocking read (the first 512 bytes, used to sniff zip/
// compressed formats), not just the main copy loop — a server that
// accepts the connection, sends headers, and then sends ZERO body bytes
// before going silent must still be caught by the stall watchdog.
func TestStreamDownload_StallDuringInitialFormatSniff_StillCaught(t *testing.T) {
	shrinkDownloadTimings(t, 150*time.Millisecond, 1, 10*time.Millisecond)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// Zero body bytes ever sent.
		<-r.Context().Done()
	}))
	defer srv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "payload.bin")

	done := make(chan error, 1)
	go func() {
		_, err := StreamDownload(context.Background(), srv.URL, dest, "", nil)
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatalf("expected a zero-body stall during format sniffing to fail")
		}
		if !strings.Contains(err.Error(), "stalled") {
			t.Fatalf("expected the error to mention a stall, got: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("download did not fail within 5s — a stall during the initial format-sniff read was not caught")
	}
}

// TestFreeDiskSpaceBytes_ReturnsAPlausibleValue is a basic sanity check of
// the free-disk-space pre-check's underlying platform call (audit item
// 12/E) — not a full simulation of a disk-full condition (not practical to
// construct safely in a unit test), but confirms the function actually
// returns a usable, nonzero value against a real directory.
func TestFreeDiskSpaceBytes_ReturnsAPlausibleValue(t *testing.T) {
	free, err := freeDiskSpaceBytes(t.TempDir())
	if err != nil {
		t.Fatalf("freeDiskSpaceBytes: %v", err)
	}
	if free == 0 {
		t.Fatalf("expected a nonzero free space value")
	}
}

// TestStreamDownload_ProgressReportingIsNonBlocking proves progress
// reporting can never stall the download itself: a slow/blocking
// onProgress callback must not slow down (or deadlock) the transfer, since
// the copy loop only touches atomic counters and a separate ticker
// goroutine is responsible for invoking onProgress.
func TestStreamDownload_ProgressReportingIsNonBlocking(t *testing.T) {
	payload := []byte(strings.Repeat("data-", 200000)) // ~1MB

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(payload)
	}))
	defer srv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "payload.bin")

	blockingOnProgress := func(Progress) {
		time.Sleep(500 * time.Millisecond) // deliberately slow
	}

	start := time.Now()
	res, err := StreamDownload(context.Background(), srv.URL, dest, sha256Hex(payload), blockingOnProgress)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("StreamDownload: %v", err)
	}
	if res.BytesTotal != int64(len(payload)) {
		t.Fatalf("unexpected byte count: %d", res.BytesTotal)
	}
	// A ~1MB download over loopback should take well under a second if
	// progress reporting isn't in the hot path; allow generous headroom.
	if elapsed > 3*time.Second {
		t.Fatalf("download took %s — a slow onProgress callback appears to be blocking the transfer", elapsed)
	}
}
