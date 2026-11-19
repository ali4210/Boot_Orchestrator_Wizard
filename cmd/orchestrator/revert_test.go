package main

import (
	"strings"
	"testing"

	"boot-orchestrator/engine"
)

func TestSelectRevertGuest_RefusesWhenNoneFound(t *testing.T) {
	_, err := selectRevertGuest(nil)
	if err == nil {
		t.Fatalf("expected an error when no guest is found")
	}
}

func TestSelectRevertGuest_PicksTheOnlyCandidate(t *testing.T) {
	guests := []engine.DetectedGuestOS{
		{Name: "Parrot OS", DevicePath: "/dev/sda3"},
	}
	got, err := selectRevertGuest(guests)
	if err != nil {
		t.Fatalf("selectRevertGuest: %v", err)
	}
	if got.DevicePath != "/dev/sda3" {
		t.Fatalf("unexpected guest selected: %+v", got)
	}
}

// TestSelectRevertGuest_RefusesToGuessAmongMultiple is the regression test
// for --revert's safety requirement: with more than one candidate, this
// must refuse rather than picking one arbitrarily, since the operation is
// destructive (it deletes a partition and resizes another).
func TestSelectRevertGuest_RefusesToGuessAmongMultiple(t *testing.T) {
	guests := []engine.DetectedGuestOS{
		{Name: "Parrot OS", DevicePath: "/dev/sda3"},
		{Name: "Windows 11", DevicePath: "/dev/sda4"},
	}
	_, err := selectRevertGuest(guests)
	if err == nil {
		t.Fatalf("expected an error when multiple guests are found")
	}
	if !strings.Contains(err.Error(), "/dev/sda3") || !strings.Contains(err.Error(), "/dev/sda4") {
		t.Fatalf("expected the error to list both candidates, got: %v", err)
	}
}

func TestRunHeadlessRevert_RefusesWithoutYes(t *testing.T) {
	err := runHeadlessRevert(false, false)
	if err == nil {
		t.Fatalf("expected --revert to refuse without --yes")
	}
	if !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("expected the error to mention --yes, got: %v", err)
	}
}
