package discovery

import "strings"

// EntryAuditResult is the outcome of auditing one catalog Entry for the
// two properties every entry must have before a user should be able to
// select it (audit item 13): at least one working download URL, and a
// checksum to verify the download against. "Working" here means present
// and well-formed; live reachability (HEAD request) is Phase 3's job, not
// this function's.
type EntryAuditResult struct {
	Entry        Entry
	HasURL       bool
	HasChecksum  bool
	Broken       bool
	BrokenReason string
}

// AuditEntry checks e for a non-empty download URL/mirror and a non-empty
// SHA256 checksum.
func AuditEntry(e Entry) EntryAuditResult {
	hasURL := len(e.GetMirrors()) > 0
	hasChecksum := strings.TrimSpace(e.SHA256) != ""

	var reasons []string
	if !hasURL {
		reasons = append(reasons, "no download URL or mirrors")
	}
	if !hasChecksum {
		reasons = append(reasons, "no SHA256 checksum")
	}

	return EntryAuditResult{
		Entry:        e,
		HasURL:       hasURL,
		HasChecksum:  hasChecksum,
		Broken:       len(reasons) > 0,
		BrokenReason: strings.Join(reasons, "; "),
	}
}

// Audit runs AuditEntry over every entry in the catalog and returns only
// the broken ones, in catalog order.
func (c *Catalog) Audit() []EntryAuditResult {
	var broken []EntryAuditResult
	for _, e := range c.Entries {
		if r := AuditEntry(e); r.Broken {
			broken = append(broken, r)
		}
	}
	return broken
}
