//go:build windows

package engine

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestInjectAutounattend_RestrictsUnattendXMLToAdministrators is the
// Windows-specific verification for the real-ACL fix found while
// investigating TestWriteScriptSecurely_*'s "every caller on Windows"
// question: writeScriptSecurely itself has no Windows caller, but
// injectAutounattend (reachable via ApplyWim's native runtime.GOOS ==
// "windows" DISM path, which is genuinely functional there — unlike the
// chroot/lsblk/ext4 code elsewhere in this engine) writes the Windows
// setup answer file with the plaintext account password in it. That file
// must not be left world/group-readable on the host for the window
// between writing it and the guest's own first boot deleting it (item
// 27's FirstLogonCommand).
//
// Mirrors safety/journal_acl_windows_test.go's approach: os.FileInfo.Mode()
// can't see a real ACL on Windows at all, so this parses real icacls.exe
// output instead. Not run or verified against a real Windows machine from
// this (Linux) development sandbox — this project's own CI
// (windows-latest) is the first real execution.
func TestInjectAutounattend_RestrictsUnattendXMLToAdministrators(t *testing.T) {
	if _, err := exec.LookPath("icacls.exe"); err != nil {
		t.Skip("icacls.exe not found on PATH, skipping")
	}

	srcDir := t.TempDir()
	srcPath := filepath.Join(srcDir, "autounattend.xml")
	template := `<?xml version="1.0" encoding="utf-8"?>
<unattend xmlns="urn:schemas-microsoft-com:unattend">
  <settings pass="oobeSystem">
    <component name="Microsoft-Windows-Shell-Setup">
      <UserAccounts>
        <LocalAccounts>
          <LocalAccount wcm:action="add">
            <Name>__ORCH_USERNAME__</Name>
            <Password><Value>__ORCH_PASSWORD__</Value><PlainText>true</PlainText></Password>
          </LocalAccount>
        </LocalAccounts>
      </UserAccounts>
    </component>
  </settings>
</unattend>`
	if err := os.WriteFile(srcPath, []byte(template), 0644); err != nil {
		t.Fatalf("writing template: %v", err)
	}

	targetRoot := t.TempDir()
	creds := Credentials{Username: "janedoe", Password: "a-fine-password"}

	if err := injectAutounattend(srcPath, targetRoot, creds); err != nil {
		t.Fatalf("injectAutounattend: %v", err)
	}

	destPath := filepath.Join(targetRoot, "Windows", "Panther", "unattend.xml")
	out, err := exec.Command("icacls.exe", destPath).CombinedOutput()
	if err != nil {
		t.Fatalf("icacls %s (verifying): %v\n%s", destPath, err, out)
	}
	report := string(out)

	for _, want := range []string{"Administrators", "SYSTEM"} {
		if !strings.Contains(report, want) {
			t.Errorf("expected icacls output to grant %q, got:\n%s", want, report)
		}
	}
	for _, forbidden := range []string{"Everyone", "BUILTIN\\Users", "Authenticated Users"} {
		if strings.Contains(report, forbidden) {
			t.Errorf("expected the inherited, broadly-scoped %q grant to have been removed, but it's still present:\n%s", forbidden, report)
		}
	}
}
