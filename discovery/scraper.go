package discovery

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"time"
)

// ubuntuDistsIndexURL is Ubuntu's official package archive mirror. Reachable
// from this build environment's network allowlist, so this scraper is
// LIVE-TESTED, not just written-and-hoped.
const ubuntuDistsIndexURL = "https://archive.ubuntu.com/ubuntu/dists/"

var suiteLinkRe = regexp.MustCompile(`href="([a-z]+)/"`)

// suiteExcludeSuffixes filters out pocket subdirectories (updates/security/
// backports/proposed) and the rolling "devel" alias, leaving only the base
// release codenames (e.g. "noble", "jammy", "focal").
var suiteExclude = map[string]bool{
	"devel": true,
}

// ScrapeUbuntuSuites fetches the live list of Ubuntu release codenames
// currently published on the archive. Returns them newest-first by the
// server's reported directory ordering isn't guaranteed alphabetical, so we
// sort lexically here (good enough to at least be deterministic; codename
// alphabetical order happens to roughly follow release order for Ubuntu's
// naming convention within a letter, though not across the full history).
func ScrapeUbuntuSuites(ctx httpContext) ([]string, error) {
	client := &http.Client{Timeout: 15 * time.Second}

	req, err := newGetRequest(ubuntuDistsIndexURL)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching %s: %w", ubuntuDistsIndexURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d from %s", resp.StatusCode, ubuntuDistsIndexURL)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1MB cap, this is a small index page
	if err != nil {
		return nil, fmt.Errorf("reading response body: %w", err)
	}

	seen := map[string]bool{}
	var suites []string
	for _, m := range suiteLinkRe.FindAllStringSubmatch(string(body), -1) {
		name := m[1]
		if suiteExclude[name] {
			continue
		}
		// Skip pocket subdirectories: only keep the bare codename, which
		// appears both as "noble/" and as prefixes like "noble-updates/".
		// We only want entries with no hyphen suffix (the base suite).
		if hasHyphenPocketSuffix(name) {
			continue
		}
		if !seen[name] {
			seen[name] = true
			suites = append(suites, name)
		}
	}

	sort.Strings(suites)
	return suites, nil
}

func hasHyphenPocketSuffix(s string) bool {
	for _, suffix := range []string{"-updates", "-security", "-backports", "-proposed"} {
		if len(s) > len(suffix) && s[len(s)-len(suffix):] == suffix {
			return true
		}
	}
	return false
}

// httpContext exists only so the exported function signature doesn't force
// callers into context.Context today while leaving room to add real
// cancellation later without another breaking signature change.
type httpContext = struct{}

func newGetRequest(url string) (*http.Request, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "boot-orchestrator-discovery/0.1")
	return req, nil
}

// VerifySHA256 computes the SHA-256 of a local file and compares it against
// the expected hex-encoded checksum from the catalog entry. This is the
// same primitive engine/streamer.go will use for downloaded images, exposed
// here so discovery can pre-validate manifest checksums against real files
// (e.g. in tests or offline caches) without depending on the engine package.
func VerifySHA256(path string, expectedHex string) (ok bool, actualHex string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return false, "", fmt.Errorf("opening %s: %w", path, err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false, "", fmt.Errorf("hashing %s: %w", path, err)
	}

	actualHex = hex.EncodeToString(h.Sum(nil))
	return actualHex == expectedHex, actualHex, nil
}
