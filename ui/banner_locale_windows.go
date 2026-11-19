//go:build windows

package ui

import "golang.org/x/sys/windows"

// localeSupportsUTF8 on Windows checks the console's own active OUTPUT
// code page rather than LC_ALL/LC_CTYPE/LANG — those POSIX locale
// variables are not what a native Windows console (cmd.exe, a legacy
// ConHost window, or Windows Terminal) uses to decide how to render
// bytes, and are typically unset there even when the console itself is
// perfectly UTF-8 capable (Windows Terminal defaults to code page
// 65001). A legacy console left on the system's original OEM code page
// (437, 850, 1252, ...) cannot render braille glyphs correctly, so this
// falls back to the compact ASCII title there instead.
func localeSupportsUTF8() bool {
	cp, err := windows.GetConsoleOutputCP()
	if err != nil {
		return false
	}
	return isUTF8CodePage(cp)
}
