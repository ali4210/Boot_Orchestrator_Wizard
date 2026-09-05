package ui

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"boot-orchestrator/discovery"
	"boot-orchestrator/engine"
	"boot-orchestrator/hypervisor"
	"boot-orchestrator/safety"
)

type Screen int

const (
	ScreenHub Screen = iota
	ScreenResumeAlert
	ScreenFolderSelect
	ScreenOSSelect
	ScreenEnvironmentCheck
	ScreenBackupPrompt
	ScreenDisasterRecovery
	ScreenRestoreConfirm
	ScreenConfirm
	ScreenRevertConfirm
	ScreenProgress
	ScreenDone
	ScreenRevertDone
	ScreenError
)

type categoryItem struct {
	category discovery.Category
	count    int
}

func (c categoryItem) Title() string       { return "📁 " + string(c.category) }
func (c categoryItem) Description() string { return fmt.Sprintf("%d releases & editions available", c.count) }
func (c categoryItem) FilterValue() string { return string(c.category) }

type osListItem struct {
	entry discovery.Entry
}

func (i osListItem) Title() string {
	return i.entry.Distro + " " + i.entry.Version
}

func (i osListItem) Description() string {
	var tags []string
	tags = append(tags, strings.ToUpper(string(i.entry.Flavor)))
	tags = append(tags, string(i.entry.Arch))
	if i.entry.RequiresLicense {
		tags = append(tags, "KEY REQ")
	}
	if i.entry.EOL {
		tags = append(tags, "EOL")
	}
	return strings.ToUpper(string(i.entry.Family)) + " ── [" + strings.Join(tags, " • ") + "]"
}

func (i osListItem) FilterValue() string {
	return i.entry.Distro + " " + i.entry.Version + " " + i.entry.Codename
}

type Model struct {
	screen        Screen
	width         int
	height        int
	hubCursor     int
	provisionMode string

	hvInfo      hypervisor.Info
	fwInfo      safety.FirmwareInfo
	guardReport safety.GuardReport
	envErr      error
	envChecked  bool

	catalog           *discovery.Catalog
	folderList        list.Model
	osList            list.Model
	selectedCategory  discovery.Category
	selectedOS        *discovery.Entry
	searchInput       textinput.Model
	eraseConfirmInput textinput.Model

	backupTargets     []engine.BlockDevice
	backupCursor      int
	backupStatusMsg   string
	isBackingUp       bool

	discoveredBackups []engine.BackupArchiveDescriptor
	restoreCursor     int
	selectedArchive   *engine.BackupArchiveDescriptor
	restoreStatusMsg  string
	isScanning        bool
	scanSpinner       spinner.Model

	progressBar progress.Model
	speed       *SpeedTracker
	statusLog   []string
	stepUpdates chan provisionStepMsg

	cancelProvision context.CancelFunc
	unfinishedState *safety.SessionState
	journal         *safety.Journal

	fatalErr error
}

func NewModel() Model {
	cat, err := discovery.LoadEmbedded()

	delegate := list.NewDefaultDelegate()
	delegate.ShowDescription = true
	delegate.SetHeight(2)
	delegate.SetSpacing(1)

	delegate.Styles.SelectedTitle = StyleSelectedItem
	delegate.Styles.SelectedDesc = StyleSelectedItem.Copy().Foreground(ColorBgDark).Bold(false)
	delegate.Styles.NormalTitle = StyleTitle
	delegate.Styles.NormalDesc = StyleMuted

	fList := list.New(nil, delegate, 76, 16)
	fList.Title = "SELECT DISTRIBUTION CATEGORY"
	fList.Styles.Title = StyleSubTitle
	fList.SetShowHelp(false)
	fList.SetShowStatusBar(true)

	if err == nil && cat != nil {
		var fItems []list.Item
		for _, catName := range cat.ListCategories() {
			entries := cat.GetEntriesByCategory(catName)
			fItems = append(fItems, categoryItem{category: catName, count: len(entries)})
		}
		fList.SetItems(fItems)
	}

	oList := list.New(nil, delegate, 76, 16)
	oList.Title = "SELECT OS RELEASE & FLAVOR"
	oList.Styles.Title = StyleSubTitle
	oList.SetShowHelp(false)
	oList.SetShowStatusBar(true)
	oList.SetFilteringEnabled(true)

	ti := textinput.New()
	ti.Placeholder = "Type to filter catalog..."

	eraseTi := textinput.New()
	eraseTi.Placeholder = "Type ERASE to confirm wipe"
	eraseTi.CharLimit = 10
	eraseTi.Width = 32

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = BadgeInfo

	pb := progress.New(
		progress.WithGradient("#00F0FF", "#00E676"),
		progress.WithWidth(72),
	)

	var unfin *safety.SessionState
	var jrn *safety.Journal
	initialScreen := ScreenHub

	if j, jErr := safety.LoadJournal("orchestrator_journal.json"); jErr == nil {
		jrn = j
		if st, ok := j.GetUnfinishedSession(); ok {
			unfin = st
			initialScreen = ScreenResumeAlert
		}
	}

	return Model{
		screen:            initialScreen,
		hubCursor:         0,
		provisionMode:     "dual-boot",
		catalog:           cat,
		folderList:        fList,
		osList:            oList,
		searchInput:       ti,
		eraseConfirmInput: eraseTi,
		scanSpinner:       sp,
		progressBar:       pb,
		unfinishedState:   unfin,
		journal:           jrn,
		fatalErr:          err,
	}
}

func (m Model) Init() tea.Cmd {
	return m.scanSpinner.Tick
}

type envCheckDoneMsg struct {
	hv       hypervisor.Info
	hvErr    error
	fw       safety.FirmwareInfo
	fwErr    error
	guard    safety.GuardReport
	guardErr error
}

type backupScanDoneMsg struct {
	targets []engine.BlockDevice
	err     error
}

type backupFinishedMsg struct {
	archivePath string
	err         error
}

type disasterScanDoneMsg struct {
	backups []engine.BackupArchiveDescriptor
	err     error
}

type restoreFinishedMsg struct {
	err error
}

type tickMsg time.Time

func runEnvironmentChecks(targetPath string, footprintBytes uint64) tea.Cmd {
	return func() tea.Msg {
		hv, hvErr := hypervisor.Detect()
		fw, fwErr := safety.DetectFirmware()
		guard, guardErr := safety.CheckGuards(targetPath, footprintBytes)
		return envCheckDoneMsg{
			hv: hv, hvErr: hvErr,
			fw: fw, fwErr: fwErr,
			guard: guard, guardErr: guardErr,
		}
	}
}

func scanBackupDrivesCmd() tea.Cmd {
	return func() tea.Msg {
		time.Sleep(500 * time.Millisecond)
		targets, err := engine.DetectBackupTargets()
		return backupScanDoneMsg{targets: targets, err: err}
	}
}

func triggerLiveBackupCmd(targetPath string, ch chan provisionStepMsg) tea.Cmd {
	return func() tea.Msg {
		archivePath, err := engine.CreateHostBackupWithProgress(targetPath, func(written int64, step string) {
			ch <- provisionStepMsg{
				StepName: step,
				Done: false,
			}
		})
		return backupFinishedMsg{archivePath: archivePath, err: err}
	}
}

func scanDisasterBackupsCmd() tea.Cmd {
	return func() tea.Msg {
		time.Sleep(600 * time.Millisecond)
		bkups, err := engine.FindBackupArchives()
		return disasterScanDoneMsg{backups: bkups, err: err}
	}
}

func triggerRestoreCmd(archivePath, targetDisk string) tea.Cmd {
	return func() tea.Msg {
		err := engine.RestoreHostFromBackup(archivePath, targetDisk, nil)
		return restoreFinishedMsg{err: err}
	}
}

func tickCmd() tea.Cmd {
	return tea.Tick(time.Millisecond*250, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.scanSpinner, cmd = m.scanSpinner.Update(msg)
		return m, cmd

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		listWidth := m.width - 6
		if listWidth > 76 {
			listWidth = 76
		}
		listHeight := m.height - 8
		if listHeight < 12 {
			listHeight = 12
		}
		m.folderList.SetSize(listWidth, listHeight)
		m.osList.SetSize(listWidth, listHeight)
		m.progressBar.Width = listWidth
		return m, nil

	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		return m.updateForScreen(msg)

	case backupScanDoneMsg:
		m.isScanning = false
		m.backupTargets = msg.targets
		return m, nil

	case backupFinishedMsg:
		m.isBackingUp = false
		if msg.err != nil {
			m.fatalErr = msg.err
			m.screen = ScreenError
			return m, nil
		}
		m.statusLog = append(m.statusLog, "=> [BACKUP VERIFIED] "+msg.archivePath)
		m.screen = ScreenConfirm
		m.eraseConfirmInput.Reset()
		m.eraseConfirmInput.Focus()
		return m, nil

	case disasterScanDoneMsg:
		m.isScanning = false
		m.discoveredBackups = msg.backups
		return m, nil

	case restoreFinishedMsg:
		if msg.err != nil {
			m.fatalErr = msg.err
			m.screen = ScreenError
			return m, nil
		}
		m.screen = ScreenRevertDone
		return m, nil

	case envCheckDoneMsg:
		m.hvInfo = msg.hv
		m.fwInfo = msg.fw
		m.guardReport = msg.guard
		m.envChecked = true
		if msg.hvErr != nil {
			m.envErr = msg.hvErr
		} else if msg.fwErr != nil {
			m.envErr = msg.fwErr
		} else if msg.guardErr != nil {
			m.envErr = msg.guardErr
		}
		return m, nil

	case revertDoneMsg:
		if msg.err != nil {
			m.screen = ScreenError
			m.fatalErr = msg.err
			return m, nil
		}
		m.screen = ScreenRevertDone
		return m, nil

	case tickMsg:
		if m.screen == ScreenProgress {
			return m, tickCmd()
		}
		return m, nil

	case provisionStepMsg:
		return m.handleProvisionStep(msg, m.stepUpdates)

	case provisionDoneMsg:
		return m.handleProvisionDone(msg)
	}

	return m, nil
}

func (m Model) updateForScreen(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	keyStr := strings.ToLower(msg.String())

	if m.fatalErr != nil {
		if keyStr == "enter" || keyStr == "esc" || keyStr == "0" {
			m.fatalErr = nil
			m.screen = ScreenHub
			return m, nil
		}
		if keyStr == "q" {
			return m, tea.Quit
		}
		return m, nil
	}

	switch m.screen {

	case ScreenResumeAlert:
		switch keyStr {
		case "y", "enter":
			if m.unfinishedState != nil {
				m.screen = ScreenProgress
				m.speed = NewSpeedTracker(uint64(m.unfinishedState.TotalBytes))
				return m, m.startProvisioning(true)
			}
			m.screen = ScreenHub
			return m, nil
		case "n", "esc", "0":
			if m.journal != nil {
				_ = m.journal.PurgeForce()
			}
			m.unfinishedState = nil
			m.screen = ScreenHub
			return m, nil
		case "q":
			return m, tea.Quit
		}

	case ScreenHub:
		switch keyStr {
		case "q":
			return m, tea.Quit
		case "up", "k":
			if m.hubCursor > 0 {
				m.hubCursor--
			}
		case "down", "j":
			if m.hubCursor < 5 {
				m.hubCursor++
			}
		case "1":
			m.provisionMode = "dual-boot"
			m.screen = ScreenFolderSelect
		case "2":
			m.provisionMode = "single-boot"
			m.screen = ScreenFolderSelect
		case "3":
			m.screen = ScreenRevertConfirm
		case "4":
			if m.journal == nil {
				m.journal, _ = safety.LoadJournal("orchestrator_journal.json")
			}
			if m.journal != nil {
				if st, ok := m.journal.GetUnfinishedSession(); ok {
					m.unfinishedState = st
				} else {
					m.unfinishedState = nil
				}
			} else {
				m.unfinishedState = nil
			}
			m.screen = ScreenResumeAlert
			return m, nil
		case "5":
			m.screen = ScreenEnvironmentCheck
			return m, runEnvironmentChecks(".", 5*1024*1024*1024)
		case "6":
			m.screen = ScreenDisasterRecovery
			m.restoreCursor = 0
			m.isScanning = true
			return m, tea.Batch(scanDisasterBackupsCmd(), m.scanSpinner.Tick)
		case "enter":
			switch m.hubCursor {
			case 0:
				m.provisionMode = "dual-boot"
				m.screen = ScreenFolderSelect
			case 1:
				m.provisionMode = "single-boot"
				m.screen = ScreenFolderSelect
			case 2:
				m.screen = ScreenRevertConfirm
			case 3:
				if m.journal == nil {
					m.journal, _ = safety.LoadJournal("orchestrator_journal.json")
				}
				if m.journal != nil {
					if st, ok := m.journal.GetUnfinishedSession(); ok {
						m.unfinishedState = st
					} else {
						m.unfinishedState = nil
					}
				} else {
					m.unfinishedState = nil
				}
				m.screen = ScreenResumeAlert
				return m, nil
			case 4:
				m.screen = ScreenEnvironmentCheck
				return m, runEnvironmentChecks(".", 5*1024*1024*1024)
			case 5:
				m.screen = ScreenDisasterRecovery
				m.restoreCursor = 0
				m.isScanning = true
				return m, tea.Batch(scanDisasterBackupsCmd(), m.scanSpinner.Tick)
			}
		}

	case ScreenFolderSelect:
		if keyStr == "q" {
			return m, tea.Quit
		}
		if (keyStr == "0" || keyStr == "esc") && m.folderList.FilterState() != list.Filtering {
			m.screen = ScreenHub
			return m, nil
		}

		if keyStr == "enter" {
			if it, ok := m.folderList.SelectedItem().(categoryItem); ok {
				m.selectedCategory = it.category
				entries := m.catalog.GetEntriesByCategory(it.category)
				var items []list.Item
				for _, e := range entries {
					items = append(items, osListItem{entry: e})
				}
				m.osList.SetItems(items)
				m.screen = ScreenOSSelect
				return m, nil
			}
		}

		var cmd tea.Cmd
		m.folderList, cmd = m.folderList.Update(msg)
		return m, cmd

	case ScreenOSSelect:
		if keyStr == "q" && m.osList.FilterState() != list.Filtering {
			return m, tea.Quit
		}
		if (keyStr == "0" || keyStr == "esc") && m.osList.FilterState() != list.Filtering {
			m.screen = ScreenFolderSelect
			return m, nil
		}

		if keyStr == "enter" {
			if it, ok := m.osList.SelectedItem().(osListItem); ok {
				e := it.entry
				m.selectedOS = &e

				if m.provisionMode == "single-boot" {
					m.screen = ScreenBackupPrompt
					m.backupCursor = 0
					m.backupStatusMsg = ""
					m.isScanning = true
					return m, tea.Batch(scanBackupDrivesCmd(), m.scanSpinner.Tick)
				}

				m.screen = ScreenConfirm
				return m, nil
			}
		}

		var cmd tea.Cmd
		m.osList, cmd = m.osList.Update(msg)
		return m, cmd

	case ScreenBackupPrompt:
		switch keyStr {
		case "esc", "0":
			m.screen = ScreenOSSelect
			return m, nil
		case "r":
			m.isScanning = true
			return m, tea.Batch(scanBackupDrivesCmd(), m.scanSpinner.Tick)
		case "up", "k":
			if m.backupCursor > 0 {
				m.backupCursor--
			}
		case "down", "j":
			if m.backupCursor < len(m.backupTargets) {
				m.backupCursor++
			}
		case "enter":
			if m.backupCursor == len(m.backupTargets) {
				m.screen = ScreenConfirm
				m.eraseConfirmInput.Reset()
				m.eraseConfirmInput.Focus()
				return m, nil
			}

			if len(m.backupTargets) > 0 && m.backupCursor < len(m.backupTargets) {
				tgt := m.backupTargets[m.backupCursor]
				targetPath := tgt.Mountpoint
				if targetPath == "" {
					targetPath = tgt.Name
				}

				m.screen = ScreenProgress
				m.isBackingUp = true
				approxBytes := engine.EstimateHostBackupSize()
				m.speed = NewSpeedTracker(approxBytes)
				m.statusLog = append(m.statusLog, fmt.Sprintf("=> Initiating live snapshot stream to %s...", targetPath))

				m.stepUpdates = make(chan provisionStepMsg, 64)
				return m, tea.Batch(
					triggerLiveBackupCmd(targetPath, m.stepUpdates),
					listenForStepUpdates(m.stepUpdates),
					tickCmd(),
				)
			}
		}

	case ScreenDisasterRecovery:
		switch keyStr {
		case "esc", "0":
			m.screen = ScreenHub
			return m, nil
		case "r":
			m.isScanning = true
			return m, tea.Batch(scanDisasterBackupsCmd(), m.scanSpinner.Tick)
		case "up", "k":
			if m.restoreCursor > 0 {
				m.restoreCursor--
			}
		case "down", "j":
			if m.restoreCursor < len(m.discoveredBackups)-1 {
				m.restoreCursor++
			}
		case "enter":
			if len(m.discoveredBackups) > 0 {
				b := m.discoveredBackups[m.restoreCursor]
				m.selectedArchive = &b
				m.screen = ScreenRestoreConfirm
			}
		}

	case ScreenRestoreConfirm:
		switch keyStr {
		case "y", "enter":
			pDisk, _, _, _, _ := engine.DetectActiveRootDisk()
			if pDisk == "" {
				pDisk = "/dev/sda"
			}
			m.screen = ScreenProgress
			m.statusLog = append(m.statusLog, "=> [DISASTER RESTORE] Rebuilding partition table and restoring host OS...")
			return m, triggerRestoreCmd(m.selectedArchive.FilePath, pDisk)
		case "n", "esc", "0":
			m.screen = ScreenDisasterRecovery
		}

	case ScreenEnvironmentCheck:
		if keyStr == "q" {
			return m, tea.Quit
		}
		if keyStr == "enter" || keyStr == "esc" || keyStr == "0" {
			m.screen = ScreenHub
		}

	case ScreenConfirm:
		if m.provisionMode == "single-boot" {
			switch keyStr {
			case "esc", "0":
				m.screen = ScreenOSSelect
				return m, nil
			case "enter":
				val := strings.ToUpper(strings.TrimSpace(m.eraseConfirmInput.Value()))
				if val == "ERASE" {
					m.screen = ScreenProgress
					m.speed = NewSpeedTracker(m.selectedOS.ApproxSizeBytes)
					return m, m.startProvisioning(false)
				}
				m.eraseConfirmInput.SetValue("")
				m.eraseConfirmInput.Placeholder = "MUST TYPE 'ERASE' TO PROCEED"
				return m, nil
			default:
				var cmd tea.Cmd
				m.eraseConfirmInput, cmd = m.eraseConfirmInput.Update(msg)
				return m, cmd
			}
		} else {
			switch keyStr {
			case "y", "enter":
				m.screen = ScreenProgress
				m.speed = NewSpeedTracker(m.selectedOS.ApproxSizeBytes)
				return m, m.startProvisioning(false)
			case "n", "esc", "0":
				m.screen = ScreenOSSelect
			case "q":
				return m, tea.Quit
			}
		}

	case ScreenRevertConfirm:
		switch keyStr {
		case "y", "enter":
			pDisk, _, hostNum, targetPart, _ := engine.DetectActiveRootDisk()
			if pDisk == "" {
				pDisk = "/dev/sda"
				hostNum = "1"
				targetPart = "/dev/sda2"
			}

			targetNum := strings.TrimPrefix(targetPart, pDisk)
			targetNum = strings.TrimPrefix(targetNum, "p")

			efiToPurge := "parrot"
			if m.selectedOS != nil {
				efiToPurge = strings.ToLower(m.selectedOS.Distro)
			} else if m.unfinishedState != nil && m.unfinishedState.DistroName != "" {
				efiToPurge = strings.ToLower(m.unfinishedState.DistroName)
			}

			return m, runRealRevert(pDisk, targetNum, hostNum, efiToPurge, "")
		case "n", "esc", "0":
			m.screen = ScreenHub
		case "q":
			return m, tea.Quit
		}

	case ScreenProgress:
		switch keyStr {
		case "p":
			if m.cancelProvision != nil {
				m.cancelProvision()
			}
			m.statusLog = append(m.statusLog, "=> [PAUSED] Signaled engine to pause. Preserving byte offset...")
		case "s":
			if m.cancelProvision != nil {
				m.cancelProvision()
			}
			m.statusLog = append(m.statusLog, "=> [ABORT] Triggered state rollback. Cleaning allocations...")
			m.screen = ScreenHub
		}

	case ScreenDone:
		switch keyStr {
		case "r":
			_ = exec.Command("sync").Run()
			_ = exec.Command("reboot").Run()
			return m, tea.Quit
		case "enter", "esc", "0":
			m.fatalErr = nil
			m.screen = ScreenHub
			return m, nil
		case "q":
			return m, tea.Quit
		}

	case ScreenRevertDone:
		switch keyStr {
		case "enter", "esc", "0":
			m.fatalErr = nil
			m.screen = ScreenHub
			return m, nil
		case "q":
			return m, tea.Quit
		}

	case ScreenError:
		if keyStr == "q" {
			return m, tea.Quit
		}
		if keyStr == "enter" || keyStr == "esc" || keyStr == "0" {
			m.fatalErr = nil
			m.screen = ScreenHub
			return m, nil
		}
	}

	return m, nil
}

func (m *Model) startProvisioning(isResume bool) tea.Cmd {
	ctx, cancel := context.WithCancel(context.Background())
	m.cancelProvision = cancel

	pDisk, _, hostNum, targetPart, _ := engine.DetectActiveRootDisk()
	if pDisk == "" {
		pDisk = "/dev/sda"
		hostNum = "1"
		targetPart = "/dev/sda2"
	}

	var mirrors []string
	var sha, distroName string
	var size uint64
	var selectedEntry *discovery.Entry

	if isResume && m.unfinishedState != nil {
		mirrors = []string{m.unfinishedState.DownloadURL}
		distroName = m.unfinishedState.DistroName
		size = uint64(m.unfinishedState.TotalBytes)
		if m.catalog != nil {
			for _, e := range m.catalog.Entries {
				if strings.EqualFold(e.Distro, distroName) {
					entryCopy := e
					selectedEntry = &entryCopy
					sha = e.SHA256
					break
				}
			}
		}
	} else if m.selectedOS != nil {
		selectedEntry = m.selectedOS
		mirrors = m.selectedOS.GetMirrors()
		sha = m.selectedOS.SHA256
		distroName = m.selectedOS.Distro
		size = m.selectedOS.ApproxSizeBytes
	}

	primaryURL := ""
	if len(mirrors) > 0 {
		primaryURL = mirrors[0]
	}

	isWindows := false
	if selectedEntry != nil {
		isWindows = strings.EqualFold(string(selectedEntry.Family), "windows")
	}

	req := engine.ProvisionRequest{
		JournalPath:      "orchestrator_journal.json",
		ImageURL:         primaryURL,
		ImageSHA256:      sha,
		DownloadDestPath: "/tmp/os_image.payload",
		TargetDiskPath:   pDisk,
		TargetPartition:  targetPart,
		HostPartNum:      hostNum,
		ESPPartNum:       "1",
		IsWindowsImage:   isWindows,
		BootLabel:        strings.ToLower(distroName),
		HypervisorKind:   m.hvInfo.Kind,
		SkipCompaction:   false,
		SelectedEntry:    selectedEntry,
		ProvisionMode:    m.provisionMode,
	}

	if m.journal == nil {
		m.journal, _ = safety.NewJournal("orchestrator_journal.json", fmt.Sprintf("txn-%d", time.Now().UnixNano()))
	}
	_ = m.journal.SetSessionState(safety.SessionState{
		TargetOSID:    distroName,
		DistroName:    distroName,
		ProvisionMode: m.provisionMode,
		TargetDisk:    pDisk,
		DownloadURL:   primaryURL,
		TotalBytes:    int64(size),
		IsPaused:      false,
	})

	cmd, ch := runProvisionWithCancel(ctx, req, "/etc/default/grub")
	m.stepUpdates = ch

	return tea.Batch(cmd, listenForStepUpdates(ch), tickCmd())
}
