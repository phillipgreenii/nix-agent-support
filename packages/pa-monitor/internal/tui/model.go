package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/phillipgreenii/pa-monitor/internal/cmuxstatus"
	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/pa-monitor/internal/core/treestate"
	"github.com/phillipgreenii/pa-monitor/internal/render"
)

// ModalKind selects which full-screen modal is currently open.
type ModalKind int

const (
	ModalNone ModalKind = iota
	ModalHelp
	ModalLegend
)

// flashLevel selects the render style of the transient nudge flash line.
// flashInfo is a neutral confirmation (queued / cancelled); flashWarn draws
// attention to an outcome the user likely did not intend (no-match,
// working-suppressed, RPC failure).
type flashLevel int

const (
	flashInfo flashLevel = iota
	flashWarn
)

type Options struct {
	Tree                 *aggregate.Tree
	Poller               Poller
	Interval             time.Duration
	CacheDir             string // used to load/save tree collapse state
	Reporter             cmuxstatus.Reporter
	SidebarIntervalTicks int
	ErrorLogger          *ErrorLogger
	// OnCaffeinateToggle, when non-nil, is called whenever the user presses C.
	// The argument is the new desired state (true = enable). Returns a
	// tea.Cmd that performs the Caffeinate RPC; the Cmd MUST emit a
	// CaffeinateResultMsg on success or CaffeinateErrMsg on failure so the
	// Update loop can lock m.caffeinateOn to the daemon's reported state.
	// (Returning a Cmd instead of doing a fire-and-forget side effect lets
	// the TUI avoid the optimistic flip + race-guard dance, which previously
	// caused the toggle to flap on press while the daemon's tick caught up.)
	OnCaffeinateToggle func(want bool) tea.Cmd
	// OnToggleAutoResume mirrors OnCaffeinateToggle for the R keybinding.
	// Returned Cmd MUST emit AutoResumeResultMsg / AutoResumeErrMsg.
	OnToggleAutoResume func(want bool) tea.Cmd
	// OnManualNudge, when non-nil, is called whenever the user presses N.
	// selector is a gRPC-style nudge selector (e.g. "session:<id>" or
	// "path:/some/dir"). cancel is true when the nudge should be cancelled
	// (all selected sessions already have a manual intent pending). The
	// returned Cmd MUST emit a NudgeResultMsg on success or NudgeErrMsg on
	// failure so the Update loop can surface the outcome (queued / already /
	// no-match / suppressed) instead of a silent no-op.
	OnManualNudge func(selector string, cancel bool) tea.Cmd
	// Version is the TUI binary's build identifier. Displayed in the [?] modal
	// alongside the daemon's reported version. Empty string falls back to "dev".
	Version string
	// StaleAfter is the status-line rate_limits staleness window (ADR 0021 §1).
	// A captured value older than this renders as stale(age) in the 5h block row.
	// Zero disables staleness labeling.
	StaleAfter time.Duration
}

type Model struct {
	tree         *aggregate.Tree
	showAll      bool
	forceID      bool
	costMode     bool
	caffeinateOn bool
	// caffeinateProcess + caffeinateGraceRemaining are the daemon-reported
	// PROCESS indicator (D6): what the wake-assertion subprocess is doing,
	// separate from caffeinateOn (the MODE). View-only; populated from
	// pollResultMsg.meta. Not race-guarded — the user's C toggle only changes
	// the MODE, never the PROCESS, so a stale snapshot can't undo a user action.
	caffeinateProcess        render.CaffeinateProcess
	caffeinateGraceRemaining time.Duration
	width, height            int
	selected                 *aggregate.SessionView
	activeModal              ModalKind
	modalScrollOffset        int
	detailsScrollOffset      int
	cursor                   int
	scrollOffset             int
	theme                    render.Theme

	poller     Poller
	interval   time.Duration
	lastErr    error
	anyWorking bool
	polling    bool

	// daemonConnected reflects whether the most recent poll succeeded.
	// Defaults to false until the first successful pollResultMsg arrives.
	// Drives the upper-left connection indicator in the controls row so the
	// user can see at a glance when the daemon RPC dies mid-session.
	daemonConnected bool

	// autoResumeEnabled and autoResumeDelay are populated from DaemonState
	// via pollResultMsg.meta. The daemon owns the scheduler; these are view-only.
	autoResumeEnabled bool
	autoResumeDelay   time.Duration

	// autoResumeUserAt marks when the user last toggled auto-resume via R.
	// pollResultMsg only overwrites autoResumeEnabled if the snapshot's
	// daemon-side timestamp is AFTER autoResumeUserAt, preventing an
	// in-flight stale poll from undoing the optimistic flip.
	autoResumeUserAt time.Time

	// caffeinateUserAt mirrors autoResumeUserAt for the C keybinding.
	// Protects caffeinateOn against stale snapshots arriving after the
	// user's optimistic flip.
	caffeinateUserAt time.Time

	// clientVersion is this TUI binary's build identifier (set by NewModel from
	// Options.Version). daemonVersion is the connected daemon's version,
	// populated from DaemonState via pollResultMsg.meta. Both shown in [?].
	clientVersion string
	daemonVersion string

	reporter             cmuxstatus.Reporter
	sidebarIntervalTicks int
	tickCount            int

	cacheDir  string
	treeState *treestate.State
	pathNodes []*aggregate.PathNode
	flatRows  []render.Row

	errorLogger *ErrorLogger

	onCaffeinateToggle func(want bool) tea.Cmd
	onToggleAutoResume func(want bool) tea.Cmd
	onManualNudge      func(selector string, cancel bool) tea.Cmd

	// nudgeFlash is a transient, footer-rendered status line surfacing the
	// outcome of the most recent manual nudge (N key): queued / already /
	// no-match / working-suppressed / failed. Empty when no flash is active.
	// nudgeFlashUntil bounds its lifetime; a nudgeFlashClearMsg (scheduled on
	// set) wipes it once elapsed. nudgeFlashLevel picks the render style.
	nudgeFlash      string
	nudgeFlashLevel flashLevel
	nudgeFlashUntil time.Time

	// staleAfter is the status-line rate_limits staleness window (ADR 0021 §1),
	// consulted by the 5h block row to label a stale capture.
	staleAfter time.Duration
}

func NewModel(o Options) *Model {
	m := &Model{
		tree:                 o.Tree,
		poller:               o.Poller,
		interval:             o.Interval,
		theme:                render.NewTheme(render.DetectColors()),
		cacheDir:             o.CacheDir,
		treeState:            treestate.Load(o.CacheDir),
		reporter:             o.Reporter,
		sidebarIntervalTicks: o.SidebarIntervalTicks,
		onCaffeinateToggle:   o.OnCaffeinateToggle,
		onToggleAutoResume:   o.OnToggleAutoResume,
		onManualNudge:        o.OnManualNudge,
		clientVersion:        o.Version,
		staleAfter:           o.StaleAfter,
	}
	if m.clientVersion == "" {
		m.clientVersion = "dev"
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
	anyWorking, anyBlocked, anyIdle := false, false, false
	for _, d := range tree.Dirs {
		for _, sv := range d.Sessions {
			switch sv.Status {
			case session.Working:
				anyWorking = true
			case session.Blocked:
				anyBlocked = true
			default:
				// Idle: ignore the dormant age-refinement, count the rest as idle.
				if !session.IsLongIdle(time.Now(), sv.TranscriptMTime, session.LongIdleThreshold) {
					anyIdle = true
				}
			}
		}
	}
	switch {
	case anyWorking:
		return cmuxstatus.StateWorking, time.Time{}
	case anyBlocked:
		// ADR 0024 R3: a blocked session maps to Paused (extending cmuxstatus's
		// existing Paused notion) rather than being lost to Idle.
		return cmuxstatus.StatePaused, time.Time{}
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
	prog, label, ok := windowProgress(m.tree, time.Now(), m.staleAfter)
	return cmuxstatus.Snapshot{
		CaffeinateOn:  m.caffeinateOn,
		NudgeOn:       m.autoResumeEnabled,
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
func windowProgress(tree *aggregate.Tree, now time.Time, staleAfter time.Duration) (float64, string, bool) {
	if tree == nil {
		return 0, "", false
	}
	if !tree.WindowResetsAt.IsZero() {
		return 1.0, "5h block exhausted — waiting for reset", true
	}
	// Prefer the authoritative five_hour used_percentage over the cost/cap estimate
	// (ADR 0021 §5), matching the on-screen BlockRow and the headless cmux-bridge, so
	// the cmux status agrees with claude.ai. Cost/cap remains the fallback only when
	// no authoritative reading was ever captured.
	costPct, costOK := 0.0, false
	if b := tree.ActiveBlock; b != nil && tree.PlanCapUSD > 0 {
		costPct = 100 * b.CostUSD / tree.PlanCapUSD
		costOK = true
	}
	return render.CmuxBlockProgress(tree.FiveHourPct, tree.LimitsCapturedAt, costPct, costOK, now, staleAfter)
}
