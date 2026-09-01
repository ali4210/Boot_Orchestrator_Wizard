package main

import (
	"fmt"
	"os"

	"boot-orchestrator/discovery"
	"boot-orchestrator/hypervisor"
	"boot-orchestrator/safety"
)

func main() {
	info, err := hypervisor.Detect()
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	fmt.Println("=== Hypervisor ===")
	fmt.Println("Kind:", info.Kind)
	fmt.Println("IsVirtual:", info.IsVirtual)
	fmt.Println("RawVendor:", info.RawVendor)
	for _, e := range info.Evidence {
		fmt.Println(" -", e)
	}

	fw, err := safety.DetectFirmware()
	if err != nil {
		fmt.Println("firmware error:", err)
		return
	}
	fmt.Println("\n=== Firmware ===")
	fmt.Println("Mode:", fw.Mode)
	for _, e := range fw.Evidence {
		fmt.Println(" -", e)
	}

	guard, err := safety.CheckGuards("/home/claude", 5*1024*1024*1024) // pretend 5GB image
	if err != nil {
		fmt.Println("guard error:", err)
		return
	}
	fmt.Println("\n=== Disk & Power Guard ===")
	fmt.Println("HasEnoughSpace:", guard.HasEnoughSpace)
	fmt.Println("PowerOK:", guard.PowerOK)
	for _, e := range guard.Evidence {
		fmt.Println(" -", e)
	}

	fmt.Println("\n=== Discovery Catalog (embedded manifests) ===")
	cat, err := discovery.LoadEmbedded()
	if err != nil {
		fmt.Println("catalog load error:", err)
		os.Exit(1)
	}
	fmt.Printf("Loaded %d entries\n", len(cat.Entries))
	ttyLinux := cat.Filter(discovery.FamilyLinux, discovery.FlavorTTY, "", false)
	guiWin := cat.Filter(discovery.FamilyWindows, discovery.FlavorGUI, "", false)
	fmt.Printf("Linux TTY entries: %d | Windows GUI entries: %d\n", len(ttyLinux), len(guiWin))
	fuzzy := cat.FuzzyMatch("kali")
	fmt.Printf("FuzzyMatch(\"kali\") -> %d result(s):\n", len(fuzzy))
	for _, e := range fuzzy {
		fmt.Printf("  - %s (%s %s, %s/%s)\n", e.ID, e.Distro, e.Version, e.Flavor, e.Arch)
	}

	fmt.Println("\n=== Live Ubuntu Suite Scraper (archive.ubuntu.com) ===")
	suites, err := discovery.ScrapeUbuntuSuites(discovery_ctx())
	if err != nil {
		fmt.Println("scrape error:", err)
	} else {
		fmt.Printf("Found %d live suites: %v\n", len(suites), suites)
	}
}

func discovery_ctx() struct{} { return struct{}{} }
