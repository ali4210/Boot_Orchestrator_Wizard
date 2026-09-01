package safety

import "fmt"

// GuardReport is the result of pre-flight power + storage headroom checks.
type GuardReport struct {
	AvailableBytes   uint64
	RequiredBytes    uint64
	HasEnoughSpace   bool
	OnACPower        bool
	BatteryPercent   int // -1 if no battery present (desktop) or unreadable
	PowerOK          bool
	Evidence         []string
}

// requiredHeadroomBytes returns imageFootprintBytes + 25GB safety margin,
// per the blueprint's assertion: Space >= ImageFootprint + 25GB.
func requiredHeadroomBytes(imageFootprintBytes uint64) uint64 {
	const safetyMarginBytes = 25 * 1024 * 1024 * 1024 // 25 GB
	return imageFootprintBytes + safetyMarginBytes
}

// CheckGuards runs the power + storage headroom pre-flight checks for a
// target install path and a given OS image footprint size.
func CheckGuards(targetPath string, imageFootprintBytes uint64) (GuardReport, error) {
	report := GuardReport{
		RequiredBytes: requiredHeadroomBytes(imageFootprintBytes),
		BatteryPercent: -1,
	}

	avail, err := availableDiskBytes(targetPath)
	if err != nil {
		return report, fmt.Errorf("checking disk headroom: %w", err)
	}
	report.AvailableBytes = avail
	report.HasEnoughSpace = avail >= report.RequiredBytes
	report.Evidence = append(report.Evidence, fmt.Sprintf(
		"available=%d bytes, required=%d bytes (footprint %d + 25GB margin)",
		avail, report.RequiredBytes, imageFootprintBytes,
	))

	onAC, pct, err := powerStatus()
	if err != nil {
		report.Evidence = append(report.Evidence, "power status unreadable: "+err.Error())
		// Fail closed: if we can't confirm power state, don't claim it's OK.
		report.PowerOK = false
		return report, nil
	}
	report.OnACPower = onAC
	report.BatteryPercent = pct
	// Per blueprint: AC connection OR >50% battery.
	report.PowerOK = onAC || pct > 50
	report.Evidence = append(report.Evidence, fmt.Sprintf("onAC=%v, battery=%d%%", onAC, pct))

	return report, nil
}
