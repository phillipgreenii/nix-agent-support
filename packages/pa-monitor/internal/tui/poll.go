package tui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	pb "github.com/phillipgreenii/pa-monitor/internal/proto"
	"github.com/phillipgreenii/pa-monitor/internal/render"
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
	// Two-indicator caffeinate state (D6): MODE (the user toggle) and the
	// PROCESS state + grace-remaining countdown.
	LastCaffeinateMode() bool
	LastCaffeinateProcess() pb.CaffeinateProcess
	LastCaffeinateGraceRemaining() time.Duration
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
	// Two-indicator caffeinate state (D6).
	CaffeinateMode           bool
	CaffeinateProcess        render.CaffeinateProcess
	CaffeinateGraceRemaining time.Duration
	DaemonVersion            string
	DaemonNow                time.Time
}

// caffeinateProcessFromProto maps the wire CaffeinateProcess enum onto the
// render-layer display state, keeping render a leaf package free of proto.
func caffeinateProcessFromProto(p pb.CaffeinateProcess) render.CaffeinateProcess {
	switch p {
	case pb.CaffeinateProcess_CAFFEINATE_PROCESS_ON:
		return render.CaffeinateProcessOn
	case pb.CaffeinateProcess_CAFFEINATE_PROCESS_GRACE:
		return render.CaffeinateProcessGrace
	case pb.CaffeinateProcess_CAFFEINATE_PROCESS_ERROR:
		return render.CaffeinateProcessError
	default:
		return render.CaffeinateProcessOff
	}
}

type pollTickMsg struct{}

func tickCmd(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return pollTickMsg{} })
}

// pollTimeout must be long enough for the native cost scan, which parses every
// ~/.claude/projects/**/*.jsonl transcript and can take a few seconds on a busy
// workstation. Too small a timeout aborts the scan and makes the 5h-block
// header display "unavailable".
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
				AutoResumeEnabled:        mp.LastAutoResumeEnabled(),
				AutoResumeDelay:          mp.LastAutoResumeDelay(),
				CaffeinateActive:         mp.LastCaffeinateActive(),
				CaffeinateMode:           mp.LastCaffeinateMode(),
				CaffeinateProcess:        caffeinateProcessFromProto(mp.LastCaffeinateProcess()),
				CaffeinateGraceRemaining: mp.LastCaffeinateGraceRemaining(),
				DaemonVersion:            mp.LastDaemonVersion(),
				DaemonNow:                mp.LastDaemonNow(),
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
