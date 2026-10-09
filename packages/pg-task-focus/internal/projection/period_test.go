package projection

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/civil"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
)

// logb builds a log one event at a time. Events are numbered in the order
// they are added, so an id names the position it was added at, and every
// batch gets its own id and its batch.committed.
type logb struct {
	t       *testing.T
	events  []event.Event
	batches int
}

func newLog(t *testing.T) *logb {
	t.Helper()
	return &logb{t: t}
}

// addAt appends an event the daemon recorded at recorded and that took effect
// at effective.
func (b *logb) addAt(recorded, effective time.Time, p event.Payload) event.ID {
	e := event.Event{
		Envelope: event.Envelope{
			V: event.SchemaVersion, ID: eid(len(b.events) + 1),
			At: event.At(recorded), EffectiveAt: event.At(effective), Type: p.EventType(),
		},
		Payload: p,
	}
	b.events = append(b.events, e)
	return e.ID
}

// add appends an event recorded and effective min minutes after the epoch.
func (b *logb) add(min int, p event.Payload) event.ID { return b.addAt(at(min), at(min), p) }

// batch appends the members and their batch.committed, all at min minutes
// after the epoch, and returns the batch id.
func (b *logb) batch(min int, members ...func(event.ID) event.Payload) event.ID {
	b.batches++
	id := bid(b.batches)
	for _, m := range members {
		b.add(min, m(id))
	}
	b.add(min, event.BatchCommitted{Batch: id})
	return id
}

// members lists the ids of the events of batch id, in log order.
func (b *logb) members(id event.ID) []event.ID {
	var out []event.ID
	for _, e := range b.events {
		if e.Payload.BatchID() == id && e.Payload.EventType() != event.TypeBatchCommitted {
			out = append(out, e.ID)
		}
	}
	return out
}

// split returns the log so far and the events added after the call, which is
// how a test says "this is what a request would add".
func (b *logb) split() (base []event.Event) { return slices.Clone(b.events) }

func (b *logb) added(base []event.Event) []event.Event { return slices.Clone(b.events[len(base):]) }

func profileOf(name string) func(event.ID) event.Payload {
	return func(bt event.ID) event.Payload { return event.ProfileChanged{Profile: name, Batch: bt} }
}

func periodOf(kind string, start civil.Date, end *civil.Date, tz, label string) func(event.ID) event.Payload {
	return func(bt event.ID) event.Payload {
		return event.PeriodChanged{Kind: kind, Start: start, End: end, TZ: tz, Label: label, Batch: bt}
	}
}

func dayOf(start civil.Date) func(event.ID) event.Payload {
	return periodOf("day", start, nil, "America/New_York", "")
}

func weekOf(start, end civil.Date) func(event.ID) event.Payload {
	return periodOf("week", start, &end, "America/New_York", "Week 41")
}

// instantOn is midnight UTC of the date, plus the given hours.
func instantOn(d civil.Date, hours int) time.Time {
	return time.Date(d.Year, d.Month, d.Day, hours, 0, 0, 0, time.UTC)
}

func TestPeriodCurrentIsLatestLiveStart(t *testing.T) {
	week1 := civil.Date{Year: 2026, Month: time.October, Day: 5}
	week1End := civil.Date{Year: 2026, Month: time.October, Day: 11}
	week2 := week1.AddDays(7)
	week2End := week1End.AddDays(7)
	sprint1 := civil.Date{Year: 2026, Month: time.September, Day: 28}
	sprint1End := sprint1.AddDays(13)

	build := func() (*logb, map[string]event.ID) {
		b := newLog(t)
		ids := map[string]event.ID{}
		b.batch(0, profileOf("work"), dayOf(day1), weekOf(week1, week1End),
			periodOf("sprint", sprint1, &sprint1End, "Europe/Berlin", "Sprint 12"))
		ids["day1"] = b.members(bid(1))[1]
		ids["week1"] = b.members(bid(1))[2]
		ids["sprint1"] = b.members(bid(1))[3]
		b.batch(1440, dayOf(day2))
		ids["day2"] = b.members(bid(2))[0]
		b.batch(2*1440, weekOf(week2, week2End))
		ids["week2"] = b.members(bid(3))[0]
		return b, ids
	}

	b, ids := build()
	m, err := Replay(b.events)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}

	tests := []struct {
		kind     Kind
		start    civil.Date
		end      civil.Date
		label    string
		tz       string
		openedBy string
	}{
		{Day, day2, day2, "", "America/New_York", "day2"},
		{Week, week2, week2End, "Week 41", "America/New_York", "week2"},
		{Sprint, sprint1, sprint1End, "Sprint 12", "Europe/Berlin", "sprint1"},
	}
	for _, tt := range tests {
		t.Run(string(tt.kind), func(t *testing.T) {
			p, ok := m.Period(tt.kind)
			if !ok {
				t.Fatalf("Period(%s) not found", tt.kind)
			}
			if p.Kind != tt.kind || p.Start != tt.start || p.End != tt.end || p.Label != tt.label ||
				p.TZ.Name() != tt.tz || p.OpenedBy != ids[tt.openedBy] {
				t.Errorf("Period(%s) = %+v (tz %s), want start %s end %s label %q tz %s opened by %s",
					tt.kind, p, p.TZ.Name(), tt.start, tt.end, tt.label, tt.tz, ids[tt.openedBy])
			}
		})
	}

	t.Run("a kind with no change has no current period", func(t *testing.T) {
		b := newLog(t)
		b.batch(0, profileOf("work"), dayOf(day1))
		m, err := Replay(b.events)
		if err != nil {
			t.Fatalf("Replay: %v", err)
		}
		if p, ok := m.Period(Week); ok {
			t.Errorf("Period(week) = %+v, want none", p)
		}
		if _, ok := m.Period(Day); !ok {
			t.Error("Period(day) not found")
		}
	})
}

func TestRetractedPeriodRestoresPreviousCurrent(t *testing.T) {
	b := newLog(t)
	b.batch(0, profileOf("work"), dayOf(day1))
	first := b.members(bid(1))[1]
	second := b.batch(1440, dayOf(day2))
	b.add(1441, event.EventRetracted{TargetBatch: second})

	m, err := Replay(b.events)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	p, ok := m.Period(Day)
	if !ok || p.Start != day1 || p.OpenedBy != first {
		t.Errorf("Period(day) = %+v, %v, want the first period %s opened by %s", p, ok, day1, first)
	}

	b.add(1442, event.EventRetracted{Target: eid(len(b.events))}) // undo the retraction
	m, err = Replay(b.events)
	if err != nil {
		t.Fatalf("Replay after undoing the retraction: %v", err)
	}
	if p, _ := m.Period(Day); p.Start != day2 {
		t.Errorf("Period(day).Start = %s after undoing the retraction, want %s", p.Start, day2)
	}
}

func TestActiveProfileIsLatestLiveProfileChanged(t *testing.T) {
	t.Run("an empty log has no profile", func(t *testing.T) {
		m, err := Replay(nil)
		if err != nil {
			t.Fatalf("Replay: %v", err)
		}
		if got := m.Profile(); got != "" {
			t.Errorf("Profile() = %q, want empty", got)
		}
		if got := m.Lines(); got != 0 {
			t.Errorf("Lines() = %d, want 0", got)
		}
	})

	b := newLog(t)
	b.batch(0, profileOf("work"), dayOf(day1))
	b.batch(60, profileOf("focus"))
	latest := b.batch(120, profileOf("review"))

	m, err := Replay(b.events)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if got := m.Profile(); got != "review" {
		t.Errorf("Profile() = %q, want the latest, review", got)
	}
	if got, want := m.Lines(), len(b.events); got != want {
		t.Errorf("Lines() = %d, want %d", got, want)
	}

	b.add(121, event.EventRetracted{TargetBatch: latest})
	m, err = Replay(b.events)
	if err != nil {
		t.Fatalf("Replay after the retraction: %v", err)
	}
	if got := m.Profile(); got != "focus" {
		t.Errorf("Profile() = %q after retracting the latest, want focus", got)
	}
}

func TestProfileOrderFollowsEffectiveAtThenLogPosition(t *testing.T) {
	b := newLog(t)
	b.batch(0, profileOf("work"), dayOf(day1))
	b.batch(100, profileOf("later"))
	// Recorded last but effective before "later": the timeline's latest wins.
	b.batch(50, profileOf("backdated"))
	m, err := Replay(b.events)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if got := m.Profile(); got != "later" {
		t.Errorf("Profile() = %q, want later (the latest effective_at, not the last recorded)", got)
	}

	// At one instant the later log position wins.
	b = newLog(t)
	b.batch(0, profileOf("work"), dayOf(day1))
	b.batch(100, profileOf("first"))
	b.batch(100, profileOf("second"))
	if m, err = Replay(b.events); err != nil || m.Profile() != "second" {
		t.Errorf("Profile() = %q, %v, want second", m.Profile(), err)
	}
}

func TestPeriodNotLaterIsPeriodUnchanged(t *testing.T) {
	tests := []struct {
		name  string
		start civil.Date // the start of the second change
	}{
		{"the same start", day1},
		{"an earlier start", day1.AddDays(-1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := newLog(t)
			b.batch(0, profileOf("work"), dayOf(day1))
			first := b.members(bid(1))[1]
			base := b.split()
			b.batch(60, dayOf(tt.start))
			added := b.added(base)

			second := b.members(bid(2))[0]
			stored := append(slices.Clone(base), added...)
			_, err := Replay(stored)
			inv := asInvalid(t, err)
			assertFinding(t, inv, codePeriodUnchanged, "day", []event.ID{first, second}, at(0), at(60))
			if !strings.Contains(inv.Message, "America/New_York") {
				t.Errorf("message %q does not name the day period's zone", inv.Message)
			}

			_, err = Candidate(base, added)
			inv = asInvalid(t, err)
			assertFinding(t, inv, codePeriodUnchanged, "day", []event.ID{first}, at(0), at(60))
			if !strings.Contains(inv.Message, "the new event") || strings.Contains(inv.Message, string(second)) {
				t.Errorf("message %q must call the added change the new event and never cite its id %s", inv.Message, second)
			}
		})
	}
}

// assertFinding checks the facts of a finding: its code, entity, the stored
// events and the instants (all in minutes after the epoch, in this order).
func assertFinding(t *testing.T, inv *Invalid, code Code, entity string, events []event.ID, instants ...time.Time) {
	t.Helper()
	if inv.Code != code {
		t.Errorf("Code = %q, want %q (message %q)", inv.Code, code, inv.Message)
	}
	if inv.Entity != entity {
		t.Errorf("Entity = %q, want %q", inv.Entity, entity)
	}
	if !slices.Equal(inv.Events, events) {
		t.Errorf("Events = %v, want %v", inv.Events, events)
	}
	for _, want := range instants {
		if !slices.ContainsFunc(inv.Instants, func(got time.Time) bool { return got.Equal(want) }) {
			t.Errorf("Instants = %v lacks %s", inv.Instants, want.Format(instantLayout))
		}
	}
	if len(inv.Instants) != len(instants) {
		t.Errorf("Instants = %v, want exactly %d instants", inv.Instants, len(instants))
	}
}

func TestBackdatedPeriodChangeIsPeriodOutOfOrder(t *testing.T) {
	d := func(day int) civil.Date { return civil.Date{Year: 2026, Month: time.October, Day: day} }
	type change struct {
		start       civil.Date
		recordedDay int
		effectDay   int
	}
	tests := []struct {
		name string
		// p1 and p2 are the stored changes; p3 is the one a request adds, or,
		// when stored is set, recorded with them.
		p1, p2, p3 change
		want       Code
		offender   string // which change the finding is about
		wantOthers string // the stored change the finding also names
	}{
		{
			name: "the new change starts later but sorts before a change with a lower start",
			p1:   change{d(1), 1, 1}, p2: change{d(8), 8, 8}, p3: change{d(9), 9, 3},
			want: codePeriodOutOfOrder, offender: "p3", wantOthers: "p2",
		},
		{
			name: "the new change starts no later than the change it sorts after",
			p1:   change{d(1), 1, 1}, p2: change{d(8), 8, 8}, p3: change{d(5), 9, 9},
			want: codePeriodUnchanged, offender: "p3", wantOthers: "p2",
		},
		{
			name: "the change later in the log is the offender whatever it was recorded at",
			p1:   change{d(1), 1, 1}, p2: change{d(8), 9, 8}, p3: change{d(9), 5, 3},
			want: codePeriodOutOfOrder, offender: "p3", wantOthers: "p2",
		},
		{
			name: "an equal start that sorts first is not a later start",
			p1:   change{d(1), 1, 1}, p2: change{d(8), 8, 8}, p3: change{d(8), 9, 3},
			want: codePeriodUnchanged, offender: "p3", wantOthers: "p2",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := newLog(t)
			ids := map[string]event.ID{}
			addChange := func(name string, c change) {
				ids[name] = b.addAt(instantOn(d(c.recordedDay), 12), instantOn(d(c.effectDay), 12),
					event.PeriodChanged{Kind: "day", Start: c.start, TZ: "America/New_York", Batch: bid(1)})
			}
			// Each change is a member of the one batch of the fixture, which
			// keeps the events valid for the codec; the ordering rules look at
			// the events alone.
			addChange("p1", tt.p1)
			addChange("p2", tt.p2)
			base := b.split()
			addChange("p3", tt.p3)
			b.add(0, event.BatchCommitted{Batch: bid(1)})
			added := b.added(base)

			// Replay of the stored log, with the third change stored.
			_, err := Replay(b.events)
			inv := asInvalid(t, err)
			if inv.Code != tt.want || inv.Entity != "day" {
				t.Fatalf("Replay: code %q entity %q (%s), want %q for day", inv.Code, inv.Entity, inv.Message, tt.want)
			}
			wantEvents := []event.ID{ids[tt.offender], ids[tt.wantOthers]}
			slices.Sort(wantEvents)
			gotEvents := slices.Clone(inv.Events)
			slices.Sort(gotEvents)
			if !slices.Equal(gotEvents, wantEvents) {
				t.Errorf("Replay: Events = %v, want %v", inv.Events, wantEvents)
			}

			// Candidate replay of the same events, the third being the new one.
			_, err = Candidate(base, added)
			cinv := asInvalid(t, err)
			if cinv.Code != tt.want || cinv.Entity != "day" {
				t.Fatalf("Candidate: code %q entity %q (%s), want %q for day", cinv.Code, cinv.Entity, cinv.Message, tt.want)
			}
			if slices.Contains(cinv.Events, ids["p3"]) {
				t.Errorf("Candidate: Events = %v lists the new change %s", cinv.Events, ids["p3"])
			}
			if !strings.Contains(cinv.Message, "the new event") {
				t.Errorf("Candidate: message %q does not call the added change the new event", cinv.Message)
			}
		})
	}
}
