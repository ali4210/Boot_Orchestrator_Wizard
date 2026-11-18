// Command env-check is a standalone diagnostic: prints the same
// hypervisor/firmware/disk-headroom/catalog checks preflight.Run performs
// before the real TUI/headless entry points launch (cmd/orchestrator),
// in human-readable form, for troubleshooting outside the main binary.
package main

import (
	"flag"
	"fmt"
	"os"

	"boot-orchestrator/discovery"
	"boot-orchestrator/preflight"
)

func main() {
	targetPath := flag.String("target-path", ".", "path to check disk headroom against (defaults to the current directory)")
	liveScrape := flag.Bool("live-scrape", false, "also make a live network call to archive.ubuntu.com to list current Ubuntu suites (audit 2.14: off by default so this diagnostic stays usable offline/airgapped)")
	flag.Parse()

	report, err := preflight.Run(*targetPath)
	if err != nil {
		fmt.Println("preflight error:", err)
		os.Exit(1)
	}

	fmt.Println("=== Hypervisor ===")
	fmt.Println("Kind:", report.Hypervisor.Kind)
	fmt.Println("IsVirtual:", report.Hypervisor.IsVirtual)
	fmt.Println("RawVendor:", report.Hypervisor.RawVendor)
	for _, e := range report.Hypervisor.Evidence {
		fmt.Println(" -", e)
	}

	fmt.Println("\n=== Firmware ===")
	fmt.Println("Mode:", report.Firmware.Mode)
	for _, e := range report.Firmware.Evidence {
		fmt.Println(" -", e)
	}

	fmt.Println("\n=== Disk & Power Guard ===")
	fmt.Println("HasEnoughSpace:", report.Guard.HasEnoughSpace)
	fmt.Println("PowerOK:", report.Guard.PowerOK)
	for _, e := range report.Guard.Evidence {
		fmt.Println(" -", e)
	}

	fmt.Println("\n=== Discovery Catalog (embedded manifests) ===")
	fmt.Printf("Loaded %d entries\n", report.CatalogEntries)
	if cat, catErr := discovery.LoadEmbedded(); catErr == nil {
		ttyLinux := cat.Filter(discovery.FamilyLinux, discovery.FlavorTTY, "", false)
		guiWin := cat.Filter(discovery.FamilyWindows, discovery.FlavorGUI, "", false)
		fmt.Printf("Linux TTY entries: %d | Windows GUI entries: %d\n", len(ttyLinux), len(guiWin))
		fuzzy := cat.FuzzyMatch("kali")
		fmt.Printf("FuzzyMatch(\"kali\") -> %d result(s):\n", len(fuzzy))
		for _, e := range fuzzy {
			fmt.Printf("  - %s (%s %s, %s/%s)\n", e.ID, e.Distro, e.Version, e.Flavor, e.Arch)
		}
	}

	if len(report.Warnings) > 0 {
		fmt.Println("\n=== Warnings ===")
		for _, w := range report.Warnings {
			fmt.Println(" !", w)
		}
	}
	if len(report.Blocking) > 0 {
		fmt.Println("\n=== Blocking issues (would refuse to launch the real TUI by default) ===")
		for _, b := range report.Blocking {
			fmt.Println(" X", b)
		}
	}

	if *liveScrape {
		fmt.Println("\n=== Live Ubuntu Suite Scraper (archive.ubuntu.com) — explicitly requested via --live-scrape ===")
		suites, err := preflight.RunLiveSuiteCheck()
		if err != nil {
			fmt.Println("scrape error:", err)
		} else {
			fmt.Printf("Found %d live suites: %v\n", len(suites), suites)
		}
	} else {
		fmt.Println("\n(Skipped the live archive.ubuntu.com suite scrape — pass --live-scrape to run it.)")
	}

	if len(report.Blocking) > 0 {
		os.Exit(1)
	}
}
