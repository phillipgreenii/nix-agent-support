package projection

import (
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
)

func TestFormatInstantMatchesTheFindingStyle(t *testing.T) {
	bare, err := Replay(nil)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	day, err := Replay(dayLog(t).events)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	evening := time.Date(2026, time.October, 7, 2, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		m    *Model
		t    time.Time
		want string
	}{
		{"no day period gives UTC alone", bare, at(0), "2026-10-07T13:30:00.000Z"},
		{"a day period adds its zone", day, at(0), "2026-10-07T13:30:00.000Z (09:30 America/New_York)"},
		{"a local date that differs is given", day, evening, "2026-10-07T02:00:00.000Z (2026-10-06 22:00 America/New_York)"},
		{"a zone other than UTC is read as the same instant", day, at(0).In(time.FixedZone("x", 3600)), "2026-10-07T13:30:00.000Z (09:30 America/New_York)"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.m.FormatInstant(tc.t); got != tc.want {
				t.Errorf("FormatInstant = %q, want %q", got, tc.want)
			}
		})
	}

	t.Run("a finding writes its instants the same way", func(t *testing.T) {
		b := dayLog(t)
		b.add(0, startOf(cycleA, 25))
		base := b.split()
		b.add(0, stopOf(cycleA))
		_, err := Candidate(base, b.added(base))
		inv := assertCode(t, err, codeStopNotAfterStart)
		if want := day.FormatInstant(at(0)); !strings.Contains(inv.Message, want) {
			t.Errorf("Message %q does not contain %q", inv.Message, want)
		}
	})
}

func TestNewestEventOfAnEntity(t *testing.T) {
	b := dayLog(t)
	b.add(0, startOf(cycleA, 25))
	boosted := b.add(30, boostOf(cycleA, 5))
	paused := b.addAt(at(40), at(20), pauseOf(cycleA)) // recorded last, effective before the boost
	tests := []struct {
		name   string
		build  func() *logb
		entity string
		want   event.ID
		wantAt time.Time
	}{
		{
			name:   "the latest effective_at wins over the latest log position",
			build:  func() *logb { return b },
			entity: string(cycleA), want: boosted, wantAt: at(30),
		},
		{
			name: "a retracted event does not count",
			build: func() *logb {
				f := b.fork(t)
				f.add(41, event.EventRetracted{Target: boosted})
				return f
			},
			entity: string(cycleA), want: paused, wantAt: at(20),
		},
		{
			name: "a corrected effective_at counts as corrected",
			build: func() *logb {
				f := b.fork(t)
				f.add(41, event.EventCorrected{Target: paused, Fields: fieldsOf("effective_at", event.At(at(35)))})
				return f
			},
			entity: string(cycleA), want: paused, wantAt: at(35),
		},
		{
			name: "a later resume is newer than the boost",
			build: func() *logb {
				f := b.fork(t)
				f.add(39, resumeOf(cycleA))
				return f
			},
			entity: string(cycleA), want: eid(len(b.events) + 1), wantAt: at(39),
		},
		{
			name: "of two events at one instant the later in the log wins",
			build: func() *logb {
				f := b.fork(t)
				f.add(30, boostOf(cycleA, 10))
				return f
			},
			entity: string(cycleA), want: eid(len(b.events) + 1), wantAt: at(30),
		},
		{
			name: "the interrupted cycle's newest event is the start that interrupts it",
			build: func() *logb {
				f := b.fork(t)
				f.add(45, resumeOf(cycleA))
				f.add(50, interruptOf(cycleB, cycleA, 25))
				return f
			},
			entity: string(cycleA), want: eid(len(b.events) + 2), wantAt: at(50),
		},
		{
			name:   "the start alone",
			build:  func() *logb { f := dayLog(t); f.add(0, startOf(cycleA, 25)); return f },
			entity: string(cycleA), want: eid(4), wantAt: at(0),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, err := Replay(tc.build().events)
			if err != nil {
				t.Fatalf("Replay: %v", err)
			}
			e, ok := m.NewestEvent(tc.entity)
			if !ok {
				t.Fatalf("NewestEvent(%s) found nothing", tc.entity)
			}
			if e.ID != tc.want || !e.EffectiveAt.Time().Equal(tc.wantAt) {
				t.Errorf("NewestEvent = %s at %v, want %s at %v", e.ID, e.EffectiveAt.Time(), tc.want, tc.wantAt)
			}
		})
	}

	t.Run("a task's events", func(t *testing.T) {
		events := []event.Event{
			ev(1, 0, event.ProfileChanged{Profile: "work", Batch: bid(1)}),
			periodChanged(t, 2, 0, bid(1)),
			materialized(t, 3, 0, bid(1)),
			committed(4, 0, bid(1)),
			completed(5, 10),
		}
		m, err := Replay(events)
		if err != nil {
			t.Fatalf("Replay: %v", err)
		}
		e, ok := m.NewestEvent(string(taskA))
		if !ok || e.ID != eid(5) {
			t.Errorf("NewestEvent(task) = %s, %v, want %s", e.ID, ok, eid(5))
		}
	})

	t.Run("an entity with no live event", func(t *testing.T) {
		m, err := Replay(b.events)
		if err != nil {
			t.Fatalf("Replay: %v", err)
		}
		if e, ok := m.NewestEvent(string(cycleB)); ok {
			t.Errorf("NewestEvent(unknown) = %s, want none", e.ID)
		}
	})

	t.Run("the empty entity names nothing", func(t *testing.T) {
		// A profile.changed has no entity, and a cycle.started that interrupts
		// nothing has an empty interrupts: neither is an event of "".
		m, err := Replay(b.events)
		if err != nil {
			t.Fatalf("Replay: %v", err)
		}
		if e, ok := m.NewestEvent(""); ok {
			t.Errorf("NewestEvent(\"\") = %s (%s), want none", e.ID, e.Payload.EventType())
		}
	})
}
