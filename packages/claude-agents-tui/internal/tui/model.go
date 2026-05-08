package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/phillipgreenii/claude-agents-tui/internal/aggregate"
	"github.com/phillipgreenii/claude-agents-tui/internal/caffeinate"
	"github.com/phillipgreenii/claude-agents-tui/internal/render"
	"github.com/phillipgreenii/claude-agents-tui/internal/signal"
	"github.com/phillipgreenii/claude-agents-tui/internal/treestate"
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
}

type Model struct {
	tree          *aggregate.Tree
	showAll       bool
	forceID       bool
	costMode      bool
	caffeinateOn  bool
	width, height int
	selected      *aggregate.SessionView
	cursor        int
	scrollOffset  int
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

	cacheDir  string
	treeState *treestate.State
	pathNodes []*aggregate.PathNode
	flatRows  []render.Row
}

func NewModel(o Options) *Model {
	m := &Model{
		tree:              o.Tree,
		poller:            o.Poller,
		interval:          o.Interval,
		caffeinate:        o.Caffeinate,
		theme:             render.NewTheme(render.DetectColors()),
		cacheDir:          o.CacheDir,
		treeState:         treestate.Load(o.CacheDir),
		signalers:         o.Signalers,
		autoResumeDelay:   o.AutoResumeDelay,
		autoResumeMessage: o.AutoResumeMessage,
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
