//go:build windows

package safety

import (
	"fmt"
	"os/exec"
)

// restrictJournalDirToAdministrators locks dir down to Administrators and
// SYSTEM only, via icacls.exe — the real Windows equivalent of the 0700
// mode DefaultJournalPath already relies on for the same guarantee on
// Unix (see that function's own doc comment for why os.MkdirAll's mode
// parameter alone does nothing meaningful here).
//
// /inheritance:r removes every inherited ACE first — %ProgramData%'s
// default permissions are far broader than Administrators/SYSTEM (every
// local user typically has at least read access), so without this,
// /grant:r would only ADD Administrators/SYSTEM on top of permissions
// that were already too open, not replace them. /grant:r then sets an
// explicit (non-inherited) grant list, replacing whatever was there.
// "(OI)(CI)F" = Object Inherit + Container Inherit + Full control, so
// the restriction also applies to anything created inside dir later
// (journal.json itself, any temp files), not just dir's own ACL entry.
//
// Not verified against a real Windows machine from this (Linux) sandbox
// — no Windows host is available here to actually run icacls and inspect
// the result. Written to documented icacls semantics and covered by
// safety/journal_acl_windows_test.go, which this project's own CI
// (windows-latest) is the first real execution of either.
func restrictJournalDirToAdministrators(dir string) error {
	cmd := exec.Command("icacls.exe", dir,
		"/inheritance:r",
		"/grant:r", "Administrators:(OI)(CI)F",
		"/grant:r", "SYSTEM:(OI)(CI)F",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("icacls restricting %s to Administrators/SYSTEM: %w (%s)", dir, err, out)
	}
	return nil
}
