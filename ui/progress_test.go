package ui

import (
	"testing"
	"time"
)

func TestFormatBytes(t *testing.T) {
	cases := map[uint64]string{
		0:                 "0 B",
		512:               "512 B",
		1024:              "1.0 KB",
		1536:              "1.5 KB",
		1024 * 1024:       "1.0 MB",
		5 * 1024 * 1024 * 1024: "5.0 GB",
	}
	for input, want := range cases {
		if got := FormatBytes(input); got != want {
			t.Errorf("FormatBytes(%d) = %q, want %q", input, got, want)
		}
	}
}

func TestFormatETA(t *testing.T) {
	if got := FormatETA(0, false); got != "calculating..." {
		t.Errorf("FormatETA(unknown) = %q", got)
	}
	if got := FormatETA(45*time.Second, true); got != "45s" {
		t.Errorf("FormatETA(45s) = %q", got)
	}
	if got := FormatETA(125*time.Second, true); got != "2m 5s" {
		t.Errorf("FormatETA(125s) = %q", got)
	}
	if got := FormatETA(3725*time.Second, true); got != "1h 2m 5s" {
		t.Errorf("FormatETA(3725s) = %q", got)
	}
}

func TestSpeedTrackerPercentAndETA(t *testing.T) {
	total := uint64(1000)
	s := NewSpeedTracker(total)

	if pct := s.PercentComplete(); pct != 0 {
		t.Errorf("initial PercentComplete = %v, want 0", pct)
	}

	// Simulate two samples 100ms apart with a known delta, bypassing real
	// wall-clock sleep by manipulating the internal timestamp directly.
	s.lastSampleAt = time.Now().Add(-100 * time.Millisecond)
	s.Sample(250) // 250 bytes in ~100ms => ~2500 B/s

	if s.PercentComplete() != 25 {
		t.Errorf("PercentComplete after 250/1000 = %v, want 25", s.PercentComplete())
	}
	if s.BytesPerSecond() <= 0 {
		t.Errorf("BytesPerSecond should be > 0 after a sample, got %v", s.BytesPerSecond())
	}

	_, ok := s.ETA()
	if !ok {
		t.Errorf("ETA should be computable after a sample with known total size")
	}
}

func TestSpeedTrackerUnknownTotal(t *testing.T) {
	s := NewSpeedTracker(0)
	if pct := s.PercentComplete(); pct != -1 {
		t.Errorf("PercentComplete with unknown total = %v, want -1", pct)
	}
	s.lastSampleAt = time.Now().Add(-100 * time.Millisecond)
	s.Sample(500)
	if _, ok := s.ETA(); ok {
		t.Errorf("ETA should be unavailable when total size is unknown")
	}
}

func TestSpeedTrackerGuardsAgainstDecreasingBytes(t *testing.T) {
	s := NewSpeedTracker(1000)
	s.lastSampleAt = time.Now().Add(-100 * time.Millisecond)
	s.Sample(500)
	rateAfterFirst := s.BytesPerSecond()

	// A caller passing a smaller count than before (e.g. a bug upstream)
	// should not produce a negative rate.
	s.lastSampleAt = time.Now().Add(-100 * time.Millisecond)
	s.Sample(300)
	if s.BytesPerSecond() < 0 {
		t.Errorf("BytesPerSecond went negative after a decreasing sample: %v (was %v)", s.BytesPerSecond(), rateAfterFirst)
	}
}
