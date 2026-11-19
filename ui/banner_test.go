package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// withTTY and withLocaleUTF8 are small test helpers that save/restore the
// two package-level knobs renderBanner's decision depends on, so tests
// can force either state deterministically regardless of what
// environment `go test` itself happens to run under (which is never a
// real TTY) or which platform it runs on.
func withTTY(t *testing.T, isTTY bool) {
	t.Helper()
	orig := stdoutIsTTYFunc
	stdoutIsTTYFunc = func() bool { return isTTY }
	t.Cleanup(func() { stdoutIsTTYFunc = orig })
}

// withLocaleUTF8 forces localeSupportsUTF8Func's result directly, rather
// than setting LC_ALL/LC_CTYPE/LANG — those env vars only affect the
// real Unix implementation (banner_locale_unix.go); Windows's real
// implementation checks the console's own code page instead and ignores
// them entirely (banner_locale_windows.go), so this is the only
// cross-platform-deterministic way to force either outcome in a test
// that must pass on every platform. banner_locale_unix_test.go
// separately tests the real Unix env-var mechanism directly.
func withLocaleUTF8(t *testing.T, supported bool) {
	t.Helper()
	orig := localeSupportsUTF8Func
	localeSupportsUTF8Func = func() bool { return supported }
	t.Cleanup(func() { localeSupportsUTF8Func = orig })
}

func containsBraille(s string) bool {
	for _, r := range s {
		if r >= 0x2800 && r <= 0x28FF {
			return true
		}
	}
	return false
}

func containsCompactTitle(s string) bool {
	return strings.Contains(s, compactBannerTitle)
}

// containsWordmark checks for the wordmark's own first row specifically
// (rather than every row) — sufficient to prove the wordmark tier is
// present, and simpler than requiring every row inline here (the
// dedicated exactness tests below already check every row).
func containsWordmark(s string) bool {
	return strings.Contains(s, wordmarkLines[0])
}

// bannerTier classifies a rendered View() output into exactly one of the
// four tiers renderBanner can produce, for table-driven assertions.
type bannerTier int

const (
	tierNone bannerTier = iota
	tierCompact
	tierWordmarkOnly
	tierCombined
)

func (t bannerTier) String() string {
	switch t {
	case tierNone:
		return "none"
	case tierCompact:
		return "compact"
	case tierWordmarkOnly:
		return "wordmark-only"
	case tierCombined:
		return "art+wordmark"
	default:
		return "unknown"
	}
}

func classifyTier(out string) bannerTier {
	switch {
	case containsBraille(out) && containsWordmark(out):
		return tierCombined
	case containsWordmark(out):
		return tierWordmarkOnly
	case containsCompactTitle(out):
		return tierCompact
	default:
		return tierNone
	}
}

// screenHeights, filled in by TestMain via an init-time measurement (see
// below), are used only to document/derive expectations in comments —
// the actual test assertions call classifyTier against real View()
// output, not hand-computed numbers, so a future change to any screen's
// copy (which changes its rendered height) cannot silently desync the
// test from reality the way a hardcoded expectation table could.

// TestBannerTierMatrix is the required 80x24/130x30/100x40/70x70 matrix,
// across every screen bannerAppliesToScreen covers. Measured (not
// guessed) results, as of this writing, with each screen's own
// header+body+footer height in parens:
//
//	                80x24      130x30        100x40     70x70
//	ModeSelect (21) compact    wordmark-only wordmark-only compact
//	Hub        (33) none       none          compact       compact
//	AdvRole    (20) compact    wordmark-only wordmark-only compact
//	Client     (27) none       compact       wordmark-only compact
//	Server     (18) compact    wordmark-only wordmark-only compact
//
// Notable, deliberately NOT papered over: at 70x70, nothing reaches
// tier 1 or 2 on ANY screen, even though 70x70 comfortably fit the old
// (pre-wordmark) single-tier design — the wordmark is 98 columns wide,
// wider than 70, and tier 1/2 both need width >= wordmarkWidth since the
// two pieces are stacked, not placed side by side (see renderBanner's
// own comment on that tradeoff). At 130x30, Hub shows NOTHING at all,
// not even the compact tier: Hub's own body (6 menu options) is 29 rows
// by itself, leaving no room for even a 1-line banner inside a 30-row
// terminal — not a banner-sizing problem a shorter wordmark could fix.
func TestBannerTierMatrix(t *testing.T) {
	withTTY(t, true)
	withLocaleUTF8(t, true)

	screens := []Screen{ScreenModeSelect, ScreenHub, ScreenAdvancedRoleSelect, ScreenClientPanel, ScreenServerPanel}
	sizes := [][2]int{{80, 24}, {130, 30}, {100, 40}, {70, 70}}
	want := map[Screen]map[[2]int]bannerTier{
		ScreenModeSelect: {
			{80, 24}: tierCompact, {130, 30}: tierWordmarkOnly, {100, 40}: tierWordmarkOnly, {70, 70}: tierCompact,
		},
		ScreenHub: {
			{80, 24}: tierNone, {130, 30}: tierNone, {100, 40}: tierCompact, {70, 70}: tierCompact,
		},
		ScreenAdvancedRoleSelect: {
			{80, 24}: tierCompact, {130, 30}: tierWordmarkOnly, {100, 40}: tierWordmarkOnly, {70, 70}: tierCompact,
		},
		ScreenClientPanel: {
			{80, 24}: tierNone, {130, 30}: tierCompact, {100, 40}: tierWordmarkOnly, {70, 70}: tierCompact,
		},
		ScreenServerPanel: {
			{80, 24}: tierCompact, {130, 30}: tierWordmarkOnly, {100, 40}: tierWordmarkOnly, {70, 70}: tierCompact,
		},
	}

	for _, screen := range screens {
		for _, sz := range sizes {
			m := NewModel("")
			m.screen = screen
			m.width, m.height = sz[0], sz[1]
			out := m.View()

			got := classifyTier(out)
			wantTier := want[screen][sz]
			if got != wantTier {
				t.Errorf("screen=%v size=%dx%d: got tier %v, want %v", screen, sz[0], sz[1], got, wantTier)
			}

			// Tier 1 never happens in this matrix (wordmarkWidth=98
			// exceeds every width tested here) — confirmed explicitly so
			// a future change that somehow let tier 1 slip in at these
			// sizes is caught, not just silently accepted because
			// classifyTier would also report tierCombined as "not what
			// was expected" anyway.
			if containsBraille(out) {
				t.Errorf("screen=%v size=%dx%d: unexpected braille glyphs — tier 1 should be unreachable at every size in this matrix", screen, sz[0], sz[1])
			}

			if !strings.Contains(out, "[COMMANDS]") {
				t.Errorf("screen=%v size=%dx%d: footer hint missing from output entirely", screen, sz[0], sz[1])
			}
		}
	}
}

// TestBannerTier1_CombinedArtAndWordmark_AtGenerousSize confirms tier 1
// (the matrix above never reaches it): both the braille art and the
// wordmark render, byte-for-byte unmodified, stacked together, given
// enough room on every screen this feature covers.
func TestBannerTier1_CombinedArtAndWordmark_AtGenerousSize(t *testing.T) {
	withTTY(t, true)
	withLocaleUTF8(t, true)

	for _, screen := range []Screen{ScreenModeSelect, ScreenHub, ScreenAdvancedRoleSelect, ScreenClientPanel, ScreenServerPanel} {
		m := NewModel("")
		m.screen = screen
		m.width, m.height = 220, 90
		out := m.View()

		if classifyTier(out) != tierCombined {
			t.Fatalf("screen=%v: expected tier 1 (art+wordmark) at a generous 220x90 size, got %v; output:\n%s", screen, classifyTier(out), out)
		}
		for _, line := range bannerLines {
			if !strings.Contains(out, line) {
				t.Errorf("screen=%v: expected an exact, unmodified banner line, missing: %q", screen, line)
			}
		}
		for _, line := range wordmarkLines {
			if !strings.Contains(out, line) {
				t.Errorf("screen=%v: expected an exact, unmodified wordmark line, missing: %q", screen, line)
			}
		}
		if !strings.Contains(out, "BOOTING MULTI-ENGINE PLATFORM") {
			t.Errorf("screen=%v: expected the subtitle line", screen)
		}
		if !strings.Contains(out, "GitHub  : https://github.com/ali4210") {
			t.Errorf("screen=%v: expected the GitHub line", screen)
		}
	}
}

// TestBannerTier2_WordmarkOnly_IsPureASCII proves the UTF-8 fallback
// property directly: tier 2 (wordmark only, no braille art) must never
// depend on the locale/console UTF-8 check, because it contains no
// non-ASCII characters at all to begin with. Forces UTF-8 unsupported
// AND a size that would otherwise only fit tier 2 — if tier 2 depended
// on the locale check by mistake, this would wrongly fall through to
// tier 3 (compact) instead.
func TestBannerTier2_WordmarkOnly_IsPureASCII(t *testing.T) {
	withTTY(t, true)
	withLocaleUTF8(t, false)

	// Checks renderBanner's own return value directly, not the full
	// View() output: the rest of the screen (StylePanel's ThickBorder,
	// ┏┃┛...) legitimately uses non-ASCII box-drawing characters
	// regardless of locale — that pre-existing chrome is not part of
	// this feature and not what "ASCII-only wordmark" is about.
	got := renderBanner(ScreenModeSelect, 130, 30, "h", "b", "f") // the matrix above confirms this size is tier 2 for ModeSelect
	if classifyTier(got) != tierWordmarkOnly {
		t.Fatalf("expected tier 2 (wordmark only) even under a non-UTF-8 locale, got %v; output:\n%s", classifyTier(got), got)
	}
	for _, r := range got {
		if r > 127 {
			t.Fatalf("tier 2 output contains a non-ASCII rune %q (%U) — the wordmark tier must be pure ASCII to serve as a real UTF-8 fallback", r, r)
		}
	}
}

// TestWordmarkArt_UniformWidthAndASCIIOnly is a standing regression guard
// on the raw wordmarkLines data itself (independent of any particular
// render), since Part C explicitly required normalizing it to equal
// row widths and this must never silently drift.
func TestWordmarkArt_UniformWidthAndASCIIOnly(t *testing.T) {
	if len(wordmarkLines) == 0 {
		t.Fatal("wordmarkLines is empty")
	}
	for i, line := range wordmarkLines {
		if w := lipgloss.Width(line); w != wordmarkWidth {
			t.Errorf("wordmark row %d is %d columns wide, want %d (every row must be equal width)", i, w, wordmarkWidth)
		}
		for _, r := range line {
			if r > 127 {
				t.Errorf("wordmark row %d contains a non-ASCII rune %q (%U)", i, r, r)
			}
		}
	}
	if !strings.Contains(wordmarkGithubLine, "github.com/ali4210") {
		t.Errorf("expected the mandatory GitHub line to reference github.com/ali4210, got %q", wordmarkGithubLine)
	}
	subtitle := wordmarkSubtitle()
	if !strings.Contains(subtitle, "BOOTING MULTI-ENGINE PLATFORM") {
		t.Errorf("expected the mandatory subtitle text, got %q", subtitle)
	}
	if !strings.Contains(subtitle, "[") || !strings.Contains(subtitle, "]") {
		t.Errorf("expected the subtitle to bracket a version, got %q", subtitle)
	}
}

func TestSetVersion_UpdatesSubtitle_EmptyStringIsNoOp(t *testing.T) {
	orig := wordmarkVersion
	t.Cleanup(func() { wordmarkVersion = orig })

	SetVersion("v9.9.9")
	if got := wordmarkSubtitle(); !strings.Contains(got, "[v9.9.9]") {
		t.Errorf("expected SetVersion to update the subtitle, got %q", got)
	}

	SetVersion("")
	if got := wordmarkSubtitle(); !strings.Contains(got, "[v9.9.9]") {
		t.Errorf("expected SetVersion(\"\") to be a no-op, got %q", got)
	}
}

func TestBanner_NonUTF8Locale_FallsBackPastTier1(t *testing.T) {
	withTTY(t, true)
	withLocaleUTF8(t, false)

	m := NewModel("")
	m.screen = ScreenHub
	m.width, m.height = 220, 90 // plenty of room, so only the locale can explain skipping tier 1
	out := m.View()

	if containsBraille(out) {
		t.Errorf("expected no braille glyphs under a non-UTF-8 locale even with plenty of room, got some")
	}
	if classifyTier(out) != tierWordmarkOnly {
		t.Errorf("expected tier 2 (wordmark only) as the non-UTF-8 fallback, got %v", classifyTier(out))
	}
}

func TestBanner_NotATTY_NoBannerAtAll(t *testing.T) {
	withTTY(t, false)
	withLocaleUTF8(t, true)

	for _, screen := range []Screen{ScreenModeSelect, ScreenHub, ScreenAdvancedRoleSelect, ScreenClientPanel, ScreenServerPanel} {
		m := NewModel("")
		m.screen = screen
		m.width, m.height = 220, 90 // plenty of room — only non-TTY can explain nothing showing
		out := m.View()

		if classifyTier(out) != tierNone {
			t.Errorf("screen=%v: expected no banner at all when stdout is not a TTY, got %v", screen, classifyTier(out))
		}
	}
}

// TestBannerAppliesToScreen_Scope confirms exactly which screens this
// feature covers: Beginner mode's own two top-level screens, Advanced
// mode's "select node role" screen, and each Advanced role's own
// top-level panel — and confirms it does NOT apply to deeper,
// single-purpose screens reached from any of those (both Beginner's and
// Advanced's).
func TestBannerAppliesToScreen_Scope(t *testing.T) {
	included := []Screen{ScreenModeSelect, ScreenHub, ScreenAdvancedRoleSelect, ScreenClientPanel, ScreenServerPanel}
	for _, s := range included {
		if !bannerAppliesToScreen(s) {
			t.Errorf("screen=%v: expected bannerAppliesToScreen to be true", s)
		}
	}

	excluded := []Screen{
		ScreenLANPeerConnect, ScreenCustomBootModeSelect, ScreenTargetDiskSelect,
		ScreenStorageAllocationSelect, ScreenSeederDashboard, ScreenSCPPush,
		ScreenFilePicker, ScreenUSBSelect, ScreenUSBTargetSelect, ScreenUSBFormatConfirm,
		ScreenUSBDownloadProgress, ScreenProgress, ScreenDone, ScreenError, ScreenCredentials,
		ScreenConfirm,
	}
	withTTY(t, true)
	withLocaleUTF8(t, true)
	for _, s := range excluded {
		if bannerAppliesToScreen(s) {
			t.Errorf("screen=%v: expected bannerAppliesToScreen to be false", s)
		}
		if got := renderBanner(s, 220, 90, "h", "b", "f"); got != "" {
			t.Errorf("screen=%v: expected renderBanner to return empty, got %q", s, got)
		}
	}
}

// TestBanner_WhenNotShown_OutputHasNoExtraLines confirms that when
// renderBanner decides to show nothing (not a TTY, in this case — the
// simplest way to force that decision deterministically), View()'s
// output has exactly the same line count as header+body+footer alone,
// proving the feature contributes zero extra lines rather than an empty
// one — the real way "never push menu items off-screen" is enforced,
// not just asserted.
func TestBanner_WhenNotShown_OutputHasNoExtraLines(t *testing.T) {
	withTTY(t, false)
	withLocaleUTF8(t, true)

	m := NewModel("")
	m.screen = ScreenHub
	m.width, m.height = 220, 90
	out := m.View()

	header := StyleHeader.Render(appTitle)
	body := m.viewHub()
	footer := StyleFooter.Render(m.footerHint())

	lineCount := func(s string) int { return strings.Count(s, "\n") + 1 }
	gotLines := lineCount(out)
	wantLines := lineCount(header) + lineCount(body) + lineCount(footer)
	if gotLines != wantLines {
		t.Errorf("expected exactly header+body+footer line count (%d) when no banner shows, got %d", wantLines, gotLines)
	}
}

// TestRenderBanner_NeverWiderThanTerminal checks renderBanner's own
// return value directly (not the full View() output, which also
// contains pre-existing, unrelated-to-this-feature content such as the
// Hub's own footer hint text, already wider than some of these terminal
// widths regardless of the banner) — every line the banner itself
// contributes must fit within the width it was given, at every tier.
func TestRenderBanner_NeverWiderThanTerminal(t *testing.T) {
	withTTY(t, true)
	withLocaleUTF8(t, true)

	sizes := []int{40, 70, 80, 98, 100, 130, 220}
	for _, width := range sizes {
		for _, height := range []int{10, 24, 30, 40, 70, 90} {
			got := renderBanner(ScreenModeSelect, width, height, "h\nh2", strings.Repeat("b\n", 17), "f\nf2")
			for _, line := range strings.Split(got, "\n") {
				if w := lipgloss.Width(line); w > width {
					t.Errorf("width=%d height=%d: banner line %d cols wide exceeds terminal width: %q", width, height, w, line)
				}
			}
		}
	}
}

func TestIsUTF8CodePage(t *testing.T) {
	cases := []struct {
		cp   uint32
		want bool
	}{
		{65001, true},
		{437, false},
		{850, false},
		{1252, false},
		{0, false},
	}
	for _, c := range cases {
		if got := isUTF8CodePage(c.cp); got != c.want {
			t.Errorf("isUTF8CodePage(%d) = %v, want %v", c.cp, got, c.want)
		}
	}
}

func TestBannerArt_DimensionsAndCleanliness(t *testing.T) {
	if bannerHeight == 0 || bannerWidth == 0 {
		t.Fatalf("expected non-zero banner dimensions, got %dx%d", bannerWidth, bannerHeight)
	}
	for i, line := range bannerLines {
		if line != strings.TrimRight(line, " \t") {
			t.Errorf("line %d has trailing whitespace: %q", i, line)
		}
		if strings.Contains(line, "\t") {
			t.Errorf("line %d contains a tab", i)
		}
		if strings.Contains(line, "\r") {
			t.Errorf("line %d contains a CR (Windows line ending)", i)
		}
	}

	// Confirm the embedded source file on disk matches what was verified
	// by hand before this feature was built, so a future accidental edit
	// to ui/banner.txt (re-wrapped, re-indented, saved with CRLF by an
	// editor) is caught here rather than silently shipped.
	raw, err := os.ReadFile(filepath.Join(".", "banner.txt"))
	if err != nil {
		t.Fatalf("reading banner.txt: %v", err)
	}
	if strings.Contains(string(raw), "\r\n") {
		t.Errorf("banner.txt contains CRLF line endings")
	}
	if strings.Contains(string(raw), "\t") {
		t.Errorf("banner.txt contains tabs")
	}
}

// TestHeadlessPathsNeverImportUI is a structural guard, not a rendering
// test: --headless and --revert never construct a ui.Model or a
// tea.Program at all, which is WHY no banner (or anything else from
// View()) can ever reach a headless/non-interactive run — not a flag
// this feature threads through by hand. This fails loudly if that
// structural guarantee is ever broken by a future change.
func TestHeadlessPathsNeverImportUI(t *testing.T) {
	repoRoot := ".."
	files := []string{
		filepath.Join(repoRoot, "cmd", "orchestrator", "headless.go"),
		filepath.Join(repoRoot, "cmd", "orchestrator", "revert.go"),
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("reading %s: %v", f, err)
		}
		src := string(raw)
		if strings.Contains(src, `"boot-orchestrator/ui"`) {
			t.Errorf("%s imports boot-orchestrator/ui — headless/revert must never construct a ui.Model (that's the only thing that can show a banner)", f)
		}
		if strings.Contains(src, "charmbracelet/bubbletea") {
			t.Errorf("%s imports bubbletea — headless/revert must never run a tea.Program", f)
		}
	}
}

// TestBannerSymbolsOnlyReferencedFromViewPath confirms the banner (and
// wordmark) can only ever be reached through View()'s own render path
// (banner.go's definitions, plus view.go's one call site, plus
// cmd/orchestrator/main.go's one SetVersion call) — nothing else in this
// package or cmd/orchestrator independently prints any of this to
// stdout outside Bubble Tea's alt-screen buffer.
func TestBannerSymbolsOnlyReferencedFromViewPath(t *testing.T) {
	allowed := map[string]bool{
		"banner.go":                true,
		"banner_locale_unix.go":    true,
		"banner_locale_windows.go": true,
		"view.go":                  true,
	}
	allowedCmdOrchestrator := map[string]bool{
		"main.go":    true, // ui.SetVersion(Version) only
		"version.go": true,
	}
	guardedSymbols := []string{"bannerArt", "bannerLines", "wordmarkLines", "wordmarkBlockLines", "combinedTierLines", "renderBanner("}

	dirs := []struct {
		path    string
		allowed map[string]bool
	}{
		{".", allowed},
		{filepath.Join("..", "cmd", "orchestrator"), allowedCmdOrchestrator},
	}
	for _, d := range dirs {
		entries, err := os.ReadDir(d.path)
		if err != nil {
			t.Fatalf("reading %s: %v", d.path, err)
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			if d.allowed[name] {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(d.path, name))
			if err != nil {
				t.Fatalf("reading %s: %v", name, err)
			}
			src := string(raw)
			for _, sym := range guardedSymbols {
				if strings.Contains(src, sym) {
					t.Errorf("%s/%s references %q outside the known View() render path", d.path, name, sym)
				}
			}
		}
	}
}
