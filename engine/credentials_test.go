package engine

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestHashPassword_ProducesSHA512Crypt(t *testing.T) {
	hash, err := (Credentials{Password: "correct horse battery staple"}).HashPassword()
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if !strings.HasPrefix(hash, "$6$") {
		t.Fatalf("expected a SHA-512 crypt hash (starts with $6$), got %q", hash)
	}
}

func TestResolvedPasswordHash_PrefersPrecomputedHash(t *testing.T) {
	creds := Credentials{PasswordHash: "$6$already-hashed$stuff"}
	got, err := creds.ResolvedPasswordHash()
	if err != nil {
		t.Fatalf("ResolvedPasswordHash: %v", err)
	}
	if got != creds.PasswordHash {
		t.Fatalf("expected the precomputed hash to be returned as-is, got %q", got)
	}
}

func TestValidUsername(t *testing.T) {
	cases := map[string]bool{
		"alex":           true,
		"_svc":           true,
		"a-b_9":          true,
		"":               false,
		"Alex":           false, // uppercase not allowed
		"9alex":          false, // can't start with a digit
		"alex; rm -rf /": false,
		"alex$(whoami)":  false,
		"a b":            false,
		// audit 1A.4: reserved system/builtin accounts, rejected even though
		// they otherwise match the character-class regex.
		"root":          false,
		"daemon":        false,
		"bin":           false,
		"sys":           false,
		"nobody":        false,
		"administrator": false, // Windows builtin, matched case-insensitively
		"guest":         false,
	}
	for input, want := range cases {
		if got := ValidUsername(input); got != want {
			t.Errorf("ValidUsername(%q) = %v, want %v", input, got, want)
		}
	}
}

// TestValidPassword is the regression test for audit 1A.4's minimum
// password length requirement.
func TestValidPassword(t *testing.T) {
	cases := map[string]bool{
		"":          false, // 0 chars
		"1234567":   false, // 7 chars: below MinPasswordLength
		"12345678":  true,  // 8 chars: exactly MinPasswordLength
		"123456789": true,  // 9 chars: above MinPasswordLength
	}
	for input, want := range cases {
		if got := ValidPassword(input); got != want {
			t.Errorf("ValidPassword(%q) (len=%d) = %v, want %v", input, len(input), got, want)
		}
	}
}

// TestPasswordHashSurvivesHeredocRoundTrip exercises the exact pattern used
// in orchestrator.go's autonomous-installer worker script: the hash is
// written to a file via a quoted heredoc (so its own "$" characters are not
// expanded during the write), then read back inside a single-quoted outer
// `bash -c '...'` block and embedded via a double-quoted shell variable
// reference. This proves a real SHA-512 crypt hash (which always contains
// multiple "$" characters) survives that round trip byte-for-byte, rather
// than trusting the quoting analysis alone. No chroot, mount, or
// destructive operation is involved — just plain files in a temp dir.
func TestPasswordHashSurvivesHeredocRoundTrip(t *testing.T) {
	hash, err := (Credentials{Password: "correct horse battery staple"}).HashPassword()
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if strings.Count(hash, "$") < 3 {
		t.Fatalf("expected a crypt hash with multiple '$' characters, got %q", hash)
	}

	dir := t.TempDir()
	hashFile := filepath.Join(dir, ".orch_pwhash")

	// Mirrors: cat > .../.orch_pwhash <<'ORCH_HASH_EOF' / %hash% / ORCH_HASH_EOF
	writeScript := "cat > " + shellSingleQuote(hashFile) + ` <<'ORCH_HASH_EOF'
` + hash + `
ORCH_HASH_EOF
`
	if out, err := exec.Command("bash", "-c", writeScript).CombinedOutput(); err != nil {
		t.Fatalf("heredoc write failed: %v\n%s", err, out)
	}

	written, err := os.ReadFile(hashFile)
	if err != nil {
		t.Fatalf("reading hash file: %v", err)
	}
	if strings.TrimRight(string(written), "\n") != hash {
		t.Fatalf("heredoc write corrupted the hash: got %q, want %q", strings.TrimRight(string(written), "\n"), hash)
	}

	// Mirrors the single-quoted outer `chroot ... bash -c '...'` block:
	// read the file into $HASH, then embed it via a double-quoted
	// reference, exactly as the production script does.
	outerScript := "bash -c '" +
		"HASH=$(cat " + shellSingleQuote(hashFile) + "); " +
		`echo "myuser:$HASH"` +
		"'"
	out, err := exec.Command("bash", "-c", outerScript).CombinedOutput()
	if err != nil {
		t.Fatalf("outer single-quoted round trip failed: %v\n%s", err, out)
	}

	want := "myuser:" + hash + "\n"
	if string(out) != want {
		t.Fatalf("hash corrupted across the single-quote round trip:\n got:  %q\n want: %q", string(out), want)
	}
}
