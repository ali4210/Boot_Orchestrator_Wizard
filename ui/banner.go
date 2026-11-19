package ui

import (
	_ "embed"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-isatty"
)

// banner.txt is a 65-column x 32-row braille-art rendering (verified by
// hand before this feature was built: UTF-8, no BOM, LF line endings only,
// no tabs, no trailing whitespace on any line). It is rendered only
// inside Bubble Tea's own View() — see renderBanner below — never printed
// directly to stdout, so it can never leak into the normal terminal
// buffer or outlive the alt screen.
//
//go:embed banner.txt
var bannerArt string

// bannerLines/bannerWidth/bannerHeight are computed once at package init
// from the embedded art, not hardcoded, so they can never drift from the
// actual file.
var bannerLines = strings.Split(strings.TrimRight(bannerArt, "\n"), "\n")

var bannerHeight = len(bannerLines)

var bannerWidth = func() int {
	w := 0
	for _, l := range bannerLines {
		if lw := lipgloss.Width(l); lw > w {
			w = lw
		}
	}
	return w
}()

// compactBannerTitle is the ASCII (no braille) one-line fallback shown
// either when the terminal is too small for the full art, or when the
// active locale/console can't render braille reliably.
const compactBannerTitle = "=== BOOT ORCHESTRATOR WIZARD ==="

var styleBanner = lipgloss.NewStyle().Foreground(ColorCyan)

// windowsUTF8CodePage is CP_UTF8, the well-known Windows code page
// number for UTF-8 — golang.org/x/sys/windows does not export it as a
// constant.
const windowsUTF8CodePage = 65001

// isUTF8CodePage is split out from banner_locale_windows.go's
// localeSupportsUTF8 (which calls the real, Windows-only
// windows.GetConsoleOutputCP syscall) so the actual comparison logic can
// be unit-tested from any platform, including this project's Linux
// development sandbox, which cannot execute Windows syscalls at all.
func isUTF8CodePage(cp uint32) bool {
	return cp == windowsUTF8CodePage
}

// bannerAppliesToScreen reports whether s is one of the two screens this
// feature shows a banner on: the Beginner/Advanced picker (the very first
// screen) and the main Enterprise Hub menu. Every other screen is
// unaffected.
func bannerAppliesToScreen(s Screen) bool {
	return s == ScreenModeSelect || s == ScreenHub
}

// stdoutIsTTYFunc is a package variable (not a direct os.Stdout call) so
// tests can override it without needing a real terminal attached to the
// test process — the same indirection pattern this codebase already uses
// for exec.Command (engine.execCommand) and filesystem calls.
var stdoutIsTTYFunc = func() bool {
	fd := os.Stdout.Fd()
	return isatty.IsTerminal(fd) || isatty.IsCygwinTerminal(fd)
}

// renderBanner decides what, if anything, to show above the
// already-rendered header/body/footer for this frame: the full braille
// art, a compact one-line ASCII fallback, or nothing. It never wraps or
// truncates the art — anything that would not fully fit at the current
// width/height is simply omitted rather than clipped, so a resize can
// never push menu items off-screen or split a braille glyph across a
// line wrap.
func renderBanner(screen Screen, width, height int, header, body, footer string) string {
	if !bannerAppliesToScreen(screen) {
		return ""
	}
	// No real tea.WindowSizeMsg has arrived yet — showing nothing is
	// safer than guessing a size that might not actually fit.
	if width <= 0 || height <= 0 {
		return ""
	}
	if !stdoutIsTTYFunc() {
		return ""
	}

	nonBannerHeight := lipgloss.Height(header) + lipgloss.Height(body) + lipgloss.Height(footer)

	if localeSupportsUTF8() && width >= bannerWidth && height >= bannerHeight+nonBannerHeight {
		return styleBanner.Render(strings.Join(bannerLines, "\n"))
	}

	compact := StyleSubTitle.Render(compactBannerTitle)
	if width >= lipgloss.Width(compact) && height >= 1+nonBannerHeight {
		return compact
	}
	return ""
}
