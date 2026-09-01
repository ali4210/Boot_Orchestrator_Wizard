package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

const appTitle = " UNIVERSAL OS, STORAGE & HYPERVISOR ORCHESTRATOR :: [ENTERPRISE PRO] "

// View satisfies tea.Model.
func (m Model) View() string {
	if m.fatalErr != nil {
		return StyleDanger.Render(fmt.Sprintf("\n [FATAL SYSTEM ERROR]: %v\n", m.fatalErr))
	}

	header := StyleHeader.Render(appTitle)
	var body string

	switch m.screen {
	case ScreenWelcome:
		body = m.viewWelcome()
	case ScreenEnvironmentCheck:
		body = m.viewEnvironmentCheck()
	case ScreenOSSelect:
		body = m.viewOSSelect()
	case ScreenConfirm:
		body = m.viewConfirm()
	case ScreenProgress:
		body = m.viewProgress()
	case ScreenDone:
		body = m.viewDone()
	case ScreenError:
		body = m.viewError()
	}

	footer := StyleFooter.Render(m.footerHint())

	return lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
}

func (m Model) footerHint() string {
	var hint string
	switch m.screen {
	case ScreenWelcome:
		hint = "ENTER: Start Diagnostics  |  Q: Abort"
	case ScreenEnvironmentCheck:
		if m.envChecked {
			hint = "ENTER: Proceed to Catalog  |  Q: Abort"
		} else {
			hint = "Probing Bus & Firmware Geometry...  |  Q: Abort"
		}
	case ScreenOSSelect:
		hint = "UP/DOWN: Browse  |  /: Filter  |  ENTER: Select  |  ESC: Back  |  Q: Abort"
	case ScreenConfirm:
		hint = "Y/ENTER: Commit Transaction  |  N/ESC: Cancel & Return"
	case ScreenProgress:
		hint = "DO NOT POWER OFF: Streaming payload to raw partition..."
	case ScreenDone, ScreenError:
		hint = "ENTER: Exit Orchestrator"
	default:
		hint = "Q: Exit"
	}
	return " [COMMANDS] " + hint
}

func (m Model) viewWelcome() string {
	var b strings.Builder

	fmt.Fprintf(&b, "%s\n", StyleTitle.Render("AUTOMATED BARE-METAL & HYPERVISOR PROVISIONING WIZARD"))
	fmt.Fprintf(&b, "%s\n\n", StyleMuted.Render("Production-Grade Multi-Boot, Partitioning & RootFS Deployment Engine"))

	fmt.Fprintf(&b, "  %s  %s\n", StyleSubTitle.Render("[1]"), "Hardware & Pre-Flight Gate Assertions (UEFI, NVRAM, Headroom)")
	fmt.Fprintf(&b, "  %s  %s\n", StyleSubTitle.Render("[2]"), "Universal Distribution Catalog (Linux, Windows, TTY & GUI Profiles)")
	fmt.Fprintf(&b, "  %s  %s\n", StyleSubTitle.Render("[3]"), "Transactional Extraction, Docker Isolation & Non-Destructive Shrink")
	fmt.Fprintf(&b, "  %s  %s\n\n", StyleSubTitle.Render("[4]"), "Automated State Journaling with Atomic Crash Recovery")

	fmt.Fprintf(&b, "=> %s", StyleSubTitle.Render("Press ENTER to initialize pre-flight diagnostics."))

	return StylePanel.Render(b.String())
}
func (m Model) viewEnvironmentCheck() string {
	if !m.envChecked {
		return StylePanel.Render(" [DIAGNOSTICS] Probing DMI tables, UEFI NVRAM, and storage controllers...")
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", StyleTitle.Render("PRE-FLIGHT DIAGNOSTICS & SYSTEM ASSERTIONS"))

	// Explicit string conversions
	hvStr := fmt.Sprint(m.hvInfo.Kind)
	fwStr := fmt.Sprint(m.fwInfo.Mode)

	hvBadge := BadgeInfo.Render(" " + strings.ToUpper(hvStr) + " ")
	fwBadge := BadgeSuccess.Render(" " + strings.ToUpper(fwStr) + " ")
	if !strings.EqualFold(fwStr, "uefi") {
		fwBadge = BadgeDanger.Render(" " + strings.ToUpper(fwStr) + " ")
	}

	fmt.Fprintf(&b, "  %s  %s\n", StyleProgressLabel.Render("Target Architecture:"), hvBadge)
	fmt.Fprintf(&b, "  %s  %s\n\n", StyleProgressLabel.Render("Firmware Mode:      "), fwBadge)

	// Disk Headroom assertion (Compact line formatting)
	spaceBadge := BadgeSuccess.Render(" SUFFICIENT ")
	if !m.guardReport.HasEnoughSpace {
		spaceBadge = BadgeDanger.Render(" INSUFFICIENT ")
	}
	fmt.Fprintf(&b, "  %s  %s\n", StyleProgressLabel.Render("Disk Headroom:      "), spaceBadge)
	fmt.Fprintf(&b, "    %s\n\n", StyleMuted.Render(fmt.Sprintf("└─ %s Free / %s Target Required",
		FormatBytes(m.guardReport.AvailableBytes),
		FormatBytes(m.guardReport.RequiredBytes),
	)))

	// Power Assertion
	powerBadge := BadgeSuccess.Render(" AC CONNECTED ")
	if !m.guardReport.PowerOK {
		powerBadge = BadgeDanger.Render(" BATTERY CRITICAL (<50%) ")
	}
	fmt.Fprintf(&b, "  %s  %s\n", StyleProgressLabel.Render("Power Status:       "), powerBadge)

	if m.envErr != nil {
		fmt.Fprintf(&b, "\n%s\n", BadgeWarning.Render(" WARNING ")+" "+m.envErr.Error())
	}

	return StylePanel.Render(b.String())
}
func (m Model) viewOSSelect() string {
	return StylePanelFocused.Render(m.osList.View())
}

func (m Model) viewConfirm() string {
	if m.selectedOS == nil {
		return StylePanel.Render(StyleDanger.Render("No target distribution selected."))
	}
	e := *m.selectedOS

	flavorBadge := BadgeSuccess.Render(" DESKTOP GUI ")
	if strings.ToLower(string(e.Flavor)) == "tty" {
		flavorBadge = BadgeWarning.Render(" HEADLESS / TTY ")
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", StyleTitle.Render("TRANSACTION CONFIRMATION & WRITE ASSERTION"))
	fmt.Fprintf(&b, "  => Target OS:        %s %s\n", StyleSubTitle.Render(e.Distro), e.Version)
	fmt.Fprintf(&b, "  => Profile:          %s\n", flavorBadge)
	fmt.Fprintf(&b, "  => Architecture:     %s\n", e.Arch)
	fmt.Fprintf(&b, "  => Allocation:       %d GB Dedicated Partition\n", e.MinDiskGB)

	if e.RequiresLicense {
		fmt.Fprintf(&b, "\n  %s %s\n", BadgeWarning.Render(" LICENSE "), "Requires official product license post-installation.")
	}
	if e.EOL {
		fmt.Fprintf(&b, "  %s %s\n", BadgeDanger.Render(" EOL NOTICE "), "Legacy release without upstream security patches.")
	}
	if e.Notes != "" {
		fmt.Fprintf(&b, "\n  %s: %s\n", StyleProgressLabel.Render("Deployment Notes"), StyleMuted.Render(e.Notes))
	}

	fmt.Fprintf(&b, "\n%s", StyleSubTitle.Render("=> Commit partition changes and begin extraction? (y/n)"))

	return StylePanel.Render(b.String())
}

func (m Model) viewProgress() string {
	pct := 0.0
	etaStr := "Calculating..."
	speedStr := "-- MB/s"

	if m.speed != nil {
		if p := m.speed.PercentComplete(); p >= 0 {
			pct = p / 100
		}
		d, ok := m.speed.ETA()
		etaStr = FormatETA(d, ok)
		speedStr = FormatBytesPerSecond(m.speed.BytesPerSecond())
	}

	bar := m.progressBar.ViewAs(pct)
	percentBadge := BadgeInfo.Render(fmt.Sprintf(" %.1f%% ", pct*100))

	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", StyleTitle.Render("PIPELINE EXECUTION: STREAMING & PROVISIONING"))
	fmt.Fprintf(&b, "%s\n\n", bar)
	fmt.Fprintf(&b, "Status: %s    Speed: %s    %s\n\n",
		percentBadge,
		StyleProgressLabel.Render(speedStr),
		StyleETA.Render("ETA: "+etaStr),
	)

	if len(m.statusLog) > 0 {
		fmt.Fprintf(&b, "%s\n", StyleTitle.Render("Recent Steps:"))
		start := 0
		if len(m.statusLog) > 4 {
			start = len(m.statusLog) - 4
		}
		for _, log := range m.statusLog[start:] {
			fmt.Fprintf(&b, "  %s %s\n", StyleSubTitle.Render("=>"), StyleMuted.Render(log))
		}
	}

	return StylePanel.Render(b.String())
}

func (m Model) viewDone() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", BadgeSuccess.Render(" PROVISIONING COMPLETE "))
	fmt.Fprintf(&b, "All operations committed successfully:\n")
	fmt.Fprintf(&b, "  => Partition resized, allocated and formatted\n")
	fmt.Fprintf(&b, "  => OS payload extracted and boot files registered to ESP\n")
	fmt.Fprintf(&b, "  => UEFI NVRAM BootNext sequence updated\n\n")
	fmt.Fprintf(&b, "%s", StyleSubTitle.Render("Press ENTER to exit. Reboot system to initialize newly deployed OS."))
	return StylePanel.Render(b.String())
}

func (m Model) viewError() string {
	msg := "An operational failure occurred during deployment."
	if m.fatalErr != nil {
		msg = m.fatalErr.Error()
	} else if m.envErr != nil {
		msg = m.envErr.Error()
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", BadgeDanger.Render(" TRANSACTION FAILED "))
	fmt.Fprintf(&b, "%s\n\n", StyleDanger.Render(msg))
	fmt.Fprintf(&b, "%s", StyleMuted.Render("State journal executed atomic rollback. Host filesystem preserved."))
	return StylePanel.Render(b.String())
}