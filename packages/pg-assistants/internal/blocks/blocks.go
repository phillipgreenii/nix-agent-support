// Package blocks tracks the account's 5-hour and 7-day usage blocks. Every
// running session reports the same account's readings, so the daemon keeps
// one reading per block kind, merged from every report, and turns it into a
// block meter for each session's ladder and into the start hold.
//
// The rules:
//
//   - A stale reading never lowers a fresh one: the engine updates its figures
//     only after a model response, so an idle session reports old ones. Per
//     kind the reading with the latest reset time wins; for an equal reset
//     time, the higher fraction (use within one block only grows). A reading
//     without a reset time never replaces one that has one.
//   - A reading is current until its reset time, or, without one, until its
//     observation time plus the unknown-reset hold. A reading that is no
//     longer current is discarded, so the next one replaces it.
//   - While any current reading is at or above the nudge point, new starts are
//     held until it stops being current.
//
// The tracker is shared by every assistant's loop. A mutex guards it and
// covers only memory reads and writes, never I/O. It reads no clock: the
// caller passes the time in.
package blocks

import (
	"sort"
	"sync"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-assistants/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-assistants/internal/control"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-assistants/internal/ladder"
)

// Tracker holds the current reading per block kind.
type Tracker struct {
	cfg    config.Blocks
	mu     sync.Mutex
	byKind map[string]control.Limit
}

// NewTracker returns a tracker with no readings, using cfg's points for the
// meters and hold and cfg.UnknownResetHold for readings without a reset time.
func NewTracker(cfg config.Blocks) *Tracker {
	return &Tracker{cfg: cfg, byKind: map[string]control.Limit{}}
}

// Observe merges readings per kind: the latest ResetsAt wins; for an equal
// ResetsAt the higher Fraction wins; a zero ResetsAt never replaces a non-zero
// one. A kept reading whose time has passed at the new reading's ObservedAt is
// replaced whatever the rule says. A reading the daemon does not track
// (control.Limit.Valid) or already past its time is ignored.
func (t *Tracker) Observe(limits []control.Limit) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, l := range limits {
		t.merge(l)
	}
}

// Seed is Observe for readings from a previous status.json; those no longer
// current at now are dropped.
func (t *Tracker) Seed(limits []control.Limit, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, l := range limits {
		if t.current(l, now) {
			t.merge(l)
		}
	}
}

// Meters returns the current block meters (readings whose reset time has not
// passed), ordered by kind, for every running session's ladder.
func (t *Tracker) Meters(now time.Time) []ladder.Meter {
	var out []ladder.Meter
	for _, l := range t.Snapshot(now) {
		m := ladder.Meter{Name: l.Kind, Fraction: l.Fraction, Points: t.cfg.Points}
		if !l.ResetsAt.IsZero() {
			m.Detail = "resets " + l.ResetsAt.UTC().Format(time.RFC3339)
		}
		out = append(out, m)
	}
	return out
}

// Hold reports whether starts are held: any current block at or above
// cfg.Nudge holds until its ResetsAt, or until ObservedAt + cfg.UnknownResetHold
// when ResetsAt is zero. With several held blocks it holds until the last one
// ends.
func (t *Tracker) Hold(now time.Time) (bool, time.Time) {
	var until time.Time
	if !(t.cfg.Nudge > 0) {
		return false, until // no nudge point: nothing can hold (validation never allows it)
	}
	for _, l := range t.Snapshot(now) {
		if l.Fraction >= t.cfg.Nudge {
			if end := t.end(l); end.After(until) {
				until = end
			}
		}
	}
	return !until.IsZero(), until
}

// Snapshot returns a copy of the current readings, ordered by kind, for the
// status file. It changes nothing: a reading no longer current is skipped
// here and replaced by the next one merged.
func (t *Tracker) Snapshot(now time.Time) []control.Limit {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []control.Limit
	for _, l := range t.byKind {
		if t.current(l, now) {
			out = append(out, l)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Kind < out[j].Kind })
	return out
}

// merge applies one reading. The caller holds t.mu.
func (t *Tracker) merge(l control.Limit) {
	if !l.Valid() || !t.current(l, l.ObservedAt) {
		return
	}
	old, ok := t.byKind[l.Kind]
	if !ok || !t.current(old, l.ObservedAt) || replaces(l, old) {
		t.byKind[l.Kind] = l
	}
}

// replaces reports whether reading n wins over the kept reading o of the same
// kind, both current.
func replaces(n, o control.Limit) bool {
	switch {
	case n.ResetsAt.IsZero() != o.ResetsAt.IsZero():
		return o.ResetsAt.IsZero() // a reset time beats none, never the reverse
	case !n.ResetsAt.Equal(o.ResetsAt):
		return n.ResetsAt.After(o.ResetsAt)
	}
	// Same block: the higher fraction wins; an equal one refreshes the
	// observation time (which ends a hold without a reset time).
	return n.Fraction >= o.Fraction
}

// end is when reading l stops being current.
func (t *Tracker) end(l control.Limit) time.Time {
	if !l.ResetsAt.IsZero() {
		return l.ResetsAt
	}
	return l.ObservedAt.Add(t.cfg.UnknownResetHold)
}

func (t *Tracker) current(l control.Limit, now time.Time) bool { return now.Before(t.end(l)) }
