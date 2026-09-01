// Package discovery provides the unified catalog of installable operating
// systems (Linux distributions and Windows releases), each tagged with
// flavor (TTY/minimal vs. GUI/desktop), architecture, and download metadata.
package discovery

import (
	"embed"
	"encoding/json"
	"fmt"
)

//go:embed manifests/*.json
var embeddedManifests embed.FS

// Family is the top-level OS family.
type Family string

const (
	FamilyLinux   Family = "linux"
	FamilyWindows Family = "windows"
)

// Flavor distinguishes minimal/TTY installs from full desktop GUI installs,
// per the blueprint's Profile Filter.
type Flavor string

const (
	FlavorTTY    Flavor = "tty"    // minimal/server/cloud rootfs, no desktop environment
	FlavorGUI    Flavor = "gui"    // full desktop experience
	FlavorServer Flavor = "server" // Windows Server Core / headless server editions
)

// Arch is the target CPU architecture.
type Arch string

const (
	ArchAMD64 Arch = "amd64"
	ArchARM64 Arch = "arm64"
	ArchI386  Arch = "i386" // needed for XP/older Windows and 32-bit-only legacy distros
)

// Entry is a single installable OS image in the catalog.
type Entry struct {
	ID          string `json:"id"`   // unique slug, e.g. "ubuntu-24.04-gui-amd64"
	Family      Family `json:"family"`
	Distro      string `json:"distro"`  // e.g. "Ubuntu", "Kali Linux", "Windows 11"
	Version     string `json:"version"` // e.g. "24.04", "11", "2022"
	Codename    string `json:"codename,omitempty"`
	Flavor      Flavor `json:"flavor"`
	Arch        Arch   `json:"arch"`
	// DownloadURL may be empty for entries that must be resolved dynamically
	// via a scraper (e.g. Windows ISOs behind license-gated portals, or
	// Ubuntu releases whose exact filename changes with point releases).
	DownloadURL      string `json:"download_url,omitempty"`
	SHA256           string `json:"sha256,omitempty"`
	ApproxSizeBytes  uint64 `json:"approx_size_bytes,omitempty"`
	RequiresLicense  bool   `json:"requires_license,omitempty"` // true for Windows retail media
	MinDiskGB        int    `json:"min_disk_gb"`
	Notes            string `json:"notes,omitempty"`
	EOL              bool   `json:"eol,omitempty"` // end-of-life / unsupported by upstream
}

// Catalog is the full set of known entries, loaded from the bundled
// manifests and optionally augmented at runtime by scrapers.
type Catalog struct {
	Entries []Entry
}

// LoadEmbedded loads the catalog from the manifests bundled into the binary
// at compile time (manifests/windows.json, manifests/linux.json).
func LoadEmbedded() (*Catalog, error) {
	c := &Catalog{}
	for _, name := range []string{"manifests/windows.json", "manifests/linux.json"} {
		data, err := embeddedManifests.ReadFile(name)
		if err != nil {
			return nil, fmt.Errorf("reading embedded manifest %s: %w", name, err)
		}
		var entries []Entry
		if err := json.Unmarshal(data, &entries); err != nil {
			return nil, fmt.Errorf("parsing manifest %s: %w", name, err)
		}
		c.Entries = append(c.Entries, entries...)
	}
	return c, nil
}

// Merge adds externally-sourced entries (e.g. from a live scraper) into the
// catalog, replacing any existing entry with the same ID.
func (c *Catalog) Merge(entries []Entry) {
	byID := make(map[string]int, len(c.Entries))
	for i, e := range c.Entries {
		byID[e.ID] = i
	}
	for _, e := range entries {
		if idx, ok := byID[e.ID]; ok {
			c.Entries[idx] = e
		} else {
			c.Entries = append(c.Entries, e)
			byID[e.ID] = len(c.Entries) - 1
		}
	}
}

// Filter returns entries matching all provided non-zero-value criteria.
// Pass "" / zero-value for any field you don't want to filter on.
func (c *Catalog) Filter(family Family, flavor Flavor, arch Arch, includeEOL bool) []Entry {
	var out []Entry
	for _, e := range c.Entries {
		if family != "" && e.Family != family {
			continue
		}
		if flavor != "" && e.Flavor != flavor {
			continue
		}
		if arch != "" && e.Arch != arch {
			continue
		}
		if e.EOL && !includeEOL {
			continue
		}
		out = append(out, e)
	}
	return out
}

// FuzzyMatch does a simple case-insensitive substring search across Distro,
// Version, and Codename — a placeholder for the TUI's "instant fuzzy search"
// requirement until the full fuzzy-matching library is wired into ui/.
func (c *Catalog) FuzzyMatch(query string) []Entry {
	if query == "" {
		return c.Entries
	}
	q := toLower(query)
	var out []Entry
	for _, e := range c.Entries {
		if containsFold(e.Distro, q) || containsFold(e.Version, q) || containsFold(e.Codename, q) || containsFold(e.ID, q) {
			out = append(out, e)
		}
	}
	return out
}
