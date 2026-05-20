package tui

import tea "github.com/charmbracelet/bubbletea"

func isQuit(msg tea.KeyMsg) bool {
	switch msg.String() {
	case "q", "ctrl+c":
		return true
	}
	return false
}
