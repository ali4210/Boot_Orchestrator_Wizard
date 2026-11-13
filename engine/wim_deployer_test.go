package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestInjectAutounattend_SubstitutesPlaceholders is the regression test for
// audit 2.3's Windows path: the answer file template uses
// __ORCH_USERNAME__/__ORCH_PASSWORD__ placeholders, never a hardcoded
// account, and injectAutounattend must replace them with the real,
// caller-supplied credentials.
func TestInjectAutounattend_SubstitutesPlaceholders(t *testing.T) {
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
	creds := Credentials{Username: "janedoe", Password: `p@ss<&>"'word`}

	if err := injectAutounattend(srcPath, targetRoot, creds); err != nil {
		t.Fatalf("injectAutounattend: %v", err)
	}

	out, err := os.ReadFile(filepath.Join(targetRoot, "Windows", "Panther", "unattend.xml"))
	if err != nil {
		t.Fatalf("reading injected answer file: %v", err)
	}
	got := string(out)

	if strings.Contains(got, "__ORCH_USERNAME__") || strings.Contains(got, "__ORCH_PASSWORD__") {
		t.Fatalf("placeholders were not substituted:\n%s", got)
	}
	if strings.Contains(got, "saleem") {
		t.Fatalf("injected answer file contains the hardcoded account:\n%s", got)
	}
	if !strings.Contains(got, "<Name>janedoe</Name>") {
		t.Fatalf("expected the configured username in the output:\n%s", got)
	}
	// The password contains XML-special characters; confirm they were
	// escaped rather than producing malformed or injected XML.
	if strings.Contains(got, "<Value>p@ss<&>") {
		t.Fatalf("password was embedded unescaped, corrupting the XML:\n%s", got)
	}
}

// TestAutounattendManifest_NoHardcodedSaleem guards the checked-in template
// itself: it must use the placeholder tokens, never a literal default
// account.
func TestAutounattendManifest_NoHardcodedSaleem(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "discovery", "manifests", "autounattend.xml"))
	if err != nil {
		t.Skipf("manifest not found relative to this package, skipping: %v", err)
	}
	content := string(data)
	if strings.Contains(content, "saleem") {
		t.Fatalf("discovery/manifests/autounattend.xml still contains a hardcoded account")
	}
	if !strings.Contains(content, "__ORCH_USERNAME__") || !strings.Contains(content, "__ORCH_PASSWORD__") {
		t.Fatalf("discovery/manifests/autounattend.xml is missing the expected substitution placeholders")
	}
}
