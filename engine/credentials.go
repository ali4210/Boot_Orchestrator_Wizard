package engine

import (
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

// Credentials holds the account the provisioning engine creates on a newly
// installed OS, as entered by the user in the TUI — there is no hardcoded
// default account or password (audit 2.3). Password is kept only long
// enough for the caller to derive a hash via HashPassword and, for Windows
// targets, to populate the unattend answer file; Windows's unattend
// mechanism has no hashed-password option for local account creation, so
// embedding plaintext there is inherent to that format, not a choice made
// here. Password must never be logged.
type Credentials struct {
	Username string

	// Password is the plaintext, held only transiently (e.g. while the TUI's
	// credentials screen computes PasswordHash) and never retained in a
	// ProvisionRequest that travels through the rest of the engine.
	Password string

	// PasswordHash is a precomputed SHA-512 crypt ("$6$...") hash, normally
	// set once by the TUI immediately after the user submits the
	// credentials screen (via HashPassword), so the plaintext Password can
	// be discarded right away.
	PasswordHash string

	AllowRootLogin bool // if false (the default), root stays locked
}

// ResolvedPasswordHash returns PasswordHash if it is already set, otherwise
// derives it on demand from Password via HashPassword.
func (c Credentials) ResolvedPasswordHash() (string, error) {
	if c.PasswordHash != "" {
		return c.PasswordHash, nil
	}
	return c.HashPassword()
}

// validUsernameRe mirrors useradd's default NAME_REGEX: lowercase letters,
// digits, underscore and hyphen, starting with a letter or underscore.
var validUsernameRe = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

// ValidUsername reports whether username is safe to embed directly into
// shell scripts and useradd/chpasswd invocations (no shell metacharacters,
// matches standard Linux username rules).
func ValidUsername(username string) bool {
	return validUsernameRe.MatchString(username)
}

// HashPassword returns a glibc crypt(3) SHA-512 ("$6$...") hash of
// c.Password, suitable for `chpasswd -e`. The password is piped via Stdin,
// never passed as a CLI argument, so it never appears in a process listing.
func (c Credentials) HashPassword() (string, error) {
	cmd := exec.Command("openssl", "passwd", "-6", "-stdin")
	cmd.Stdin = strings.NewReader(c.Password)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("hashing password via openssl: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// xmlEscape escapes the characters that are special inside XML element text
// content, so an arbitrary username/password can be embedded in a Windows
// unattend answer file without producing malformed or injected XML.
func xmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, `"`, "&quot;")
	s = strings.ReplaceAll(s, "'", "&apos;")
	return s
}

// shellSingleQuote wraps s in single quotes for safe embedding in a shell
// command, escaping any single quotes already in s. Used for values (like a
// password hash) that must reach the shell as one opaque literal token with
// no expansion.
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}
