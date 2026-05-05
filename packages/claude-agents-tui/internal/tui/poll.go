package tui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/phillipgreenii/claude-agents-tui/internal/aggregate"
)

type Poller interface {
	Snapshot(ctx context.Context) (*aggregate.Tree, bool /*anyWorking*/, error)
}

type pollTickMsg struct{}

func tickCmd(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return pollTickMsg{} })
}

// pollTimeout must be long enough for `ccusage blocks --active --json`, which
// parses every ~/.claude/projects/**/*.jsonl transcript and routinely takes
// ~5s on a busy workstation. Too small a timeout silently kills ccusage and
// makes the 5h-block header display "unavailable".
const pollTimeout = 10 * time.Second

func (m *Model) pollNow() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), pollTimeout)
		defer cancel()
		tree, working, err := m.poller.Snapshot(ctx)
		if err != nil {
			return pollErrMsg{err: err}
		}
		return pollResultMsg{tree: tree, anyWorking: working}
	}
}

type pollResultMsg struct {
	tree       *aggregate.Tree
	anyWorking bool
}
type pollErrMsg struct{ err error }
