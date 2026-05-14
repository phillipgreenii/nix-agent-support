package tui

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/phillipgreenii/claude-agents-tui/internal/aggregate"
	"github.com/phillipgreenii/claude-agents-tui/internal/caffeinate"
	"github.com/phillipgreenii/claude-agents-tui/internal/cmuxstatus"
	"github.com/phillipgreenii/claude-agents-tui/internal/render"
	"github.com/phillipgreenii/claude-agents-tui/internal/session"
	"github.com/phillipgreenii/claude-agents-tui/internal/signal"
	"github.com/phillipgreenii/claude-agents-tui/internal/treestate"
)

// ModalKind selects which full-screen modal is currently open.
type ModalKind int

const (
	ModalNone ModalKind = iota
	ModalHelp
	ModalLegend
)

type Options struct {
	Tree       *aggregate.Tree
	Poller     Poller
	Interval   time.Duration
	Caffeinate *caffeinate.Manager
	CacheDir   string // used to load/save tree collapse state
	Signalers         []signal.Signaler
	AutoResumeDelay   time.Duration
	AutoResumeMessage string
	Reporter             cmuxstatus.Reporter
	SidebarIntervalTicks int
}

type Model struct {
	tree          *aggregate.Tree
	showAll       bool
	forceID       bool
	costMode      bool
	caffeinateOn  bool
	width, height     int
	selected          *aggregate.SessionView
	activeModal       ModalKind
	modalScrollOffset int
	cursor            int
	scrollOffset      int
	theme         render.Theme

	poller     Poller
	interval   time.Duration
	caffeinate *caffeinate.Manager
	lastErr    error
	anyWorking bool
	polling    bool

	autoResume        bool
	autoResumeFired   bool
	countdownTick     bool
	signalers         []signal.Signaler
	autoResumeDelay   time.Duration
	autoResumeMessage string

	reporter             cmuxstatus.Reporter
	sidebarIntervalTicks int
	tickCount            int

	cacheDir  string
	treeState *treestate.State
	pathNodes []*aggregate.PathNode
	flatRows  []render.Row

	signalLogMu   sync.Mutex
	signalLogFile io.WriteCloser // lazily opened; nil until first write
}

func NewModel(o Options) *Model {
	m := &Model{
		tree:                 o.Tree,
		poller:               o.Poller,
		interval:             o.Interval,
		caffeinate:           o.Caffeinate,
		theme:                render.NewTheme(render.DetectColors()),
		cacheDir:             o.CacheDir,
		treeState:            treestate.Load(o.CacheDir),
		signalers:            o.Signalers,
		autoResumeDelay:      o.AutoResumeDelay,
		autoResumeMessage:    o.AutoResumeMessage,
		reporter:             o.Reporter,
		sidebarIntervalTicks: o.SidebarIntervalTicks,
	}
	if m.reporter == nil {
		m.reporter = noopReporter{}
	}
	m.rebuildFlatRows()
	return m
}

func (m *Model) Init() tea.Cmd {
	if m.poller == nil || m.interval <= 0 {
		return nil
	}
	m.polling = true
	return tea.Batch(m.pollNow(), tickCmd(m.interval))
}

// rebuildFlatRows rebuilds pathNodes and flatRows from the current tree and treeState.
// Must be called after m.tree or m.treeState changes.
func (m *Model) rebuildFlatRows() {
	if m.tree == nil {
		m.pathNodes = nil
		m.flatRows = nil
		return
	}
	opts := render.TreeOpts{ShowAll: m.showAll}
	m.pathNodes = aggregate.BuildPathTree(m.tree.Dirs)
	m.flatRows = render.FlattenPathTree(m.pathNodes, m.treeState, opts)
}

// signalLog writes a single line to <cacheDir>/signal-errors.log (append mode),
// opening the file lazily on first use. Silently drops if cacheDir is empty or
// the file cannot be opened. Never panics. The caller does not provide a
// trailing newline; this method appends one.
func (m *Model) signalLog(msg string) {
	if m.cacheDir == "" {
		return
	}
	m.signalLogMu.Lock()
	defer m.signalLogMu.Unlock()
	if m.signalLogFile == nil {
		if err := os.MkdirAll(m.cacheDir, 0o755); err != nil {
			return
		}
		f, err := os.OpenFile(filepath.Join(m.cacheDir, "signal-errors.log"),
			os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return
		}
		m.signalLogFile = f
	}
	fmt.Fprintln(m.signalLogFile, msg)
}

// rowAt returns the Row at index idx in flatRows, and whether it exists.
func (m *Model) rowAt(idx int) (render.Row, bool) {
	if idx < 0 || idx >= len(m.flatRows) {
		return render.Row{}, false
	}
	return m.flatRows[idx], true
}

// selectable reports whether the cursor is allowed to land on a row.
// Blank separator rows are not selectable; sessions and path-tree nodes are.
func selectable(r render.Row) bool {
	return r.Kind != render.BlankKind
}

// nextSelectable scans rows starting at `from` in direction `dir` (+1 or -1)
// and returns the index of the first selectable row encountered. If no
// selectable row exists in that direction within bounds, it returns from
// unchanged so the caller's "stay put when at the edge" semantics work.
func nextSelectable(rows []render.Row, from, dir int) int {
	for i := from; i >= 0 && i < len(rows); i += dir {
		if selectable(rows[i]) {
			return i
		}
	}
	return from
}

func (m *Model) clampCursor() {
	n := len(m.flatRows)
	if n == 0 {
		m.cursor = 0
		return
	}
	if m.cursor >= n {
		m.cursor = n - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	if !selectable(m.flatRows[m.cursor]) {
		// Try moving up first (preserves "stay close to where you were"); fall
		// back to scanning down if nothing selectable exists above.
		up := nextSelectable(m.flatRows, m.cursor, -1)
		if selectable(m.flatRows[up]) && up != m.cursor {
			m.cursor = up
			return
		}
		down := nextSelectable(m.flatRows, m.cursor, +1)
		if selectable(m.flatRows[down]) {
			m.cursor = down
		}
	}
}

// aggregateState collapses the tree into the single state we expose on the
// cmux sidebar. Paused (rate-limit hit) wins over everything; otherwise any
// Working session promotes the aggregate to Working; otherwise Idle if any
// non-dormant session exists; otherwise Dormant.
func aggregateState(tree *aggregate.Tree) (cmuxstatus.State, time.Time) {
	if tree == nil {
		return cmuxstatus.StateUnknown, time.Time{}
	}
	if !tree.WindowResetsAt.IsZero() {
		return cmuxstatus.StatePaused, tree.WindowResetsAt
	}
	anyWorking, anyIdle := false, false
	for _, d := range tree.Dirs {
		for _, sv := range d.Sessions {
			switch sv.Status {
			case session.Working:
				anyWorking = true
			case session.Dormant:
				// ignore
			default:
				anyIdle = true
			}
		}
	}
	switch {
	case anyWorking:
		return cmuxstatus.StateWorking, time.Time{}
	case anyIdle:
		return cmuxstatus.StateIdle, time.Time{}
	default:
		return cmuxstatus.StateDormant, time.Time{}
	}
}

// noopReporter is the fallback when Options.Reporter is nil. Keeps every Model
// call non-nil-safe without forcing every caller to construct a real Reporter.
type noopReporter struct{}

func (noopReporter) Push(cmuxstatus.Snapshot) {}
func (noopReporter) Notify(string, string)    {}
func (noopReporter) Clear()                   {}

// buildSidebarSnapshot collects current TUI state into a Snapshot for push.
func (m *Model) buildSidebarSnapshot() cmuxstatus.Snapshot {
	state, resetAt := aggregateState(m.tree)
	prog, label, ok := windowProgress(m.tree, time.Now())
	return cmuxstatus.Snapshot{
		CaffeinateOn:  m.caffeinateOn,
		NudgeOn:       m.autoResume,
		State:         state,
		PausedResetAt: resetAt,
		Progress:      prog,
		ProgressLabel: label,
		HasProgress:   ok,
	}
}

// windowProgress derives (used, label, ok) from the active 5h block. When ok
// is false the caller should leave Snapshot.HasProgress false so the reporter
// skips the cmux set-progress call.
func windowProgress(tree *aggregate.Tree, now time.Time) (float64, string, bool) {
	if tree == nil {
		return 0, "", false
	}
	if !tree.WindowResetsAt.IsZero() {
		return 1.0, "5h block exhausted — waiting for reset", true
	}
	b := tree.ActiveBlock
	if b == nil {
		return 0, "", false
	}
	span := b.EndTime.Sub(b.StartTime)
	if span <= 0 {
		return 0, "", false
	}
	used := float64(now.Sub(b.StartTime)) / float64(span)
	if used < 0 {
		used = 0
	}
	if used > 1 {
		used = 1
	}
	return used, fmt.Sprintf("5h block %.0f%% used", used*100), true
}
