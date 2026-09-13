// Package ui — provision.go manages background provisioning, real-time telemetry,
// monotonic progress interpolation, step updating, and state recovery.
package ui

import (
	"context"
	"errors"
	"os"
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

// runRealRevert connects ExecuteRealReversion to Bubble Tea's event loop via a streaming channel.
func runRealRevert(disk, partNum, hostPart, efiDir, bootEntry string) (tea.Cmd, chan provisionStepMsg) {
	updates := make(chan provisionStepMsg, 64)

	cmd := func() tea.Msg {
		params := engine.RealRevertParams{
			DiskDevice:        disk,
			PartitionNum:      partNum,
			HostPartNum:       hostPart,
			EFIDirNameToPurge: efiDir,
			EFIBootEntryNum:   bootEntry,
		}

		err := engine.ExecuteRealReversionWithProgress(params, func(msg string) {
			select {
			case updates <- provisionStepMsg{StepName: msg, Done: false}:
			default:
			}
		}, func(step string, pct float64) {
			select {
			case updates <- provisionStepMsg{StepName: step, Done: pct >= 1.0}:
			default:
			}
		})

		close(updates)
		return revertDoneMsg{err: err}
	}

	return cmd, updates
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

	lineLower := strings.ToLower(line)

	if m.speed == nil || m.speed.TotalBytes == 0 {
		m.speed = NewSpeedTracker(1000)
	}

	// 1. Dynamic Extraction Percentage Parser (unsquashfs / rootfs tree unpacking)
	if strings.Contains(lineLower, "extracting") || strings.Contains(lineLower, "unpacking") || strings.Contains(lineLower, "unsquashfs") {
		parts := strings.Fields(line)
		for _, part := range parts {
			if strings.HasSuffix(part, "%") {
				pctStr := strings.TrimSuffix(part, "%")
				if pctVal, err := strconv.ParseFloat(pctStr, 64); err == nil && pctVal >= 0 {
					overallPct := 0.40 + (pctVal/100.0)*0.45
					calcBytes := uint64(overallPct * float64(m.speed.TotalBytes))
					if m.speed.CurrentBytes < calcBytes {
						m.speed.Sample(calcBytes)
					}
					if len(m.statusLog) > 0 && (strings.Contains(strings.ToLower(m.statusLog[len(m.statusLog)-1]), "extracting") || strings.Contains(strings.ToLower(m.statusLog[len(m.statusLog)-1]), "unsquashfs")) {
						m.statusLog[len(m.statusLog)-1] = line
						return m, listenForStepUpdates(ch)
					}
				}
				break
			}
		}
	}

	// 2. Reversion Step Parser (Option 3 Progress Driver)
	if strings.Contains(lineLower, "[revert") {
		parts := strings.Fields(line)
		for _, part := range parts {
			if strings.HasSuffix(part, "%]") {
				raw := strings.TrimPrefix(part, "[revert")
				raw = strings.TrimSuffix(raw, "%]")
				raw = strings.TrimPrefix(raw, " ")
				if pctVal, err := strconv.ParseFloat(raw, 64); err == nil && pctVal >= 0 {
					calcBytes := uint64((pctVal / 100.0) * float64(m.speed.TotalBytes))
					if m.speed.CurrentBytes < calcBytes {
						m.speed.Sample(calcBytes)
					}
				}
				break
			}
		}
		m.statusLog = append(m.statusLog, line)
		return m, listenForStepUpdates(ch)
	}

	// 3. Network Stream & USB Payload Staging Parser (Full 0% to 100% scale)
	isStaging := strings.Contains(lineLower, "payload staging:")
	isDownload := strings.HasPrefix(lineLower, "downloading:")
	isCompress := strings.HasPrefix(lineLower, "compressing:")

	if isStaging || isDownload || isCompress {
		parts := strings.Fields(line)
		for _, part := range parts {
			if strings.HasSuffix(part, "%") {
				pctStr := strings.TrimSuffix(part, "%")
				if pctVal, err := strconv.ParseFloat(pctStr, 64); err == nil && pctVal >= 0 {
					var overallPct float64
					if isStaging {
						// Payload staging to USB runs 0% -> 100%
						overallPct = pctVal / 100.0
					} else {
						// Remote downloads consume first half of pipeline
						overallPct = (pctVal / 100.0) * 0.50
					}

					calcBytes := uint64(overallPct * float64(m.speed.TotalBytes))
					m.speed.Sample(calcBytes)
				}
				break
			}
		}

		prefix := "downloading:"
		if isCompress {
			prefix = "compressing:"
		} else if isStaging {
			prefix = "payload staging:"
		}

		if len(m.statusLog) > 0 && strings.HasPrefix(strings.ToLower(m.statusLog[len(m.statusLog)-1]), prefix) {
			m.statusLog[len(m.statusLog)-1] = line
		} else {
			m.statusLog = append(m.statusLog, line)
		}
		return m, listenForStepUpdates(ch)
	}

	// 4. Monotonic Milestone Progress Steps
	var stepTarget float64
	switch {
	case strings.Contains(lineLower, "verifying cryptographic payload"):
		stepTarget = 0.52
	case strings.Contains(lineLower, "evaluating hardware block geometry") || strings.Contains(lineLower, "partition-shrink"):
		stepTarget = 0.55
	case strings.Contains(lineLower, "partition carved") || strings.Contains(lineLower, "physical dual-boot partition carved"):
		stepTarget = 0.58
	case strings.Contains(lineLower, "rootfs deploy") || strings.Contains(lineLower, "image-deploy"):
		stepTarget = 0.65
	case strings.Contains(lineLower, "persistence") || strings.Contains(lineLower, "inspecting target partition"):
		stepTarget = 0.88
	case strings.Contains(lineLower, "configuring autonomous dual-boot") || strings.Contains(lineLower, "bootloader-configure"):
		stepTarget = 0.94
	case strings.Contains(lineLower, "completed successfully") || strings.Contains(lineLower, "complete 100%"):
		stepTarget = 1.00
	}

	if stepTarget > 0 && m.speed.TotalBytes > 0 {
		targetBytes := uint64(stepTarget * float64(m.speed.TotalBytes))
		if targetBytes > m.speed.CurrentBytes {
			m.speed.Sample(targetBytes)
		}
	}

	m.statusLog = append(m.statusLog, line)
	return m, listenForStepUpdates(ch)
}

func (m Model) handleProvisionDone(msg provisionDoneMsg) (Model, tea.Cmd) {
	res := engine.ProvisionResult(msg)

	if errors.Is(res.OriginalError, engine.ErrDownloadPaused) {
		m.screen = ScreenHub
		m.statusLog = append(m.statusLog, "=> Installation paused by user. Progress saved to journal.")
		return m, nil
	}

	if res.Success {
		_ = engine.PurgeStagedPayload()
		_ = engine.PurgeIntermediatePayloads("/mnt/orch_usb_storage")

		m.hasStagedPayload = false
		m.stagedPayloadSize = 0
		m.stagedPayloadPath = ""

		if m.speed != nil && m.speed.TotalBytes > 0 {
			m.speed.Sample(m.speed.TotalBytes)
		}

		m.statusLog = append(m.statusLog, "=> [POST-INSTALL CLEANUP] Autonomous purge completed: intermediate payloads reclaimed.")
		m.screen = ScreenDone
		return m, nil
	}

	_ = os.Remove("/var/tmp/os_image.payload.part")
	_ = os.Remove("/tmp/os_image.payload.part")

	m.screen = ScreenError
	m.fatalErr = res.OriginalError
	if res.RolledBack {
		if res.RollbackError != nil {
			m.statusLog = append(m.statusLog, "ROLLBACK INCOMPLETE — manual recovery required: "+res.RollbackError.Error())
		} else {
			m.statusLog = append(m.statusLog, "Failed step "+res.FailedAtStep+" — automatic rollback completed safely.")
		}
	}
	return m, nil
}
