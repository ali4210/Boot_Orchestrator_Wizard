package ui

import (
	"fmt"
	"strings"

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
		fmt.Fprintf(&b, "=> %s", StyleSubTitle.Render("Press ENTER, 0, or ESC to return to the Enterprise Hub."))

		return lipgloss.JoinVertical(
			lipgloss.Left,
			header,
			StylePanel.Render(b.String()),
			StyleFooter.Render(" [COMMANDS] ENTER/0/ESC: Return to Main Menu  |  Q: Quit"),
		)
	}

	var body string
	switch m.screen {
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
	case ScreenResumeAlert:
		if m.unfinishedState != nil {
			hint = "Y/ENTER: Resume Session  |  N/0/ESC: Purge & Reset  |  Q: Quit"
		} else {
			hint = "ENTER/0/ESC: Return to Hub Menu  |  Q: Quit"
		}
	case ScreenHub:
		hint = "UP/DOWN: Navigate  |  1-6: Direct Jump  |  ENTER: Select  |  Q: Quit"
	case ScreenFolderSelect:
		hint = "UP/DOWN: Browse Folders  |  ENTER: Open  |  0/ESC: Back  |  Q: Quit"
	case ScreenOSSelect:
		hint = "UP/DOWN: Browse Releases  |  /: Filter  |  ENTER: Pick  |  0/ESC: Back  |  Q: Quit"
	case ScreenBackupPrompt:
		hint = "UP/DOWN: Select Drive  |  ENTER: Commit Backup  |  0/ESC: Back"
	case ScreenDisasterRecovery:
		hint = "UP/DOWN: Select Archive  |  R: Refresh Drives  |  ENTER: Restore  |  0/ESC: Back"
	case ScreenRestoreConfirm:
		hint = "Y/ENTER: Commit Bare-Metal Recovery  |  0/ESC: Abort to Menu"
	case ScreenEnvironmentCheck:
		hint = "ENTER/0/ESC: Return to Main Menu  |  Q: Quit"
	case ScreenConfirm:
		if m.provisionMode == "single-boot" {
			hint = "Type 'ERASE' & ENTER: Confirm Wipe  |  0/ESC: Cancel"
		} else {
			hint = "Y/ENTER: Commit Dual-Boot Transaction  |  N/0/ESC: Back  |  Q: Quit"
		}
	case ScreenRevertConfirm:
		hint = "Y/ENTER: Confirm Removal  |  N/0/ESC: Back to Hub  |  Q: Quit"
	case ScreenProgress:
		hint = "P: Pause & Save State  |  S: Stop & Rollback Everything"
	case ScreenDone:
		hint = "R: Reboot Into New OS Now  |  ENTER/0/ESC: Return to Hub Menu  |  Q: Quit"
	case ScreenRevertDone:
		hint = "ENTER/0/ESC: Return to Hub Menu  |  Q: Quit"
	case ScreenError:
		hint = "ENTER/0/ESC: Return to Main Menu  |  Q: Quit"
	default:
		hint = "0 / ESC: Back  |  Q: Quit"
	}
	return " [COMMANDS] " + hint
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
		fmt.Fprintf(&b, "  %s  Purge temporary files, release locks & discard session\n", StyleDanger.Render("[N/0/Esc]"))
	} else {
		fmt.Fprintf(&b, "%s\n\n", BadgeSuccess.Render(" SYSTEM JOURNAL CLEAN "))
		fmt.Fprintf(&b, "No interrupted or paused installation sessions were detected on this machine.\n\n")
		fmt.Fprintf(&b, "  => Journal Path:    %s\n", StyleMuted.Render("orchestrator_journal.json"))
		fmt.Fprintf(&b, "  => Status:          %s\n", StyleSubTitle.Render("Idle (Zero In-Flight Operations)"))
		fmt.Fprintf(&b, "  => Partitions:      %s\n\n", StyleMuted.Render("No locks or half-written allocations active"))
		fmt.Fprintf(&b, "%s", StyleSubTitle.Render("=> Press ENTER, 0, or ESC to return to the Main Menu."))
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
		{"1", "Deploy New OS (Dual-Boot Isolation)", "Carves headroom or uses unallocated disk sectors alongside current OS."},
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
	fmt.Fprintf(&b, "Single-Boot mode replaces your host operating system and disk partitions.\n")
	fmt.Fprintf(&b, "Select an external drive/USB to stream a compressed .tar.gz snapshot before wiping:\n\n")

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
	if m.backupCursor == len(m.backupTargets) {
		skipCursor = "=>"
		skipStyle = StyleDanger
	}
	fmt.Fprintf(&b, "%s [X] %s\n\n", skipCursor, skipStyle.Render("Skip Backup & Proceed to Destructive Wipe (Irreversible)"))

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
		fmt.Fprintf(&b, "  => Press %s to return to Hub Menu.\n", StyleMuted.Render("[ESC/0]"))
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
		fmt.Fprintf(&b, "=> Press %s to begin restoration, or %s to cancel.", StyleSubTitle.Render("[ENTER]"), StyleMuted.Render("[ESC/0]"))
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

	fmt.Fprintf(&b, "%s", StyleDanger.Render("=> Press Y or ENTER to begin bare-metal restore, or 0/ESC to cancel."))
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

	modeBadge := BadgeInfo.Render(" DUAL-BOOT (SIDE-BY-SIDE) ")
	if m.provisionMode == "single-boot" {
		modeBadge = BadgeDanger.Render(" SINGLE-BOOT (DESTRUCTIVE REPLACEMENT) ")
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", StyleTitle.Render("TRANSACTION CONFIRMATION & WRITE ASSERTION"))
	fmt.Fprintf(&b, "  => Target OS:        %s %s\n", StyleSubTitle.Render(e.Distro), e.Version)
	fmt.Fprintf(&b, "  => Execution Mode:   %s\n", modeBadge)
	fmt.Fprintf(&b, "  => Profile:          %s\n", flavorBadge)
	fmt.Fprintf(&b, "  => Architecture:     %s\n", e.Arch)
	fmt.Fprintf(&b, "  => Required Space:   %d GB\n", e.MinDiskGB)

	if m.provisionMode == "single-boot" {
		fmt.Fprintf(&b, "\n  %s\n", BadgeDanger.Render(" CRITICAL DATA LOSS WARNING "))
		fmt.Fprintf(&b, "  %s\n", StyleDanger.Render("All partitions, files, and operating systems on this drive will be wiped."))
		fmt.Fprintf(&b, "  %s\n\n", StyleMuted.Render("The payload will be verified in scratch space first. To proceed, confirm below:"))
		fmt.Fprintf(&b, "  Type %s to commit disk wipe: %s\n\n", StyleDanger.Render("ERASE"), m.eraseConfirmInput.View())
		fmt.Fprintf(&b, "%s", StyleMuted.Render("=> Press ENTER to commit or ESC/0 to abort safely."))
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
	fmt.Fprintf(&b, "%s", StyleDanger.Render("=> Press Y to confirm removal, or 0/ESC to return to Hub."))
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
	fmt.Fprintf(&b, "  => %s Persistent root filesystem mapped in /etc/fstab\n", BadgeSuccess.Render(" OK "))
	fmt.Fprintf(&b, "  => %s Host GRUB bootloader stanza generated & verified\n", BadgeSuccess.Render(" OK "))
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

	fmt.Fprintf(&b, "%s\n", StyleTitle.Render("ACTIONS:"))
	fmt.Fprintf(&b, "  %s  Reboot now directly into the OS\n", StyleSubTitle.Render("[R]      "))
	fmt.Fprintf(&b, "  %s  Return to Enterprise Hub Menu\n\n", StyleMuted.Render("[ENTER/0]"))

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
	fmt.Fprintf(&b, "  %s  Return to Enterprise Hub Menu\n\n", StyleSubTitle.Render("[ENTER/0/ESC]"))

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
