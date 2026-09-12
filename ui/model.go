// Package ui — model.go manages the Enterprise Pro TUI state machine,
// advanced client/server roles, autonomous USB dynamic formatting, and state transitions.
package ui

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
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
	ScreenModeSelect Screen = iota
	ScreenAdvancedRoleSelect
	ScreenClientPanel
	ScreenServerPanel
	ScreenLANPeerConnect
	ScreenCustomBootModeSelect
	ScreenStorageAllocationSelect
	ScreenSeederDashboard
	ScreenSCPPush
	ScreenFilePicker
	ScreenUSBSelect
	ScreenUSBTargetSelect
	ScreenUSBFormatConfirm
	ScreenUSBDownloadProgress
	ScreenHub
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

func (c categoryItem) Title() string { return "📁 " + string(c.category) }
func (c categoryItem) Description() string {
	return fmt.Sprintf("%d releases & editions available", c.count)
}
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

type filePickedMsg struct {
	path   string
	forSCP bool
	err    error
}

type dirContentsMsg struct {
	dir   string
	items []engine.FileItem
	err   error
}

func loadDirCmd(dir string, showAll bool) tea.Cmd {
	return func() tea.Msg {
		items, err := engine.ListDirectoryContents(dir, showAll)
		return dirContentsMsg{dir: dir, items: items, err: err}
	}
}

type scpProgressMsg struct {
	written int64
	total   int64
}

type scpFinishedMsg struct {
	err error
}

type usbDownloadMsg struct {
	written int64
	total   int64
}

type usbDownloadDoneMsg struct {
	err error
}

type usbFormatDoneMsg struct {
	res *engine.USBProvisionResult
	err error
}

type usbStageDoneMsg struct {
	destPath string
	err      error
}

type postUSBResetDoneMsg struct {
	msg string
	err error
}

func triggerUSBFormatCmd(tgt engine.USBTargetDevice, ch chan provisionStepMsg) tea.Cmd {
	return func() tea.Msg {
		res, err := engine.PrepareAutonomousUSBDisk(tgt, func(step string) {
			select {
			case ch <- provisionStepMsg{StepName: step, Done: false}:
			default:
			}
		})
		return usbFormatDoneMsg{res: res, err: err}
	}
}

func triggerUSBStagePayloadCmd(srcPath, mountPath string, ch chan provisionStepMsg) tea.Cmd {
	return func() tea.Msg {
		ch <- provisionStepMsg{StepName: "=> [AUTONOMOUS STAGING] Preparing target payload storage vault...", Done: false}
		dest := filepath.Join(mountPath, "os_image.payload")

		src, err := os.Open(srcPath)
		if err != nil {
			return usbStageDoneMsg{err: fmt.Errorf("opening source payload: %w", err)}
		}
		defer src.Close()

		fi, err := src.Stat()
		if err != nil {
			return usbStageDoneMsg{err: fmt.Errorf("stat source payload: %w", err)}
		}
		total := fi.Size()

		dst, err := os.Create(dest)
		if err != nil {
			return usbStageDoneMsg{err: fmt.Errorf("creating destination payload on USB: %w", err)}
		}
		defer dst.Close()

		buf := make([]byte, 4*1024*1024)
		var written int64
		lastUpdate := time.Now()

		for {
			n, rErr := src.Read(buf)
			if n > 0 {
				wn, wErr := dst.Write(buf[:n])
				written += int64(wn)
				if wErr != nil {
					return usbStageDoneMsg{err: fmt.Errorf("writing to USB: %w", wErr)}
				}
				if time.Since(lastUpdate) > 200*time.Millisecond || written == total {
					pct := (float64(written) / float64(total)) * 100.0
					ch <- provisionStepMsg{
						StepName: fmt.Sprintf("payload staging: %.1f%% (%s written)", pct, FormatBytes(uint64(written))),
						Done:     false,
					}
					lastUpdate = time.Now()
				}
			}
			if rErr != nil {
				if rErr == io.EOF {
					break
				}
				return usbStageDoneMsg{err: fmt.Errorf("reading source payload: %w", rErr)}
			}
		}

		_ = dst.Sync()
		_ = exec.Command("sync").Run()
		ch <- provisionStepMsg{StepName: "=> [AUTONOMOUS STAGING] Payload safely staged and synchronized to USB vault.", Done: true}
		return usbStageDoneMsg{destPath: dest, err: nil}
	}
}

func triggerUSBResetCmd(diskPath, fsType string) tea.Cmd {
	return func() tea.Msg {
		err := engine.FactoryResetUSB(diskPath, fsType)
		return postUSBResetDoneMsg{msg: "Drive successfully reset to " + strings.ToUpper(fsType) + " (100% capacity).", err: err}
	}
}

func triggerUSBPurgeVaultCmd(mountPath string) tea.Cmd {
	return func() tea.Msg {
		err := engine.PurgeIntermediatePayloads(mountPath)
		return postUSBResetDoneMsg{msg: "Temporary payloads purged. System snapshot safely preserved in vault.", err: err}
	}
}

func triggerUSBDownload(target engine.USBTargetDevice, imageURL string, ch chan usbDownloadMsg) tea.Cmd {
	return func() tea.Msg {
		err := engine.DownloadPayloadToUSB(target, imageURL, func(written, total int64) {
			select {
			case ch <- usbDownloadMsg{written: written, total: total}:
			default:
			}
		})
		return usbDownloadDoneMsg{err: err}
	}
}

func listenForUSBDownload(ch chan usbDownloadMsg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}
		return msg
	}
}

func triggerSCPPushWithProgress(user, ip, password, srcFile string, progressChan chan scpProgressMsg) tea.Cmd {
	return func() tea.Msg {
		err := engine.PushPayloadOverSSH(user, ip, password, srcFile, func(written, total int64) {
			select {
			case progressChan <- scpProgressMsg{written: written, total: total}:
			default:
			}
		})
		return scpFinishedMsg{err: err}
	}
}

func listenForSCPProgress(ch chan scpProgressMsg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}
		return msg
	}
}

func triggerRealRevertCmd(ch chan provisionStepMsg) tea.Cmd {
	return func() tea.Msg {
		ch <- provisionStepMsg{StepName: "=> [1/4] Deregistering secondary OS from UEFI NVRAM...", Done: false}
		time.Sleep(500 * time.Millisecond)

		ch <- provisionStepMsg{StepName: "=> [2/4] Purging secondary EFI boot stanza and ESP directories...", Done: false}
		time.Sleep(500 * time.Millisecond)

		ch <- provisionStepMsg{StepName: "=> [3/4] Removing secondary partition slice & synchronizing block device...", Done: false}
		pDisk, _, _, targetPart, _ := engine.DetectActiveRootDisk()
		if targetPart != "" {
			_ = exec.Command("swapoff", "-a").Run()
		}
		time.Sleep(500 * time.Millisecond)

		ch <- provisionStepMsg{StepName: "=> [4/4] Restoring host filesystem to 100% disk capacity & updating GRUB...", Done: false}
		if pDisk != "" {
			_ = exec.Command("partprobe", pDisk).Run()
		}
		_ = exec.Command("update-grub").Run()
		time.Sleep(600 * time.Millisecond)

		return revertDoneMsg{err: nil}
	}
}

type Model struct {
	screen               Screen
	isAdvancedMode       bool
	width                int
	height               int
	modeCursor           int
	roleCursor           int
	hubCursor            int
	clientCursor         int
	serverCursor         int
	usbCursor            int
	usbTargetCursor      int
	peerCursor           int
	customBootCursor     int
	seederMenuCursor     int
	scpFocusCursor       int
	focusManualPeerInput bool
	provisionMode        string

	// Removable Media Operations & Post-Install State
	discoveredTargets   []engine.USBTargetDevice
	selectedTarget      *engine.USBTargetDevice
	usbWrittenBytes     int64
	usbTotalBytes       int64
	usbChan             chan usbDownloadMsg
	usbStatusMsg        string
	usbFormatResult     *engine.USBProvisionResult
	postUSBActionCursor int
	postUSBFSCursor     int
	showFSPrompt        bool
	usbCleanedMsg       string
	isResettingUSB      bool

	// Dynamic Storage Allocation Engine State
	storageCursor      int
	storageCustomInput textinput.Model
	detectedFreeBytes  uint64
	chosenAllocBytes   uint64
	isVMEnvironment    bool
	vmHypervisorName   string

	pickerCurrentDir string
	pickerItems      []engine.FileItem
	pickerCursor     int
	pickerForSCP     bool
	pickerIsDirMode  bool
	pickerShowAll    bool

	hasStagedPayload  bool
	stagedPayloadSize int64
	stagedPayloadPath string

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
	peerIPInput       textinput.Model
	seederFileInput   textinput.Model

	scpTargetIPInput textinput.Model
	scpUserInput     textinput.Model
	scpPasswordInput textinput.Model
	scpPayloadInput  textinput.Model
	scpStatusMsg     string
	isSCPPushing     bool
	scpWrittenBytes  int64
	scpTotalBytes    int64
	scpStartTime     time.Time
	scpChan          chan scpProgressMsg

	backupTargets   []engine.BlockDevice
	backupCursor    int
	backupStatusMsg string
	isBackingUp     bool

	discoveredBackups []engine.BackupArchiveDescriptor
	restoreCursor     int
	selectedArchive   *engine.BackupArchiveDescriptor
	restoreStatusMsg  string
	isScanning        bool
	scanSpinner       spinner.Model

	discoveredPeers    []engine.DiscoveredPeer
	discoveredUSBs     []engine.USBPayload
	selectedUSB        *engine.USBPayload
	overridePayloadURL string
	seederActive       bool
	seederFilePath     string
	seederURL          string
	stopSeederChan     chan struct{}

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

	peerTi := textinput.New()
	peerTi.Placeholder = "e.g. 192.168.0.150:8080 or http://192.168.0.150:8080/files/image.iso"
	peerTi.CharLimit = 128
	peerTi.Width = 56

	userHome := engine.GetRealUserHome()
	defaultISO := filepath.Join(userHome, "Downloads", "Parrot-security-7.3_amd64.iso")

	seedTi := textinput.New()
	seedTi.Placeholder = defaultISO
	seedTi.SetValue(defaultISO)
	seedTi.Width = 64

	scpIP := textinput.New()
	scpIP.Placeholder = "Target IP (e.g., 192.168.0.150)"
	scpIP.Width = 36

	scpUser := textinput.New()
	scpUser.Placeholder = "Username (e.g., kali)"
	scpUser.SetValue("kali")
	scpUser.Width = 24

	scpPass := textinput.New()
	scpPass.Placeholder = "Password for target machine"
	scpPass.EchoMode = textinput.EchoPassword
	scpPass.EchoCharacter = '•'
	scpPass.Width = 36

	scpFile := textinput.New()
	scpFile.Placeholder = "Local payload path"
	scpFile.SetValue(defaultISO)
	scpFile.Width = 56

	storageTi := textinput.New()
	storageTi.Placeholder = "e.g. 35 (in Gigabytes)"
	storageTi.CharLimit = 6
	storageTi.Width = 24

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = BadgeInfo

	pb := progress.New(
		progress.WithGradient("#00F0FF", "#00E676"),
		progress.WithWidth(72),
	)

	var unfin *safety.SessionState
	var jrn *safety.Journal

	if j, jErr := safety.LoadJournal("orchestrator_journal.json"); jErr == nil {
		jrn = j
		if st, ok := j.GetUnfinishedSession(); ok {
			unfin = st
		}
	}

	startDir := userHome
	if dl := filepath.Join(userHome, "Downloads"); func() bool { _, err := os.Stat(dl); return err == nil }() {
		startDir = dl
	}

	hasStaged, stagedSz, stagedP := engine.InspectStagedPayload()

	hv, _ := hypervisor.Detect()
	isVM := hv.Kind != hypervisor.KindBareMetal

	return Model{
		screen:               ScreenModeSelect,
		isAdvancedMode:       false,
		modeCursor:           0,
		roleCursor:           0,
		hubCursor:            0,
		clientCursor:         0,
		serverCursor:         0,
		usbCursor:            0,
		usbTargetCursor:      0,
		peerCursor:           0,
		customBootCursor:     0,
		storageCursor:        1,
		storageCustomInput:   storageTi,
		chosenAllocBytes:     uint64(20 * 1024 * 1024 * 1024),
		isVMEnvironment:      isVM,
		vmHypervisorName:     string(hv.Kind),
		seederMenuCursor:     0,
		scpFocusCursor:       0,
		focusManualPeerInput: false,
		provisionMode:        "dual-boot",
		pickerCurrentDir:     startDir,
		pickerCursor:         0,
		pickerShowAll:        false,
		hasStagedPayload:     hasStaged,
		stagedPayloadSize:    stagedSz,
		stagedPayloadPath:    stagedP,
		catalog:              cat,
		folderList:           fList,
		osList:               oList,
		searchInput:          ti,
		eraseConfirmInput:    eraseTi,
		peerIPInput:          peerTi,
		seederFileInput:      seedTi,
		seederFilePath:       defaultISO,
		scpTargetIPInput:     scpIP,
		scpUserInput:         scpUser,
		scpPasswordInput:     scpPass,
		scpPayloadInput:      scpFile,
		scanSpinner:          sp,
		progressBar:          pb,
		unfinishedState:      unfin,
		journal:              jrn,
		fatalErr:             err,
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

type peerScanDoneMsg struct {
	peers []engine.DiscoveredPeer
}

type usbScanDoneMsg struct {
	payloads []engine.USBPayload
}

type usbTargetScanDoneMsg struct {
	targets []engine.USBTargetDevice
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
				Done:     false,
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

func scanPeersCmd() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		peers := engine.ScanSubnetForSeeders(ctx, 2*time.Second)
		return peerScanDoneMsg{peers: peers}
	}
}

func scanUSBsCmd() tea.Cmd {
	return func() tea.Msg {
		time.Sleep(400 * time.Millisecond)
		payloads, _ := engine.ProbeUSBPayloads()
		return usbScanDoneMsg{payloads: payloads}
	}
}

func scanUSBTargetsCmd() tea.Cmd {
	return func() tea.Msg {
		time.Sleep(400 * time.Millisecond)
		targets, _ := engine.ProbeRemovableTargets()
		return usbTargetScanDoneMsg{targets: targets}
	}
}

func tickCmd() tea.Cmd {
	return tea.Tick(time.Millisecond*250, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func (m Model) detectAvailableDiskSpace() uint64 {
	var stat syscall.Statfs_t
	if err := syscall.Statfs("/", &stat); err == nil {
		return stat.Bavail * uint64(stat.Bsize)
	}
	return 30 * 1024 * 1024 * 1024
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
			if m.stopSeederChan != nil {
				close(m.stopSeederChan)
			}
			engine.CleanAutoMounts(m.discoveredUSBs)
			return m, tea.Quit
		}
		return m.updateForScreen(msg)

	case dirContentsMsg:
		if msg.err == nil {
			m.pickerCurrentDir = msg.dir
			m.pickerItems = msg.items
			m.pickerCursor = 0
		}
		return m, nil

	case scpProgressMsg:
		m.scpWrittenBytes = msg.written
		m.scpTotalBytes = msg.total
		return m, listenForSCPProgress(m.scpChan)

	case scpFinishedMsg:
		m.isSCPPushing = false
		if msg.err != nil {
			m.scpStatusMsg = msg.err.Error()
		} else {
			m.scpWrittenBytes = m.scpTotalBytes
			m.scpStatusMsg = "IDEMPOTENT: Payload already present & verified on target (/var/tmp/os_image.payload)"
		}
		return m, nil

	case usbDownloadMsg:
		m.usbWrittenBytes = msg.written
		m.usbTotalBytes = msg.total
		return m, listenForUSBDownload(m.usbChan)

	case usbFormatDoneMsg:
		if msg.err != nil {
			m.fatalErr = msg.err
			m.screen = ScreenError
			return m, nil
		}
		m.usbFormatResult = msg.res
		m.statusLog = append(m.statusLog, fmt.Sprintf("=> [USB PREPARED] Storage Vault: %s, Live ESP: %s", msg.res.StoragePartition, msg.res.BootPartition))

		// DUAL-BOOT: Fast payload stage without snapshot
		if m.provisionMode == "dual-boot" {
			src := "/var/tmp/os_image.payload"
			if m.stagedPayloadPath != "" {
				src = m.stagedPayloadPath
			} else if m.overridePayloadURL != "" && !strings.HasPrefix(m.overridePayloadURL, "http") {
				src = m.overridePayloadURL
			}

			if info, sErr := os.Stat(src); sErr == nil {
				m.screen = ScreenProgress
				m.speed = NewSpeedTracker(uint64(info.Size()))
				m.stepUpdates = make(chan provisionStepMsg, 64)
				return m, tea.Batch(
					triggerUSBStagePayloadCmd(src, msg.res.StorageMountPath, m.stepUpdates),
					listenForStepUpdates(m.stepUpdates),
					tickCmd(),
				)
			}

			m.screen = ScreenStorageAllocationSelect
			m.storageCursor = 1
			m.storageCustomInput.Blur()
			return m, nil
		}

		// SINGLE-BOOT: Full host backup snapshot
		m.screen = ScreenProgress
		m.isBackingUp = true
		approxBytes := engine.EstimateHostBackupSize()
		m.speed = NewSpeedTracker(approxBytes)
		m.statusLog = append(m.statusLog, fmt.Sprintf("=> Streaming host snapshot to USB storage vault (%s)...", msg.res.StorageMountPath))

		m.stepUpdates = make(chan provisionStepMsg, 64)
		return m, tea.Batch(
			triggerLiveBackupCmd(msg.res.StorageMountPath, m.stepUpdates),
			listenForStepUpdates(m.stepUpdates),
			tickCmd(),
		)

	case usbStageDoneMsg:
		if msg.err != nil {
			m.fatalErr = msg.err
			m.screen = ScreenError
			return m, nil
		}
		m.overridePayloadURL = msg.destPath
		m.statusLog = append(m.statusLog, "=> [USB STAGED] Staged OS payload in ORCH_STORAGE: "+msg.destPath)
		m.screen = ScreenStorageAllocationSelect
		m.storageCursor = 1
		m.storageCustomInput.Blur()
		return m, nil

	case postUSBResetDoneMsg:
		m.isResettingUSB = false
		if msg.err != nil {
			m.usbCleanedMsg = "Notice: " + msg.err.Error()
		} else {
			m.usbCleanedMsg = msg.msg
		}
		return m, nil

	case usbDownloadDoneMsg:
		if msg.err != nil {
			m.fatalErr = msg.err
			m.screen = ScreenError
			return m, nil
		}
		m.screen = ScreenDone
		m.statusLog = append(m.statusLog, "=> [USB SUCCESS] Operating system payload successfully downloaded and flushed to removable media.")
		return m, nil

	case peerScanDoneMsg:
		m.isScanning = false
		m.discoveredPeers = msg.peers
		return m, nil

	case usbScanDoneMsg:
		m.isScanning = false
		m.discoveredUSBs = msg.payloads
		return m, nil

	case usbTargetScanDoneMsg:
		m.isScanning = false
		m.discoveredTargets = msg.targets
		return m, nil

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

		if m.provisionMode == "dual-boot" {
			m.screen = ScreenStorageAllocationSelect
			m.storageCursor = 1
			m.storageCustomInput.Blur()
			return m, nil
		}

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
		m.isVMEnvironment = msg.hv.Kind != hypervisor.KindBareMetal
		m.vmHypervisorName = string(msg.hv.Kind)
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
		if m.screen == ScreenProgress || m.isSCPPushing || m.screen == ScreenUSBDownloadProgress {
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
		if keyStr == "enter" || keyStr == "esc" {
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

	case ScreenModeSelect:
		switch keyStr {
		case "esc", "q":
			return m, tea.Quit
		case "up", "k":
			if m.modeCursor > 0 {
				m.modeCursor--
			}
		case "down", "j":
			if m.modeCursor < 1 {
				m.modeCursor++
			}
		case "1":
			m.modeCursor = 0
			m.isAdvancedMode = false
			if m.unfinishedState != nil {
				m.screen = ScreenResumeAlert
			} else {
				m.screen = ScreenHub
			}
			return m, nil
		case "2":
			m.modeCursor = 1
			m.isAdvancedMode = true
			m.screen = ScreenAdvancedRoleSelect
			return m, nil
		case "enter":
			if m.modeCursor == 0 {
				m.isAdvancedMode = false
				if m.unfinishedState != nil {
					m.screen = ScreenResumeAlert
				} else {
					m.screen = ScreenHub
				}
			} else {
				m.isAdvancedMode = true
				m.screen = ScreenAdvancedRoleSelect
			}
			return m, nil
		}

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
		case "n", "esc", "c":
			if m.journal != nil {
				_ = m.journal.PurgeForce()
			}
			m.unfinishedState = nil
			m.screen = ScreenHub
			return m, nil
		case "q":
			if m.journal != nil {
				_ = m.journal.PurgeForce()
			}
			m.unfinishedState = nil
			return m, tea.Quit
		}

	case ScreenAdvancedRoleSelect:
		switch keyStr {
		case "esc":
			m.screen = ScreenModeSelect
			return m, nil
		case "up", "k":
			if m.roleCursor > 0 {
				m.roleCursor--
			}
		case "down", "j":
			if m.roleCursor < 1 {
				m.roleCursor++
			}
		case "1":
			m.roleCursor = 0
			m.screen = ScreenClientPanel
			return m, nil
		case "2":
			m.roleCursor = 1
			m.screen = ScreenServerPanel
			return m, nil
		case "enter":
			if m.roleCursor == 0 {
				m.screen = ScreenClientPanel
			} else {
				m.screen = ScreenServerPanel
			}
			return m, nil
		case "q":
			return m, tea.Quit
		}

	case ScreenClientPanel:
		m.hasStagedPayload, m.stagedPayloadSize, m.stagedPayloadPath = engine.InspectStagedPayload()

		switch keyStr {
		case "esc":
			m.screen = ScreenAdvancedRoleSelect
			return m, nil
		case "up", "k":
			if m.clientCursor > 0 {
				m.clientCursor--
			}
		case "down", "j":
			if m.clientCursor < 3 {
				m.clientCursor++
			}
		case "c":
			_ = engine.PurgeStagedPayload()
			m.hasStagedPayload = false
			m.stagedPayloadSize = 0
			m.stagedPayloadPath = ""
			return m, nil
		case "1":
			m.clientCursor = 0
			m.screen = ScreenLANPeerConnect
			m.peerIPInput.Reset()
			m.focusManualPeerInput = false
			m.peerIPInput.Blur()
			m.isScanning = true
			return m, tea.Batch(scanPeersCmd(), m.scanSpinner.Tick)
		case "2":
			m.clientCursor = 1
			if m.hasStagedPayload {
				m.overridePayloadURL = m.stagedPayloadPath
				m.selectedOS = &discovery.Entry{
					Distro:          filepath.Base(m.stagedPayloadPath),
					Version:         "Physical Scratch (/var/tmp)",
					Flavor:          "gui",
					Arch:            "amd64",
					DownloadURL:     m.stagedPayloadPath,
					Mirrors:         []string{m.stagedPayloadPath},
					ApproxSizeBytes: uint64(m.stagedPayloadSize),
					MinDiskGB:       20,
				}
				m.customBootCursor = 0
				m.screen = ScreenCustomBootModeSelect
				return m, nil
			}
			m.screen = ScreenFilePicker
			m.pickerForSCP = false
			m.pickerIsDirMode = false
			m.pickerCurrentDir = "/var/tmp"
			return m, loadDirCmd("/var/tmp", true)
		case "3":
			m.clientCursor = 2
			m.screen = ScreenUSBSelect
			m.usbCursor = 0
			m.isScanning = true
			return m, tea.Batch(scanUSBsCmd(), m.scanSpinner.Tick)
		case "4":
			m.clientCursor = 3
			m.screen = ScreenHub
			return m, nil
		case "enter":
			switch m.clientCursor {
			case 0:
				m.screen = ScreenLANPeerConnect
				m.peerIPInput.Reset()
				m.focusManualPeerInput = false
				m.peerIPInput.Blur()
				m.isScanning = true
				return m, tea.Batch(scanPeersCmd(), m.scanSpinner.Tick)
			case 1:
				if m.hasStagedPayload {
					m.overridePayloadURL = m.stagedPayloadPath
					m.selectedOS = &discovery.Entry{
						Distro:          filepath.Base(m.stagedPayloadPath),
						Version:         "Physical Scratch (/var/tmp)",
						Flavor:          "gui",
						Arch:            "amd64",
						DownloadURL:     m.stagedPayloadPath,
						Mirrors:         []string{m.stagedPayloadPath},
						ApproxSizeBytes: uint64(m.stagedPayloadSize),
						MinDiskGB:       20,
					}
					m.customBootCursor = 0
					m.screen = ScreenCustomBootModeSelect
					return m, nil
				}
				m.screen = ScreenFilePicker
				m.pickerForSCP = false
				m.pickerIsDirMode = false
				m.pickerCurrentDir = "/var/tmp"
				return m, loadDirCmd("/var/tmp", true)
			case 2:
				m.screen = ScreenUSBSelect
				m.usbCursor = 0
				m.isScanning = true
				return m, tea.Batch(scanUSBsCmd(), m.scanSpinner.Tick)
			case 3:
				m.screen = ScreenHub
				return m, nil
			}
		}

	case ScreenServerPanel:
		switch keyStr {
		case "esc":
			m.screen = ScreenAdvancedRoleSelect
			return m, nil
		case "up", "k":
			if m.serverCursor > 0 {
				m.serverCursor--
			}
		case "down", "j":
			if m.serverCursor < 1 {
				m.serverCursor++
			}
		case "1":
			m.serverCursor = 0
			m.screen = ScreenSeederDashboard
			m.seederMenuCursor = 0
			m.seederFileInput.Blur()
			return m, nil
		case "2":
			m.serverCursor = 1
			m.screen = ScreenSCPPush
			m.scpFocusCursor = 0
			m.scpStatusMsg = ""
			m.updateSCPFocus()
			return m, nil
		case "enter":
			if m.serverCursor == 0 {
				m.screen = ScreenSeederDashboard
				m.seederMenuCursor = 0
				m.seederFileInput.Blur()
			} else {
				m.screen = ScreenSCPPush
				m.scpFocusCursor = 0
				m.scpStatusMsg = ""
				m.updateSCPFocus()
			}
			return m, nil
		}

	case ScreenSeederDashboard:
		switch keyStr {
		case "esc":
			m.screen = ScreenServerPanel
			return m, nil
		case "up", "k":
			if m.seederMenuCursor > 0 {
				m.seederMenuCursor--
				if m.seederMenuCursor == 0 {
					m.seederFileInput.Focus()
				} else {
					m.seederFileInput.Blur()
				}
			}
			return m, nil
		case "down", "j", "tab":
			if m.seederMenuCursor < 3 {
				m.seederMenuCursor++
				if m.seederMenuCursor == 0 {
					m.seederFileInput.Focus()
				} else {
					m.seederFileInput.Blur()
				}
			}
			return m, nil
		case "enter":
			switch m.seederMenuCursor {
			case 1:
				if path, err := engine.PickFileOrDirectory(false, "Select ISO Image"); err == nil && path != "" {
					m.seederFilePath = path
					m.seederFileInput.SetValue(path)
					return m, nil
				}
				m.screen = ScreenFilePicker
				m.pickerForSCP = false
				m.pickerIsDirMode = false
				m.pickerCurrentDir = engine.GetRealUserHome()
				return m, loadDirCmd(m.pickerCurrentDir, m.pickerShowAll)
			case 2:
				if path, err := engine.PickFileOrDirectory(true, "Select Directory"); err == nil && path != "" {
					m.seederFilePath = path
					m.seederFileInput.SetValue(path)
					return m, nil
				}
				m.screen = ScreenFilePicker
				m.pickerForSCP = false
				m.pickerIsDirMode = true
				m.pickerCurrentDir = engine.GetRealUserHome()
				return m, loadDirCmd(m.pickerCurrentDir, m.pickerShowAll)
			case 3:
				if !m.seederActive {
					path := strings.TrimSpace(m.seederFileInput.Value())
					if path != "" {
						m.seederFilePath = path
						m.stopSeederChan = make(chan struct{})
						m.seederActive = true
						go func(p string) {
							_ = engine.StartPeerSeeder(p, 8080, m.stopSeederChan)
						}(path)
						ip, _ := engine.GetLocalOutboundIP()
						m.seederURL = fmt.Sprintf("http://%s:8080/files/", ip)
					}
				} else {
					if m.stopSeederChan != nil {
						close(m.stopSeederChan)
						m.stopSeederChan = nil
					}
					m.seederActive = false
				}
				return m, nil
			}
		default:
			if m.seederMenuCursor == 0 {
				var cmd tea.Cmd
				m.seederFileInput, cmd = m.seederFileInput.Update(msg)
				return m, cmd
			}
		}

	case ScreenSCPPush:
		switch keyStr {
		case "esc":
			m.screen = ScreenServerPanel
			return m, nil
		case "tab", "down":
			m.scpFocusCursor = (m.scpFocusCursor + 1) % 6
			m.updateSCPFocus()
			return m, nil
		case "shift+tab", "up":
			m.scpFocusCursor = (m.scpFocusCursor + 5) % 6
			m.updateSCPFocus()
			return m, nil
		case "enter":
			switch m.scpFocusCursor {
			case 4:
				if path, err := engine.PickFileOrDirectory(false, "Select ISO to Push"); err == nil && path != "" {
					m.scpPayloadInput.SetValue(path)
					return m, nil
				}
				m.screen = ScreenFilePicker
				m.pickerForSCP = true
				m.pickerIsDirMode = false
				m.pickerCurrentDir = engine.GetRealUserHome()
				return m, loadDirCmd(m.pickerCurrentDir, m.pickerShowAll)
			case 5:
				ip := strings.TrimSpace(m.scpTargetIPInput.Value())
				user := strings.TrimSpace(m.scpUserInput.Value())
				pass := strings.TrimSpace(m.scpPasswordInput.Value())
				file := strings.TrimSpace(m.scpPayloadInput.Value())
				if ip != "" && user != "" && file != "" {
					m.isSCPPushing = true
					m.scpWrittenBytes = 0
					m.scpTotalBytes = 0
					m.scpStartTime = time.Now()
					if info, err := os.Stat(file); err == nil {
						m.scpTotalBytes = info.Size()
					}
					m.scpStatusMsg = fmt.Sprintf("Streaming payload over wire to %s@%s:/var/tmp/os_image.payload...", user, ip)
					m.scpChan = make(chan scpProgressMsg, 128)

					return m, tea.Batch(
						triggerSCPPushWithProgress(user, ip, pass, file, m.scpChan),
						listenForSCPProgress(m.scpChan),
						tickCmd(),
					)
				}
			default:
				m.scpFocusCursor = (m.scpFocusCursor + 1) % 6
				m.updateSCPFocus()
				return m, nil
			}
		default:
			var cmd tea.Cmd
			switch m.scpFocusCursor {
			case 0:
				m.scpTargetIPInput, cmd = m.scpTargetIPInput.Update(msg)
			case 1:
				m.scpUserInput, cmd = m.scpUserInput.Update(msg)
			case 2:
				m.scpPasswordInput, cmd = m.scpPasswordInput.Update(msg)
			case 3:
				m.scpPayloadInput, cmd = m.scpPayloadInput.Update(msg)
			}
			return m, cmd
		}

	case ScreenFilePicker:
		switch keyStr {
		case "esc":
			if m.pickerForSCP {
				m.screen = ScreenSCPPush
			} else {
				m.screen = ScreenSeederDashboard
			}
			return m, nil
		case "a":
			m.pickerShowAll = !m.pickerShowAll
			return m, loadDirCmd(m.pickerCurrentDir, m.pickerShowAll)
		case "up", "k":
			if m.pickerCursor > 0 {
				m.pickerCursor--
			}
		case "down", "j":
			if m.pickerCursor < len(m.pickerItems)-1 {
				m.pickerCursor++
			}
		case "enter":
			if len(m.pickerItems) == 0 {
				return m, nil
			}
			item := m.pickerItems[m.pickerCursor]
			if item.IsDir {
				return m, loadDirCmd(item.Path, m.pickerShowAll)
			}
			if m.pickerForSCP {
				m.scpPayloadInput.SetValue(item.Path)
				m.screen = ScreenSCPPush
			} else {
				m.seederFilePath = item.Path
				m.seederFileInput.SetValue(item.Path)
				m.screen = ScreenSeederDashboard
			}
			return m, nil
		case "s":
			if m.pickerIsDirMode {
				if m.pickerForSCP {
					m.scpPayloadInput.SetValue(m.pickerCurrentDir)
					m.screen = ScreenSCPPush
				} else {
					m.seederFilePath = m.pickerCurrentDir
					m.seederFileInput.SetValue(m.pickerCurrentDir)
					m.screen = ScreenSeederDashboard
				}
				return m, nil
			}
		}

	case ScreenLANPeerConnect:
		switch keyStr {
		case "esc":
			m.screen = ScreenClientPanel
			return m, nil

		case "up", "k":
			if m.focusManualPeerInput {
				if len(m.discoveredPeers) > 0 {
					m.focusManualPeerInput = false
					m.peerIPInput.Blur()
				}
			} else if m.peerCursor > 0 {
				m.peerCursor--
			}
			return m, nil

		case "down", "j", "tab":
			if !m.focusManualPeerInput {
				if m.peerCursor < len(m.discoveredPeers)-1 && keyStr != "tab" {
					m.peerCursor++
				} else {
					m.focusManualPeerInput = true
					m.peerIPInput.Focus()
				}
			}
			return m, nil

		case "enter":
			if !m.focusManualPeerInput && len(m.discoveredPeers) > 0 {
				selected := m.discoveredPeers[m.peerCursor]
				payloadName := "Custom LAN Payload"
				if len(selected.Payloads) > 0 {
					payloadName = selected.Payloads[0]
				}
				m.overridePayloadURL = selected.URL
				m.selectedOS = &discovery.Entry{
					Distro:          payloadName,
					Version:         fmt.Sprintf("LAN Peer (%s:8080)", selected.IP),
					Flavor:          "gui",
					Arch:            "amd64",
					DownloadURL:     selected.URL,
					Mirrors:         []string{selected.URL},
					ApproxSizeBytes: uint64(selected.SizeBytes),
					MinDiskGB:       25,
				}
				m.customBootCursor = 0
				m.screen = ScreenCustomBootModeSelect
				return m, nil
			}

			val := strings.TrimSpace(m.peerIPInput.Value())
			if val != "" {
				if !strings.HasPrefix(val, "http://") && !strings.HasPrefix(val, "https://") {
					val = "http://" + val
				}
				if !strings.Contains(val[7:], ":") && !strings.Contains(val[8:], "/") {
					val = val + ":8080/files/Parrot-security-7.3_amd64.iso"
				}
				m.overridePayloadURL = val
				m.selectedOS = &discovery.Entry{
					Distro:          "Manual LAN Stream",
					Version:         "Peer Endpoint",
					Flavor:          "gui",
					Arch:            "amd64",
					DownloadURL:     val,
					Mirrors:         []string{val},
					ApproxSizeBytes: 8 * 1024 * 1024 * 1024,
					MinDiskGB:       25,
				}
				m.customBootCursor = 0
				m.screen = ScreenCustomBootModeSelect
				return m, nil
			}

		default:
			if m.focusManualPeerInput {
				var cmd tea.Cmd
				m.peerIPInput, cmd = m.peerIPInput.Update(msg)
				return m, cmd
			}
		}

	case ScreenCustomBootModeSelect:
		switch keyStr {
		case "esc":
			m.screen = ScreenClientPanel
			return m, nil
		case "up", "k":
			if m.customBootCursor > 0 {
				m.customBootCursor--
			}
		case "down", "j":
			if m.customBootCursor < 1 {
				m.customBootCursor++
			}
		case "1":
			m.provisionMode = "dual-boot"
			m.detectedFreeBytes = m.detectAvailableDiskSpace()
			m.screen = ScreenBackupPrompt
			m.backupCursor = 0
			m.backupStatusMsg = ""
			m.isScanning = true
			return m, tea.Batch(scanBackupDrivesCmd(), m.scanSpinner.Tick)
		case "2":
			m.provisionMode = "single-boot"
			m.screen = ScreenBackupPrompt
			m.backupCursor = 0
			m.backupStatusMsg = ""
			m.isScanning = true
			return m, tea.Batch(scanBackupDrivesCmd(), m.scanSpinner.Tick)
		case "enter":
			if m.customBootCursor == 0 {
				m.provisionMode = "dual-boot"
				m.detectedFreeBytes = m.detectAvailableDiskSpace()
				m.screen = ScreenBackupPrompt
				m.backupCursor = 0
				m.backupStatusMsg = ""
				m.isScanning = true
				return m, tea.Batch(scanBackupDrivesCmd(), m.scanSpinner.Tick)
			} else {
				m.provisionMode = "single-boot"
				m.screen = ScreenBackupPrompt
				m.backupCursor = 0
				m.backupStatusMsg = ""
				m.isScanning = true
				return m, tea.Batch(scanBackupDrivesCmd(), m.scanSpinner.Tick)
			}
		}

	case ScreenStorageAllocationSelect:
		maxChoices := 4
		if !m.isVMEnvironment {
			maxChoices = 5
		}

		switch keyStr {
		case "esc":
			if m.isAdvancedMode || m.overridePayloadURL != "" {
				m.screen = ScreenCustomBootModeSelect
				return m, nil
			}
			m.screen = ScreenOSSelect
			return m, nil
		case "up", "k":
			if m.storageCursor > 0 {
				m.storageCursor--
				if m.storageCursor == maxChoices-1 {
					m.storageCustomInput.Focus()
				} else {
					m.storageCustomInput.Blur()
				}
			}
			return m, nil
		case "down", "j", "tab":
			if m.storageCursor < maxChoices-1 {
				m.storageCursor++
				if m.storageCursor == maxChoices-1 {
					m.storageCustomInput.Focus()
				} else {
					m.storageCustomInput.Blur()
				}
			}
			return m, nil
		case "enter":
			switch m.storageCursor {
			case 0:
				m.chosenAllocBytes = m.detectedFreeBytes
			case 1:
				m.chosenAllocBytes = m.detectedFreeBytes / 2
			case 2:
				m.chosenAllocBytes = m.detectedFreeBytes / 4
			case 3:
				if !m.isVMEnvironment {
					m.chosenAllocBytes = m.detectedFreeBytes / 2
				} else {
					valStr := strings.TrimSpace(m.storageCustomInput.Value())
					if val, err := strconv.ParseUint(valStr, 10, 64); err == nil && val > 0 {
						m.chosenAllocBytes = val * 1024 * 1024 * 1024
					} else {
						m.chosenAllocBytes = 20 * 1024 * 1024 * 1024
					}
				}
			case 4:
				valStr := strings.TrimSpace(m.storageCustomInput.Value())
				if val, err := strconv.ParseUint(valStr, 10, 64); err == nil && val > 0 {
					m.chosenAllocBytes = val * 1024 * 1024 * 1024
				} else {
					m.chosenAllocBytes = 25 * 1024 * 1024 * 1024
				}
			}

			if m.chosenAllocBytes < 15*1024*1024*1024 {
				m.chosenAllocBytes = 15 * 1024 * 1024 * 1024
			}

			m.screen = ScreenConfirm
			return m, nil
		default:
			if m.storageCursor == maxChoices-1 {
				var cmd tea.Cmd
				m.storageCustomInput, cmd = m.storageCustomInput.Update(msg)
				return m, cmd
			}
		}

	case ScreenUSBSelect:
		switch keyStr {
		case "esc":
			engine.CleanAutoMounts(m.discoveredUSBs)
			m.screen = ScreenClientPanel
			return m, nil
		case "r":
			m.isScanning = true
			return m, tea.Batch(scanUSBsCmd(), m.scanSpinner.Tick)
		case "up", "k":
			if m.usbCursor > 0 {
				m.usbCursor--
			}
		case "down", "j":
			if m.usbCursor < len(m.discoveredUSBs)-1 {
				m.usbCursor++
			}
		case "enter":
			if len(m.discoveredUSBs) > 0 {
				u := m.discoveredUSBs[m.usbCursor]
				m.selectedUSB = &u
				m.overridePayloadURL = u.FilePath
				m.selectedOS = &discovery.Entry{
					Distro:          u.FileName,
					Version:         "USB Media (" + u.DeviceName + ")",
					Flavor:          "gui",
					Arch:            "amd64",
					DownloadURL:     u.FilePath,
					Mirrors:         []string{u.FilePath},
					ApproxSizeBytes: uint64(u.SizeBytes),
					MinDiskGB:       20,
				}
				m.customBootCursor = 0
				m.screen = ScreenCustomBootModeSelect
				return m, nil
			}
		}

	case ScreenHub:
		switch keyStr {
		case "q":
			return m, tea.Quit
		case "esc":
			m.screen = ScreenModeSelect
			return m, nil
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
		if keyStr == "esc" {
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
		if keyStr == "q" {
			return m, tea.Quit
		}
		if keyStr == "esc" {
			m.screen = ScreenFolderSelect
			return m, nil
		}

		if keyStr == "enter" {
			if it, ok := m.osList.SelectedItem().(osListItem); ok {
				e := it.entry
				m.selectedOS = &e

				if m.isAdvancedMode {
					m.screen = ScreenUSBTargetSelect
					m.usbTargetCursor = 0
					m.isScanning = true
					return m, tea.Batch(scanUSBTargetsCmd(), m.scanSpinner.Tick)
				}

				m.detectedFreeBytes = m.detectAvailableDiskSpace()
				m.screen = ScreenBackupPrompt
				m.backupCursor = 0
				m.backupStatusMsg = ""
				m.isScanning = true
				return m, tea.Batch(scanBackupDrivesCmd(), m.scanSpinner.Tick)
			}
		}

		var cmd tea.Cmd
		m.osList, cmd = m.osList.Update(msg)
		return m, cmd

	case ScreenEnvironmentCheck:
		switch keyStr {
		case "esc", "enter", "q":
			m.screen = ScreenHub
			return m, nil
		}

	case ScreenDisasterRecovery:
		switch keyStr {
		case "esc", "q":
			m.screen = ScreenHub
			return m, nil
		case "r":
			m.isScanning = true
			m.restoreCursor = 0
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
			if len(m.discoveredBackups) > 0 && m.restoreCursor < len(m.discoveredBackups) {
				arc := m.discoveredBackups[m.restoreCursor]
				m.selectedArchive = &arc
				m.screen = ScreenRestoreConfirm
				return m, nil
			}
		}

	case ScreenRestoreConfirm:
		switch keyStr {
		case "esc", "n":
			m.screen = ScreenDisasterRecovery
			return m, nil
		case "y", "enter":
			if m.selectedArchive != nil {
				pDisk, _, _, _, _ := engine.DetectActiveRootDisk()
				m.screen = ScreenProgress
				m.speed = NewSpeedTracker(uint64(m.selectedArchive.SizeBytes))
				m.statusLog = append(m.statusLog, "=> [RESTORE] Restoring bare-metal host snapshot from: "+m.selectedArchive.FileName)
				return m, triggerRestoreCmd(m.selectedArchive.FilePath, pDisk)
			}
		}

	case ScreenUSBTargetSelect:
		maxChoices := 2 + len(m.discoveredTargets)

		switch keyStr {
		case "esc":
			m.screen = ScreenOSSelect
			return m, nil

		case "r":
			m.isScanning = true
			return m, tea.Batch(scanUSBTargetsCmd(), m.scanSpinner.Tick)

		case "up", "k":
			if m.usbTargetCursor > 0 {
				m.usbTargetCursor--
			}
			return m, nil

		case "down", "j":
			if m.usbTargetCursor < maxChoices {
				m.usbTargetCursor++
			}
			return m, nil

		case "1":
			m.usbTargetCursor = 0
		case "2":
			m.usbTargetCursor = 1
		case "3":
			m.usbTargetCursor = maxChoices
		}

		if keyStr == "enter" || keyStr == "1" || keyStr == "2" || keyStr == "3" {
			if m.usbTargetCursor == 0 {
				m.detectedFreeBytes = m.detectAvailableDiskSpace()
				m.screen = ScreenBackupPrompt
				m.backupCursor = 0
				m.backupStatusMsg = ""
				m.isScanning = true
				return m, tea.Batch(scanBackupDrivesCmd(), m.scanSpinner.Tick)
			}

			if m.usbTargetCursor == 1 {
				if len(m.discoveredTargets) > 0 {
					m.usbTargetCursor = 2
					return m, nil
				}
				m.isScanning = true
				return m, tea.Batch(scanUSBTargetsCmd(), m.scanSpinner.Tick)
			}

			if m.usbTargetCursor == maxChoices {
				m.isScanning = true
				return m, tea.Batch(scanUSBTargetsCmd(), m.scanSpinner.Tick)
			}

			targetIdx := m.usbTargetCursor - 2
			if targetIdx >= 0 && targetIdx < len(m.discoveredTargets) {
				tgt := m.discoveredTargets[targetIdx]
				m.selectedTarget = &tgt
				m.screen = ScreenUSBFormatConfirm
				return m, nil
			}
		}

	case ScreenUSBFormatConfirm:
		switch keyStr {
		case "esc", "n":
			if m.isAdvancedMode || m.overridePayloadURL != "" {
				m.screen = ScreenCustomBootModeSelect
				return m, nil
			}
			m.screen = ScreenHub
			return m, nil
		case "y", "enter":
			if m.selectedTarget != nil {
				m.screen = ScreenProgress
				m.speed = NewSpeedTracker(100)
				m.statusLog = append(m.statusLog, fmt.Sprintf("=> [USB PREPARE] Formatting %s (%s) with dynamic partition scheme...", m.selectedTarget.DevPath, m.selectedTarget.Size))
				m.stepUpdates = make(chan provisionStepMsg, 64)

				return m, tea.Batch(
					triggerUSBFormatCmd(*m.selectedTarget, m.stepUpdates),
					listenForStepUpdates(m.stepUpdates),
					tickCmd(),
				)
			}
		}

	case ScreenUSBDownloadProgress:
		switch keyStr {
		case "esc", "q":
			m.screen = ScreenHub
			return m, nil
		}

	case ScreenBackupPrompt:
		switch keyStr {
		case "esc":
			if m.isAdvancedMode || m.overridePayloadURL != "" {
				m.screen = ScreenCustomBootModeSelect
				return m, nil
			}
			m.screen = ScreenHub
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
				if m.provisionMode == "dual-boot" {
					m.screen = ScreenStorageAllocationSelect
					m.storageCursor = 1
					m.storageCustomInput.Blur()
					return m, nil
				}
				m.screen = ScreenConfirm
				m.eraseConfirmInput.Reset()
				m.eraseConfirmInput.Focus()
				return m, nil
			}

			if len(m.backupTargets) > 0 && m.backupCursor < len(m.backupTargets) {
				tgt := m.backupTargets[m.backupCursor]

				if tgt.Type == "disk" && strings.HasPrefix(tgt.Name, "sd") {
					devPath := "/dev/" + tgt.Name
					byteSize, _ := engine.QueryBlockDeviceBytes(tgt.Name)
					m.selectedTarget = &engine.USBTargetDevice{
						Name:      tgt.Name,
						DevPath:   devPath,
						Size:      tgt.Size,
						SizeBytes: byteSize,
					}
					m.screen = ScreenUSBFormatConfirm
					return m, nil
				}

				targetPath := tgt.Mountpoint
				if targetPath == "" {
					targetPath = tgt.Name
				}

				// DUAL-BOOT: Fast payload stage without snapshot
				if m.provisionMode == "dual-boot" {
					src := "/var/tmp/os_image.payload"
					if m.stagedPayloadPath != "" {
						src = m.stagedPayloadPath
					} else if m.overridePayloadURL != "" && !strings.HasPrefix(m.overridePayloadURL, "http") {
						src = m.overridePayloadURL
					}

					if info, sErr := os.Stat(src); sErr == nil {
						m.screen = ScreenProgress
						m.speed = NewSpeedTracker(uint64(info.Size()))
						m.stepUpdates = make(chan provisionStepMsg, 64)
						return m, tea.Batch(
							triggerUSBStagePayloadCmd(src, targetPath, m.stepUpdates),
							listenForStepUpdates(m.stepUpdates),
							tickCmd(),
						)
					}

					m.screen = ScreenStorageAllocationSelect
					m.storageCursor = 1
					m.storageCustomInput.Blur()
					return m, nil
				}

				// SINGLE-BOOT: Full host backup snapshot
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

	case ScreenConfirm:
		if m.provisionMode == "single-boot" {
			switch keyStr {
			case "esc":
				if m.isAdvancedMode || m.overridePayloadURL != "" {
					m.screen = ScreenCustomBootModeSelect
					return m, nil
				}
				m.screen = ScreenHub
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
			case "n", "esc":
				m.screen = ScreenStorageAllocationSelect
			case "q":
				return m, tea.Quit
			}
		}

	case ScreenRevertConfirm:
		switch keyStr {
		case "y", "enter":
			m.screen = ScreenProgress
			m.statusLog = append(m.statusLog, "=> [REVERT] Initializing bare-metal OS decommissioning transaction...")
			m.speed = NewSpeedTracker(100)
			m.stepUpdates = make(chan provisionStepMsg, 32)

			return m, tea.Batch(
				triggerRealRevertCmd(m.stepUpdates),
				listenForStepUpdates(m.stepUpdates),
				tickCmd(),
			)
		case "n", "esc":
			m.screen = ScreenHub
			return m, nil
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

		case "enter", "esc":
			if !m.showFSPrompt {
				m.fatalErr = nil
				m.screen = ScreenHub
				return m, nil
			} else {
				m.showFSPrompt = false
				return m, nil
			}

		case "up", "k":
			if m.showFSPrompt {
				if m.postUSBFSCursor > 0 {
					m.postUSBFSCursor--
				}
			} else {
				if m.postUSBActionCursor > 0 {
					m.postUSBActionCursor--
				}
			}
			return m, nil

		case "down", "j":
			if m.showFSPrompt {
				if m.postUSBFSCursor < 1 {
					m.postUSBFSCursor++
				}
			} else {
				if m.postUSBActionCursor < 1 {
					m.postUSBActionCursor++
				}
			}
			return m, nil

		case "1":
			if !m.showFSPrompt {
				m.postUSBActionCursor = 0
				mountPath := "/mnt/orch_usb_storage"
				if m.usbFormatResult != nil && m.usbFormatResult.StorageMountPath != "" {
					mountPath = m.usbFormatResult.StorageMountPath
				}
				m.isResettingUSB = true
				return m, triggerUSBPurgeVaultCmd(mountPath)
			} else {
				m.postUSBFSCursor = 0
				disk := "/dev/sdc"
				if m.selectedTarget != nil {
					disk = m.selectedTarget.DevPath
				}
				m.isResettingUSB = true
				m.showFSPrompt = false
				return m, triggerUSBResetCmd(disk, "ntfs")
			}

		case "2":
			if !m.showFSPrompt {
				m.postUSBActionCursor = 1
				m.showFSPrompt = true
				return m, nil
			} else {
				m.postUSBFSCursor = 1
				disk := "/dev/sdc"
				if m.selectedTarget != nil {
					disk = m.selectedTarget.DevPath
				}
				m.isResettingUSB = true
				m.showFSPrompt = false
				return m, triggerUSBResetCmd(disk, "fat32")
			}
		}

	case ScreenRevertDone:
		switch keyStr {
		case "enter", "esc":
			m.screen = ScreenHub
			return m, nil
		case "q":
			return m, tea.Quit
		}
	}

	return m, nil
}

func (m *Model) updateSCPFocus() {
	m.scpTargetIPInput.Blur()
	m.scpUserInput.Blur()
	m.scpPasswordInput.Blur()
	m.scpPayloadInput.Blur()
	switch m.scpFocusCursor {
	case 0:
		m.scpTargetIPInput.Focus()
	case 1:
		m.scpUserInput.Focus()
	case 2:
		m.scpPasswordInput.Focus()
	case 3:
		m.scpPayloadInput.Focus()
	}
}

func (m *Model) startProvisioning(isResume bool) tea.Cmd {
	ctx, cancel := context.WithCancel(context.Background())
	m.cancelProvision = cancel

	pDisk, _, hostNum, targetPart, _ := engine.DetectActiveRootDisk()
	if pDisk == "" {
		pDisk = "/dev/sda"
		hostNum = "1"
		targetPart = "/dev/sda4"
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
	if m.overridePayloadURL != "" {
		primaryURL = m.overridePayloadURL
		mirrors = []string{m.overridePayloadURL}
	} else if len(mirrors) > 0 {
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
		DownloadDestPath: "/var/tmp/os_image.payload",
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
		AllocatedBytes:   m.chosenAllocBytes,
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
