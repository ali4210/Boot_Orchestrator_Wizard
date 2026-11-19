package ui

import (
	_ "embed"
	"fmt"
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
//
// The \r\n -> \n normalization below is defense in depth, not the actual
// fix: a Windows checkout with core.autocrlf (Git for Windows' default)
// silently rewrites banner.txt's LF endings to CRLF, which this project
// hit for real on windows-latest CI — the real fix is .gitattributes
// (`ui/banner.txt text eol=lf`), which stops that rewrite from happening
// at checkout in the first place. This replace is kept anyway so the
// embedded content is correct even if .gitattributes is ever missing,
// stripped, or not honored by some future git version/host.
var bannerLines = strings.Split(strings.TrimRight(strings.ReplaceAll(bannerArt, "\r\n", "\n"), "\n"), "\n")

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

// wordmarkLines is the "BOOT WIZARD" block-letter wordmark. The source
// text (pasted in from another tool/project) used '.' instead of ' ' for
// padding — a common trick so copy/paste doesn't strip trailing
// whitespace — converted to plain spaces below. All 7 rows were already
// exactly 98 columns wide before that conversion (confirmed, not
// assumed: every row's length was checked equal before any character was
// touched), so no row needed padding to match another. Deliberately NOT
// porting anything else from that other project — its own
// Cyan/Bold/Reset color constants or its displaySessionStatus function —
// only this raw art string belongs here; this project's own styling
// (styleBanner, below) is applied instead.
var wordmarkLines = []string{
	"  ########   #######   #######  ########    ##      ## #### ########    ###    ########  ######## ",
	"  ##     ## ##     ## ##     ##    ##       ##  ##  ##  ##       ##    ## ##   ##     ## ##     ##",
	"  ##     ## ##     ## ##     ##    ##       ##  ##  ##  ##      ##    ##   ##  ##     ## ##     ##",
	"  ########  ##     ## ##     ##    ##       ##  ##  ##  ##     ##    ##     ## ########  ##     ##",
	"  ##     ## ##     ## ##     ##    ##       ##  ##  ##  ##    ##     ######### ##   ##   ##     ##",
	"  ##     ## ##     ## ##     ##    ##       ##  ##  ##  ##   ##      ##     ## ##    ##  ##     ##",
	"  ########   #######   #######     ##        ###  ###  #### ######## ##     ## ##     ## ######## ",
}

var wordmarkHeight = len(wordmarkLines)

var wordmarkWidth = func() int {
	w := 0
	for _, l := range wordmarkLines {
		if lw := lipgloss.Width(l); lw > w {
			w = lw
		}
	}
	return w
}()

const wordmarkGithubLine = "GitHub  : https://github.com/ali4210"

// wordmarkVersion defaults to "dev" — this project has no other version
// source (no git tags, VERSION file, or CHANGELOG as of this writing) —
// and is overridden once at startup via SetVersion, which
// cmd/orchestrator/main.go calls with its own build-time Version
// (cmd/orchestrator/version.go, itself settable via
// `-ldflags "-X main.Version=..."`, see autorun.sh). A plain cross-package
// var assignment isn't possible here the other way around:
// cmd/orchestrator already imports ui, so ui importing cmd/orchestrator
// back would be a cycle (and "main" packages can't be imported at all
// regardless). A setter function also avoids changing NewModel's
// signature — and every one of its existing test call sites — just for
// one display string.
var wordmarkVersion = "dev"

// SetVersion lets cmd/orchestrator pass through its build-time Version
// for display in the wordmark subtitle below. A no-op for an empty
// string, so a caller that never calls this keeps the "dev" default.
func SetVersion(v string) {
	if v != "" {
		wordmarkVersion = v
	}
}

func wordmarkSubtitle() string {
	return fmt.Sprintf("BOOTING MULTI-ENGINE PLATFORM [%s]", wordmarkVersion)
}

// centerTo pads s with equal (as equal as an odd remainder allows)
// leading/trailing spaces to width columns. Returns s unchanged if it's
// already at least that wide, rather than truncating — this function
// never shortens anything.
func centerTo(width int, s string) string {
	pad := width - lipgloss.Width(s)
	if pad <= 0 {
		return s
	}
	left := pad / 2
	right := pad - left
	return strings.Repeat(" ", left) + s + strings.Repeat(" ", right)
}

// wordmarkBlockLines returns the wordmark art plus its centered subtitle
// and the (also centered) GitHub line, as a single ready-to-join slice —
// used both standalone (the "wordmark only" tier) and stacked under the
// braille art (the combined tier). Deliberately no blank-line padding
// between the three pieces: every row here counts against the tightest
// tier's height budget (see renderBanner's tier 2, which this project
// specifically wants to still fit at a modest 130x30 terminal where
// every row matters).
func wordmarkBlockLines() []string {
	lines := make([]string, 0, wordmarkHeight+2)
	lines = append(lines, wordmarkLines...)
	lines = append(lines, centerTo(wordmarkWidth, wordmarkSubtitle()))
	lines = append(lines, centerTo(wordmarkWidth, wordmarkGithubLine))
	return lines
}

// combinedTierLines is tier 1: the braille art, a single blank separator
// line, then the wordmark block.
func combinedTierLines() []string {
	lines := make([]string, 0, bannerHeight+1+wordmarkHeight+2)
	lines = append(lines, bannerLines...)
	lines = append(lines, "")
	lines = append(lines, wordmarkBlockLines()...)
	return lines
}

// compactBannerTitle is the ASCII one-line fallback shown when nothing
// bigger fits.
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

// bannerAppliesToScreen reports whether s is one of the screens this
// feature shows a banner on: the Beginner/Advanced picker and the main
// Enterprise Hub menu (Beginner mode's own top-level screens), plus
// Advanced mode's equivalent top-level screens — the "select node role"
// screen (ScreenAdvancedRoleSelect) and each role's own top-level menu
// once chosen (ScreenClientPanel, ScreenServerPanel). Those three are
// included because they play the exact same structural role in Advanced
// mode that ScreenModeSelect/ScreenHub play in Beginner mode: a
// numbered-options "home" screen you land on and navigate from, not a
// deep, single-purpose workflow step. Screens reached FROM those three
// (ScreenLANPeerConnect, ScreenCustomBootModeSelect,
// ScreenTargetDiskSelect, ScreenStorageAllocationSelect,
// ScreenSeederDashboard, ScreenSCPPush, ScreenFilePicker, the USB
// screens, ...) are excluded for the same reason Beginner mode's own
// deeper screens (ScreenCredentials, ScreenConfirm, ScreenProgress, ...)
// already were: they're mid-task, not a "home" screen.
func bannerAppliesToScreen(s Screen) bool {
	switch s {
	case ScreenModeSelect, ScreenHub, ScreenAdvancedRoleSelect, ScreenClientPanel, ScreenServerPanel:
		return true
	default:
		return false
	}
}

// stdoutIsTTYFunc is a package variable (not a direct os.Stdout call) so
// tests can override it without needing a real terminal attached to the
// test process — the same indirection pattern this codebase already uses
// for exec.Command (engine.execCommand) and filesystem calls.
var stdoutIsTTYFunc = func() bool {
	fd := os.Stdout.Fd()
	return isatty.IsTerminal(fd) || isatty.IsCygwinTerminal(fd)
}

// localeSupportsUTF8Func indirects to the real, per-platform
// localeSupportsUTF8 (banner_locale_unix.go/banner_locale_windows.go) so
// cross-platform tests can force either outcome deterministically.
// Needed because the two real implementations check entirely different
// things (POSIX env vars vs. the Windows console's own code page) — a
// test cannot portably force "non-UTF-8" by setting LC_ALL the way
// banner_locale_unix_test.go does, since that has zero effect on
// Windows's real implementation, which ignores those variables by
// design (see banner_locale_windows.go's own comment for why).
var localeSupportsUTF8Func = localeSupportsUTF8

// renderBanner decides what, if anything, to show above the
// already-rendered header/body/footer for this frame, as a strict ladder
// from biggest to nothing — never wraps or truncates anything: a tier
// that doesn't fully fit at the current width/height is simply skipped
// in favor of the next smaller one, so a resize can never push menu
// items off-screen or split a glyph across a line wrap.
//
// Four tiers:
//  1. Braille art + wordmark, stacked. Needs real UTF-8 support (the
//     braille glyphs) — every tier below this point is pure ASCII
//     (confirmed: wordmarkLines/compactBannerTitle contain only
//     '#'/' '/'='/letters) and so never needs that check — this is what
//     makes tier 2 a correct UTF-8 fallback rather than just a smaller
//     copy of tier 1.
//  2. The wordmark alone (no braille art).
//  3. A one-line compact ASCII title.
//  4. Nothing.
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

	combinedHeight := bannerHeight + 1 + wordmarkHeight + 2 // +1 separator, +2 subtitle/github
	if localeSupportsUTF8Func() && width >= wordmarkWidth && height >= combinedHeight+nonBannerHeight {
		return styleBanner.Render(strings.Join(combinedTierLines(), "\n"))
	}

	wordmarkOnlyHeight := wordmarkHeight + 2
	if width >= wordmarkWidth && height >= wordmarkOnlyHeight+nonBannerHeight {
		return styleBanner.Render(strings.Join(wordmarkBlockLines(), "\n"))
	}

	compact := StyleSubTitle.Render(compactBannerTitle)
	if width >= lipgloss.Width(compact) && height >= 1+nonBannerHeight {
		return compact
	}
	return ""
}
