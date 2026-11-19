//go:build !windows

package ui

import (
	"os"
	"strings"
)

// localeSupportsUTF8 checks the standard POSIX locale precedence
// (LC_ALL overrides LC_CTYPE overrides LANG — the same order glibc/musl
// use to resolve the active character encoding) for a UTF-8 charmap. An
// empty/unset chain is treated as the POSIX "C"/"POSIX" locale, which is
// not UTF-8 — the conservative choice, since showing braille glyphs in a
// locale that cannot render them would be worse than the plain fallback.
func localeSupportsUTF8() bool {
	for _, name := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		if v := os.Getenv(name); v != "" {
			return localeStringIsUTF8(v)
		}
	}
	return false
}

// localeStringIsUTF8 is split out from localeSupportsUTF8 so the parsing
// logic (matching glibc's own locale-string shape, e.g.
// "en_US.UTF-8@modifier" or "C.UTF-8") is directly unit-testable without
// needing to mutate process environment variables.
func localeStringIsUTF8(locale string) bool {
	upper := strings.ToUpper(locale)
	return strings.Contains(upper, "UTF-8") || strings.Contains(upper, "UTF8")
}
