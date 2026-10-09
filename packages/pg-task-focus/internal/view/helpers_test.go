package view_test

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/civil"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/due"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/projection"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/view"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/zone"
)

// The tests build their logs from payload structs, pass every event through
// the real encoder and decoder, and replay them, so a fixture cannot drift
// from the codec. Configurations are the example fixture, edited in place.

const configFixture = "../../testdata/config/valid.json"

const (
	newYork = "America/New_York"

	cycleA = event.CycleID("01J9ZZZZZZZZZZZZZZZZZZZZZA")
	cycleB = event.CycleID("01J9ZZZZZZZZZZZZZZZZZZZZZB")
	cycleC = event.CycleID("01J9ZZZZZZZZZZZZZZZZZZZZZC")
	cycleD = event.CycleID("01J9ZZZZZZZZZZZZZZZZZZZZZD")
	cycleE = event.CycleID("01J9ZZZZZZZZZZZZZZZZZZZZZE")
)

var (
	// t0 is 08:00 on 2026-10-07 in New York.
	t0   = time.Date(2026, time.October, 7, 12, 0, 0, 0, time.UTC)
	day1 = civil.Date{Year: 2026, Month: time.October, Day: 7}
)

// at is the instant n minutes after t0.
func at(n int) time.Time { return t0.Add(time.Duration(n) * time.Minute) }

// logb builds a log one event at a time, every event recorded and effective at
// the instant it is added at.
type logb struct {
	t       *testing.T
	events  []event.Event
	ids     uint32
	batches int
}

func newLog(t *testing.T) *logb {
	t.Helper()
	return &logb{t: t}
}

func (b *logb) id() event.ID {
	b.ids++
	var entropy [10]byte
	binary.BigEndian.PutUint32(entropy[6:], b.ids)
	return event.NewID(t0, bytes.NewReader(entropy[:]))
}

func (b *logb) add(when time.Time, p event.Payload) event.ID {
	b.t.Helper()
	line, err := event.Encode(event.Event{
		Envelope: event.Envelope{V: event.SchemaVersion, ID: b.id(), At: event.At(when), EffectiveAt: event.At(when), Type: p.EventType()},
		Payload:  p,
	})
	if err != nil {
		b.t.Fatalf("Encode %T: %v", p, err)
	}
	e, err := event.Decode(line)
	if err != nil {
		b.t.Fatalf("Decode %T: %v", p, err)
	}
	e.Line = len(b.events) + 1
	b.events = append(b.events, e)
	return e.ID
}

// batch adds the members and their batch.committed at the instant when.
func (b *logb) batch(when time.Time, members ...func(event.ID) event.Payload) {
	b.t.Helper()
	id := b.id()
	for _, m := range members {
		b.add(when, m(id))
	}
	b.add(when, event.BatchCommitted{Batch: id})
}

func (b *logb) model() *projection.Model {
	b.t.Helper()
	m, err := projection.Replay(b.events)
	if err != nil {
		b.t.Fatalf("Replay: %v", err)
	}
	return m
}

func profileOf(name string) func(event.ID) event.Payload {
	return func(bt event.ID) event.Payload { return event.ProfileChanged{Profile: name, Batch: bt} }
}

func periodOf(kind string, start civil.Date, end *civil.Date, tz string) func(event.ID) event.Payload {
	return func(bt event.ID) event.Payload {
		return event.PeriodChanged{Kind: kind, Start: start, End: end, TZ: tz, Batch: bt}
	}
}

func dayIn(start civil.Date, tz string) func(event.ID) event.Payload {
	return periodOf("day", start, nil, tz)
}

// taskOf materializes definition def of the given cadence for the period
// starting on period, due at the instant dueAt in the given group.
func taskOf(def string, c due.Cadence, period civil.Date, group string, dueAt time.Time) func(event.ID) event.Payload {
	z, err := zone.Load(newYork)
	if err != nil {
		panic(err)
	}
	rule := due.Rule{At: civil.TimeOfDay{Hour: 9, Minute: 30}, TZ: z}
	switch c {
	case due.Weekly:
		thursday := time.Thursday
		rule.Weekday = &thursday
	case due.Sprint:
		first := 1
		rule.Day = &first
	}
	return func(bt event.ID) event.Payload {
		return event.TaskMaterialized{
			TaskID: event.NewTaskID(c, period, def), Definition: def, Cadence: c, Period: period,
			Title: "Task " + def, Group: group, Link: "https://example.test/" + def, Due: event.At(dueAt),
			DueRule: rule, Batch: bt,
		}
	}
}

func dailyID(def string) event.TaskID { return event.NewTaskID(due.Daily, day1, def) }

func startOf(id event.CycleID, typ string, planned int) event.Payload {
	return event.CycleStarted{CycleID: id, Type: typ, Title: "Snapshot " + typ, PlannedMinutes: planned}
}

func interruptOf(id, of event.CycleID, typ string, planned int) event.Payload {
	return event.CycleStarted{CycleID: id, Type: typ, Title: "Snapshot " + typ, PlannedMinutes: planned, Interrupts: of}
}

func pauseOf(id event.CycleID) event.Payload  { return event.CyclePaused{CycleID: id} }
func resumeOf(id event.CycleID) event.Payload { return event.CycleResumed{CycleID: id} }
func stopOf(id event.CycleID) event.Payload   { return event.CycleStopped{CycleID: id} }

// switchTo adds a switch at instant when: from, the running cycle, pauses and to
// resumes, in one batch.
func (b *logb) switchTo(when time.Time, from, to event.CycleID) {
	b.batch(
		when,
		func(bt event.ID) event.Payload { return event.CyclePaused{CycleID: from, Batch: bt} },
		func(bt event.ID) event.Payload { return event.CycleResumed{CycleID: to, Batch: bt} },
	)
}

// bootstrapped is a log whose first batch, at t0, sets the profile "normal"
// and the day in New York.
func bootstrapped(t *testing.T) *logb {
	t.Helper()
	b := newLog(t)
	b.batch(t0, profileOf("normal"), dayIn(day1, newYork))
	return b
}

// loadConfig is the example configuration after edit has changed its generic
// tree; a nil edit leaves it as it is.
func loadConfig(t *testing.T, edit func(c map[string]any)) *config.Config {
	t.Helper()
	raw, err := os.ReadFile(configFixture)
	if err != nil {
		t.Fatal(err)
	}
	if edit != nil {
		var c map[string]any
		if err := json.Unmarshal(raw, &c); err != nil {
			t.Fatal(err)
		}
		edit(c)
		if raw, err = json.Marshal(c); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := config.Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return cfg
}

// focusOf is the focus of the state, or the zero view when there is none, so a
// test that expects one fails on its assertion instead of panicking.
func focusOf(st view.State) view.CycleView {
	if st.Focus == nil {
		return view.CycleView{}
	}
	return *st.Focus
}

// nextOf is the next task of the state, or the zero view when there is none.
func nextOf(st view.State) view.TaskView {
	if st.Next == nil {
		return view.TaskView{}
	}
	return *st.Next
}

func cycleIDs(cs []view.CycleView) []event.CycleID {
	var out []event.CycleID
	for _, c := range cs {
		out = append(out, c.Cycle.ID)
	}
	return out
}

func dimmedIDs(st view.State) []event.CycleID {
	var out []event.CycleID
	for _, c := range st.Dimmed {
		out = append(out, c.Cycle.ID)
	}
	return out
}

func cycleViews(ds []view.DimmedCycle) []view.CycleView {
	var out []view.CycleView
	for _, d := range ds {
		out = append(out, d.CycleView)
	}
	return out
}
