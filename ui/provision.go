package ui

import (
	"context"
	"errors"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"boot-orchestrator/engine"
)

type provisionStepMsg engine.StepUpdate
type provisionDoneMsg engine.ProvisionResult

type revertDoneMsg struct {
	err error
}

// runProvisionWithCancel starts the background engine pipeline with an explicit
// cancellable context so the TUI can trigger Pause or Stop on keypress.
func runProvisionWithCancel(ctx context.Context, req engine.ProvisionRequest, grubDefaultPath string) (tea.Cmd, chan provisionStepMsg) {
	updates := make(chan provisionStepMsg, 64)

	cmd := func() tea.Msg {
		result := engine.RunProvision(ctx, req, grubDefaultPath, func(u engine.StepUpdate) {
			select {
			case updates <- provisionStepMsg(u):
			default:
			}
		})
		close(updates)
		return provisionDoneMsg(result)
	}

	return cmd, updates
}

// runRealRevert executes parted deletion, efibootmgr purge, and host filesystem expansion.
func runRealRevert(disk, partNum, hostPart, efiDir, bootEntry string) tea.Cmd {
	return func() tea.Msg {
		params := engine.RealRevertParams{
			DiskDevice:        disk,
			PartitionNum:      partNum,
			HostPartNum:       hostPart,
			EFIDirNameToPurge: efiDir,
			EFIBootEntryNum:   bootEntry,
		}
		err := engine.ExecuteRealReversion(params, nil)
		return revertDoneMsg{err: err}
	}
}

func listenForStepUpdates(ch chan provisionStepMsg) tea.Cmd {
	return func() tea.Msg {
		u, ok := <-ch
		if !ok {
			return nil
		}
		return u
	}
}

func (m Model) handleProvisionStep(msg provisionStepMsg, ch chan provisionStepMsg) (Model, tea.Cmd) {
	line := msg.StepName
	if msg.Err != nil {
		line += ": ERROR: " + msg.Err.Error()
	}

	// Real-Time Dynamic Progress Bar Parser
	// Matches: "downloading: 45.2% (12.4 MB/s)"
	if strings.HasPrefix(line, "downloading:") {
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			pctStr := strings.TrimSuffix(parts[1], "%")
			if pctVal, err := strconv.ParseFloat(pctStr, 64); err == nil && pctVal >= 0 {
				if m.speed != nil && m.speed.TotalBytes > 0 {
					calcBytes := uint64((pctVal / 100.0) * float64(m.speed.TotalBytes))
					m.speed.Sample(calcBytes)
				}
			}
		}
		// In-place update to prevent terminal scrolling clutter
		if len(m.statusLog) > 0 && strings.HasPrefix(m.statusLog[len(m.statusLog)-1], "downloading:") {
			m.statusLog[len(m.statusLog)-1] = line
		} else {
			m.statusLog = append(m.statusLog, line)
		}
		return m, listenForStepUpdates(ch)
	}

	m.statusLog = append(m.statusLog, line)
	return m, listenForStepUpdates(ch)
}

func (m Model) handleProvisionDone(msg provisionDoneMsg) (Model, tea.Cmd) {
	res := engine.ProvisionResult(msg)

	// If paused cleanly by user, transition back to Hub Menu
	if errors.Is(res.OriginalError, engine.ErrDownloadPaused) {
		m.screen = ScreenHub
		m.statusLog = append(m.statusLog, "=> Installation paused by user. Progress saved to journal.")
		return m, nil
	}

	if res.Success {
		m.screen = ScreenDone
		return m, nil
	}

	m.screen = ScreenError
	m.fatalErr = res.OriginalError
	if res.RolledBack {
		if res.RollbackError != nil {
			m.statusLog = append(m.statusLog, "ROLLBACK INCOMPLETE — manual recovery required: "+res.RollbackError.Error())
		} else {
			m.statusLog = append(m.statusLog, "Failed step "+res.FailedAtStep+" — automatic rollback completed successfully.")
		}
	}
	return m, nil
}
