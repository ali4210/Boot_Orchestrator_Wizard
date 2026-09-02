package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"boot-orchestrator/ui"
)

func main() {
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
