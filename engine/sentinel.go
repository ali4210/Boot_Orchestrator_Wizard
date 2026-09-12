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
	"strings"
	"time"
)

// MirrorVerdict captures the real-time health telemetry of an endpoint.
type MirrorVerdict struct {
	URL           string
	IsAlive       bool
	SupportsRange bool
	SpeedMBs      float64
	ContentLength int64
	MimeType      string
	Err           error
}

// SentinelConfig governs preflight testing parameters.
type SentinelConfig struct {
	ProbeTimeout     time.Duration
	MinThroughputMBs float64
	SampleSizeBytes  int64
	MaxRedirectHops  int
	DisallowedMimes  []string
}

// DefaultSentinelConfig provides balanced production defaults.
func DefaultSentinelConfig() SentinelConfig {
	return SentinelConfig{
		ProbeTimeout:     7 * time.Second,
		MinThroughputMBs: 0.35,            // Reject connections under ~350 KB/s as throttled
		SampleSizeBytes:  2 * 1024 * 1024, // 2 MB probe sample
		MaxRedirectHops:  10,
		DisallowedMimes:  []string{"text/html", "text/plain", "application/json", "text/xml"},
	}
}

// ProbeMirror executes the 4-pillar health audit on a candidate mirror URL.
func ProbeMirror(ctx context.Context, rawURL string, cfg SentinelConfig) MirrorVerdict {
	verdict := MirrorVerdict{URL: rawURL}

	// 1. Local path bypass: Always 100% healthy
	if strings.HasPrefix(rawURL, "/") || strings.HasPrefix(rawURL, "~/") || !strings.Contains(rawURL, "://") {
		verdict.IsAlive = true
		verdict.SupportsRange = true
		verdict.SpeedMBs = 999.0
		return verdict
	}

	probeCtx, cancel := context.WithTimeout(ctx, cfg.ProbeTimeout)
	defer cancel()

	client := &http.Client{
		Timeout: cfg.ProbeTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= cfg.MaxRedirectHops {
				return errors.New("stopped after redirect recursion limit")
			}
			return nil
		},
	}

	// Issue Range probe request
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		verdict.Err = fmt.Errorf("request build error: %w", err)
		return verdict
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) Boot-Orchestrator-Universal/2.0")
	req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", cfg.SampleSizeBytes-1))

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		verdict.Err = fmt.Errorf("probe request failed: %w", err)
		return verdict
	}
	defer resp.Body.Close()

	verdict.ContentLength = resp.ContentLength
	verdict.MimeType = resp.Header.Get("Content-Type")

	// Filter out fake 200 HTML error pages, captive portals, WAF blocks
	for _, badMime := range cfg.DisallowedMimes {
		if strings.HasPrefix(strings.ToLower(verdict.MimeType), badMime) {
			verdict.Err = fmt.Errorf("rejected captive portal/WAF MIME type: %s", verdict.MimeType)
			return verdict
		}
	}

	if resp.StatusCode == http.StatusPartialContent {
		verdict.SupportsRange = true
	} else if resp.StatusCode == http.StatusOK {
		verdict.SupportsRange = false
	} else {
		verdict.Err = fmt.Errorf("http error code: %d", resp.StatusCode)
		return verdict
	}

	// Throughput floor audit: read probe sample
	buf := make([]byte, 64*1024)
	var downloaded int64
	for downloaded < cfg.SampleSizeBytes {
		n, rErr := resp.Body.Read(buf)
		if n > 0 {
			downloaded += int64(n)
		}
		if rErr != nil {
			if errors.Is(rErr, io.EOF) {
				break
			}
			verdict.Err = fmt.Errorf("probe read dropped: %w", rErr)
			return verdict
		}
	}

	elapsed := time.Since(start).Seconds()
	if elapsed > 0 {
		verdict.SpeedMBs = (float64(downloaded) / (1024 * 1024)) / elapsed
	}

	// Check throughput floor
	if downloaded >= cfg.SampleSizeBytes && verdict.SpeedMBs < cfg.MinThroughputMBs {
		verdict.Err = fmt.Errorf("throughput %.2f MB/s below minimum floor %.2f MB/s", verdict.SpeedMBs, cfg.MinThroughputMBs)
		return verdict
	}

	verdict.IsAlive = true
	return verdict
}

// SelectBestMirror iterates candidate mirrors, probes them with the Sentinel,
// and returns the fastest verified mirror.
func SelectBestMirror(ctx context.Context, mirrors []string) (string, []string) {
	if len(mirrors) == 0 {
		return "", nil
	}
	if len(mirrors) == 1 {
		return mirrors[0], mirrors
	}

	cfg := DefaultSentinelConfig()
	var validMirrors []string
	var fastestMirror string
	var topSpeed float64 = -1

	for _, m := range mirrors {
		clean := strings.TrimSpace(m)
		if clean == "" {
			continue
		}

		// Instant prioritize local files
		if strings.HasPrefix(clean, "/") || strings.HasPrefix(clean, "~/") || !strings.Contains(clean, "://") {
			return clean, append([]string{clean}, mirrors...)
		}

		verdict := ProbeMirror(ctx, clean, cfg)
		if verdict.IsAlive {
			validMirrors = append(validMirrors, clean)
			if verdict.SpeedMBs > topSpeed {
				topSpeed = verdict.SpeedMBs
				fastestMirror = clean
			}
		}
	}

	if fastestMirror != "" {
		// Place the fastest mirror at index 0
		var ordered []string
		ordered = append(ordered, fastestMirror)
		for _, m := range validMirrors {
			if m != fastestMirror {
				ordered = append(ordered, m)
			}
		}
		return fastestMirror, ordered
	}

	// If probe failed on all (e.g. strict firewall), fall back to raw input array
	return mirrors[0], mirrors
}

// FetchRemoteChecksum attempts to fetch upstream SHA256SUMS located in the same parent directory.
func FetchRemoteChecksum(ctx context.Context, isoURL, targetFilename string) (string, error) {
	lastSlash := strings.LastIndex(isoURL, "/")
	if lastSlash == -1 {
		return "", errors.New("invalid url format")
	}
	baseURL := isoURL[:lastSlash+1]

	checksumNames := []string{"SHA256SUMS", "sha256sum.txt", "SHA256SUMS.txt"}
	client := &http.Client{Timeout: 5 * time.Second}

	for _, name := range checksumNames {
		sumURL := baseURL + name
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, sumURL, nil)
		if err != nil {
			continue
		}
		resp, err := client.Do(req)
		if err != nil || resp.StatusCode != http.StatusOK {
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		lines := strings.Split(string(body), "\n")
		for _, line := range lines {
			if strings.Contains(line, targetFilename) {
				parts := strings.Fields(line)
				if len(parts) >= 2 && len(parts[0]) == 64 {
					return parts[0], nil
				}
			}
		}
	}
	return "", errors.New("upstream checksum file not resolved")
}

// VerifyFileSHA256 calculates and asserts SHA-256 for a committed local payload.
func VerifyFileSHA256(filePath, expected string) (bool, string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return false, "", err
	}
	defer f.Close()

	hasher := sha256.New()
	buf := make([]byte, 1024*1024)
	if _, err := io.CopyBuffer(hasher, f, buf); err != nil {
		return false, "", err
	}
	calc := hex.EncodeToString(hasher.Sum(nil))
	if expected == "" {
		return true, calc, nil
	}
	return strings.EqualFold(calc, expected), calc, nil
}
