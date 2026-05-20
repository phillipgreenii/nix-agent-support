package tui

import (
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/caffeinate"
	"github.com/phillipgreenii/pa-monitor/internal/cmuxstatus"
	"github.com/phillipgreenii/pa-monitor/internal/render"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/pa-monitor/internal/signal"
	"github.com/phillipgreenii/pa-monitor/internal/core/treestate"
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
	ErrorLogger          *ErrorLogger
	// OnCaffeinateToggle, when non-nil, is called whenever the user
	// toggles caffeinate via the C keybinding. Used by --remote mode to
	// dispatch the Caffeinate RPC against the daemon instead of (or in
	// addition to) the local Manager.
	OnCaffeinateToggle func(on bool)
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

	errorLogger *ErrorLogger

	onCaffeinateToggle func(bool)
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
		onCaffeinateToggle:   o.OnCaffeinateToggle,
	}
	if m.reporter == nil {
		m.reporter = noopReporter{}
	}
	m.errorLogger = o.ErrorLogger
	if m.errorLogger == nil && o.CacheDir != "" {
		m.errorLogger = &ErrorLogger{CacheDir: o.CacheDir}
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

// signalLog forwards to the shared ErrorLogger. Kept as a Model method so
// existing callers in update.go don't need to thread the logger explicitly.
func (m *Model) signalLog(msg string) {
	m.errorLogger.LogString(msg)
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

// windowProgress returns (used, label, ok) for the cmux progress bar. The
// metric matches the TUI header's 5h block percent: cost / plan cap, with the
// raw (unclamped) percent in the label and the bar value clamped to [0,1].
// When PlanCapUSD <= 0 we mirror the TUI's "plan cap unknown" branch and set
// ok=false so the reporter skips the bar entirely. Paused (rate-limit) state
// forces 1.0 with an explanatory label.
func windowProgress(tree *aggregate.Tree, now time.Time) (float64, string, bool) {
	_ = now // retained for signature parity; the cost-based metric doesn't depend on wall-clock
	if tree == nil {
		return 0, "", false
	}
	if !tree.WindowResetsAt.IsZero() {
		return 1.0, "5h block exhausted — waiting for reset", true
	}
	b := tree.ActiveBlock
	if b == nil || tree.PlanCapUSD <= 0 {
		return 0, "", false
	}
	pct := 100 * b.CostUSD / tree.PlanCapUSD
	v := pct / 100
	if v < 0 {
		v = 0
	}
	if v > 1 {
		v = 1
	}
	return v, fmt.Sprintf("5h block %.0f%% of cap", pct), true
}
