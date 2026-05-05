package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/phillipgreenii/claude-agents-tui/internal/aggregate"
	"github.com/phillipgreenii/claude-agents-tui/internal/caffeinate"
	"github.com/phillipgreenii/claude-agents-tui/internal/render"
)

type Options struct {
	Tree       *aggregate.Tree
	Poller     Poller
	Interval   time.Duration
	Caffeinate *caffeinate.Manager
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
}

func NewModel(o Options) *Model {
	return &Model{
		tree:       o.Tree,
		poller:     o.Poller,
		interval:   o.Interval,
		caffeinate: o.Caffeinate,
		theme:      render.NewTheme(render.DetectColors()),
	}
}

func (m *Model) Init() tea.Cmd {
	if m.poller == nil || m.interval <= 0 {
		return nil
	}
	m.polling = true
	return tea.Batch(m.pollNow(), tickCmd(m.interval))
}

func (m *Model) clampCursor() {
	maxIdx := 0
	if m.tree != nil {
		for _, d := range m.tree.Dirs {
			maxIdx += len(d.Sessions)
		}
	}
	if m.cursor >= maxIdx {
		m.cursor = maxIdx - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

func (m *Model) sessionAt(idx int) *aggregate.SessionView {
	i := 0
	if m.tree == nil {
		return nil
	}
	for _, d := range m.tree.Dirs {
		for _, s := range d.Sessions {
			if i == idx {
				return s
			}
			i++
		}
	}
	return nil
}
