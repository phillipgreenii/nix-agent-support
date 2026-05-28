package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
)

// SignalLogForTest is a whitebox test hook exposing signalLog. Not for production use.
func (m *Model) SignalLogForTest(msg string) { m.signalLog(msg) }

// PollResultForTest constructs the unexported pollResultMsg for tests.
func PollResultForTest(tree *aggregate.Tree, anyWorking bool) tea.Msg {
	return pollResultMsg{tree: tree, anyWorking: anyWorking}
}

// SetActiveModalForTest forces the modal selection for whitebox view tests.
func (m *Model) SetActiveModalForTest(kind ModalKind) { m.activeModal = kind }

// SetSizeForTest sets Model.width/height for whitebox view tests.
func (m *Model) SetSizeForTest(w, h int) { m.width = w; m.height = h }
