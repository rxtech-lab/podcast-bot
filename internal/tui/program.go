package tui

import (
	tea "github.com/charmbracelet/bubbletea"
)

// Run starts the TUI program. It returns the program (for Send) and a wait
// function that blocks until the program exits.
func Run(m *Model) (*tea.Program, func() error) {
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	wait := func() error {
		_, err := p.Run()
		return err
	}
	return p, wait
}
