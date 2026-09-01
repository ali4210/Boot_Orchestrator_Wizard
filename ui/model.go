package ui

import (
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"boot-orchestrator/discovery"
	"boot-orchestrator/hypervisor"
	"boot-orchestrator/safety"
)

type Screen int

const (
	ScreenWelcome Screen = iota
	ScreenEnvironmentCheck
	ScreenOSSelect
	ScreenFlavorFilter
	ScreenConfirm
	ScreenProgress
	ScreenDone
	ScreenError
)

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
	screen Screen
	width  int
	height int

	hvInfo      hypervisor.Info
	fwInfo      safety.FirmwareInfo
	guardReport safety.GuardReport
	envErr      error
	envChecked  bool

	catalog     *discovery.Catalog
	osList      list.Model
	selectedOS  *discovery.Entry
	searchInput textinput.Model

	progressBar progress.Model
	speed       *SpeedTracker
	statusLog   []string

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

	l := list.New(nil, delegate, 76, 18)
	l.Title = "UNIVERSAL OPERATING SYSTEM CATALOG"
	l.Styles.Title = StyleSubTitle
	l.SetShowStatusBar(true)
	l.SetFilteringEnabled(true)
	l.SetShowHelp(false)
	l.SetShowPagination(true)

	if err == nil && cat != nil {
		items := make([]list.Item, 0, len(cat.Entries))
		for _, e := range cat.Entries {
			items = append(items, osListItem{entry: e})
		}
		l.SetItems(items)
	}

	ti := textinput.New()
	ti.Placeholder = "Type to filter catalog..."
	ti.CharLimit = 64

	pb := progress.New(
		progress.WithGradient("#00F0FF", "#00E676"),
		progress.WithWidth(70),
	)

	return Model{
		screen:      ScreenWelcome,
		catalog:     cat,
		osList:      l,
		searchInput: ti,
		progressBar: pb,
		fatalErr:    err,
	}
}

func (m Model) Init() tea.Cmd {
	return nil
}

type envCheckDoneMsg struct {
	hv       hypervisor.Info
	hvErr    error
	fw       safety.FirmwareInfo
	fwErr    error
	guard    safety.GuardReport
	guardErr error
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

func tickCmd() tea.Cmd {
	return tea.Tick(time.Millisecond*250, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

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
		m.osList.SetSize(listWidth, listHeight)
		m.progressBar.Width = listWidth
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			if m.screen != ScreenOSSelect {
				return m, tea.Quit
			}
		}
		return m.updateForScreen(msg)

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

	case tickMsg:
		if m.screen == ScreenProgress {
			return m, tickCmd()
		}
		return m, nil
	}

	return m, nil
}

func (m Model) updateForScreen(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.screen {

	case ScreenWelcome:
		if msg.String() == "enter" {
			m.screen = ScreenEnvironmentCheck
			return m, runEnvironmentChecks(".", 5*1024*1024*1024)
		}

	case ScreenEnvironmentCheck:
		if msg.String() == "enter" && m.envChecked {
			m.screen = ScreenOSSelect
		}

	case ScreenOSSelect:
		var cmd tea.Cmd
		m.osList, cmd = m.osList.Update(msg)
		if msg.String() == "enter" {
			if item, ok := m.osList.SelectedItem().(osListItem); ok {
				e := item.entry
				m.selectedOS = &e
				m.screen = ScreenConfirm
			}
		}
		if msg.String() == "esc" {
			m.screen = ScreenWelcome
		}
		return m, cmd

	case ScreenConfirm:
		switch msg.String() {
		case "y", "enter":
			m.screen = ScreenProgress
			m.speed = NewSpeedTracker(m.selectedOS.ApproxSizeBytes)
			return m, tickCmd()
		case "n", "esc":
			m.screen = ScreenOSSelect
		}

	case ScreenDone, ScreenError:
		if msg.String() == "enter" {
			return m, tea.Quit
		}
	}

	return m, nil
}