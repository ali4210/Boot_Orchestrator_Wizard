package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"boot-orchestrator/ui"
)

func main() {
	p := tea.NewProgram(ui.NewModel(), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "error running TUI:", err)
		os.Exit(1)
	}
}
