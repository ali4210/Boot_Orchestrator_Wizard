// Package discovery provides the unified catalog of installable operating
// systems with hierarchical folder categorization and multi-mirror failover.
package discovery

import (
	"embed"
	"encoding/json"
	"fmt"
	"strings"
)

//go:embed manifests/*.json
var embeddedManifests embed.FS

// Family is the top-level OS family.
type Family string

const (
	FamilyLinux   Family = "linux"
	FamilyWindows Family = "windows"
	FamilyMacOS   Family = "macos"
)

// Category organizes distributions into interactive TUI folders.
type Category string

const (
	CategoryWindows   Category = "Windows Family"
	CategoryKali      Category = "Kali Linux"
	CategoryParrot    Category = "Parrot OS"
	CategoryUbuntu    Category = "Ubuntu / Debian"
	CategoryArchOther Category = "Arch & Specialty Distros"
	CategoryMacOS     Category = "Apple macOS"
)

// Flavor distinguishes minimal/TTY installs from full desktop GUI installs.
type Flavor string

const (
	FlavorTTY    Flavor = "tty"
	FlavorGUI    Flavor = "gui"
	FlavorServer Flavor = "server"
)

// Arch is the target CPU architecture.
type Arch string

const (
	ArchAMD64 Arch = "amd64"
	ArchARM64 Arch = "arm64"
	ArchI386  Arch = "i386"
)

// Entry is a single installable OS image in the catalog.
type Entry struct {
	ID              string   `json:"id"`
	Family          Family   `json:"family"`
	Category        Category `json:"category"`
	Distro          string   `json:"distro"`
	Version         string   `json:"version"`
	Codename        string   `json:"codename,omitempty"`
	Flavor          Flavor   `json:"flavor"`
	Arch            Arch     `json:"arch"`
	DownloadURL     string   `json:"download_url,omitempty"`
	Mirrors         []string `json:"mirrors,omitempty"`
	SHA256          string   `json:"sha256,omitempty"`

	// SignatureURL/SigningKeyURL (audit 2.7/item 28) point at a detached
	// OpenPGP signature (armored or binary) over the download and an
	// ASCII-armored public keyring to verify it against, respectively.
	// A SHA256 checksum alone doesn't protect against a compromised
	// mirror serving a tampered file alongside a tampered checksum of
	// its own, obtained from that same mirror — a signature made by the
	// distro's own key, fetched independently, closes that gap. Both
	// empty (the case for every current catalog entry — see item 20/28's
	// notes in PROGRESS.md on why real values aren't invented here) means
	// no signature check runs for that entry, exactly like SHA256 being
	// empty today.
	SignatureURL  string `json:"signature_url,omitempty"`
	SigningKeyURL string `json:"signing_key_url,omitempty"`
	ApproxSizeBytes uint64   `json:"approx_size_bytes,omitempty"`
	RequiresLicense bool     `json:"requires_license,omitempty"`
	MinDiskGB       int      `json:"min_disk_gb"`
	Notes           string   `json:"notes,omitempty"`
	EOL             bool     `json:"eol,omitempty"`
	OpenCoreProfile string   `json:"opencore_profile,omitempty"`

	// Unavailable marks an entry whose download is currently known not to
	// work at all — a dead mirror, an expired/forbidden signed URL — as
	// opposed to EOL, which is a "proceed anyway if you want" choice.
	// There is no safe way to provision from an entry whose own URLs 404/
	// 403 today, so it's hidden from the TUI's catalog listing
	// (GetEntriesByCategory/ListCategories) and refused outright in
	// headless mode, until UnavailableReason's underlying problem is
	// actually fixed in the data (audit follow-up G1; see
	// OS_STATUS_MATRIX.md for how each of these was confirmed dead, not
	// assumed).
	Unavailable       bool   `json:"unavailable,omitempty"`
	UnavailableReason string `json:"unavailable_reason,omitempty"`
}

// GetMirrors returns all available mirror URLs, falling back to DownloadURL.
func (e *Entry) GetMirrors() []string {
	if len(e.Mirrors) > 0 {
		return e.Mirrors
	}
	if e.DownloadURL != "" {
		return []string{e.DownloadURL}
	}
	return nil
}

// Catalog holds all known entries loaded from manifests.
type Catalog struct {
	Entries []Entry
}

// LoadEmbedded loads the catalog from all JSON manifests bundled into the binary.
func LoadEmbedded() (*Catalog, error) {
	c := &Catalog{}
	manifestFiles := []string{
		"manifests/windows.json",
		"manifests/linux.json",
		"manifests/macos.json",
	}

	for _, name := range manifestFiles {
		data, err := embeddedManifests.ReadFile(name)
		if err != nil {
			// macos.json can be optional initially
			if name == "manifests/macos.json" {
				continue
			}
			return nil, fmt.Errorf("reading embedded manifest %s: %w", name, err)
		}
		var entries []Entry
		if err := json.Unmarshal(data, &entries); err != nil {
			return nil, fmt.Errorf("parsing manifest %s: %w", name, err)
		}
		for i := range entries {
			assignCategory(&entries[i])
		}
		c.Entries = append(c.Entries, entries...)
	}
	return c, nil
}

// assignCategory ensures every distribution has a valid category folder.
func assignCategory(e *Entry) {
	if e.Category != "" {
		return
	}
	switch e.Family {
	case FamilyWindows:
		e.Category = CategoryWindows
	case FamilyMacOS:
		e.Category = CategoryMacOS
	case FamilyLinux:
		lower := strings.ToLower(e.Distro)
		switch {
		case strings.Contains(lower, "kali"):
			e.Category = CategoryKali
		case strings.Contains(lower, "parrot"):
			e.Category = CategoryParrot
		case strings.Contains(lower, "ubuntu") || strings.Contains(lower, "debian"):
			e.Category = CategoryUbuntu
		default:
			e.Category = CategoryArchOther
		}
	}
}

// ListCategories returns a deduplicated list of available category folders.
func (c *Catalog) ListCategories() []Category {
	seen := make(map[Category]bool)
	var categories []Category

	order := []Category{
		CategoryKali,
		CategoryParrot,
		CategoryUbuntu,
		CategoryArchOther,
		CategoryWindows,
		CategoryMacOS,
	}

	for _, cat := range order {
		for _, e := range c.Entries {
			if e.Category == cat && !e.Unavailable && !seen[cat] {
				seen[cat] = true
				categories = append(categories, cat)
				break
			}
		}
	}
	return categories
}

// GetEntriesByCategory filters entries by folder category, hiding any
// entry marked Unavailable (audit follow-up G1) — there is no safe way to
// provision from media whose own download URLs are confirmed dead, so
// unlike EOL (still listed, just tagged), these never appear for the TUI
// to offer at all.
func (c *Catalog) GetEntriesByCategory(cat Category) []Entry {
	var out []Entry
	for _, e := range c.Entries {
		if e.Unavailable {
			continue
		}
		if e.Category == cat {
			out = append(out, e)
		}
	}
	return out
}

// Merge adds externally sourced entries, replacing duplicates by ID.
func (c *Catalog) Merge(entries []Entry) {
	byID := make(map[string]int, len(c.Entries))
	for i, e := range c.Entries {
		byID[e.ID] = i
	}
	for _, e := range entries {
		assignCategory(&e)
		if idx, ok := byID[e.ID]; ok {
			c.Entries[idx] = e
		} else {
			c.Entries = append(c.Entries, e)
			byID[e.ID] = len(c.Entries) - 1
		}
	}
}

// Filter matches non-zero-value criteria.
func (c *Catalog) Filter(family Family, flavor Flavor, arch Arch, includeEOL bool) []Entry {
	var out []Entry
	for _, e := range c.Entries {
		if e.Unavailable {
			continue
		}
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

// FuzzyMatch performs case-insensitive substring search across all descriptive fields.
func (c *Catalog) FuzzyMatch(query string) []Entry {
	if query == "" {
		return c.Entries
	}
	q := strings.ToLower(query)
	var out []Entry
	for _, e := range c.Entries {
		if strings.Contains(strings.ToLower(e.Distro), q) ||
			strings.Contains(strings.ToLower(e.Version), q) ||
			strings.Contains(strings.ToLower(e.Codename), q) ||
			strings.Contains(strings.ToLower(string(e.Category)), q) ||
			strings.Contains(strings.ToLower(e.ID), q) {
			out = append(out, e)
		}
	}
	return out
}
