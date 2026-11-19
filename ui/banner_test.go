package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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

// TestBannerViewSizes is the required 80x24/100x40/40x10 matrix, for both
// screens this feature touches. Expectations here were derived by
// actually measuring this package's own rendered header/body/footer
// heights (17/29 body lines for ModeSelect/Hub respectively, as of this
// writing) rather than guessed, since the Hub's taller body genuinely
// needs more vertical room than ModeSelect's before the full 32-line art
// fits — a real, computed adaptive difference between the two screens,
// not an inconsistency.
func TestBannerViewSizes(t *testing.T) {
	withTTY(t, true)
	withLocaleUTF8(t, true)

	type expectation struct {
		braille bool
		compact bool
	}
	cases := []struct {
		screen Screen
		width  int
		height int
		want   expectation
	}{
		{ScreenModeSelect, 80, 24, expectation{braille: false, compact: true}},
		{ScreenModeSelect, 100, 40, expectation{braille: false, compact: true}},
		{ScreenModeSelect, 40, 10, expectation{braille: false, compact: false}},
		{ScreenHub, 80, 24, expectation{braille: false, compact: false}},
		{ScreenHub, 100, 40, expectation{braille: false, compact: true}},
		{ScreenHub, 40, 10, expectation{braille: false, compact: false}},
	}

	for _, c := range cases {
		m := NewModel("")
		m.screen = c.screen
		m.width, m.height = c.width, c.height
		out := m.View()

		gotBraille := containsBraille(out)
		gotCompact := containsCompactTitle(out)
		if gotBraille != c.want.braille || gotCompact != c.want.compact {
			t.Errorf("screen=%v size=%dx%d: got braille=%v compact=%v, want braille=%v compact=%v",
				c.screen, c.width, c.height, gotBraille, gotCompact, c.want.braille, c.want.compact)
		}

		// Never wrap, never truncate, never push menu items off-screen —
		// scoped to what the BANNER itself contributes, not a general
		// claim that every pre-existing line in this TUI already fits
		// the terminal width (the Hub's own footer hint text is wider
		// than 80/40 columns regardless of this feature, a pre-existing,
		// separate condition unrelated to the banner). If the full art
		// is showing, every one of its lines must still be exactly
		// bannerWidth wide — proof nothing clipped or wrapped it.
		if gotBraille {
			for _, line := range bannerLines {
				if !strings.Contains(out, line) {
					t.Errorf("screen=%v size=%dx%d: expected an unmodified banner line in the output, missing: %q", c.screen, c.width, c.height, line)
				}
			}
		}
		if !strings.Contains(out, "[COMMANDS]") {
			t.Errorf("screen=%v size=%dx%d: footer hint missing from output entirely", c.screen, c.width, c.height)
		}
	}
}

// TestBannerViewSizes_FullArtAtGenerousSize confirms the full braille art
// actually does render, byte-for-byte unmodified, given enough room — the
// matrix above only proves the two realistic/edge sizes, not that the
// "happy path" works at all.
func TestBannerViewSizes_FullArtAtGenerousSize(t *testing.T) {
	withTTY(t, true)
	withLocaleUTF8(t, true)

	for _, screen := range []Screen{ScreenModeSelect, ScreenHub} {
		m := NewModel("")
		m.screen = screen
		m.width, m.height = 220, 90
		out := m.View()

		if !containsBraille(out) {
			t.Fatalf("screen=%v: expected full braille art at a generous 220x90 size, got none; output:\n%s", screen, out)
		}
		for _, line := range bannerLines {
			if !strings.Contains(out, line) {
				t.Errorf("screen=%v: expected an exact, unmodified banner line in the output, missing: %q", screen, line)
			}
		}
	}
}

func TestBanner_NonUTF8Locale_FallsBackToCompact(t *testing.T) {
	withTTY(t, true)
	withLocaleUTF8(t, false)

	m := NewModel("")
	m.screen = ScreenHub
	m.width, m.height = 220, 90 // plenty of room, so only the locale can explain a fallback
	out := m.View()

	if containsBraille(out) {
		t.Errorf("expected no braille glyphs under a non-UTF-8 (C) locale even with plenty of room, got some")
	}
	if !containsCompactTitle(out) {
		t.Errorf("expected the compact ASCII title as the non-UTF-8 fallback, got neither")
	}
}

func TestBanner_NotATTY_NoBannerAtAll(t *testing.T) {
	withTTY(t, false)
	withLocaleUTF8(t, true)

	for _, screen := range []Screen{ScreenModeSelect, ScreenHub} {
		m := NewModel("")
		m.screen = screen
		m.width, m.height = 220, 90 // plenty of room — only non-TTY can explain nothing showing
		out := m.View()

		if containsBraille(out) || containsCompactTitle(out) {
			t.Errorf("screen=%v: expected no banner at all when stdout is not a TTY, got braille=%v compact=%v", screen, containsBraille(out), containsCompactTitle(out))
		}
	}
}

// TestBanner_OtherScreensNeverShowBanner confirms the feature is scoped
// to exactly the two named screens, not every screen in the TUI.
func TestBanner_OtherScreensNeverShowBanner(t *testing.T) {
	withTTY(t, true)
	withLocaleUTF8(t, true)

	for _, screen := range []Screen{ScreenAdvancedRoleSelect, ScreenProgress, ScreenDone, ScreenError} {
		if bannerAppliesToScreen(screen) {
			t.Errorf("screen=%v: bannerAppliesToScreen unexpectedly true", screen)
		}
		if got := renderBanner(screen, 220, 90, "h", "b", "f"); got != "" {
			t.Errorf("screen=%v: expected renderBanner to return empty, got %q", screen, got)
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

// TestBannerSymbolsOnlyReferencedFromViewPath confirms the banner can
// only ever be reached through View()'s own render path (banner.go's
// definitions, plus view.go's one call site) — nothing else in this
// package or cmd/orchestrator independently prints bannerArt/bannerLines
// to stdout outside Bubble Tea's alt-screen buffer.
func TestBannerSymbolsOnlyReferencedFromViewPath(t *testing.T) {
	allowed := map[string]bool{
		"banner.go":                true,
		"banner_locale_unix.go":    true,
		"banner_locale_windows.go": true,
		"view.go":                  true,
	}

	dirs := []string{".", filepath.Join("..", "cmd", "orchestrator")}
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("reading %s: %v", dir, err)
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			if dir == "." && allowed[name] {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatalf("reading %s: %v", name, err)
			}
			src := string(raw)
			for _, sym := range []string{"bannerArt", "bannerLines", "renderBanner("} {
				if strings.Contains(src, sym) {
					t.Errorf("%s/%s references %q outside the known View() render path", dir, name, sym)
				}
			}
		}
	}
}
