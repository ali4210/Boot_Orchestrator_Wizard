// Package ui — view.go implements the rendering engine and screen layouts
// for the Enterprise Pro TUI, including physical disk carving telemetry,
// autonomous dynamic USB partitioning warnings, and target selection panels.
package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

const appTitle = " UNIVERSAL OS, STORAGE & HYPERVISOR ORCHESTRATOR :: [ENTERPRISE PRO] "

func (m Model) View() string {
	header := StyleHeader.Render(appTitle)

	if m.fatalErr != nil {
		var b strings.Builder
		fmt.Fprintf(&b, "%s\n\n", BadgeDanger.Render(" OPERATIONAL STORAGE NOTICE "))
		fmt.Fprintf(&b, "%s\n\n", StyleDanger.Render(fmt.Sprintf("%v", m.fatalErr)))
		fmt.Fprintf(&b, "%s\n\n", StyleMuted.Render("The transactional state journal halted execution safely. Your host files and partitions are 100% intact."))
		fmt.Fprintf(&b, "=> %s", StyleSubTitle.Render("Press ENTER or ESC to return to the Enterprise Hub."))

		return lipgloss.JoinVertical(
			lipgloss.Left,
			header,
			StylePanel.Render(b.String()),
			StyleFooter.Render(" [COMMANDS] ENTER/ESC: Return to Main Menu  |  ESC: Quit"),
		)
	}

	var body string
	switch m.screen {
	case ScreenModeSelect:
		body = m.viewModeSelect()
	case ScreenAdvancedRoleSelect:
		body = m.viewAdvancedRoleSelect()
	case ScreenClientPanel:
		body = m.viewClientPanel()
	case ScreenServerPanel:
		body = m.viewServerPanel()
	case ScreenLANPeerConnect:
		body = m.viewLANPeerConnect()
	case ScreenCustomBootModeSelect:
		body = m.viewCustomBootModeSelect()
	case ScreenStorageAllocationSelect:
		body = m.viewStorageAllocationSelect()
	case ScreenSeederDashboard:
		body = m.viewSeederDashboard()
	case ScreenSCPPush:
		body = m.viewSCPPush()
	case ScreenFilePicker:
		body = m.viewFilePicker()
	case ScreenUSBSelect:
		body = m.viewUSBSelect()
	case ScreenUSBTargetSelect:
		body = m.viewUSBTargetSelect()
	case ScreenUSBFormatConfirm:
		body = m.viewUSBFormatConfirm()
	case ScreenUSBDownloadProgress:
		body = m.viewUSBDownloadProgress()
	case ScreenResumeAlert:
		body = m.viewResumeAlert()
	case ScreenHub:
		body = m.viewHub()
	case ScreenFolderSelect:
		body = StylePanelFocused.Render(m.folderList.View())
	case ScreenOSSelect:
		body = StylePanelFocused.Render(m.osList.View())
	case ScreenEnvironmentCheck:
		body = m.viewEnvironmentCheck()
	case ScreenBackupPrompt:
		body = m.viewBackupPrompt()
	case ScreenDisasterRecovery:
		body = m.viewDisasterRecovery()
	case ScreenRestoreConfirm:
		body = m.viewRestoreConfirm()
	case ScreenConfirm:
		body = m.viewConfirm()
	case ScreenRevertConfirm:
		body = m.viewRevertConfirm()
	case ScreenProgress:
		body = m.viewProgress()
	case ScreenDone:
		body = m.viewDone()
	case ScreenRevertDone:
		body = m.viewRevertDone()
	case ScreenError:
		body = m.viewError()
	}

	footer := StyleFooter.Render(m.footerHint())
	return lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
}

func (m Model) footerHint() string {
	var hint string
	switch m.screen {
	case ScreenModeSelect:
		hint = "UP/DOWN: Navigate  |  ENTER: Select Mode  |  1-2: Quick Select  |  ESC: Quit"
	case ScreenAdvancedRoleSelect:
		hint = "UP/DOWN: Navigate  |  ENTER: Select Role  |  1-2: Quick Select  |  ESC: Back  |  ESC: Quit"
	case ScreenClientPanel:
		hint = "UP/DOWN: Navigate  |  ENTER: Select Source  |  1-4: Quick Select  |  C: Purge Scratch  |  ESC: Back"
	case ScreenServerPanel:
		hint = "UP/DOWN: Navigate  |  ENTER: Select Tool  |  1-2: Quick Select  |  ESC: Back  |  ESC: Quit"
	case ScreenLANPeerConnect:
		hint = "UP/DOWN: Pick Server  |  TAB: Manual Entry  |  ENTER: Commit  |  ESC: Back"
	case ScreenCustomBootModeSelect:
		hint = "UP/DOWN: Choose Dual/Single Boot  |  ENTER: Commit  |  ESC: Back"
	case ScreenStorageAllocationSelect:
		hint = "UP/DOWN: Pick Scheme  |  TAB: Custom Input  |  ENTER: Confirm Storage  |  ESC: Back"
	case ScreenSeederDashboard:
		hint = "UP/DOWN: Highlight Option  |  ENTER: Execute  |  ESC: Back"
	case ScreenSCPPush:
		hint = "TAB/UP/DOWN: Cycle Fields  |  ENTER: Advance/Execute  |  ESC: Back"
	case ScreenFilePicker:
		hint = "UP/DOWN: Browse Files  |  ENTER: Open/Select  |  A: Toggle All  |  S: Pick Dir  |  ESC: Back"
	case ScreenUSBSelect:
		hint = "UP/DOWN: Select Image  |  ENTER: Commit USB Image  |  R: Rescan  |  ESC: Back"
	case ScreenUSBTargetSelect:
		hint = "UP/DOWN: Navigate  |  ENTER: Commit Choice  |  R: Rescan Hardware Bus  |  ESC: Back"
	case ScreenUSBFormatConfirm:
		hint = "Y/ENTER: Confirm Dynamic USB Formatting  |  N/ESC: Abort to Menu"
	case ScreenUSBDownloadProgress:
		hint = "ESC: Cancel / Return to Hub"
	case ScreenResumeAlert:
		hint = "Y/ENTER: Resume Incomplete Session  |  N/ESC: Purge Journal & Start Clean"
	case ScreenHub:
		hint = "UP/DOWN: Navigate  |  1-6: Direct Jump  |  ENTER: Select  |  ESC: Back  |  ESC: Quit"
	case ScreenFolderSelect:
		hint = "UP/DOWN: Browse Folders  |  ENTER: Open  |  ESC: Back to Hub  |  ESC: Quit"
	case ScreenOSSelect:
		hint = "UP/DOWN: Browse Releases  |  /: Filter  |  ENTER: Pick  |  ESC: Back  |  ESC: Quit"
	case ScreenBackupPrompt:
		hint = "UP/DOWN: Select Drive  |  ENTER: Commit Backup  |  ESC: Back"
	case ScreenDisasterRecovery:
		hint = "UP/DOWN: Select Archive  |  R: Refresh Drives  |  ENTER: Restore  |  ESC: Back"
	case ScreenRestoreConfirm:
		hint = "Y/ENTER: Commit Bare-Metal Recovery  |  ESC: Abort to Menu"
	case ScreenEnvironmentCheck:
		hint = "ENTER/ESC: Return to Main Menu  |  ESC: Quit"
	case ScreenConfirm:
		if m.provisionMode == "single-boot" {
			hint = "Type 'ERASE' & ENTER: Confirm Wipe  |  ESC: Cancel"
		} else {
			hint = "Y/ENTER: Commit Dual-Boot Transaction  |  N/ESC: Back  |  ESC: Quit"
		}
	case ScreenRevertConfirm:
		hint = "Y/ENTER: Confirm Removal  |  N/ESC: Back to Hub  |  ESC: Quit"
	case ScreenProgress:
		hint = "P: Pause & Save State  |  S: Stop & Rollback Everything"
	case ScreenDone:
		hint = "R: Reboot Into New OS Now  |  1-2: Media Management  |  ENTER/ESC: Return to Hub"
	case ScreenRevertDone:
		hint = "ENTER/ESC: Return to Hub Menu  |  ESC: Quit"
	case ScreenError:
		hint = "ENTER/ESC: Return to Main Menu  |  ESC: Quit"
	default:
		hint = "ESC: Back  |  ESC: Quit"
	}
	return " [COMMANDS] " + hint
}

func (m Model) viewModeSelect() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", StyleTitle.Render("WELCOME TO BOOT ORCHESTRATOR WIZARD"))
	fmt.Fprintf(&b, "%s\n\n", StyleMuted.Render("Select your operational tier using Arrow Keys [UP/DOWN] and press ENTER:"))

	modes := []struct {
		title string
		desc  string
	}{
		{
			title: "[1] BEGINNER MODE (Standard Orchestration Hub)",
			desc:  "Full access to Dual-Boot, Single-Boot, Diagnostics, and Recovery.\n     Automated physical GPT carving & native UEFI bootloader registration.",
		},
		{
			title: "[2] ADVANCED MODE (Distributed Mesh & Staging)",
			desc:  "Decoupled Client vs Server roles, Port 8080 LAN Seeder/Client, physical\n     disk scratch staging with automated post-install purge, and USB discovery.",
		},
	}

	for i, mode := range modes {
		cursor := "  "
		itemStyle := StyleNormalItem
		if m.modeCursor == i {
			cursor = "=>"
			itemStyle = StyleSelectedItem
		}
		fmt.Fprintf(&b, "%s %s\n     %s\n\n",
			cursor,
			itemStyle.Render(mode.title),
			StyleMuted.Render(mode.desc),
		)
	}

	fmt.Fprintf(&b, "=> %s", StyleSubTitle.Render("Use UP/DOWN to navigate, ENTER to commit, or ESC to quit."))
	return StylePanel.Render(b.String())
}

func (m Model) viewAdvancedRoleSelect() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", StyleTitle.Render("ADVANCED ARCHITECTURE: SELECT NODE ROLE"))
	fmt.Fprintf(&b, "%s\n\n", StyleMuted.Render("Choose whether this machine is receiving or distributing payloads:"))

	roles := []struct {
		title string
		desc  string
	}{
		{
			title: "[1] CLIENT MODE (Target Node)",
			desc:  "Install & deploy operating systems directly onto this machine.\n     Pull payloads via LAN seeder, physical scratch, USB, or catalog hub.",
		},
		{
			title: "[2] SERVER MODE (Host / Seeder Node)",
			desc:  "Host and broadcast operating system images across your network.\n     Spin up a Port 8080 HTTP daemon or push staged images to targets.",
		},
	}

	for i, role := range roles {
		cursor := "  "
		itemStyle := StyleNormalItem
		if m.roleCursor == i {
			cursor = "=>"
			itemStyle = StyleSelectedItem
		}
		fmt.Fprintf(&b, "%s %s\n     %s\n\n",
			cursor,
			itemStyle.Render(role.title),
			StyleMuted.Render(role.desc),
		)
	}

	fmt.Fprintf(&b, "=> %s", StyleSubTitle.Render("Use UP/DOWN to navigate, ENTER to select, or ESC to go back."))
	return StylePanel.Render(b.String())
}

func (m Model) viewClientPanel() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", StyleTitle.Render("CLIENT NODE: INGESTION & DEPLOYMENT OPTIONS"))
	fmt.Fprintf(&b, "%s\n\n", StyleMuted.Render("Choose your payload source for installing onto this machine:"))

	stagedStatus := StyleMuted.Render("None found (0 MB)")
	if m.hasStagedPayload {
		stagedStatus = BadgeSuccess.Render(fmt.Sprintf(" READY (%s) ", FormatBytes(uint64(m.stagedPayloadSize))))
	}

	options := []struct {
		num  string
		text string
		desc string
	}{
		{"1", "Pull from LAN Peer HTTP Seeder (Port 8080)", "Connect to another machine hosting an ISO file and stream directly."},
		{"2", "Deploy Staged Payload from Scratch Space (/var/tmp)", fmt.Sprintf("Status: %s — Auto-purges after install. Press C to clean.", stagedStatus)},
		{"3", "Deploy from Attached Removable Media (USB)", "Probe USB flash drives, auto-mount partitions, and select ISO/ZIP files."},
		{"4", "Standard Catalog Hub & Direct Mirror Install", "Access complete OS family catalog with Single/Dual boot disk options."},
	}

	for i, opt := range options {
		cursor := "  "
		style := StyleNormalItem
		if m.clientCursor == i {
			cursor = "=>"
			style = StyleSelectedItem
		}
		fmt.Fprintf(&b, "%s [%s] %s\n     %s\n\n",
			cursor,
			opt.num,
			style.Render(opt.text),
			StyleMuted.Render(opt.desc),
		)
	}

	fmt.Fprintf(&b, "=> %s", StyleSubTitle.Render("Use UP/DOWN to navigate, ENTER to commit, C to purge scratch, or ESC to return."))
	return StylePanel.Render(b.String())
}

func (m Model) viewServerPanel() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", StyleTitle.Render("SERVER NODE: BROADCAST & DISTRIBUTION CONSOLE"))
	fmt.Fprintf(&b, "%s\n\n", StyleMuted.Render("Manage distribution of operating system payloads to other machines:"))

	options := []struct {
		num  string
		text string
		desc string
	}{
		{"1", "Live LAN HTTP Distribution Server (Port 8080)", "Serve local Downloads directory/ISO over LAN so target nodes can pull."},
		{"2", "Push Payload to Remote Node via Network Wire (SCP)", "Stream payload over SSH into target laptop physical scratch (/var/tmp)."},
	}

	for i, opt := range options {
		cursor := "  "
		style := StyleNormalItem
		if m.serverCursor == i {
			cursor = "=>"
			style = StyleSelectedItem
		}
		fmt.Fprintf(&b, "%s [%s] %s\n     %s\n\n",
			cursor,
			opt.num,
			style.Render(opt.text),
			StyleMuted.Render(opt.desc),
		)
	}

	if m.seederActive {
		fmt.Fprintf(&b, "  %s %s\n\n", BadgeSuccess.Render(" SEEDER ACTIVE "), StyleSubTitle.Render(m.seederURL))
	}

	fmt.Fprintf(&b, "=> %s", StyleSubTitle.Render("Use UP/DOWN to navigate, ENTER to commit, or ESC to return."))
	return StylePanel.Render(b.String())
}

func (m Model) viewSeederDashboard() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", StyleTitle.Render("SEEDER NODE: LIVE LAN HTTP DISTRIBUTION SERVER"))

	statusBadge := BadgeDanger.Render(" STOPPED / OFFLINE ")
	if m.seederActive {
		statusBadge = BadgeSuccess.Render(" ACTIVE & BROADCASTING (PORT 8080) ")
	}

	fmt.Fprintf(&b, "  Server Status:     %s\n", statusBadge)
	if m.seederActive {
		fmt.Fprintf(&b, "  Broadcast URL:     %s\n", StyleSubTitle.Render(m.seederURL))
	}
	fmt.Fprintf(&b, "\n  %s\n", StyleTitle.Render("Payload Target (File or Directory):"))

	c0, c1, c2, c3 := "  ", "  ", "  ", "  "
	s1, s2, s3 := StyleNormalItem, StyleNormalItem, StyleNormalItem

	switch m.seederMenuCursor {
	case 0:
		c0 = "=>"
	case 1:
		c1 = "=>"
		s1 = StyleSelectedItem
	case 2:
		c2 = "=>"
		s2 = StyleSelectedItem
	case 3:
		c3 = "=>"
		s3 = StyleSelectedItem
	}

	fmt.Fprintf(&b, " %s %s\n\n", c0, m.seederFileInput.View())
	fmt.Fprintf(&b, " %s %s\n", c1, s1.Render("[ Browse ISO File (File Selector) ]"))
	fmt.Fprintf(&b, " %s %s\n", c2, s2.Render("[ Browse Directory (Folder Selector) ]"))
	fmt.Fprintf(&b, " %s %s\n\n", c3, s3.Render("[ Toggle Server: Start / Stop on Port 8080 ]"))

	fmt.Fprintf(&b, "=> %s\n", StyleSubTitle.Render("UP/DOWN: Move to option | ENTER: Launch action | ESC: Back"))
	fmt.Fprintf(&b, "   %s", StyleMuted.Render("The server continues running in background when returning to menu."))

	return StylePanel.Render(b.String())
}

func (m Model) viewSCPPush() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", StyleTitle.Render("SERVER NODE: PUSH PAYLOAD TO REMOTE TARGET VIA NETWORK WIRE"))
	fmt.Fprintf(&b, "Transmit a bootable ISO into target laptop physical scratch space (/var/tmp):\n\n")

	c0, c1, c2, c3, c4, c5 := "  ", "  ", "  ", "  ", "  ", "  "
	s4, s5 := StyleNormalItem, StyleNormalItem

	switch m.scpFocusCursor {
	case 0:
		c0 = "=>"
	case 1:
		c1 = "=>"
	case 2:
		c2 = "=>"
	case 3:
		c3 = "=>"
	case 4:
		c4 = "=>"
		s4 = StyleSelectedItem
	case 5:
		c5 = "=>"
		s5 = StyleSelectedItem
	}

	fmt.Fprintf(&b, " %s Target Host IP:     %s\n", c0, m.scpTargetIPInput.View())
	fmt.Fprintf(&b, " %s Target Username:    %s\n", c1, m.scpUserInput.View())
	fmt.Fprintf(&b, " %s Target Password:    %s\n", c2, m.scpPasswordInput.View())
	fmt.Fprintf(&b, " %s Source File Path:   %s\n\n", c3, m.scpPayloadInput.View())

	fmt.Fprintf(&b, " %s %s\n", c4, s4.Render("[ Browse Local ISO File (File Selector) ]"))
	fmt.Fprintf(&b, " %s %s\n\n", c5, s5.Render("[ Transmit Payload via Network Wire (SCP) ]"))

	if m.isSCPPushing {
		pct := 0.0
		if m.scpTotalBytes > 0 {
			pct = float64(m.scpWrittenBytes) / float64(m.scpTotalBytes)
		}
		bar := m.progressBar.ViewAs(pct)
		speedStr := "-- MB/s"
		elapsed := time.Since(m.scpStartTime).Seconds()
		if elapsed > 0 && m.scpWrittenBytes > 0 {
			speedStr = FormatBytesPerSecond(float64(m.scpWrittenBytes) / elapsed)
		}
		fmt.Fprintf(&b, "  %s\n\n", bar)
		fmt.Fprintf(&b, "  Status: %s    Transferred: %s / %s    Speed: %s\n\n",
			BadgeInfo.Render(fmt.Sprintf(" %.1f%% ", pct*100)),
			FormatBytes(uint64(m.scpWrittenBytes)),
			FormatBytes(uint64(m.scpTotalBytes)),
			StyleProgressLabel.Render(speedStr),
		)
	} else if m.scpStatusMsg != "" {
		badge := BadgeSuccess.Render(" DONE ")
		if strings.HasPrefix(m.scpStatusMsg, "Transfer Failed") {
			badge = BadgeDanger.Render(" ERROR ")
		}
		fmt.Fprintf(&b, "  %s %s\n\n", badge, StyleSubTitle.Render(m.scpStatusMsg))
	}

	fmt.Fprintf(&b, "=> %s\n", StyleSubTitle.Render("TAB/UP/DOWN: Cycle inputs and action buttons | ENTER: Advance/Execute | ESC: Back"))
	fmt.Fprintf(&b, "   %s", StyleMuted.Render("The remote machine stages payload at /var/tmp/os_image.payload and cleans it after install."))
	return StylePanel.Render(b.String())
}

func (m Model) viewFilePicker() string {
	var b strings.Builder
	filterLabel := "Bootable & Compressed Archives (.iso, .zip, .xz, .gz, .zst, .7z)"
	if m.pickerShowAll {
		filterLabel = "ALL FILES (Unfiltered)"
	}

	fmt.Fprintf(&b, "%s\n\n", StyleTitle.Render("FILE SYSTEM BROWSER: SELECT PAYLOAD"))
	fmt.Fprintf(&b, "Current Path: %s\n", StyleSubTitle.Render(m.pickerCurrentDir))
	fmt.Fprintf(&b, "Filter Mode:  %s\n\n", StyleMuted.Render(filterLabel))

	if len(m.pickerItems) == 0 {
		fmt.Fprintf(&b, "  %s\n\n", StyleDanger.Render("No matching bootable or archive files found in this directory."))
		fmt.Fprintf(&b, "  => Press %s to toggle showing all files regardless of extension.\n\n", StyleSubTitle.Render("[A]"))
	} else {
		start := 0
		if m.pickerCursor > 8 {
			start = m.pickerCursor - 8
		}
		end := start + 10
		if end > len(m.pickerItems) {
			end = len(m.pickerItems)
		}

		for i := start; i < end; i++ {
			item := m.pickerItems[i]
			cursor := "  "
			style := StyleNormalItem
			if m.pickerCursor == i {
				cursor = "=>"
				style = StyleSelectedItem
			}

			if item.IsDir {
				fmt.Fprintf(&b, "%s 📁 %s  %s\n", cursor, style.Render(item.Name), StyleMuted.Render("(DIR)"))
			} else {
				fmt.Fprintf(&b, "%s 📦 %s  [%s - %s]\n",
					cursor,
					style.Render(item.Name),
					BadgeInfo.Render(" "+item.Ext+" "),
					FormatBytes(uint64(item.Size)),
				)
			}
		}
		fmt.Fprintf(&b, "\n")
	}

	if m.pickerIsDirMode {
		fmt.Fprintf(&b, "=> %s\n", StyleSubTitle.Render("ENTER: Open folder | S: Select current directory | A: Toggle All Files | ESC: Cancel"))
	} else {
		fmt.Fprintf(&b, "=> %s\n", StyleSubTitle.Render("ENTER: Select file / open folder | A: Toggle All Files | ESC: Cancel"))
	}
	return StylePanel.Render(b.String())
}

func (m Model) viewLANPeerConnect() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", StyleTitle.Render("CLIENT NODE: CONNECT TO LAN PEER HTTP SEEDER"))
	fmt.Fprintf(&b, "Select an auto-discovered seeder or manually type the host IP address:\n\n")

	if m.isScanning {
		fmt.Fprintf(&b, "  %s %s\n\n", BadgeInfo.Render(" SCANNING "), StyleSubTitle.Render("Sweeping local subnet for active port 8080 seeders..."))
	}

	if len(m.discoveredPeers) > 0 {
		fmt.Fprintf(&b, "%s\n", StyleTitle.Render("Auto-Discovered LAN Seeders (UP/DOWN to Select):"))
		for i, peer := range m.discoveredPeers {
			cursor := "  "
			style := StyleNormalItem
			if !m.focusManualPeerInput && m.peerCursor == i {
				cursor = "=>"
				style = StyleSelectedItem
			}
			payloadLabel := "Bootable Payload"
			if len(peer.Payloads) > 0 {
				payloadLabel = peer.Payloads[0]
			}
			fmt.Fprintf(&b, "%s [%d] %s  Payload: %s (%s)\n     URL: %s\n\n",
				cursor,
				i+1,
				style.Render(peer.IP+":8080"),
				StyleSubTitle.Render(payloadLabel),
				FormatBytes(uint64(peer.SizeBytes)),
				StyleMuted.Render(peer.URL),
			)
		}
	} else if !m.isScanning {
		fmt.Fprintf(&b, "  %s\n\n", StyleMuted.Render("No active seeders detected on local subnet."))
	}

	manualCursor := "  "
	if m.focusManualPeerInput || len(m.discoveredPeers) == 0 {
		manualCursor = "=>"
	}

	fmt.Fprintf(&b, "%s %s: %s\n\n", manualCursor, StyleTitle.Render("Manual Host IP / URL"), m.peerIPInput.View())
	fmt.Fprintf(&b, "=> %s", StyleSubTitle.Render("UP/DOWN: Navigate seeders | TAB: Focus manual input | ENTER: Commit | ESC: Back"))
	return StylePanel.Render(b.String())
}

func (m Model) viewCustomBootModeSelect() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", StyleTitle.Render("TARGET NODE: SELECT DEPLOYMENT SCHEME"))
	fmt.Fprintf(&b, "Payload Acquired: %s\n", StyleSubTitle.Render(m.overridePayloadURL))
	fmt.Fprintf(&b, "%s\n\n", StyleMuted.Render("Choose how to partition and apply this operating system to the host drive:"))

	options := []struct {
		title string
		desc  string
	}{
		{
			title: "[1] DUAL-BOOT (Install Alongside Current OS)",
			desc:  "Dynamically carves dedicated hardware partition from unallocated sectors or free space.\n     Preserves current OS and registers a direct physical boot entry in GRUB/NVRAM.",
		},
		{
			title: "[2] SINGLE-BOOT (Destructive Disk Replacement)",
			desc:  "Replaces host OS completely. Offers an automated host backup before\n     requiring explicit confirmation to erase target drive blocks.",
		},
	}

	for i, opt := range options {
		cursor := "  "
		style := StyleNormalItem
		if m.customBootCursor == i {
			cursor = "=>"
			style = StyleSelectedItem
		}
		fmt.Fprintf(&b, "%s %s\n     %s\n\n",
			cursor,
			style.Render(opt.title),
			StyleMuted.Render(opt.desc),
		)
	}

	fmt.Fprintf(&b, "=> %s", StyleSubTitle.Render("Use UP/DOWN to navigate, ENTER to commit, or ESC to go back."))
	return StylePanel.Render(b.String())
}

func (m Model) viewStorageAllocationSelect() string {
	var b strings.Builder

	freeGB := float64(m.detectedFreeBytes) / (1024 * 1024 * 1024)
	fmt.Fprintf(&b, "%s\n\n", StyleTitle.Render("STORAGE MANAGEMENT: DUAL-BOOT ALLOCATION SCHEME"))

	envBadge := BadgeSuccess.Render(" BARE-METAL PHYSICAL HARDWARE ")
	if m.isVMEnvironment {
		envBadge = BadgeInfo.Render(" VIRTUAL MACHINE DETECTED (" + strings.ToUpper(m.vmHypervisorName) + ") ")
	}
	fmt.Fprintf(&b, "  Hardware Environment: %s\n", envBadge)
	fmt.Fprintf(&b, "  Host Usable Headroom: %s\n\n", StyleSubTitle.Render(fmt.Sprintf("%.1f GB Free Storage Available", freeGB)))

	if m.isVMEnvironment && freeGB < 20.0 {
		fmt.Fprintf(&b, "  %s\n", BadgeDanger.Render(" LOW STORAGE ADVISORY "))
		fmt.Fprintf(&b, "  %s\n", StyleDanger.Render("Available host disk headroom is critically low (< 20 GB)."))
		fmt.Fprintf(&b, "  %s\n\n", StyleMuted.Render("=> ACTION RECOMMENDED:\n   1. Shut down this VM.\n   2. Open VM Settings -> Storage -> Expand Virtual Disk (+30 GB).\n   3. Boot back up and launch ./autorun.sh -f. Newly added sectors will be detected automatically."))
	}

	if m.isVMEnvironment {
		fmt.Fprintf(&b, "Select storage proportion to dedicate to the secondary OS partition:\n\n")

		options := []struct {
			name string
			calc string
		}{
			{"100% Full Free Storage", fmt.Sprintf("%.1f GB (Maximum allocation)", freeGB)},
			{"50% Balanced Storage", fmt.Sprintf("%.1f GB (Recommended for dual-boot)", freeGB*0.5)},
			{"25% Minimal Scratch", fmt.Sprintf("%.1f GB (Minimal footprint)", freeGB*0.25)},
			{"Manually Type Allocation", "Specify custom Gigabytes in field below"},
		}

		for i, opt := range options {
			cursor := "  "
			style := StyleNormalItem
			if m.storageCursor == i {
				cursor = "=>"
				style = StyleSelectedItem
			}
			fmt.Fprintf(&b, " %s [%d] %s  ──  %s\n", cursor, i+1, style.Render(opt.name), StyleMuted.Render(opt.calc))
		}

		customCursor := "  "
		if m.storageCursor == 3 {
			customCursor = "=>"
		}
		fmt.Fprintf(&b, "\n %s Custom Storage Allocation: %s GB\n\n", customCursor, m.storageCustomInput.View())

	} else {
		fmt.Fprintf(&b, "Select partition boundary allocation for bare-metal hardware slice:\n\n")

		options := []struct {
			name string
			calc string
		}{
			{"100% Free Headroom Allocation", fmt.Sprintf("%.1f GB (Dedicate all unallocated disk sectors)", freeGB)},
			{"50 / 50 Equal Split", fmt.Sprintf("%.1f GB (Balance host and secondary OS equally)", freeGB*0.5)},
			{"25% Minimal Footprint", fmt.Sprintf("%.1f GB (Secondary test footprint)", freeGB*0.25)},
			{"Shared Data Partition Scheme", fmt.Sprintf("%.1f GB to OS, remaining storage shared cross-boot", freeGB*0.5)},
			{"Manually Type Allocation", "Specify custom Gigabytes in field below"},
		}

		for i, opt := range options {
			cursor := "  "
			style := StyleNormalItem
			if m.storageCursor == i {
				cursor = "=>"
				style = StyleSelectedItem
			}
			fmt.Fprintf(&b, " %s [%d] %s\n     %s\n\n", cursor, i+1, style.Render(opt.name), StyleMuted.Render(opt.calc))
		}

		customCursor := "  "
		if m.storageCursor == 4 {
			customCursor = "=>"
		}
		fmt.Fprintf(&b, " %s Custom Storage Allocation: %s GB\n\n", customCursor, m.storageCustomInput.View())
	}

	fmt.Fprintf(&b, "=> %s", StyleSubTitle.Render("UP/DOWN: Select scheme | TAB: Focus custom input | ENTER: Advance to Confirmation | ESC: Back"))
	return StylePanel.Render(b.String())
}

func (m Model) viewUSBSelect() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", StyleTitle.Render("CLIENT NODE: SELECT BOOTABLE IMAGE FROM USB"))

	if m.isScanning {
		fmt.Fprintf(&b, "  %s %s\n\n", BadgeInfo.Render(" SCANNING "), StyleSubTitle.Render("Probing USB devices and scanning filesystem directories..."))
	}

	if len(m.discoveredUSBs) == 0 && !m.isScanning {
		fmt.Fprintf(&b, "  %s\n\n", StyleDanger.Render("No bootable ISO, IMG, or archive files found on attached USB storage."))
		fmt.Fprintf(&b, "  => Connect your USB drive and press %s to scan again.\n", StyleSubTitle.Render("[R]"))
		fmt.Fprintf(&b, "  => Press %s to return to Client Menu.\n", StyleMuted.Render("[ESC]"))
		return StylePanel.Render(b.String())
	}

	if len(m.discoveredUSBs) > 0 {
		fmt.Fprintf(&b, "%s\n\n", StyleMuted.Render("Select payload to deploy directly to this machine:"))
		for i, u := range m.discoveredUSBs {
			cursor := "  "
			style := StyleNormalItem
			if m.usbCursor == i {
				cursor = "=>"
				style = StyleSelectedItem
			}
			fmt.Fprintf(&b, "%s [%d] %s (%s)\n     Device: %s  |  Path: %s\n\n",
				cursor,
				i+1,
				style.Render(u.FileName),
				FormatBytes(uint64(u.SizeBytes)),
				u.DeviceName,
				StyleMuted.Render(u.FilePath),
			)
		}
		fmt.Fprintf(&b, "=> Press %s to select image for single-boot wipe, or %s to cancel.", StyleSubTitle.Render("[ENTER]"), StyleMuted.Render("[ESC]"))
	}

	return StylePanel.Render(b.String())
}

func (m Model) viewUSBTargetSelect() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", StyleTitle.Render("ADVANCED CLIENT: CHOOSE DEPLOYMENT ROUTE"))
	if m.selectedOS != nil {
		fmt.Fprintf(&b, "Selected OS: %s\n", StyleSubTitle.Render(m.selectedOS.Distro+" "+m.selectedOS.Version))
	}
	fmt.Fprintf(&b, "%s\n\n", StyleMuted.Render("Choose whether to execute deployment locally or stream directly to removable media USB:"))

	c0 := "  "
	s0 := StyleNormalItem
	if m.usbTargetCursor == 0 {
		c0 = "=>"
		s0 = StyleSelectedItem
	}
	fmt.Fprintf(&b, "%s %s\n     %s\n\n", c0, s0.Render("[1] DIRECT LOCAL EXECUTION"), StyleMuted.Render("Proceed straight into single-boot or dual-boot partitioning and local installation."))

	c1 := "  "
	s1 := StyleNormalItem
	if m.usbTargetCursor == 1 {
		c1 = "=>"
		s1 = StyleSelectedItem
	}
	fmt.Fprintf(&b, "%s %s\n     %s\n\n", c1, s1.Render("[2] DOWNLOAD TO REMOVABLE MEDIA (USB / PEN DRIVE)"), StyleMuted.Render("Stream this ISO payload directly onto an attached physical USB flash drive."))

	if m.isScanning {
		fmt.Fprintf(&b, "  %s %s\n\n", BadgeInfo.Render(" PROBING HARDWARE BUS "), StyleSubTitle.Render("Inspecting USB block controllers for true removable pen drives..."))
	} else if len(m.discoveredTargets) > 0 {
		fmt.Fprintf(&b, "%s\n", StyleTitle.Render("Detected True Removable USB Drives (Select target drive):"))
		for idx, tgt := range m.discoveredTargets {
			cursor := "  "
			style := StyleNormalItem
			if m.usbTargetCursor == idx+2 {
				cursor = "=>"
				style = StyleSelectedItem
			}
			label := tgt.Vendor + " " + tgt.Model
			if strings.TrimSpace(label) == "" {
				label = "External Removable Flash Disk"
			}
			fmt.Fprintf(&b, "%s [Drive %d] /dev/%s  ──  Size: %s  [%s]\n",
				cursor, idx+1,
				style.Render(tgt.Name),
				tgt.Size,
				StyleMuted.Render(label),
			)
		}
		fmt.Fprintf(&b, "\n")
	} else {
		fmt.Fprintf(&b, "  %s\n\n", StyleMuted.Render("No external removable USB drives currently detected on bus."))
	}

	rescanIndex := 2 + len(m.discoveredTargets)
	cRescan := "  "
	sRescan := StyleNormalItem
	if m.usbTargetCursor == rescanIndex {
		cRescan = "=>"
		sRescan = StyleSelectedItem
	}
	fmt.Fprintf(&b, "%s %s\n     %s\n\n", cRescan, sRescan.Render("[3] RESCAN ATTACHED MEDIA (Press R)"), StyleMuted.Render("Scan hardware block controller for freshly plugged USB flash drives."))

	fmt.Fprintf(&b, "=> %s", StyleSubTitle.Render("Use UP/DOWN to navigate, ENTER to commit, R to rescan, or ESC to go back."))
	return StylePanel.Render(b.String())
}

func (m Model) viewUSBFormatConfirm() string {
	if m.selectedTarget == nil {
		return StylePanel.Render(StyleDanger.Render("No target USB selected."))
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", BadgeDanger.Render(" CRITICAL REMOVABLE MEDIA WIPE WARNING "))
	fmt.Fprintf(&b, "Preparing this USB drive will %s all existing files and partition tables on:\n\n", StyleDanger.Render("PERMANENTLY ERASE"))
	fmt.Fprintf(&b, "  => Target Device:       %s (/dev/%s)\n", StyleSubTitle.Render(m.selectedTarget.DevPath), m.selectedTarget.Name)
	fmt.Fprintf(&b, "  => Total USB Capacity:  %s (%s)\n", StyleSubTitle.Render(m.selectedTarget.Size), m.selectedTarget.Vendor+" "+m.selectedTarget.Model)

	totalBytes := m.selectedTarget.SizeBytes
	bootBytes := uint64(8 * 1024 * 1024 * 1024)
	if totalBytes > 0 && totalBytes <= 16*1024*1024*1024 {
		bootBytes = uint64(6 * 1024 * 1024 * 1024)
	}
	storageBytes := totalBytes - bootBytes
	if totalBytes == 0 {
		storageBytes = 0
	}

	fmt.Fprintf(&b, "\n%s\n", StyleTitle.Render("AUTONOMOUS DUAL-PARTITION SCHEME TO BE PROVISIONED:"))
	fmt.Fprintf(&b, "  [1] Partition 1: %s (ext4: ORCH_STORAGE) ── Secure Host Backup & Payload Vault\n",
		StyleSubTitle.Render(fmt.Sprintf("%.1f GB", float64(storageBytes)/(1024*1024*1024))))
	fmt.Fprintf(&b, "  [2] Partition 2: %s (FAT32: ORCH_BOOT)    ── Live Deployment Kernel & UEFI Bootloader\n\n",
		StyleSubTitle.Render(fmt.Sprintf("%.1f GB", float64(bootBytes)/(1024*1024*1024))))

	fmt.Fprintf(&b, "%s\n", StyleDanger.Render("CAVEAT: If you have important files on this USB, press ESC now, back them up, and return."))
	fmt.Fprintf(&b, "%s", StyleSubTitle.Render("=> Press Y or ENTER to format USB and begin snapshot stream, or ESC to abort."))
	return StylePanel.Render(b.String())
}

func (m Model) viewUSBDownloadProgress() string {
	pct := 0.0
	if m.usbTotalBytes > 0 {
		pct = float64(m.usbWrittenBytes) / float64(m.usbTotalBytes)
	}
	bar := m.progressBar.ViewAs(pct)
	percentBadge := BadgeInfo.Render(fmt.Sprintf(" %.1f%% ", pct*100))

	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", StyleTitle.Render("USB STREAMING: DOWNLOADING PAYLOAD TO REMOVABLE MEDIA"))
	if m.selectedTarget != nil {
		fmt.Fprintf(&b, "Target Device: %s (/dev/%s)\n\n", StyleSubTitle.Render(m.selectedTarget.Vendor+" "+m.selectedTarget.Model), m.selectedTarget.Name)
	}
	fmt.Fprintf(&b, "%s\n\n", bar)
	fmt.Fprintf(&b, "Status: %s    Transferred: %s / %s\n\n",
		percentBadge,
		FormatBytes(uint64(m.usbWrittenBytes)),
		FormatBytes(uint64(m.usbTotalBytes)),
	)
	fmt.Fprintf(&b, "=> %s", StyleMuted.Render("Streaming payload directly to USB blocks. Please do not unplug the drive."))
	return StylePanel.Render(b.String())
}

func (m Model) viewResumeAlert() string {
	var b strings.Builder
	if m.unfinishedState != nil {
		fmt.Fprintf(&b, "%s\n\n", BadgeWarning.Render(" UNFINISHED SESSION DETECTED "))
		fmt.Fprintf(&b, "An interrupted or paused deployment was found in the state journal:\n\n")
		fmt.Fprintf(&b, "  => Target OS:       %s\n", StyleSubTitle.Render(m.unfinishedState.DistroName))
		fmt.Fprintf(&b, "  => Checkpoint:      %s downloaded\n", FormatBytes(uint64(m.unfinishedState.BytesDownloaded)))
		fmt.Fprintf(&b, "  => Target Disk:     %s\n\n", StyleMuted.Render(m.unfinishedState.TargetDisk))
		fmt.Fprintf(&b, "%s\n", StyleTitle.Render("Choose an action:"))
		fmt.Fprintf(&b, "  %s  Resume from paused offset\n", StyleSubTitle.Render("[Y/Enter]"))
		fmt.Fprintf(&b, "  %s  Purge temporary files, release locks & discard session\n", StyleDanger.Render("[N/Esc]"))
	} else {
		fmt.Fprintf(&b, "%s\n\n", BadgeSuccess.Render(" SYSTEM JOURNAL CLEAN "))
		fmt.Fprintf(&b, "No interrupted or paused installation sessions were detected on this machine.\n\n")
		fmt.Fprintf(&b, "  => Journal Path:    %s\n", StyleMuted.Render("orchestrator_journal.json"))
		fmt.Fprintf(&b, "  => Status:          %s\n", StyleSubTitle.Render("Idle (Zero In-Flight Operations)"))
		fmt.Fprintf(&b, "  => Partitions:      %s\n\n", StyleMuted.Render("No locks or half-written allocations active"))
		fmt.Fprintf(&b, "%s", StyleSubTitle.Render("=> Press ENTER or ESC to return to the Main Menu."))
	}
	return StylePanel.Render(b.String())
}

func (m Model) viewHub() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", StyleTitle.Render("ENTERPRISE ORCHESTRATION HUB"))
	fmt.Fprintf(&b, "%s\n\n", StyleMuted.Render("Autonomous Bare-Metal & Hypervisor Multi-Boot Pipeline"))

	options := []struct {
		num  string
		text string
		desc string
	}{
		{"1", "Deploy New OS (Dual-Boot Physical Partition)", "Carves dedicated hardware partition alongside current OS without loopback containers."},
		{"2", "Deploy New OS (Single-Boot Replace)", "Replaces host OS completely; triggers live host backup & explicit ERASE."},
		{"3", "Decommission & Revert to Single-Boot", "Purges secondary OS, cleans ESP/NVRAM, and reclaims 100% disk space."},
		{"4", "Resume / Recover Paused Session", "Inspects state journal and continues incomplete streaming transactions."},
		{"5", "Pre-Flight Diagnostics & Storage Guard", "Audits UEFI NVRAM, block geometry, battery power, and hypervisors."},
		{"6", "Disaster Recovery: Restore OS Backup", "Scans USB/external media, verifies checksums, and restores full OS."},
	}

	for i, opt := range options {
		cursor := "  "
		style := StyleNormalItem
		if m.hubCursor == i {
			cursor = "=>"
			style = StyleSelectedItem
		}
		fmt.Fprintf(&b, "%s [%s] %s\n     %s\n\n",
			cursor,
			opt.num,
			style.Render(opt.text),
			StyleMuted.Render(opt.desc),
		)
	}
	return StylePanel.Render(b.String())
}

func (m Model) viewBackupPrompt() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", BadgeWarning.Render(" DISASTER RECOVERY & SYSTEM SNAPSHOT "))

	if m.provisionMode == "dual-boot" {
		fmt.Fprintf(&b, "Dual-Boot mode installs secondary OS alongside current host system.\n")
		fmt.Fprintf(&b, "Select your USB pen drive to store a safety snapshot & stage offline resizer:\n\n")
	} else {
		fmt.Fprintf(&b, "Single-Boot mode replaces your host operating system and disk partitions.\n")
		fmt.Fprintf(&b, "Select an external drive/USB to stream a compressed .tar.gz snapshot before wiping:\n\n")
	}

	if len(m.backupTargets) == 0 {
		fmt.Fprintf(&b, "  %s\n", StyleMuted.Render("No secondary partitions or USB drives currently detected on the storage bus."))
	}

	for i, dev := range m.backupTargets {
		cursor := "  "
		style := StyleNormalItem
		if m.backupCursor == i {
			cursor = "=>"
			style = StyleSelectedItem
		}
		mountStr := dev.Mountpoint
		if mountStr == "" {
			mountStr = "(unmounted - auto mount)"
		}
		fmt.Fprintf(&b, "%s [%d] Drive: %s  Format: %s  Size: %s\n     Target: %s\n\n",
			cursor,
			i+1,
			style.Render(dev.Name),
			dev.FSType,
			dev.Size,
			StyleMuted.Render(mountStr),
		)
	}

	skipCursor := "  "
	skipStyle := StyleMuted
	skipLabel := "Skip Backup & Proceed to Destructive Wipe (Irreversible)"
	if m.provisionMode == "dual-boot" {
		skipLabel = "Skip Backup & Proceed to Partition Allocation"
	}

	if m.backupCursor == len(m.backupTargets) {
		skipCursor = "=>"
		if m.provisionMode == "dual-boot" {
			skipStyle = StyleSubTitle
		} else {
			skipStyle = StyleDanger
		}
	}
	fmt.Fprintf(&b, "%s [X] %s\n\n", skipCursor, skipStyle.Render(skipLabel))

	if m.isBackingUp {
		fmt.Fprintf(&b, "%s\n", StyleProgressLabel.Render(m.backupStatusMsg))
	} else if m.backupStatusMsg != "" {
		fmt.Fprintf(&b, "%s\n", StyleSubTitle.Render(m.backupStatusMsg))
	}

	return StylePanel.Render(b.String())
}

func (m Model) viewDisasterRecovery() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", BadgeInfo.Render(" DISASTER RECOVERY: EXTERNAL BACKUP SCANNER "))
	fmt.Fprintf(&b, "Connect the USB drive or external hard drive containing your host backup archive.\n\n")

	if m.isScanning {
		fmt.Fprintf(&b, "  %s %s\n\n", BadgeInfo.Render(" SCANNING "), StyleSubTitle.Render("Polling kernel block devices and inspecting filesystems..."))
	}

	if len(m.discoveredBackups) == 0 && !m.isScanning {
		fmt.Fprintf(&b, "  %s\n\n", StyleDanger.Render("No host_os_backup_*.tar.gz archives detected on connected storage."))
		fmt.Fprintf(&b, "  => Insert external media and press %s to scan again.\n", StyleSubTitle.Render("[R]"))
		fmt.Fprintf(&b, "  => Press %s to return to Hub Menu.\n", StyleMuted.Render("[ESC]"))
		return StylePanel.Render(b.String())
	}

	if len(m.discoveredBackups) > 0 {
		fmt.Fprintf(&b, "%s\n", StyleTitle.Render("Select backup image to restore:"))
		for i, bk := range m.discoveredBackups {
			cursor := "  "
			style := StyleNormalItem
			if m.restoreCursor == i {
				cursor = "=>"
				style = StyleSelectedItem
			}
			statusBadge := BadgeSuccess.Render(" SHA256 OK ")
			if !bk.HasChecksum {
				statusBadge = BadgeWarning.Render(" NO CHECKSUM ")
			}

			fmt.Fprintf(&b, "%s [%d] %s (%s) %s\n     Path: %s\n\n",
				cursor,
				i+1,
				style.Render(bk.FileName),
				FormatBytes(uint64(bk.SizeBytes)),
				statusBadge,
				StyleMuted.Render(bk.FilePath),
			)
		}
		fmt.Fprintf(&b, "=> Press %s to begin restoration, or %s to cancel.", StyleSubTitle.Render("[ENTER]"), StyleMuted.Render("[ESC]"))
	}

	return StylePanel.Render(b.String())
}

func (m Model) viewRestoreConfirm() string {
	if m.selectedArchive == nil {
		return StylePanel.Render(StyleDanger.Render("No backup selected."))
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", BadgeDanger.Render(" CRITICAL BARE-METAL OVERWRITE CONFIRMATION "))
	fmt.Fprintf(&b, "Restoring this backup will format the primary disk and re-extract all system files:\n\n")
	fmt.Fprintf(&b, "  => Backup Source:   %s\n", StyleSubTitle.Render(m.selectedArchive.FileName))
	fmt.Fprintf(&b, "  => Backup Size:     %s\n", FormatBytes(uint64(m.selectedArchive.SizeBytes)))
	fmt.Fprintf(&b, "  => Mount Origin:    %s\n", StyleMuted.Render(m.selectedArchive.MountPoint))
	fmt.Fprintf(&b, "  => Actions:         Wipe disk -> Format ext4 -> Extract snapshot -> Reinstall GRUB\n\n")

	fmt.Fprintf(&b, "%s", StyleDanger.Render("=> Press Y or ENTER to begin bare-metal restore, or ESC to cancel."))
	return StylePanel.Render(b.String())
}

func (m Model) viewEnvironmentCheck() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", StyleTitle.Render("PRE-FLIGHT DIAGNOSTICS & SYSTEM ASSERTIONS"))

	hvStr := fmt.Sprint(m.hvInfo.Kind)
	fwStr := fmt.Sprint(m.fwInfo.Mode)

	hvBadge := BadgeInfo.Render(" " + strings.ToUpper(hvStr) + " ")
	fwBadge := BadgeSuccess.Render(" " + strings.ToUpper(fwStr) + " ")
	if !strings.EqualFold(fwStr, "uefi") {
		fwBadge = BadgeDanger.Render(" " + strings.ToUpper(fwStr) + " ")
	}

	fmt.Fprintf(&b, "  %s  %s\n", StyleProgressLabel.Render("Target Architecture:"), hvBadge)
	fmt.Fprintf(&b, "  %s  %s\n\n", StyleProgressLabel.Render("Firmware Mode:      "), fwBadge)

	spaceBadge := BadgeSuccess.Render(" SUFFICIENT ")
	if !m.guardReport.HasEnoughSpace {
		spaceBadge = BadgeDanger.Render(" INSUFFICIENT ")
	}
	fmt.Fprintf(&b, "  %s  %s\n", StyleProgressLabel.Render("Disk Headroom:      "), spaceBadge)
	fmt.Fprintf(&b, "    %s\n\n", StyleMuted.Render(fmt.Sprintf("└─ %s Free / %s Target Required",
		FormatBytes(m.guardReport.AvailableBytes),
		FormatBytes(m.guardReport.RequiredBytes),
	)))

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

func (m Model) viewConfirm() string {
	if m.selectedOS == nil {
		return StylePanel.Render(StyleDanger.Render("No distribution selected."))
	}
	e := *m.selectedOS

	flavorBadge := BadgeSuccess.Render(" DESKTOP GUI ")
	if strings.ToLower(string(e.Flavor)) == "tty" {
		flavorBadge = BadgeWarning.Render(" HEADLESS / TTY ")
	}

	modeBadge := BadgeInfo.Render(" DUAL-BOOT (PHYSICAL PARTITION) ")
	if m.provisionMode == "single-boot" {
		modeBadge = BadgeDanger.Render(" SINGLE-BOOT (DESTRUCTIVE REPLACEMENT) ")
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", StyleTitle.Render("TRANSACTION CONFIRMATION & WRITE ASSERTION"))
	fmt.Fprintf(&b, "  => Target OS:        %s %s\n", StyleSubTitle.Render(e.Distro), e.Version)
	fmt.Fprintf(&b, "  => Execution Mode:   %s\n", modeBadge)
	fmt.Fprintf(&b, "  => Profile:          %s\n", flavorBadge)
	fmt.Fprintf(&b, "  => Architecture:     %s\n", e.Arch)
	fmt.Fprintf(&b, "  => Storage Quota:    %s\n", StyleSubTitle.Render(FormatBytes(m.chosenAllocBytes)))
	if m.overridePayloadURL != "" {
		fmt.Fprintf(&b, "  => Active Payload:   %s\n", StyleSubTitle.Render(m.overridePayloadURL))
	}

	if m.provisionMode == "single-boot" {
		fmt.Fprintf(&b, "\n  %s\n", BadgeDanger.Render(" CRITICAL DATA LOSS WARNING "))
		fmt.Fprintf(&b, "  %s\n", StyleDanger.Render("All partitions, files, and operating systems on this drive will be wiped."))
		fmt.Fprintf(&b, "  %s\n\n", StyleMuted.Render("The payload will be verified in physical scratch space first. To proceed, confirm below:"))
		fmt.Fprintf(&b, "  Type %s to commit disk wipe: %s\n\n", StyleDanger.Render("ERASE"), m.eraseConfirmInput.View())
		fmt.Fprintf(&b, "%s", StyleMuted.Render("=> Press ENTER to commit or ESC to abort safely."))
	} else {
		fmt.Fprintf(&b, "\n%s", StyleSubTitle.Render("=> Commit partition changes and begin extraction? (y/n)"))
	}

	return StylePanel.Render(b.String())
}

func (m Model) viewRevertConfirm() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", StyleTitle.Render("REVERT TO SINGLE-OS RESTORATION"))
	fmt.Fprintf(&b, "%s\n\n", StyleMuted.Render("This action will restore your machine to single-boot mode:"))
	fmt.Fprintf(&b, "  1. Deregister secondary OS from motherboard UEFI NVRAM\n")
	fmt.Fprintf(&b, "  2. Purge secondary EFI directory from ESP partition\n")
	fmt.Fprintf(&b, "  3. Delete secondary partition allocation\n")
	fmt.Fprintf(&b, "  4. Expand primary host filesystem to 100%% capacity\n\n")
	fmt.Fprintf(&b, "%s", StyleDanger.Render("=> Press Y to confirm removal, or ESC to return to Hub."))
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
	fmt.Fprintf(&b, "Status: %s    Speed: %s    %s\n",
		percentBadge,
		StyleProgressLabel.Render(speedStr),
		StyleETA.Render("ETA: "+etaStr),
	)

	fmt.Fprintf(&b, "\n[CONTROLS]: %s to Pause & Save State  |  %s to Stop & Rollback\n\n",
		StyleSubTitle.Render("[P]"),
		StyleDanger.Render("[S]"),
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
	fmt.Fprintf(&b, "%s\n\n", BadgeSuccess.Render(" OPERATION COMPLETED & VERIFIED "))
	fmt.Fprintf(&b, "Environment has been autonomously provisioned and validated:\n")
	fmt.Fprintf(&b, "  => %s Linux kernel & ramdisk verified in target /boot\n", BadgeSuccess.Render(" OK "))
	fmt.Fprintf(&b, "  => %s Dedicated physical partition mapped in /etc/fstab\n", BadgeSuccess.Render(" OK "))
	fmt.Fprintf(&b, "  => %s Host GRUB bootloader stanza generated & verified\n", BadgeSuccess.Render(" OK "))
	fmt.Fprintf(&b, "  => %s Staged scratch files autonomously purged from /var/tmp\n", BadgeSuccess.Render(" OK "))
	fmt.Fprintf(&b, "  => %s Visible boot selection menu enforced (10s timeout)\n\n", BadgeSuccess.Render(" OK "))

	fmt.Fprintf(&b, "%s\n", StyleTitle.Render("PRESET SYSTEM CREDENTIALS (AUTONOMOUS INJECTION):"))
	fmt.Fprintf(&b, "  => Root Admin:     Username: %s     | Password: %s\n",
		StyleSubTitle.Render("root"),
		StyleDanger.Render("toor"),
	)
	fmt.Fprintf(&b, "  => Standard User:  Username: %s   | Password: %s   (Sudo Enabled)\n\n",
		StyleSubTitle.Render("parrot"),
		StyleDanger.Render("parrot"),
	)

	fmt.Fprintf(&b, "%s\n", StyleTitle.Render("REMOVABLE MEDIA (USB) MANAGEMENT:"))

	if m.isResettingUSB {
		fmt.Fprintf(&b, "  %s %s\n\n", BadgeInfo.Render(" PROCESSING "), StyleSubTitle.Render("Applying filesystem and partition operations to USB drive..."))
	} else if m.usbCleanedMsg != "" {
		fmt.Fprintf(&b, "  %s %s\n\n", BadgeSuccess.Render(" USB FINALIZED "), StyleSubTitle.Render(m.usbCleanedMsg))
	} else if m.showFSPrompt {
		fmt.Fprintf(&b, "  %s\n", StyleTitle.Render("Select Cross-Platform Filesystem Format for Factory Reset:"))

		c0, c1 := "  ", "  "
		s0, s1 := StyleNormalItem, StyleNormalItem
		if m.postUSBFSCursor == 0 {
			c0 = "=>"
			s0 = StyleSelectedItem
		} else {
			c1 = "=>"
			s1 = StyleSelectedItem
		}

		fmt.Fprintf(&b, "  %s [1] %s ── Supported by Windows, Linux, Mac (Handles files >4 GB)\n", c0, s0.Render("NTFS / exFAT Format (Modern Universal)"))
		fmt.Fprintf(&b, "  %s [2] %s ── Legacy Universal (Limited to 4 GB max file size)\n\n", c1, s1.Render("FAT32 Format (Legacy Compatibility)"))
		fmt.Fprintf(&b, "  => %s\n\n", StyleSubTitle.Render("Press 1 or 2 to format, or ESC to return to main options."))
	} else {
		c0, c1 := "  ", "  "
		s0, s1 := StyleNormalItem, StyleNormalItem
		if m.postUSBActionCursor == 0 {
			c0 = "=>"
			s0 = StyleSelectedItem
		} else {
			c1 = "=>"
			s1 = StyleSelectedItem
		}

		fmt.Fprintf(&b, "  %s [1] %s\n       %s\n",
			c0, s0.Render("Keep as Disaster Recovery Vault"),
			StyleMuted.Render("Preserves host OS snapshot in ORCH_STORAGE; purges temporary installer files."),
		)
		fmt.Fprintf(&b, "  %s [2] %s\n       %s\n\n",
			c1, s1.Render("Factory Reset USB Drive (Wipe & Restore Full Capacity)"),
			StyleMuted.Render("Clears bootloader & creates a single cross-platform partition for normal file storage."),
		)
	}

	fmt.Fprintf(&b, "%s\n", StyleTitle.Render("ACTIONS:"))
	fmt.Fprintf(&b, "  %s  Reboot now directly into the OS\n", StyleSubTitle.Render("[R]      "))
	fmt.Fprintf(&b, "  %s  Return to Enterprise Hub Menu\n\n", StyleMuted.Render("[ENTER/ESC]"))

	return StylePanel.Render(b.String())
}

func (m Model) viewRevertDone() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", BadgeSuccess.Render(" SYSTEM RESTORATION COMPLETE "))
	fmt.Fprintf(&b, "Operating system has been successfully restored and verified:\n")
	fmt.Fprintf(&b, "  => %s Block partitions synchronized\n", BadgeSuccess.Render(" OK "))
	fmt.Fprintf(&b, "  => %s Host filesystem reconstructed to 100%% disk capacity\n", BadgeSuccess.Render(" OK "))
	fmt.Fprintf(&b, "  => %s Bootloader re-injected into drive MBR/ESP\n", BadgeSuccess.Render(" OK "))
	fmt.Fprintf(&b, "  => %s Storage locks and journals cleanly purged\n\n", BadgeSuccess.Render(" OK "))

	fmt.Fprintf(&b, "%s\n", StyleTitle.Render("ACTIONS:"))
	fmt.Fprintf(&b, "  %s  Return to Enterprise Hub Menu\n\n", StyleMuted.Render("[ENTER/ESC]"))

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
