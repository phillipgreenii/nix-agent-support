// Package alert schedules the overtime sounds of a work cycle. When a running
// cycle's time is up it plays the cycle type's sound once (the expiry), then
// its reminder sound each time the cycle has run a further repeat_minutes of
// running time, until the cycle is stopped. There is no acknowledge, mute,
// snooze or repeat cap. Every alert is also a notification trigger; this
// package only reports it.
//
// Alerts are not events: the log never records one. What the scheduler has
// played lives in its memory, per process, and the memory is lost on a
// restart. The scheduler never reads the clock and never sleeps; every method
// takes the instant it reads at.
package alert

import (
	"sync"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/projection"
)

// Kind says which sound an alert plays.
type Kind string

// The kinds of alert.
const (
	Expiry   Kind = "expiry"   // the cycle's time is up: the type's sound
	Reminder Kind = "reminder" // a further repeat_minutes of running time in overtime: the reminder sound
)

// Alert is one sound to play and one notification to send, for the cycle
// CycleID. Sound is the name of the sound the configuration gives the cycle's
// type at the read instant, empty when it gives no sound (INV-CONF-7): the
// notification is still sent. A client reads the cycle's title and timer from the model by
// CycleID.
type Alert struct {
	Kind    Kind
	CycleID event.CycleID
	Sound   string
}

// Scheduler decides which alert is due. Reminders count the cycle's running
// time: a paused cycle accrues none, so it plays nothing, and resuming it
// continues the count where it stopped.
//
// For each cycle it has seen, the scheduler keeps an anchor: the cycle's
// elapsed running time when it last played an alert in the current overtime
// stretch, or unset. The memory is kept for the life of the process, also for
// a stopped cycle, so a retraction that makes it run again finds its anchor.
// A Scheduler is safe for concurrent use.
type Scheduler struct {
	mu     sync.Mutex
	polled bool // a first Poll has run
	memory map[event.CycleID]*anchor
}

// anchor is what the scheduler remembers of one cycle. at is meaningful only
// when set. unknown marks a cycle that was already in the log at the first
// poll: what was played for it before the process started cannot be known.
type anchor struct {
	at      time.Duration
	set     bool
	unknown bool
}

// NewScheduler returns a scheduler with no memory.
func NewScheduler() *Scheduler {
	return &Scheduler{memory: map[event.CycleID]*anchor{}}
}

// Poll observes every cycle that is not stopped at now, records what it plays,
// and returns the alert due at now, or nil. At most one alert is returned, and
// only a running cycle can alert. The daemon calls it after every commit and
// every configuration reload, before NextAt, and at the instant NextAt names.
//
// For each cycle that is not stopped, with X its type's repeat_minutes:
//
//   - a cycle whose time is not up has its anchor unset, so entering overtime
//     again plays the expiry again;
//   - a running cycle whose time is up and whose anchor is unset plays the
//     expiry, and the anchor becomes its elapsed time; a sleep of the host
//     across the expiry gives this too;
//   - a running cycle whose time is up and that has run X since its anchor
//     plays one reminder, and the anchor becomes its elapsed time, so a long
//     sleep gives one catch-up reminder and the next comes X later;
//   - a paused cycle plays nothing and its anchor stays where it is;
//   - when the elapsed time is below the anchor (a back-filled break or a
//     correction took running time away), the anchor moves silently to the
//     last whole interval of overtime, deadline + floor(over/X)*X in elapsed
//     time, so reminders neither stop nor double;
//   - a cycle already in the log at the scheduler's first poll has no known
//     history: if it is running with its time up, it plays the expiry when it
//     is less than X over and one reminder otherwise; if it is paused with its
//     time up, it plays nothing. Either way its anchor moves to the last whole
//     interval, so the restart keeps the cadence. A cycle first seen on a later
//     poll has an unset anchor, so it plays the expiry.
//
// A boost that leaves the cycle's time up does not move its anchor.
//
// The scheduler learns that a cycle left time-up only by observing it, so
// Poll MUST run at every commit and every reload, in particular at the commit
// of a boost that takes a cycle out of overtime: that poll unsets the anchor.
// If it is skipped, the anchor of the old overtime stretch survives, and the
// next time-up plays a reminder or nothing instead of the expiry.
//
// The cycles are examined in log order; should two cycles run at once, the first
// alerts and the other keeps its memory unchanged, to alert at the next poll.
func (s *Scheduler) Poll(m *projection.Model, cfg *config.Config, now time.Time) *Alert {
	s.mu.Lock()
	defer s.mu.Unlock()

	first := !s.polled
	s.polled = true
	var out *Alert
	for _, c := range m.Cycles() {
		if c.StatusAt(now) == projection.Stopped {
			continue
		}
		a, ok := s.memory[c.ID]
		if !ok {
			a = &anchor{unknown: first}
			s.memory[c.ID] = a
		}
		if played := a.observe(c, cfg.Alert(c.Type), now, out == nil); played != nil {
			out = played
		}
	}
	return out
}

// observe applies the rules of Poll to cycle c, which is not stopped at now,
// and returns the alert it plays. A cycle plays only when mayPlay is set.
func (a *anchor) observe(c projection.Cycle, settings config.Alert, now time.Time, mayPlay bool) *Alert {
	if !c.TimeUp(now) {
		*a = anchor{}
		return nil
	}
	repeat := repeatOf(settings)
	elapsed := c.Elapsed(now)
	over := -c.Remaining(now)
	// The last whole interval of overtime at or before now, in elapsed time.
	grid := elapsed - over + over/repeat*repeat
	running := c.StatusAt(now) == projection.Running

	if a.unknown {
		if running && !mayPlay {
			return nil
		}
		*a = anchor{at: grid, set: true}
		if !running {
			return nil
		}
		kind := Reminder
		if over < repeat {
			kind = Expiry
		}
		return alertOf(c, settings, kind)
	}

	if a.set && elapsed < a.at {
		a.at = grid
	}
	if !running || !mayPlay {
		return nil
	}
	var kind Kind
	switch {
	case !a.set:
		kind = Expiry
	case elapsed-a.at >= repeat:
		kind = Reminder
	default:
		return nil
	}
	*a = anchor{at: elapsed, set: true}
	return alertOf(c, settings, kind)
}

// NextAt is the instant the cycle running at now next alerts if nothing
// changes before then: the instant its time is up when it is not up yet; now
// when its time is up and it has no anchor; otherwise the instant it has run
// a further repeat_minutes since its anchor, never earlier than now. It reads
// the memory as Poll left it; a paused or stopped cycle, or no cycle, has no
// next alert.
//
// Because the time-up instant ignores the anchor, a boost that ends overtime
// gives the right instant even before the next Poll.
func (s *Scheduler) NextAt(m *projection.Model, cfg *config.Config, now time.Time) (time.Time, bool) {
	c, ok := m.RunningAt(now)
	if !ok {
		return time.Time{}, false
	}
	if !c.TimeUp(now) {
		return now.Add(c.Remaining(now)), true
	}
	s.mu.Lock()
	a, seen := s.memory[c.ID]
	var at time.Duration
	known := seen && a.set && !a.unknown
	if known {
		at = a.at
	}
	s.mu.Unlock()
	if !known {
		return now, true
	}
	return now.Add(max(at+repeatOf(cfg.Alert(c.Type))-c.Elapsed(now), 0)), true
}

// repeatOf is the running time between reminders. A parsed configuration
// always gives at least one minute.
func repeatOf(settings config.Alert) time.Duration {
	return time.Duration(settings.RepeatMinutes) * time.Minute
}

func alertOf(c projection.Cycle, settings config.Alert, kind Kind) *Alert {
	sound := settings.Sound
	if kind == Reminder {
		sound = settings.ReminderSound
	}
	return &Alert{Kind: kind, CycleID: c.ID, Sound: sound.Name()}
}
