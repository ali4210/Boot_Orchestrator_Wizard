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
	Username string `json:"username"`

	// Password is the plaintext, held only transiently (e.g. while the TUI's
	// credentials screen computes PasswordHash) and never retained in a
	// ProvisionRequest that travels through the rest of the engine.
	// json:"-" so it can never leak through json.Marshal, however this
	// struct ends up embedded or logged in the future.
	Password string `json:"-"`

	// PasswordHash is a precomputed SHA-512 crypt ("$6$...") hash, normally
	// set once by the TUI immediately after the user submits the
	// credentials screen (via HashPassword), so the plaintext Password can
	// be discarded right away. Still excluded from JSON: it is a valid
	// credential for `chpasswd -e` and must not end up on disk or in logs.
	PasswordHash string `json:"-"`

	AllowRootLogin bool `json:"allow_root_login"` // if false (the default), root stays locked
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

// reservedUsernames blocks well-known system/service and builtin accounts
// that must never be the account this tool creates or repurposes: Debian's
// default system accounts (root, daemon, bin, sys, ...) and the Windows
// builtin accounts (administrator, guest) in case this list is ever reused
// for a Windows-side username check. Matched case-insensitively since
// Windows account names are case-insensitive even though validUsernameRe
// only accepts lowercase.
var reservedUsernames = map[string]bool{
	"root": true, "daemon": true, "bin": true, "sys": true, "sync": true,
	"games": true, "man": true, "lp": true, "mail": true, "news": true,
	"uucp": true, "proxy": true, "www-data": true, "backup": true,
	"list": true, "irc": true, "gnats": true, "nobody": true, "sshd": true,
	"systemd-network": true, "systemd-resolve": true, "systemd-timesync": true,
	"messagebus": true, "administrator": true, "guest": true,
}

// ValidUsername reports whether username is safe to embed directly into
// shell scripts and useradd/chpasswd invocations (no shell metacharacters,
// matches standard Linux username rules) and is not a reserved
// system/builtin account name (audit 1A.4).
func ValidUsername(username string) bool {
	if !validUsernameRe.MatchString(username) {
		return false
	}
	return !reservedUsernames[strings.ToLower(username)]
}

// MinPasswordLength is the minimum accepted password length (audit 1A.4).
// 8 matches the most common distro/PAM pam_unix minlen default.
const MinPasswordLength = 8

// ValidPassword reports whether password meets the minimum length
// requirement. It intentionally does not check complexity (character
// classes, dictionary words, etc.) — length alone is the only requirement
// specified for this tool's account provisioning.
func ValidPassword(password string) bool {
	return len(password) >= MinPasswordLength
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
