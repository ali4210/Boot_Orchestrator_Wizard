package main

import (
	"flag"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"boot-orchestrator/ui"
)

// CustomISOPath holds any direct image path provided via the command line
var CustomISOPath string

func main() {
	isoFlag := flag.String("iso", "", "Direct local path to an ISO or raw disk image")
	flag.Parse()

	if *isoFlag != "" {
		CustomISOPath = *isoFlag
	}

	// Initialize TUI with Alternate Screen buffer and Mouse Cell Motion support
	p := tea.NewProgram(
		ui.NewModel(),
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)

	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Fatal error executing Orchestrator Hub: %v\n", err)
		os.Exit(1)
	}
}
