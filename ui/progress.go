package ui

import (
	"fmt"
	"time"
)

// SpeedTracker computes a smoothed transfer rate and ETA from periodic byte
// count samples. It's deliberately decoupled from Bubble Tea so it can be
// unit tested without spinning up a terminal program.
type SpeedTracker struct {
	TotalBytes uint64

	startedAt   time.Time
	lastSampleAt time.Time
	lastBytes   uint64

	// smoothedBps is an exponentially-weighted moving average of bytes/sec,
	// which keeps the displayed speed from jittering wildly on every sample
	// while still reacting to real sustained changes within ~2-3 samples.
	smoothedBps float64
	sampled     bool

	CurrentBytes uint64
}

// NewSpeedTracker creates a tracker for a transfer of totalBytes total size.
// If totalBytes is 0 (unknown size, e.g. a stream without Content-Length),
// ETA() will always report "unknown".
func NewSpeedTracker(totalBytes uint64) *SpeedTracker {
	now := time.Now()
	return &SpeedTracker{
		TotalBytes:   totalBytes,
		startedAt:    now,
		lastSampleAt: now,
	}
}

// Sample records a new absolute byte-count reading (not a delta) at the
// current time and updates the smoothed rate. Call this roughly every
// 100-500ms during a transfer for a responsive but stable readout.
func (s *SpeedTracker) Sample(currentBytes uint64) {
	now := time.Now()
	elapsed := now.Sub(s.lastSampleAt).Seconds()
	s.CurrentBytes = currentBytes

	if elapsed <= 0 {
		return
	}

	deltaBytes := int64(currentBytes) - int64(s.lastBytes)
	if deltaBytes < 0 {
		deltaBytes = 0 // guard against a caller passing a decreasing count
	}
	instantBps := float64(deltaBytes) / elapsed

	const alpha = 0.3 // smoothing factor: higher = more reactive, lower = smoother
	if !s.sampled {
		s.smoothedBps = instantBps
		s.sampled = true
	} else {
		s.smoothedBps = alpha*instantBps + (1-alpha)*s.smoothedBps
	}

	s.lastBytes = currentBytes
	s.lastSampleAt = now
}

// BytesPerSecond returns the current smoothed transfer rate.
func (s *SpeedTracker) BytesPerSecond() float64 {
	return s.smoothedBps
}

// PercentComplete returns 0-100, or -1 if TotalBytes is unknown (0).
func (s *SpeedTracker) PercentComplete() float64 {
	if s.TotalBytes == 0 {
		return -1
	}
	pct := float64(s.CurrentBytes) / float64(s.TotalBytes) * 100
	if pct > 100 {
		pct = 100
	}
	return pct
}

// ETA returns the estimated remaining duration. ok is false if it cannot be
// computed yet (no samples, zero speed, or unknown total size).
func (s *SpeedTracker) ETA() (d time.Duration, ok bool) {
	if s.TotalBytes == 0 || s.smoothedBps <= 0 || !s.sampled {
		return 0, false
	}
	remaining := float64(s.TotalBytes) - float64(s.CurrentBytes)
	if remaining <= 0 {
		return 0, true
	}
	secs := remaining / s.smoothedBps
	return time.Duration(secs * float64(time.Second)), true
}

// FormatBytesPerSecond renders a human-readable rate, e.g. "42.3 MB/s".
func FormatBytesPerSecond(bps float64) string {
	return FormatBytes(uint64(bps)) + "/s"
}

// FormatBytes renders a human-readable byte count, e.g. "1.2 GB".
func FormatBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	units := "KMGTPE"
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), units[exp])
}

// FormatETA renders a duration as a compact "Hh Mm Ss" style string, or
// "calculating..." / "unknown" for the not-yet-available cases.
func FormatETA(d time.Duration, ok bool) string {
	if !ok {
		return "calculating..."
	}
	if d <= 0 {
		return "done"
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	sec := int(d.Seconds()) % 60
	switch {
	case h > 0:
		return fmt.Sprintf("%dh %dm %ds", h, m, sec)
	case m > 0:
		return fmt.Sprintf("%dm %ds", m, sec)
	default:
		return fmt.Sprintf("%ds", sec)
	}
}
