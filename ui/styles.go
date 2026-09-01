package ui

import "github.com/charmbracelet/lipgloss"

var (
	// High-Contrast Industrial Color Matrix
	ColorBgDark     = lipgloss.Color("#0A0D12")
	ColorSurface    = lipgloss.Color("#121722")
	ColorBorder     = lipgloss.Color("#26324A")
	ColorBorderCyan = lipgloss.Color("#00F0FF")
	ColorCyan       = lipgloss.Color("#00F0FF")
	ColorGreen      = lipgloss.Color("#00E676")
	ColorAmber      = lipgloss.Color("#FFB800")
	ColorRed        = lipgloss.Color("#FF1744")
	ColorWhite      = lipgloss.Color("#F0F4FC")
	ColorMuted      = lipgloss.Color("#7081A0")
	ColorBarBg      = lipgloss.Color("#1B2333")

	// Structural Industrial Header
	StyleHeader = lipgloss.NewStyle().
			Bold(true).
			Background(ColorCyan).
			Foreground(ColorBgDark).
			Padding(0, 2).
			MarginBottom(1)

	// Sharp Structural Container Panels
	StylePanel = lipgloss.NewStyle().
			Border(lipgloss.ThickBorder()).
			BorderForeground(ColorBorder).
			Background(ColorSurface).
			Padding(1, 2).
			Width(80)

	StylePanelFocused = lipgloss.NewStyle().
			Border(lipgloss.DoubleBorder()).
			BorderForeground(ColorBorderCyan).
			Background(ColorSurface).
			Padding(1, 2).
			Width(80)

	// High-Visibility Status Badges
	BadgeSuccess = lipgloss.NewStyle().
			Bold(true).
			Background(ColorGreen).
			Foreground(ColorBgDark).
			Padding(0, 1)

	BadgeDanger = lipgloss.NewStyle().
			Bold(true).
			Background(ColorRed).
			Foreground(ColorWhite).
			Padding(0, 1)

	BadgeWarning = lipgloss.NewStyle().
			Bold(true).
			Background(ColorAmber).
			Foreground(ColorBgDark).
			Padding(0, 1)

	BadgeInfo = lipgloss.NewStyle().
			Bold(true).
			Background(ColorCyan).
			Foreground(ColorBgDark).
			Padding(0, 1)

	// Typography & Item Highlights
	StyleTitle = lipgloss.NewStyle().
			Bold(true).
			Foreground(ColorWhite)

	StyleSubTitle = lipgloss.NewStyle().
			Foreground(ColorCyan).
			Bold(true)

	StyleProgressLabel = lipgloss.NewStyle().
			Bold(true).
			Foreground(ColorWhite)

	StyleDanger = lipgloss.NewStyle().
			Bold(true).
			Foreground(ColorRed)

	StyleMuted = lipgloss.NewStyle().
			Foreground(ColorMuted)

	StyleFlavorTTY = lipgloss.NewStyle().
			Bold(true).
			Foreground(ColorAmber)

	StyleFlavorGUI = lipgloss.NewStyle().
			Bold(true).
			Foreground(ColorGreen)

	StyleETA = lipgloss.NewStyle().
			Bold(true).
			Foreground(ColorCyan)

	// Interactive List Selectors
	StyleSelectedItem = lipgloss.NewStyle().
			Foreground(ColorBgDark).
			Background(ColorCyan).
			Bold(true).
			Padding(0, 1)

	StyleNormalItem = lipgloss.NewStyle().
			Foreground(ColorWhite).
			Padding(0, 1)

	// Command Footer Bar
	StyleFooter = lipgloss.NewStyle().
			Background(lipgloss.Color("#1B2230")).
			Foreground(ColorWhite).
			Padding(0, 2).
			MarginTop(1)
)

func StyleWarningLine(s string) string {
	return lipgloss.NewStyle().Foreground(ColorAmber).Render(s)
}