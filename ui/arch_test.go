package ui

import (
	"testing"

	"boot-orchestrator/discovery"
)

// TestArchForGOARCH_MatchesHostArch is the regression test for audit
// finding 2.6 (x86_64 hardcoded despite ARM64 support) as it applies to
// synthetic (user-supplied/LAN-discovered) catalog entries: before this
// fix, selectLocalPayload and both LAN-peer selection paths always
// hardcoded Arch: "amd64", mislabeling an arm64 host's own custom ISO or
// LAN payload in the TUI's architecture display.
func TestArchForGOARCH_MatchesHostArch(t *testing.T) {
	if got := archForGOARCH("arm64"); got != discovery.ArchARM64 {
		t.Errorf("arm64: expected ArchARM64, got %q", got)
	}
	if got := archForGOARCH("amd64"); got != discovery.ArchAMD64 {
		t.Errorf("amd64: expected ArchAMD64, got %q", got)
	}
}
