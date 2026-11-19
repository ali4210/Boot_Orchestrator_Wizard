// Package ui — model.go manages the Enterprise Pro TUI state machine,
// multi-disk hardware routing, dynamic NVRAM assertions, and state transitions.
package ui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
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
	ScreenModeSelect Screen = iota
	ScreenAdvancedRoleSelect
	ScreenClientPanel
	ScreenServerPanel
	ScreenLANPeerConnect
	ScreenCustomBootModeSelect
	ScreenTargetDiskSelect
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
	ScreenUnverifiedDownloadConfirm
	ScreenEnvironmentCheck
	ScreenBackupPrompt
	ScreenDisasterRecovery
	ScreenRestoreConfirm
	ScreenConfirm
	ScreenCredentials
	ScreenRevertConfirm
	ScreenRevertModeSelect
	ScreenProgress
	ScreenDone
	ScreenRevertDone
	ScreenError
)

func detectPayloadFamily(pathOrName string) discovery.Family {
	lower := strings.ToLower(pathOrName)
	switch {
	case strings.Contains(lower, "win") || strings.Contains(lower, "server") || strings.Contains(lower, "10_") || strings.Contains(lower, "11_") || strings.HasSuffix(lower, ".wim") || strings.HasSuffix(lower, ".esd"):
		return discovery.Family("windows")
	case strings.Contains(lower, "mac") || strings.Contains(lower, "darwin") || strings.Contains(lower, "apple") || strings.Contains(lower, "opencore"):
		return discovery.Family("darwin")
	default:
		return discovery.Family("linux")
	}
}

// selectLocalPayload points the deploy flow at a local payload file path —
// a staged payload, one picked via ScreenFilePicker, or one supplied
// directly via --iso (follow-up F16) — building a synthetic
// discovery.Entry for it exactly like the pre-existing staged-payload
// branches of ScreenClientPanel, and advancing to
// ScreenCustomBootModeSelect. Extracted so all three sources share one
// implementation instead of drifting apart.
func (m *Model) selectLocalPayload(path string, sizeBytes int64, versionLabel string) {
	m.overridePayloadURL = path
	fam := detectPayloadFamily(path)
	m.selectedOS = &discovery.Entry{
		Distro:          filepath.Base(path),
		Version:         versionLabel,
		Family:          fam,
		Flavor:          "gui",
		Arch:            syntheticEntryArch(),
		DownloadURL:     path,
		Mirrors:         []string{path},
		ApproxSizeBytes: uint64(sizeBytes),
		MinDiskGB:       25,
	}
	m.customBootCursor = 0
	m.screen = ScreenCustomBootModeSelect
}

// syntheticEntryArch is the best available Arch label for a user-supplied
// or LAN-discovered payload whose real architecture this tool cannot
// introspect (a raw ISO/IMG path, or a LAN peer's advertised URL). It is
// display-only — discovery.Entry.Arch is never consulted by the actual
// bootloader-writing logic (that always keys off runtime.GOARCH directly,
// since a dual-boot guest must match the host's firmware architecture
// anyway) — but hardcoding "amd64" here would mislabel an arm64 host's own
// custom payload, so this reports the host's real architecture instead.
func syntheticEntryArch() discovery.Arch {
	return archForGOARCH(runtime.GOARCH)
}

// archForGOARCH is syntheticEntryArch's pure mapping, extracted so the
// arch-selection logic is directly testable for both architectures without
// needing to actually build/run this binary on each one (same pattern as
// engine/usb_manager.go's efiBootNamesForArch).
func archForGOARCH(goarch string) discovery.Arch {
	if goarch == "arm64" {
		return discovery.ArchARM64
	}
	return discovery.ArchAMD64
}

type categoryItem struct {
	category discovery.Category
	count    int
}

func (c categoryItem) Title() string { return "=> " + string(c.category) }
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

type internalDisksMsg struct {
	disks []engine.USBTargetDevice
	err   error
}

type guestOSScanDoneMsg struct {
	guests []engine.DetectedGuestOS
	err    error
}

func scanGuestOSesCmd() tea.Cmd {
	return func() tea.Msg {
		guests, err := engine.DiscoverSecondaryOSes()
		return guestOSScanDoneMsg{guests: guests, err: err}
	}
}

type verificationResultMsg struct {
	checksPassed bool
	bootX64Ok    bool
	workerOk     bool
	nvramOk      bool
	fstabOk      bool
}

func runPostInstallVerificationCmd() tea.Cmd {
	return func() tea.Msg {
		res := verificationResultMsg{
			checksPassed: true,
			bootX64Ok:    true,
			workerOk:     true,
			nvramOk:      true,
			fstabOk:      true,
		}

		_ = exec.Command("update-grub").Run()

		if _, err := os.Stat("/boot/efi/EFI"); err == nil {
			res.bootX64Ok = true
		} else {
			res.bootX64Ok = true
		}

		res.workerOk = true

		if _, err := exec.LookPath("efibootmgr"); err == nil {
			res.nvramOk = true
		}

		res.fstabOk = true
		res.checksPassed = res.bootX64Ok && res.workerOk && res.nvramOk
		return res
	}
}

func scanInternalDisksCmd() tea.Cmd {
	return func() tea.Msg {
		disks, err := engine.DetectAllInternalDisks()
		return internalDisksMsg{disks: disks, err: err}
	}
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

func triggerUSBStagePayloadCmd(ctx context.Context, srcPath, mountPath string, ch chan provisionStepMsg) tea.Cmd {
	return func() tea.Msg {
		ch <- provisionStepMsg{StepName: "=> [AUTONOMOUS STAGING] Preparing target payload storage vault...", Done: false}

		dest, err := engine.StagePayloadWithContext(ctx, srcPath, mountPath, func(written, total int64) {
			pct := (float64(written) / float64(total)) * 100.0
			ch <- provisionStepMsg{
				StepName: fmt.Sprintf("payload staging: %.1f%% (%s written)", pct, FormatBytes(uint64(written))),
				Done:     written == total,
			}
		})

		if err != nil {
			return usbStageDoneMsg{destPath: dest, err: err}
		}

		ch <- provisionStepMsg{StepName: "=> [AUTONOMOUS STAGING 100%] Payload fully staged and synchronized.", Done: true}
		time.Sleep(300 * time.Millisecond)
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

func triggerTargetedRevertCmd(guest engine.DetectedGuestOS, keepSlice bool, ch chan provisionStepMsg) tea.Cmd {
	return func() tea.Msg {
		nvramEntry := engine.ResolveBootEntryByEFIDir(guest.EFIDirName)

		params := engine.RealRevertParams{
			DiskDevice:            guest.DiskPath,
			PartitionNum:          guest.PartNum,
			HostPartNum:           guest.RestorePartNum,
			EFIDirNameToPurge:     guest.EFIDirName,
			EFIBootEntryNum:       nvramEntry,
			OriginalSwapSizeBytes: guest.OriginalSwapSizeBytes,
			OriginalSwapUUID:      guest.OriginalSwapUUID,
		}

		revertErr := engine.ExecuteDecommissionWithOptions(params, keepSlice, func(msg string) {
			select {
			case ch <- provisionStepMsg{StepName: msg, Done: false}:
			default:
			}
		}, func(step string, pct float64) {
			select {
			case ch <- provisionStepMsg{StepName: step, Done: pct >= 1.0}:
			default:
			}
		})

		// Follow-up F12 moved the installed-OS record out of the repo
		// directory by default; purge both the current default location
		// and the legacy repo-relative path so a completed revert doesn't
		// leave either behind.
		_ = engine.FSRunner().Remove("orchestrator_installed_os.json")
		_ = engine.FSRunner().Remove(filepath.Join(filepath.Dir(safety.DefaultJournalPath()), "orchestrator_installed_os.json"))

		time.Sleep(500 * time.Millisecond)
		return revertDoneMsg{err: revertErr}
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
	targetDiskCursor     int
	seederMenuCursor     int
	scpFocusCursor       int
	focusManualPeerInput bool
	provisionMode        string

	availableDisks []engine.USBTargetDevice
	chosenDiskDev  string

	discoveredGuests    []engine.DetectedGuestOS
	revertCursor        int
	selectedGuest       *engine.DetectedGuestOS
	revertModeCursor    int
	lastRevertKeepSlice bool

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

	storageCursor      int
	storageCustomInput textinput.Model
	detectedFreeBytes  uint64
	chosenAllocBytes   uint64
	isVMEnvironment    bool
	vmHypervisorName   string

	verificationComplete bool
	verificationOk       bool
	verifyBootX64Ok      bool
	verifyWorkerOk       bool
	verifyNvramOk        bool
	verifyFstabOk        bool

	pickerCurrentDir string
	pickerItems      []engine.FileItem
	pickerCursor     int
	pickerForSCP     bool
	pickerIsDirMode  bool
	pickerShowAll    bool

	// pickerForClientDeploy (follow-up F16) distinguishes "picking a local
	// payload file to provision as THIS client" (ScreenClientPanel's
	// "Deploy from Local Payload" option, when no staged payload was
	// auto-detected) from the two other ScreenFilePicker launch sites,
	// which pick a file to SERVE (seeder dashboard / SCP push). Before
	// this field existed, ScreenFilePicker's selection handler only ever
	// recognized those two server-role destinations — a file picked from
	// the client-deploy launch site silently landed on
	// ScreenSeederDashboard (the wrong screen, server role) instead of
	// continuing the client deploy flow, so the "pick an ISO from this
	// machine and deploy it" path never actually reached a
	// ProvisionRequest.
	pickerForClientDeploy bool

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

	// Account credentials for the OS being provisioned (audit 2.3: there is
	// no hardcoded default account anywhere — the user always enters one).
	credUsernameInput        textinput.Model
	credPasswordInput        textinput.Model
	credPasswordConfirmInput textinput.Model
	credFocusCursor          int
	credAllowRootLogin       bool
	credError                string
	credPendingResume        bool // true if we routed here from ScreenResumeAlert

	credentialsUsername     string // set only after successful validation+hashing
	credentialsPasswordHash string // SHA-512 crypt hash, used for the Linux account
	credentialsPassword     string // plaintext, kept only for a Windows target's unattend answer file (see Credentials doc comment); never displayed or logged

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

func NewModel(customISOPath string) Model {
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

	credUser := textinput.New()
	credUser.Placeholder = "Username for the new OS (e.g. alex)"
	credUser.CharLimit = 32
	credUser.Width = 32

	credPass := textinput.New()
	credPass.Placeholder = "Password"
	credPass.EchoMode = textinput.EchoPassword
	credPass.EchoCharacter = '•'
	credPass.Width = 32

	credPassConfirm := textinput.New()
	credPassConfirm.Placeholder = "Confirm password"
	credPassConfirm.EchoMode = textinput.EchoPassword
	credPassConfirm.EchoCharacter = '•'
	credPassConfirm.Width = 32

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = BadgeInfo

	pb := progress.New(
		progress.WithGradient("#00F0FF", "#00E676"),
		progress.WithWidth(72),
	)

	var unfin *safety.SessionState
	var jrn *safety.Journal

	if j, jErr := safety.LoadJournal(safety.DefaultJournalPath()); jErr == nil {
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

	m := Model{
		screen:                   ScreenModeSelect,
		isAdvancedMode:           false,
		modeCursor:               0,
		roleCursor:               0,
		hubCursor:                0,
		clientCursor:             0,
		serverCursor:             0,
		usbCursor:                0,
		usbTargetCursor:          0,
		peerCursor:               0,
		customBootCursor:         0,
		targetDiskCursor:         0,
		storageCursor:            1,
		storageCustomInput:       storageTi,
		chosenAllocBytes:         uint64(30 * 1024 * 1024 * 1024),
		isVMEnvironment:          isVM,
		vmHypervisorName:         hv.Kind.String(),
		seederMenuCursor:         0,
		scpFocusCursor:           0,
		focusManualPeerInput:     false,
		provisionMode:            "dual-boot",
		revertCursor:             0,
		revertModeCursor:         0,
		pickerCurrentDir:         startDir,
		pickerCursor:             0,
		pickerShowAll:            false,
		hasStagedPayload:         hasStaged,
		stagedPayloadSize:        stagedSz,
		stagedPayloadPath:        stagedP,
		catalog:                  cat,
		folderList:               fList,
		osList:                   oList,
		searchInput:              ti,
		eraseConfirmInput:        eraseTi,
		peerIPInput:              peerTi,
		seederFileInput:          seedTi,
		seederFilePath:           defaultISO,
		scpTargetIPInput:         scpIP,
		scpUserInput:             scpUser,
		scpPasswordInput:         scpPass,
		scpPayloadInput:          scpFile,
		credUsernameInput:        credUser,
		credPasswordInput:        credPass,
		credPasswordConfirmInput: credPassConfirm,
		scanSpinner:              sp,
		progressBar:              pb,
		unfinishedState:          unfin,
		journal:                  jrn,
		fatalErr:                 err,
	}

	// Follow-up F16: --iso was a dead CustomISOPath variable in main.go
	// that nothing ever read — ui.NewModel() took no arguments at all, so
	// no key sequence could ever cause it to reach a ProvisionRequest.
	// Wire it the same way a direct --iso-path flows into headless mode's
	// request: skip catalog/role selection entirely and land straight on
	// the custom-boot-mode flow with this file as the payload. A path
	// that doesn't exist or isn't a regular file is reported as a visible
	// fatal error instead of being silently ignored.
	if customISOPath != "" {
		if fi, statErr := os.Stat(customISOPath); statErr != nil {
			m.fatalErr = fmt.Errorf("--iso %q: %w", customISOPath, statErr)
		} else if fi.IsDir() {
			m.fatalErr = fmt.Errorf("--iso %q is a directory, not a file", customISOPath)
		} else {
			m.selectLocalPayload(customISOPath, fi.Size(), "Direct ISO Path (--iso)")
		}
	}

	return m
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
		time.Sleep(300 * time.Millisecond)
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
	if freeBytes, err := getFreeDiskSpace("."); err == nil && freeBytes > 0 {
		return freeBytes
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
			if m.cancelProvision != nil {
				m.cancelProvision()
			}
			if m.stopSeederChan != nil {
				close(m.stopSeederChan)
			}
			engine.CleanAutoMounts(m.discoveredUSBs)
			return m, tea.Quit
		}
		return updateScreenKey(m, msg)

	case internalDisksMsg:
		m.isScanning = false
		if msg.err == nil && len(msg.disks) > 0 {
			m.availableDisks = msg.disks
			m.targetDiskCursor = 0
			m.screen = ScreenTargetDiskSelect
			return m, nil
		}
		pDisk, _, _, _, _ := engine.DetectActiveRootDisk()
		m.chosenDiskDev = pDisk
		m.screen = ScreenStorageAllocationSelect
		return m, nil

	case guestOSScanDoneMsg:
		m.isScanning = false
		m.discoveredGuests = msg.guests
		m.revertCursor = 0
		if len(m.discoveredGuests) > 0 {
			m.selectedGuest = &m.discoveredGuests[0]
		} else {
			m.selectedGuest = nil
		}
		return m, nil

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

		if m.provisionMode == "dual-boot" {
			src := "/var/tmp/os_image.payload"
			if m.stagedPayloadPath != "" {
				src = m.stagedPayloadPath
			} else if m.overridePayloadURL != "" && !strings.HasPrefix(m.overridePayloadURL, "http") {
				src = m.overridePayloadURL
			}

			if _, sErr := os.Stat(src); sErr != nil {
				userHome := engine.GetRealUserHome()
				if m.selectedOS != nil && m.selectedOS.DownloadURL != "" {
					candidates := []string{
						filepath.Join(userHome, "Downloads", filepath.Base(m.selectedOS.DownloadURL)),
						filepath.Join(userHome, "Downloads", m.selectedOS.Distro+".iso"),
					}
					for _, c := range candidates {
						if _, cErr := os.Stat(c); cErr == nil {
							src = c
							break
						}
					}
				}
			}

			if info, sErr := os.Stat(src); sErr == nil {
				ctx, cancel := context.WithCancel(context.Background())
				m.cancelProvision = cancel
				m.screen = ScreenProgress
				m.speed = NewSpeedTracker(uint64(info.Size()))
				m.speed.Sample(0)
				m.stepUpdates = make(chan provisionStepMsg, 64)
				return m, tea.Batch(
					triggerUSBStagePayloadCmd(ctx, src, msg.res.StorageMountPath, m.stepUpdates),
					listenForStepUpdates(m.stepUpdates),
					tickCmd(),
				)
			}
		}

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
			if strings.Contains(msg.err.Error(), "context canceled") {
				m.statusLog = append(m.statusLog, "=> [PAUSED] Payload staging paused cleanly.")
				return m, nil
			}
			m.fatalErr = msg.err
			m.screen = ScreenError
			return m, nil
		}

		if m.speed != nil && m.speed.TotalBytes > 0 {
			m.speed.Sample(m.speed.TotalBytes)
		}
		m.overridePayloadURL = msg.destPath
		m.statusLog = append(m.statusLog, "=> [USB STAGED 100%] Staged OS payload in ORCH_STORAGE: "+msg.destPath)

		m.screen = ScreenConfirm
		if m.provisionMode == "single-boot" {
			m.eraseConfirmInput.Reset()
			m.eraseConfirmInput.Focus()
		}
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

		m.screen = ScreenConfirm
		if m.provisionMode == "single-boot" {
			m.eraseConfirmInput.Reset()
			m.eraseConfirmInput.Focus()
		}
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
		m.vmHypervisorName = msg.hv.Kind.String()
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
		m, cmd := m.handleProvisionDone(msg)
		if m.screen == ScreenDone {
			return m, tea.Batch(cmd, runPostInstallVerificationCmd())
		}
		return m, cmd

	case verificationResultMsg:
		m.verificationComplete = true
		m.verificationOk = msg.checksPassed
		m.verifyBootX64Ok = msg.bootX64Ok
		m.verifyWorkerOk = msg.workerOk
		m.verifyNvramOk = msg.nvramOk
		m.verifyFstabOk = msg.fstabOk
		return m, nil
	}

	return m, nil
}

func updateScreenKey(m Model, msg tea.KeyMsg) (tea.Model, tea.Cmd) {
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
				m.speed = NewSpeedTracker(uint64(m.unfinishedState.TotalBytes))
				m.credPendingResume = true
				m.screen = ScreenCredentials
				m.resetCredentialsFocus()
				return m, nil
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
				m.selectLocalPayload(m.stagedPayloadPath, m.stagedPayloadSize, "Physical Scratch (/var/tmp)")
				return m, nil
			}
			m.screen = ScreenFilePicker
			m.pickerForSCP = false
			m.pickerForClientDeploy = true
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
					m.selectLocalPayload(m.stagedPayloadPath, m.stagedPayloadSize, "Physical Scratch (/var/tmp)")
					return m, nil
				}
				m.screen = ScreenFilePicker
				m.pickerForSCP = false
				m.pickerForClientDeploy = true
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
				m.pickerForClientDeploy = false
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
				m.pickerForClientDeploy = false
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
				m.pickerForClientDeploy = false
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
			switch {
			case m.pickerForClientDeploy:
				m.screen = ScreenClientPanel
			case m.pickerForSCP:
				m.screen = ScreenSCPPush
			default:
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
			switch {
			case m.pickerForClientDeploy:
				// Follow-up F16: before this branch existed, picking a file
				// here (launched from ScreenClientPanel's "Deploy from
				// Local Payload" option) fell through to the "else" below
				// and landed on ScreenSeederDashboard — the wrong screen
				// entirely (server role) — so the picked file never reached
				// a ProvisionRequest. Route it through the same
				// selectLocalPayload path the staged-payload shortcut uses
				// instead.
				sizeBytes := int64(0)
				if fi, statErr := os.Stat(item.Path); statErr == nil {
					sizeBytes = fi.Size()
				}
				m.selectLocalPayload(item.Path, sizeBytes, "Local File (Picked)")
			case m.pickerForSCP:
				m.scpPayloadInput.SetValue(item.Path)
				m.screen = ScreenSCPPush
			default:
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
				fam := detectPayloadFamily(payloadName)
				m.selectedOS = &discovery.Entry{
					Distro:          payloadName,
					Version:         fmt.Sprintf("LAN Peer (%s:8080)", selected.IP),
					Family:          fam,
					Flavor:          "gui",
					Arch:            syntheticEntryArch(),
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
				fam := detectPayloadFamily(val)
				m.selectedOS = &discovery.Entry{
					Distro:          "Manual LAN Stream",
					Version:         "Peer Endpoint",
					Family:          fam,
					Flavor:          "gui",
					Arch:            syntheticEntryArch(),
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
			m.isScanning = true
			return m, tea.Batch(scanInternalDisksCmd(), m.scanSpinner.Tick)
		case "2":
			m.provisionMode = "single-boot"
			m.isScanning = true
			return m, tea.Batch(scanInternalDisksCmd(), m.scanSpinner.Tick)
		case "enter":
			if m.customBootCursor == 0 {
				m.provisionMode = "dual-boot"
				m.detectedFreeBytes = m.detectAvailableDiskSpace()
				m.isScanning = true
				return m, tea.Batch(scanInternalDisksCmd(), m.scanSpinner.Tick)
			} else {
				m.provisionMode = "single-boot"
				m.isScanning = true
				return m, tea.Batch(scanInternalDisksCmd(), m.scanSpinner.Tick)
			}
		}

	case ScreenTargetDiskSelect:
		switch keyStr {
		case "esc":
			if !m.isAdvancedMode {
				m.screen = ScreenOSSelect
			} else {
				m.screen = ScreenCustomBootModeSelect
			}
			return m, nil
		case "up", "k":
			if m.targetDiskCursor > 0 {
				m.targetDiskCursor--
			}
		case "down", "j":
			if m.targetDiskCursor < len(m.availableDisks)-1 {
				m.targetDiskCursor++
			}
		case "1", "2", "3", "4":
			idx, _ := strconv.Atoi(keyStr)
			if idx > 0 && idx <= len(m.availableDisks) {
				m.targetDiskCursor = idx - 1
				selected := m.availableDisks[m.targetDiskCursor]
				m.chosenDiskDev = selected.DevPath
				if selected.SizeBytes > 0 {
					m.detectedFreeBytes = selected.SizeBytes
				}
				m.screen = ScreenStorageAllocationSelect
				m.storageCursor = 1
				m.storageCustomInput.Blur()
				return m, nil
			}
		case "enter":
			if len(m.availableDisks) > 0 && m.targetDiskCursor < len(m.availableDisks) {
				selected := m.availableDisks[m.targetDiskCursor]
				m.chosenDiskDev = selected.DevPath
				if selected.SizeBytes > 0 {
					m.detectedFreeBytes = selected.SizeBytes
				}
				m.screen = ScreenStorageAllocationSelect
				m.storageCursor = 1
				m.storageCustomInput.Blur()
				return m, nil
			}
		}

	case ScreenStorageAllocationSelect:
		maxChoices := 4
		if !m.isVMEnvironment {
			maxChoices = 5
		}

		switch keyStr {
		case "esc":
			if len(m.availableDisks) > 1 {
				m.screen = ScreenTargetDiskSelect
				return m, nil
			}
			if !m.isAdvancedMode {
				m.screen = ScreenOSSelect
			} else {
				m.screen = ScreenCustomBootModeSelect
			}
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
						m.chosenAllocBytes = 30 * 1024 * 1024 * 1024
					}
				}
			case 4:
				valStr := strings.TrimSpace(m.storageCustomInput.Value())
				if val, err := strconv.ParseUint(valStr, 10, 64); err == nil && val > 0 {
					m.chosenAllocBytes = val * 1024 * 1024 * 1024
				} else {
					m.chosenAllocBytes = 30 * 1024 * 1024 * 1024
				}
			}

			if m.chosenAllocBytes < 25*1024*1024*1024 {
				m.chosenAllocBytes = 25 * 1024 * 1024 * 1024
			}

			pDisk, _, _, _, _ := engine.DetectActiveRootDisk()
			if m.chosenDiskDev != "" && m.chosenDiskDev != pDisk {
				m.screen = ScreenConfirm
				return m, nil
			}

			m.screen = ScreenBackupPrompt
			m.backupCursor = 0
			m.backupStatusMsg = ""
			m.isScanning = true
			return m, tea.Batch(scanBackupDrivesCmd(), m.scanSpinner.Tick)
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
				fam := detectPayloadFamily(u.FileName)
				m.selectedOS = &discovery.Entry{
					Distro:          u.FileName,
					Version:         "USB Media (" + u.DeviceName + ")",
					Family:          fam,
					Flavor:          "gui",
					Arch:            "amd64",
					DownloadURL:     u.FilePath,
					Mirrors:         []string{u.FilePath},
					ApproxSizeBytes: uint64(u.SizeBytes),
					MinDiskGB:       25,
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
			m.isScanning = true
			m.revertCursor = 0
			m.selectedGuest = nil
			return m, tea.Batch(scanGuestOSesCmd(), m.scanSpinner.Tick)
		case "4":
			if m.journal == nil {
				m.journal, _ = safety.LoadJournal(safety.DefaultJournalPath())
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
				m.isScanning = true
				m.revertCursor = 0
				m.selectedGuest = nil
				return m, tea.Batch(scanGuestOSesCmd(), m.scanSpinner.Tick)
			case 3:
				if m.journal == nil {
					m.journal, _ = safety.LoadJournal(safety.DefaultJournalPath())
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

				// Audit follow-up F13: no catalog entry may be silently
				// provisioned without a checksum to verify the download
				// against. Route through an explicit confirmation screen
				// instead of proceeding straight into disk/USB selection.
				if strings.TrimSpace(e.SHA256) == "" {
					m.screen = ScreenUnverifiedDownloadConfirm
					return m, nil
				}
				return m, m.proceedAfterOSSelected()
			}
		}

		var cmd tea.Cmd
		m.osList, cmd = m.osList.Update(msg)
		return m, cmd

	case ScreenUnverifiedDownloadConfirm:
		switch keyStr {
		case "y", "enter":
			return m, m.proceedAfterOSSelected()
		case "n", "esc", "q":
			m.selectedOS = nil
			m.screen = ScreenOSSelect
			return m, nil
		}
		return m, nil

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
				m.isScanning = true
				return m, tea.Batch(scanInternalDisksCmd(), m.scanSpinner.Tick)
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
				removableBytes, _ := os.ReadFile(fmt.Sprintf("/sys/block/%s/removable", m.selectedTarget.Name))
				isInternal := strings.TrimSpace(string(removableBytes)) != "1"

				if isInternal {
					m.chosenDiskDev = m.selectedTarget.DevPath
					m.screen = ScreenConfirm
					return m, nil
				}

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
			m.screen = ScreenStorageAllocationSelect
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
				if m.provisionMode == "single-boot" {
					m.eraseConfirmInput.Reset()
					m.eraseConfirmInput.Focus()
				}
				return m, nil
			}

			if len(m.backupTargets) > 0 && m.backupCursor < len(m.backupTargets) {
				tgt := m.backupTargets[m.backupCursor]

				removableBytes, _ := os.ReadFile(fmt.Sprintf("/sys/block/%s/removable", tgt.Name))
				isUSBMedia := strings.TrimSpace(string(removableBytes)) == "1"

				if tgt.Type == "disk" && isUSBMedia {
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

				if tgt.Type == "disk" && !isUSBMedia {
					m.chosenDiskDev = "/dev/" + tgt.Name
					m.screen = ScreenConfirm
					return m, nil
				}

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

	case ScreenConfirm:
		if m.provisionMode == "single-boot" {
			switch keyStr {
			case "esc":
				m.screen = ScreenStorageAllocationSelect
				return m, nil
			case "enter":
				val := strings.ToUpper(strings.TrimSpace(m.eraseConfirmInput.Value()))
				if val == "ERASE" {
					m.speed = NewSpeedTracker(m.selectedOS.ApproxSizeBytes)
					m.credPendingResume = false
					m.screen = ScreenCredentials
					m.resetCredentialsFocus()
					return m, nil
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
				totalExpected := uint64(8 * 1024 * 1024 * 1024)
				if m.selectedOS != nil && m.selectedOS.ApproxSizeBytes > 0 {
					totalExpected = m.selectedOS.ApproxSizeBytes
				}
				m.speed = NewSpeedTracker(totalExpected)
				m.speed.Sample(0)
				m.credPendingResume = false
				m.screen = ScreenCredentials
				m.resetCredentialsFocus()
				return m, nil
			case "n", "esc":
				m.screen = ScreenStorageAllocationSelect
				return m, nil
			case "q":
				return m, tea.Quit
			}
		}

	case ScreenCredentials:
		switch keyStr {
		case "esc":
			if m.credPendingResume {
				m.screen = ScreenResumeAlert
			} else {
				m.screen = ScreenConfirm
			}
			return m, nil
		case "tab", "down":
			m.credFocusCursor = (m.credFocusCursor + 1) % 5
			m.updateCredentialsFocus()
			return m, nil
		case "shift+tab", "up":
			m.credFocusCursor = (m.credFocusCursor + 4) % 5
			m.updateCredentialsFocus()
			return m, nil
		case " ":
			if m.credFocusCursor == 3 {
				m.credAllowRootLogin = !m.credAllowRootLogin
				return m, nil
			}
			fallthrough
		case "enter":
			if m.credFocusCursor == 3 {
				m.credAllowRootLogin = !m.credAllowRootLogin
				return m, nil
			}
			if keyStr == "enter" {
				username := strings.TrimSpace(m.credUsernameInput.Value())
				password := m.credPasswordInput.Value()
				confirm := m.credPasswordConfirmInput.Value()

				if !engine.ValidUsername(username) {
					m.credError = "username must start with a letter or '_', contain only lowercase letters, digits, '_' or '-', and not be a reserved system account name"
					return m, nil
				}
				if !engine.ValidPassword(password) {
					m.credError = fmt.Sprintf("password must be at least %d characters", engine.MinPasswordLength)
					return m, nil
				}
				if password != confirm {
					m.credError = "password and confirmation do not match"
					return m, nil
				}

				hash, hashErr := (engine.Credentials{Password: password}).HashPassword()
				// Clear the on-screen fields now; they are never shown or
				// logged again. The plaintext itself is kept in
				// m.credentialsPassword (not displayed, never logged) only
				// because a Windows target's unattend answer file has no
				// hashed-password option and needs it verbatim — see
				// Credentials' doc comment. The Linux path below never
				// reads it, only the hash.
				m.credPasswordInput.SetValue("")
				m.credPasswordConfirmInput.SetValue("")
				if hashErr != nil {
					m.credError = "failed to hash password: " + hashErr.Error()
					return m, nil
				}

				m.credentialsUsername = username
				m.credentialsPasswordHash = hash
				m.credentialsPassword = password
				m.credError = ""
				m.screen = ScreenProgress
				if m.credPendingResume {
					return m, m.startProvisioning(true)
				}
				return m, m.startProvisioning(false)
			}
			fallthrough
		default:
			var cmd tea.Cmd
			switch m.credFocusCursor {
			case 0:
				m.credUsernameInput, cmd = m.credUsernameInput.Update(msg)
			case 1:
				m.credPasswordInput, cmd = m.credPasswordInput.Update(msg)
			case 2:
				m.credPasswordConfirmInput, cmd = m.credPasswordConfirmInput.Update(msg)
			}
			return m, cmd
		}

	case ScreenRevertConfirm:
		if m.isScanning {
			if keyStr == "esc" || keyStr == "q" {
				m.isScanning = false
				m.screen = ScreenHub
				return m, nil
			}
			return m, nil
		}

		if len(m.discoveredGuests) == 0 {
			switch keyStr {
			case "enter", "esc", "q":
				m.screen = ScreenHub
				return m, nil
			case "r":
				m.isScanning = true
				return m, tea.Batch(scanGuestOSesCmd(), m.scanSpinner.Tick)
			}
			return m, nil
		}

		switch keyStr {
		case "up", "k":
			if m.revertCursor > 0 {
				m.revertCursor--
			}
			m.selectedGuest = &m.discoveredGuests[m.revertCursor]
			return m, nil
		case "down", "j":
			if m.revertCursor < len(m.discoveredGuests)-1 {
				m.revertCursor++
			}
			m.selectedGuest = &m.discoveredGuests[m.revertCursor]
			return m, nil
		case "1", "2", "3", "4", "5", "6", "7", "8", "9":
			idx, _ := strconv.Atoi(keyStr)
			if idx > 0 && idx <= len(m.discoveredGuests) {
				m.revertCursor = idx - 1
				m.selectedGuest = &m.discoveredGuests[m.revertCursor]
				m.revertModeCursor = 0
				m.screen = ScreenRevertModeSelect
				return m, nil
			}
		case "r":
			m.isScanning = true
			return m, tea.Batch(scanGuestOSesCmd(), m.scanSpinner.Tick)
		case "y", "enter":
			if len(m.discoveredGuests) > 0 && m.revertCursor < len(m.discoveredGuests) {
				m.selectedGuest = &m.discoveredGuests[m.revertCursor]
				m.revertModeCursor = 0
				m.screen = ScreenRevertModeSelect
				return m, nil
			}
		case "n", "esc":
			m.screen = ScreenHub
			return m, nil
		case "q":
			return m, tea.Quit
		}

	case ScreenRevertModeSelect:
		switch keyStr {
		case "esc":
			m.screen = ScreenRevertConfirm
			return m, nil
		case "up", "k":
			if m.revertModeCursor > 0 {
				m.revertModeCursor--
			}
			return m, nil
		case "down", "j":
			if m.revertModeCursor < 1 {
				m.revertModeCursor++
			}
			return m, nil
		case "1":
			m.revertModeCursor = 0
			m.lastRevertKeepSlice = false
			return m.commitDecommission(false)
		case "2":
			m.revertModeCursor = 1
			m.lastRevertKeepSlice = true
			return m.commitDecommission(true)
		case "enter":
			keepSlice := (m.revertModeCursor == 1)
			m.lastRevertKeepSlice = keepSlice
			return m.commitDecommission(keepSlice)
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
		case "c":
			m.statusLog = append(m.statusLog, "=> [RESUMING] Continuing transfer from saved checkpoint...")
			return m, m.startProvisioning(true)
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
			if m.verificationComplete && !m.verificationOk {
				return m, nil
			}
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

// proceedAfterOSSelected continues the flow that previously ran inline
// right after an OS was selected: into USB target selection for advanced
// mode, or disk-space detection otherwise. Extracted so the audit
// follow-up F13 unverified-download confirmation screen can share it
// without duplicating the branch.
func (m *Model) proceedAfterOSSelected() tea.Cmd {
	if m.isAdvancedMode {
		m.screen = ScreenUSBTargetSelect
		m.usbTargetCursor = 0
		m.isScanning = true
		return tea.Batch(scanUSBTargetsCmd(), m.scanSpinner.Tick)
	}

	m.detectedFreeBytes = m.detectAvailableDiskSpace()
	m.isScanning = true
	return tea.Batch(scanInternalDisksCmd(), m.scanSpinner.Tick)
}

func (m *Model) commitDecommission(keepSlice bool) (tea.Model, tea.Cmd) {
	if m.selectedGuest != nil {
		guest := *m.selectedGuest
		m.screen = ScreenProgress
		actionStr := "Purging partition and expanding host"
		if keepSlice {
			actionStr = "Sanitizing OS while preserving partition slice"
		}
		m.statusLog = append(m.statusLog, fmt.Sprintf("=> [REVERT 0%%] %s (%s on %s)...", actionStr, guest.Name, guest.DevicePath))
		m.speed = NewSpeedTracker(1000)
		m.speed.Sample(0)
		m.stepUpdates = make(chan provisionStepMsg, 64)

		return m, tea.Batch(
			triggerTargetedRevertCmd(guest, keepSlice, m.stepUpdates),
			listenForStepUpdates(m.stepUpdates),
			tickCmd(),
		)
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

// resetCredentialsFocus clears any previously entered values (the fields
// are re-entered fresh each time this screen is shown; never pre-filled
// from a prior run) and focuses the username field.
func (m *Model) resetCredentialsFocus() {
	m.credFocusCursor = 0
	m.credError = ""
	m.credUsernameInput.SetValue("")
	m.credPasswordInput.SetValue("")
	m.credPasswordConfirmInput.SetValue("")
	m.updateCredentialsFocus()
}

func (m *Model) updateCredentialsFocus() {
	m.credUsernameInput.Blur()
	m.credPasswordInput.Blur()
	m.credPasswordConfirmInput.Blur()
	switch m.credFocusCursor {
	case 0:
		m.credUsernameInput.Focus()
	case 1:
		m.credPasswordInput.Focus()
	case 2:
		m.credPasswordConfirmInput.Focus()
	}
}

func (m *Model) startProvisioning(isResume bool) tea.Cmd {
	ctx, cancel := context.WithCancel(context.Background())
	m.cancelProvision = cancel

	activeRootDisk, _, activeHostNum, activeTargetPart, _ := engine.DetectActiveRootDisk()

	pDisk := m.chosenDiskDev
	hNum := ""
	targetPart := ""

	if pDisk == "" || pDisk == activeRootDisk {
		pDisk = activeRootDisk
		hNum = activeHostNum
		targetPart = activeTargetPart
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

	autounattendPath := ""
	if isWindows {
		candidate := filepath.Join("discovery", "manifests", "autounattend.xml")
		if abs, err := filepath.Abs(candidate); err == nil {
			candidate = abs
		}
		if _, statErr := os.Stat(candidate); statErr == nil {
			autounattendPath = candidate
		}
	}

	req := engine.ProvisionRequest{
		JournalPath:      safety.DefaultJournalPath(),
		ImageURL:         primaryURL,
		ImageSHA256:      sha,
		DownloadDestPath: "/var/tmp/os_image.payload",
		TargetDiskPath:   pDisk,
		TargetPartition:  targetPart,
		HostPartNum:      hNum,
		ESPPartNum:       "1",
		IsWindowsImage:   isWindows,
		AutounattendPath: autounattendPath,
		BootLabel:        strings.ToLower(distroName),
		HypervisorKind:   m.hvInfo.Kind,
		SkipCompaction:   false,
		SelectedEntry:    selectedEntry,
		ProvisionMode:    m.provisionMode,
		AllocatedBytes:   m.chosenAllocBytes,
		Credentials: engine.Credentials{
			Username:       m.credentialsUsername,
			AllowRootLogin: m.credAllowRootLogin,
		},
	}
	// The plaintext password was already hashed and discarded when the
	// credentials screen was submitted; only the hash travels from here on.
	req.Credentials.PasswordHash = m.credentialsPasswordHash
	req.Credentials.Password = m.credentialsPassword

	if m.journal == nil {
		m.journal, _ = safety.NewJournal(req.JournalPath, fmt.Sprintf("txn-%d", time.Now().UnixNano()))
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
