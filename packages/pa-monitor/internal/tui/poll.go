package tui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
)

type Poller interface {
	Snapshot(ctx context.Context) (*aggregate.Tree, bool /*anyWorking*/, error)
}

// MetaPoller is an optional extension: pollers that have access to richer
// daemon-state (e.g. RemotePoller) implement this so the TUI can read
// view-state that doesn't live on the aggregate.Tree.
type MetaPoller interface {
	LastAutoResumeEnabled() bool
	LastAutoResumeDelay() time.Duration
	LastCaffeinateActive() bool
	LastDaemonVersion() string
	// LastDaemonNow returns the daemon-side wallclock observed on the most
	// recent GetState. Used by the Model to break optimistic-update races:
	// an in-flight poll that captured state BEFORE a user toggle would
	// otherwise overwrite the optimistic value with stale data; comparing
	// daemonNow against the user-action time tells us whether to adopt.
	LastDaemonNow() time.Time
}

// DaemonMeta carries the values pulled off a MetaPoller and threaded through
// to the Model via pollResultMsg. Zero value means "unknown / not daemon-backed".
type DaemonMeta struct {
	AutoResumeEnabled bool
	AutoResumeDelay   time.Duration
	CaffeinateActive  bool
	DaemonVersion     string
	DaemonNow         time.Time
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
		var meta DaemonMeta
		if mp, ok := m.poller.(MetaPoller); ok {
			meta = DaemonMeta{
				AutoResumeEnabled: mp.LastAutoResumeEnabled(),
				AutoResumeDelay:   mp.LastAutoResumeDelay(),
				CaffeinateActive:  mp.LastCaffeinateActive(),
				DaemonVersion:     mp.LastDaemonVersion(),
				DaemonNow:         mp.LastDaemonNow(),
			}
		}
		return pollResultMsg{tree: tree, anyWorking: working, meta: meta}
	}
}

type pollResultMsg struct {
	tree       *aggregate.Tree
	anyWorking bool
	meta       DaemonMeta
}
type pollErrMsg struct{ err error }
