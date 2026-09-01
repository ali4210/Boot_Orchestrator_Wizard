package ui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"

	"boot-orchestrator/engine"
)

// provisionStepMsg is emitted once per engine.StepUpdate, letting the
// scrolling status journal in the progress view append a line per step
// without waiting for the whole pipeline to finish.
type provisionStepMsg engine.StepUpdate

// provisionDoneMsg is emitted once when engine.RunProvision returns,
// success or failure.
type provisionDoneMsg engine.ProvisionResult

// runProvision starts the full transactional pipeline off the UI thread.
// It returns a tea.Cmd that itself starts a goroutine feeding progress
// messages back through a channel — Bubble Tea has no native "stream of
// messages from one command" primitive, so we bridge it with a buffered
// channel plus a follow-up listen command, matching the tick-based polling
// style already used for the environment-check screen.
//
// req is built by the caller (ScreenConfirm's transition into
// ScreenProgress) from the user's OS selection, discovered partition
// target, and detected hypervisor/firmware info already sitting in Model.
func runProvision(req engine.ProvisionRequest, grubDefaultPath string) (tea.Cmd, chan provisionStepMsg) {
	updates := make(chan provisionStepMsg, 64)

	cmd := func() tea.Msg {
		ctx := context.Background()
		result := engine.RunProvision(ctx, req, grubDefaultPath, func(u engine.StepUpdate) {
			// Non-blocking send: if the UI isn't reading fast enough, drop
			// intermediate progress lines rather than deadlocking the
			// provisioning pipeline itself — the final provisionDoneMsg is
			// what actually matters for correctness, this channel is purely
			// cosmetic status text.
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

// listenForStepUpdates converts channel receives into tea.Msg sends so the
// Update loop can append each step to the scrolling status journal as it
// arrives, rather than only learning about progress when the whole
// pipeline finishes.
func listenForStepUpdates(ch chan provisionStepMsg) tea.Cmd {
	return func() tea.Msg {
		u, ok := <-ch
		if !ok {
			return nil
		}
		return u
	}
}

// handleProvisionStep appends a line to the model's status journal and
// re-arms listenForStepUpdates so subsequent steps keep arriving. Call this
// from Update() wherever provisionStepMsg is matched.
func (m Model) handleProvisionStep(msg provisionStepMsg, ch chan provisionStepMsg) (Model, tea.Cmd) {
	line := msg.StepName
	if msg.Err != nil {
		line += ": ERROR: " + msg.Err.Error()
	}
	m.statusLog = append(m.statusLog, line)
	return m, listenForStepUpdates(ch)
}

// handleProvisionDone transitions to ScreenDone or ScreenError depending on
// the pipeline's outcome, and records rollback status so ScreenError can
// tell the user plainly whether the system was left in a known-safe state.
func (m Model) handleProvisionDone(msg provisionDoneMsg) (Model, tea.Cmd) {
	res := engine.ProvisionResult(msg)
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
