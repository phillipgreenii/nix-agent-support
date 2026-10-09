package view_test

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"os"
	"slices"
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

func TestUninitializedState(t *testing.T) {
	for name, events := range map[string][]event.Event{"nil log": nil, "empty log": {}} {
		t.Run(name, func(t *testing.T) {
			m, err := projection.Replay(events)
			if err != nil {
				t.Fatalf("Replay: %v", err)
			}
			st := view.Build(m, loadConfig(t, nil), t0)
			if st.Initialized {
				t.Error("Initialized = true for an empty log")
			}
			if st.Profile != "" || len(st.Periods) != 0 || len(st.Tasks) != 0 || st.Next != nil ||
				st.Focus != nil || len(st.Dimmed) != 0 || len(st.InterruptStack) != 0 || st.ResumeOffer != nil {
				t.Errorf("State of an empty log = %+v, want nothing in it", st)
			}
		})
	}

	t.Run("a bootstrapped log is initialized", func(t *testing.T) {
		st := view.Build(bootstrapped(t).model(), loadConfig(t, nil), t0)
		if !st.Initialized || st.Profile != "normal" {
			t.Errorf("Initialized = %v, Profile = %q, want true and normal", st.Initialized, st.Profile)
		}
	})
}

func TestEndedBoundaryIsInclusiveEnd(t *testing.T) {
	weekStart := civil.Date{Year: 2026, Month: time.October, Day: 5}
	weekEnd := civil.Date{Year: 2026, Month: time.October, Day: 11}
	b := newLog(t)
	b.batch(t0, profileOf("normal"), dayIn(day1, newYork), periodOf("week", weekStart, &weekEnd, newYork))
	m, cfg := b.model(), loadConfig(t, nil)

	ny, err := time.LoadLocation(newYork)
	if err != nil {
		t.Fatal(err)
	}
	local := func(day, h, mi int) time.Time { return time.Date(2026, time.October, day, h, mi, 0, 0, ny) }
	tests := []struct {
		name                string
		now                 time.Time
		dayEnded, weekEnded bool
	}{
		{"the start of the day", local(7, 0, 0), false, false},
		{"the last minute of the day", local(7, 23, 59), false, false},
		{"the first minute of the next day", local(8, 0, 0), true, false},
		{"the last minute of the last week date", local(11, 23, 59), true, false},
		{"the first minute after the last week date", local(12, 0, 0), true, true},
		{"the same instant read in UTC", local(12, 0, 0).UTC(), true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := view.Build(m, cfg, tt.now)
			if len(st.Periods) != 2 {
				t.Fatalf("%d periods, want day and week", len(st.Periods))
			}
			if got := st.Periods[0]; got.Kind != projection.Day || got.Ended != tt.dayEnded {
				t.Errorf("day Ended = %v, want %v", got.Ended, tt.dayEnded)
			}
			if got := st.Periods[1]; got.Kind != projection.Week || got.Ended != tt.weekEnded {
				t.Errorf("week Ended = %v, want %v", got.Ended, tt.weekEnded)
			}
		})
	}
}

func TestTodayUsesPeriodZoneNotHostZone(t *testing.T) {
	// 2026-10-07 20:00 UTC is 2026-10-08 09:00 in Auckland (UTC+13 in daylight
	// saving time), 2026-10-07 16:00 in New York and 13:00 in Los Angeles.
	now := time.Date(2026, time.October, 7, 20, 0, 0, 0, time.UTC)
	auckland, newYorkDate := civil.Date{Year: 2026, Month: time.October, Day: 8}, civil.Date{Year: 2026, Month: time.October, Day: 7}

	la, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	host := time.Local
	time.Local = la
	t.Cleanup(func() { time.Local = host })

	weekEnd := civil.Date{Year: 2026, Month: time.October, Day: 11}
	b := newLog(t)
	b.batch(t0, profileOf("normal"),
		dayIn(day1, "Pacific/Auckland"),
		periodOf("week", civil.Date{Year: 2026, Month: time.October, Day: 5}, &weekEnd, newYork))
	st := view.Build(b.model(), loadConfig(t, nil), now.In(la))

	if len(st.Periods) != 2 {
		t.Fatalf("%d periods, want day and week", len(st.Periods))
	}
	if got := st.Periods[0]; got.Zone != "Pacific/Auckland" || got.Today != auckland || !got.Ended {
		t.Errorf("day: Zone %q Today %s Ended %v, want Pacific/Auckland, %s and ended: the 7th is over there", got.Zone, got.Today, got.Ended, auckland)
	}
	if got := st.Periods[1]; got.Zone != newYork || got.Today != newYorkDate || got.Ended {
		t.Errorf("week: Zone %q Today %s Ended %v, want %s, %s and not ended: each kind reads its own zone", got.Zone, got.Today, got.Ended, newYork, newYorkDate)
	}
}

func TestPeriodStateCarriesTheStoredPeriod(t *testing.T) {
	end := civil.Date{Year: 2026, Month: time.October, Day: 18}
	b := newLog(t)
	b.batch(t0, profileOf("normal"), dayIn(day1, newYork),
		func(bt event.ID) event.Payload {
			return event.PeriodChanged{Kind: "sprint", Start: civil.Date{Year: 2026, Month: time.October, Day: 5}, End: &end, TZ: "Europe/Berlin", Label: "Sprint 12", Batch: bt}
		})
	st := view.Build(b.model(), loadConfig(t, nil), at(10))
	if len(st.Periods) != 2 || st.Periods[0].Kind != projection.Day || st.Periods[1].Kind != projection.Sprint {
		t.Fatalf("Periods = %+v, want day then sprint and no week", st.Periods)
	}
	got := st.Periods[1]
	if got.Label != "Sprint 12" || got.End != end || got.Zone != "Europe/Berlin" || got.Ended || got.Banner != "" {
		t.Errorf("sprint = %+v, want its label, end and zone, not ended, no banner", got)
	}
	if d := st.Periods[0]; d.Start != day1 || d.End != day1 {
		t.Errorf("day Start %s End %s, want both %s", d.Start, d.End, day1)
	}
}

func TestFocusIsTheRunningCycleOnly(t *testing.T) {
	cfg := loadConfig(t, nil)

	t.Run("nothing runs", func(t *testing.T) {
		b := bootstrapped(t)
		b.add(at(1), startOf(cycleA, "deep-work", 50))
		b.add(at(10), pauseOf(cycleA))
		b.add(at(11), startOf(cycleB, "review", 25))
		b.add(at(20), stopOf(cycleB))
		st := view.Build(b.model(), cfg, at(30))
		if st.Focus != nil {
			t.Errorf("Focus = %+v, want none: a paused and a stopped cycle are not the focus", st.Focus.Cycle.ID)
		}
		if got := dimmedIDs(st); !slices.Equal(got, []event.CycleID{cycleA}) {
			t.Errorf("Dimmed = %v, want only the paused cycle", got)
		}
	})

	t.Run("the timer is read at now", func(t *testing.T) {
		b := bootstrapped(t)
		b.add(at(0), startOf(cycleA, "deep-work", 50))
		b.add(at(30), event.CycleBoosted{CycleID: cycleA, Minutes: 10})
		m := b.model()

		tests := []struct {
			name      string
			now       time.Time
			elapsed   time.Duration
			remaining time.Duration
			overtime  bool
		}{
			{"before the boost", at(20), 20 * time.Minute, 30 * time.Minute, false},
			{"at the end of the budget", at(60), time.Hour, 0, false},
			{"in overtime", at(65), 65 * time.Minute, -5 * time.Minute, true},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				st := view.Build(m, cfg, tt.now)
				if st.Focus == nil || st.Focus.Cycle.ID != cycleA {
					t.Fatalf("Focus = %+v, want cycle A", st.Focus)
				}
				if st.Focus.Elapsed != tt.elapsed || st.Focus.Remaining != tt.remaining || st.Focus.Overtime != tt.overtime {
					t.Errorf("Elapsed %v Remaining %v Overtime %v, want %v %v %v",
						st.Focus.Elapsed, st.Focus.Remaining, st.Focus.Overtime, tt.elapsed, tt.remaining, tt.overtime)
				}
			})
		}
	})

	t.Run("a running cycle is never also dimmed", func(t *testing.T) {
		b := bootstrapped(t)
		b.add(at(0), startOf(cycleA, "deep-work", 50))
		b.add(at(10), interruptOf(cycleB, cycleA, "notifications", 15))
		st := view.Build(b.model(), cfg, at(12))
		if st.Focus == nil || st.Focus.Cycle.ID != cycleB {
			t.Fatalf("Focus = %+v, want the interrupting cycle", st.Focus)
		}
		if got := dimmedIDs(st); !slices.Equal(got, []event.CycleID{cycleA}) {
			t.Errorf("Dimmed = %v, want only the interrupted cycle", got)
		}
	})
}

func TestDimmedListsEveryPausedNotStoppedCycle(t *testing.T) {
	cfg := loadConfig(t, nil)
	b := bootstrapped(t)
	b.add(at(-50), startOf(cycleD, "review", 25))
	b.add(at(-45), pauseOf(cycleD)) // paused by hand
	b.add(at(-30), startOf(cycleE, "review", 25))
	b.add(at(-25), stopOf(cycleE)) // stopped: never dimmed
	b.add(at(0), startOf(cycleA, "deep-work", 50))
	b.add(at(10), interruptOf(cycleB, cycleA, "notifications", 15)) // A interrupted
	b.add(at(20), pauseOf(cycleB))
	b.add(at(30), startOf(cycleC, "review", 25))
	b.switchTo(at(40), cycleC, cycleB) // C switched away from

	t.Run("a cycle runs", func(t *testing.T) {
		st := view.Build(b.model(), cfg, at(50))
		if st.Focus == nil || st.Focus.Cycle.ID != cycleB {
			t.Fatalf("Focus = %+v, want B", st.Focus)
		}
		if got, want := dimmedIDs(st), []event.CycleID{cycleC, cycleA, cycleD}; !slices.Equal(got, want) {
			t.Errorf("Dimmed = %v, want %v: switched away, interrupted and paused by hand, most recently paused first, no stopped cycle", got, want)
		}
		for _, d := range st.Dimmed {
			if !d.CanSwitch {
				t.Errorf("cycle %s CanSwitch = false while a cycle runs", d.Cycle.ID)
			}
		}
	})

	t.Run("nothing runs", func(t *testing.T) {
		b.add(at(60), stopOf(cycleB))
		st := view.Build(b.model(), cfg, at(70))
		if st.Focus != nil {
			t.Fatalf("Focus = %v, want none", st.Focus.Cycle.ID)
		}
		if got, want := dimmedIDs(st), []event.CycleID{cycleC, cycleA, cycleD}; !slices.Equal(got, want) {
			t.Errorf("Dimmed = %v, want %v", got, want)
		}
		for _, d := range st.Dimmed {
			if d.CanSwitch {
				t.Errorf("cycle %s CanSwitch = true with nothing running", d.Cycle.ID)
			}
		}
	})
}

func TestDimmedCycleCarriesItsFrozenTimer(t *testing.T) {
	b := bootstrapped(t)
	b.add(at(0), startOf(cycleA, "deep-work", 50))
	b.add(at(20), pauseOf(cycleA))
	st := view.Build(b.model(), loadConfig(t, nil), at(500))
	if len(st.Dimmed) != 1 {
		t.Fatalf("%d dimmed cycles, want 1", len(st.Dimmed))
	}
	d := st.Dimmed[0]
	if d.Elapsed != 20*time.Minute || d.Remaining != 30*time.Minute || d.Overtime {
		t.Errorf("Elapsed %v Remaining %v Overtime %v, want 20m 30m false: a paused cycle accrues nothing", d.Elapsed, d.Remaining, d.Overtime)
	}
}

func TestNotInProfileFlag(t *testing.T) {
	// The profile "normal" lists notifications, review and deep-work; the type
	// page-response belongs to "on-call", and "ad-hoc" is defined nowhere.
	cfg := loadConfig(t, nil)
	b := bootstrapped(t)
	b.add(at(0), startOf(cycleA, "deep-work", 50))
	b.add(at(5), interruptOf(cycleB, cycleA, "page-response", 25))
	b.add(at(10), interruptOf(cycleC, cycleB, "ad-hoc", 25))
	b.add(at(15), interruptOf(cycleD, cycleC, "notifications", 25))
	st := view.Build(b.model(), cfg, at(20))

	got := map[event.CycleID]bool{focusOf(st).Cycle.ID: focusOf(st).NotInProfile}
	for _, d := range st.Dimmed {
		got[d.Cycle.ID] = d.NotInProfile
	}
	want := map[event.CycleID]bool{cycleA: false, cycleB: true, cycleC: true, cycleD: false}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("cycle %s NotInProfile = %v, want %v", id, got[id], w)
		}
	}

	t.Run("a profile change moves the flag", func(t *testing.T) {
		onCall := bootstrapped(t)
		onCall.add(at(0), startOf(cycleA, "page-response", 25))
		onCall.batch(at(1), profileOf("on-call"))
		if st := view.Build(onCall.model(), cfg, at(2)); focusOf(st).NotInProfile {
			t.Error("page-response is flagged under on-call, which lists it")
		}
	})
}

func TestCycleTypeRemovedFromConfigStillRenders(t *testing.T) {
	b := bootstrapped(t)
	b.add(at(0), event.CycleStarted{CycleID: cycleA, Type: "retired-type", Title: "A cycle the config forgot", PlannedMinutes: 40})
	b.add(at(5), event.CycleStarted{CycleID: cycleB, Type: "deep-work", Title: "Deep work cycle", PlannedMinutes: 50, Interrupts: cycleA})
	m := b.model()

	t.Run("the removed type keeps its snapshot and takes the defaults", func(t *testing.T) {
		cfg := loadConfig(t, nil)
		st := view.Build(m, cfg, at(10))
		if len(st.Dimmed) != 1 {
			t.Fatalf("%d dimmed cycles, want the interrupted one", len(st.Dimmed))
		}
		c := st.Dimmed[0]
		if c.Cycle.Type != "retired-type" || c.Cycle.Title != "A cycle the config forgot" || c.Cycle.PlannedMinutes != 40 {
			t.Errorf("Type %q Title %q Planned %d, want the snapshot in the log", c.Cycle.Type, c.Cycle.Title, c.Cycle.PlannedMinutes)
		}
		if want := cfg.Defaults().Alert; c.Alert != want {
			t.Errorf("Alert = %+v, want defaults.alert %+v", c.Alert, want)
		}
		if !c.NotInProfile {
			t.Error("NotInProfile = false for a type no profile lists")
		}
		if c.Elapsed != 5*time.Minute {
			t.Errorf("Elapsed = %v, want 5m from the segments in the log", c.Elapsed)
		}
	})

	t.Run("a defined type takes its own alert", func(t *testing.T) {
		cfg := loadConfig(t, nil)
		st := view.Build(m, cfg, at(10))
		if want := cfg.Alert("deep-work"); focusOf(st).Alert != want {
			t.Errorf("Alert = %+v, want the type's %+v", focusOf(st).Alert, want)
		}
		if focusOf(st).Alert == cfg.Defaults().Alert {
			t.Error("the deep-work alert equals the defaults: the fixture no longer tells the two apart")
		}
	})

	t.Run("a defined type that the config later drops falls back", func(t *testing.T) {
		cfg := loadConfig(t, func(c map[string]any) {
			cycles := c["cycles"].(map[string]any)
			delete(cycles, "deep-work")
			profiles := c["profiles"].(map[string]any)
			for _, p := range profiles {
				p.(map[string]any)["cycles"] = []any{"notifications", "review", "page-response"}
			}
		})
		st := view.Build(m, cfg, at(10))
		if f := focusOf(st); f.Cycle.Title != "Deep work cycle" || f.Cycle.Type != "deep-work" {
			t.Errorf("Focus type %q title %q, want the snapshot", f.Cycle.Type, f.Cycle.Title)
		}
		if focusOf(st).Alert != cfg.Defaults().Alert {
			t.Errorf("Alert = %+v, want defaults.alert %+v", focusOf(st).Alert, cfg.Defaults().Alert)
		}
	})
}

func TestStoreIsLeftZero(t *testing.T) {
	if st := view.Build(bootstrapped(t).model(), loadConfig(t, nil), t0); st.Store != (view.StoreHealth{}) {
		t.Errorf("Store = %+v, want the zero value: the engine fills it from the store", st.Store)
	}
}
