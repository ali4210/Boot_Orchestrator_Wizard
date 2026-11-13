package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"boot-orchestrator/engine"
	"boot-orchestrator/safety"
)

// selectRevertGuest picks which discovered guest OS --revert should act
// on: exactly one when discovery found exactly one, otherwise an error
// listing every candidate rather than guessing — this is a destructive
// operation (it deletes a partition and resizes another), so ambiguity
// must refuse, not pick arbitrarily. Kept separate from runHeadlessRevert
// so this decision is testable without needing to run engine discovery
// for real.
func selectRevertGuest(guests []engine.DetectedGuestOS) (engine.DetectedGuestOS, error) {
	if len(guests) == 0 {
		return engine.DetectedGuestOS{}, fmt.Errorf("no secondary OS detected to revert (nothing to do)")
	}
	if len(guests) > 1 {
		var names []string
		for _, g := range guests {
			names = append(names, fmt.Sprintf("%s (%s)", g.Name, g.DevicePath))
		}
		return engine.DetectedGuestOS{}, fmt.Errorf("multiple secondary OSes detected, refusing to guess which to revert: %s", strings.Join(names, "; "))
	}
	return guests[0], nil
}

// runHeadlessRevert is the --revert entry point: discovers secondary OSes
// the same way the TUI's revert menu does (engine.DiscoverSecondaryOSes),
// requires exactly one unambiguous candidate, and runs the identical
// engine.ExecuteDecommissionWithOptions call the TUI uses
// (ui/model.go's triggerTargetedRevertCmd) — same engine path, same safety
// gates — printing progress to stdout instead of a Bubble Tea view.
func runHeadlessRevert(yes bool, keepSlice bool) error {
	if !yes {
		return fmt.Errorf("refusing to run --revert without --yes (this flag exists so a destructive revert is never triggered by accident)")
	}

	guests, err := engine.DiscoverSecondaryOSes()
	if err != nil {
		return fmt.Errorf("discovering secondary OSes: %w", err)
	}
	guest, err := selectRevertGuest(guests)
	if err != nil {
		return err
	}

	fmt.Printf("Reverting %s on %s (host partition %s will be restored)...\n", guest.Name, guest.DevicePath, guest.RestorePartNum)

	nvramEntry := engine.ResolveBootEntryByEFIDir(guest.EFIDirName)
	params := engine.RealRevertParams{
		DiskDevice:        guest.DiskPath,
		PartitionNum:      guest.PartNum,
		HostPartNum:       guest.RestorePartNum,
		EFIDirNameToPurge: guest.EFIDirName,
		EFIBootEntryNum:   nvramEntry,
	}

	err = engine.ExecuteDecommissionWithOptions(params, keepSlice, func(msg string) {
		fmt.Println(msg)
	}, func(step string, pct float64) {
		fmt.Printf("[%3.0f%%] %s\n", pct*100, step)
	})
	if err != nil {
		return err
	}

	_ = os.Remove("orchestrator_installed_os.json")
	_ = os.Remove(filepath.Join(filepath.Dir(safety.DefaultJournalPath()), "orchestrator_installed_os.json"))
	fmt.Println("Revert complete.")
	return nil
}
