// Package view derives the state clients show from a projection and the
// configuration at the instant they read it: today's date in each period's
// zone, the rollover banners, the ordered checklists, the next task, the focus
// cycle with its timer, the dimmed cycles and the resume offer.
//
// The projection is a pure function of the log and never consults the
// configuration. This package is where the two meet, at read time only, and
// the log stays the authority: a task or cycle type the configuration no
// longer defines still renders from the snapshot the log holds, and a cycle
// type that has gone takes the defaults.
package view

import (
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/civil"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/projection"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/zone"
)

// StoreHealth is the state of the event store: ReadOnly is set once the store
// has refused to write, Reason says why in one plain sentence, and Since is the
// instant it began. A healthy store is the zero value. Build leaves it zero;
// the engine fills it from the store.
type StoreHealth struct {
	ReadOnly bool
	Reason   string
	Since    time.Time
}

// PeriodState is the current period of one kind and where today stands against
// it. Today is the civil date of the read instant in the period's own zone,
// whatever the host's zone is. Ended is set once today is after the last civil
// date of the period, which is inclusive, and Banner then says so.
type PeriodState struct {
	Kind   projection.Kind
	Start  civil.Date
	End    civil.Date
	Label  string
	Zone   string // the IANA name of the zone that decides today
	Today  civil.Date
	Ended  bool
	Banner string // empty until the period has ended
}

// CycleView is a cycle with its timer read at the read instant. Elapsed,
// Remaining and Overtime come from the segments and boosts; nothing ticks into
// the log. NotInProfile flags a cycle whose type the active profile does not
// list, which is allowed; it is true for every type when the configuration no
// longer defines the active profile. Alert is the alert the configuration gives the type
// now, or defaults.alert when the type is no longer defined; the cycle's own
// type id and title always come from the snapshot in the log.
type CycleView struct {
	Cycle        projection.Cycle
	Elapsed      time.Duration
	Remaining    time.Duration // negative in overtime
	Overtime     bool
	NotInProfile bool
	Alert        config.Alert
}

// DimmedCycle is a paused cycle that is not stopped, shown beside the focus.
// CanSwitch is set while a cycle runs, when the operator can make this one the
// focus; with nothing running the action is a plain resume.
type DimmedCycle struct {
	CycleView
	CanSwitch bool
}

// State is what a client shows at one instant.
type State struct {
	// Initialized is false for a log that has no profile yet, which is an
	// empty log: there is nothing to show but the invitation to set one up.
	Initialized bool
	Profile     string
	// Periods lists the current period of every kind that has one, in the
	// order day, week, sprint.
	Periods []PeriodState
	// Tasks are the tasks of the current periods, ordered by kind, then
	// group rank, then due time.
	Tasks []TaskView
	// Next is the open task due first, across every kind; nil when none is open.
	Next *TaskView
	// Focus is the running cycle; nil when no cycle runs.
	Focus *CycleView
	// Dimmed lists every paused cycle that is not stopped, most recently
	// paused first. A cycle in the resume offer stays listed here.
	Dimmed []DimmedCycle
	// InterruptStack lists the dimmed cycles another cycle displaced, the
	// one displaced last first.
	InterruptStack []CycleView
	// ResumeOffer is the cycle the operator is asked to return to; nil when
	// there is none.
	ResumeOffer *ResumeOffer
	Store       StoreHealth
}

// Build is the state of model m read with configuration cfg at instant now.
// It never reads the clock, and it is safe for an empty model.
func Build(m *projection.Model, cfg *config.Config, now time.Time) State {
	st := State{Profile: m.Profile()}
	st.Initialized = st.Profile != ""

	for _, k := range kinds {
		if p, ok := m.Period(k); ok {
			st.Periods = append(st.Periods, periodState(p, now))
		}
	}
	st.Tasks = tasksOf(m, cfg, now, st.Periods)
	st.Next = nextOf(st.Tasks)

	inProfile := cycleTypesOf(cfg, st.Profile)
	cycleView := func(c projection.Cycle) CycleView {
		return CycleView{
			Cycle:        c,
			Elapsed:      c.Elapsed(now),
			Remaining:    c.Remaining(now),
			Overtime:     c.Overtime(now),
			NotInProfile: !inProfile[c.Type],
			Alert:        cfg.Alert(c.Type),
		}
	}

	running, hasFocus := m.Running()
	if hasFocus {
		v := cycleView(running)
		st.Focus = &v
	}
	dimmed := m.Dimmed()
	for _, c := range dimmed {
		st.Dimmed = append(st.Dimmed, DimmedCycle{CycleView: cycleView(c), CanSwitch: hasFocus})
	}
	stack := interruptStack(dimmed)
	for _, c := range stack {
		st.InterruptStack = append(st.InterruptStack, cycleView(c))
	}
	st.ResumeOffer = offerOf(m, stack, hasFocus, cycleView)
	return st
}

// periodState reads a period at now. Today is computed in the period's zone.
func periodState(p projection.Period, now time.Time) PeriodState {
	today := zone.Today(p.TZ, now)
	ended := today.Compare(p.End) > 0
	s := PeriodState{
		Kind: p.Kind, Start: p.Start, End: p.End, Label: p.Label, Zone: p.TZ.Name(),
		Today: today, Ended: ended,
	}
	if ended {
		s.Banner = RolloverBanner(p.Kind)
	}
	return s
}

// cycleTypesOf is the set of cycle types the profile lists. A profile the
// configuration does not define lists none.
func cycleTypesOf(cfg *config.Config, profile string) map[string]bool {
	p, _ := cfg.Profile(profile)
	set := make(map[string]bool, len(p.Cycles))
	for _, id := range p.Cycles {
		set[id] = true
	}
	return set
}
