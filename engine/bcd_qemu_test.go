package engine

import "testing"

// TestValidateBCDHostArch_RefusesNonAmd64 is the regression test for audit
// finding 2.6 (x86_64 hardcoded despite ARM64 support) as it applies to
// bcd_qemu.go: before this guard existed, writeWindowsBCD would run on an
// arm64 host and silently attempt qemu-system-x86_64/bootx64.efi logic that
// cannot work there, failing confusingly deep inside a QEMU invocation (or
// worse) instead of refusing clearly up front. Windows-on-ARM is out of
// scope (no catalog entry needs it — see docs/ARCH_SUPPORT.md), so the fix
// is an explicit, loud refusal rather than unverifiable arm64 support.
func TestValidateBCDHostArch_RefusesNonAmd64(t *testing.T) {
	if err := validateBCDHostArch("arm64"); err == nil {
		t.Fatal("expected validateBCDHostArch(\"arm64\") to return an error, got nil")
	}

	if err := validateBCDHostArch("amd64"); err != nil {
		t.Fatalf("expected validateBCDHostArch(\"amd64\") to succeed, got: %v", err)
	}
}
