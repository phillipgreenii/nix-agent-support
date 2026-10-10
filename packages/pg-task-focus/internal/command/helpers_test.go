package command_test

import (
	"slices"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/civil"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/command"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/due"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/projection"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/testutil"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/zone"
)

// The tests build their stored logs from payload structs, pass every event
// through the real encoder and decoder, and replay them, so a fixture cannot
// drift from the codec. The configuration is the example fixture.

const (
	newYork = "America/New_York"

	cycleA = event.CycleID("01J9ZZZZZZZZZZZZZZZZZZZZZA")
	cycleB = event.CycleID("01J9ZZZZZZZZZZZZZZZZZZZZZB")
	cycleC = event.CycleID("01J9ZZZZZZZZZZZZZZZZZZZZZC")

	deepWork      = "deep-work"
	deepWorkTitle = "Deep work cycle"
	review        = "review"
	reviewTitle   = "Review cycle"
)

var (
	// t0 is 08:00 on 2026-10-07 in New York.
	t0   = time.Date(2026, time.October, 7, 12, 0, 0, 0, time.UTC)
	day1 = civil.Date{Year: 2026, Month: time.October, Day: 7}
	day2 = day1.AddDays(1)

	// clientID is an id a client supplies with its request.
	clientID = idOf('C', 1)

	postPlan = event.NewTaskID(due.Daily, day1, "post-plan")
	planDay  = event.NewTaskID(due.Daily, day1, "plan-day")
)

// at is the instant n minutes after t0.
func at(n int) time.Time { return t0.Add(time.Duration(n) * time.Minute) }

func intPtr(n int) *int { return &n }

// logb builds a stored log one event at a time.
type logb struct {
	t      *testing.T
	events []event.Event
	ids    uint32
}

func newLog(t *testing.T) *logb {
	t.Helper()
	return &logb{t: t}
}

func (b *logb) id() event.ID {
	b.ids++
	return idOf('L', b.ids)
}

// addAt appends an event recorded at recorded and effective at effective.
func (b *logb) addAt(recorded, effective time.Time, p event.Payload) event.ID {
	b.t.Helper()
	line, err := event.Encode(event.Event{
		Envelope: event.Envelope{V: event.SchemaVersion, ID: b.id(), At: event.At(recorded), EffectiveAt: event.At(effective), Type: p.EventType()},
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

// add appends an event recorded and effective n minutes after t0.
func (b *logb) add(n int, p event.Payload) event.ID { return b.addAt(at(n), at(n), p) }

// batch appends the members and their batch.committed at n minutes after t0
// and returns the batch id.
func (b *logb) batch(n int, members ...func(event.ID) event.Payload) event.ID {
	b.t.Helper()
	id := b.id()
	for _, m := range members {
		b.add(n, m(id))
	}
	b.add(n, event.BatchCommitted{Batch: id})
	return id
}

// switchTo appends a switch at n: from pauses and to resumes, in one batch.
func (b *logb) switchTo(n int, from, to event.CycleID) event.ID {
	return b.batch(
		n,
		func(bt event.ID) event.Payload { return event.CyclePaused{CycleID: from, Batch: bt} },
		func(bt event.ID) event.Payload { return event.CycleResumed{CycleID: to, Batch: bt} },
	)
}

func (b *logb) model() *projection.Model {
	b.t.Helper()
	m, err := projection.Replay(b.events)
	if err != nil {
		b.t.Fatalf("Replay: %v", err)
	}
	return m
}

// taskOf materializes a daily task of day1 in a batch.
func taskOf(def string) func(event.ID) event.Payload {
	z, err := zone.Load(newYork)
	if err != nil {
		panic(err)
	}
	return func(bt event.ID) event.Payload {
		return event.TaskMaterialized{
			TaskID: event.NewTaskID(due.Daily, day1, def), Definition: def, Cadence: due.Daily, Period: day1,
			Title: "Task " + def, Link: "https://example.test/" + def, Due: event.At(at(90)),
			DueRule: due.Rule{At: civil.TimeOfDay{Hour: 9, Minute: 30}, TZ: z}, Batch: bt,
		}
	}
}

// bootstrapped is a log whose first batch, an hour before t0, sets the
// profile "normal" and the day in New York and materializes post-plan and
// plan-day.
func bootstrapped(t *testing.T) *logb {
	t.Helper()
	b := newLog(t)
	b.batch(
		-60,
		func(bt event.ID) event.Payload { return event.ProfileChanged{Profile: "normal", Batch: bt} },
		func(bt event.ID) event.Payload {
			return event.PeriodChanged{Kind: "day", Start: day1, TZ: newYork, Batch: bt}
		},
		taskOf("post-plan"), taskOf("plan-day"),
	)
	return b
}

// startOf is the start of a cycle of one of the example's types, with that
// type's title.
func startOf(id event.CycleID, typ string) event.Payload {
	titles := map[string]string{deepWork: deepWorkTitle, review: reviewTitle}
	return event.CycleStarted{CycleID: id, Type: typ, Title: titles[typ], PlannedMinutes: 25}
}

func interruptOf(id, of event.CycleID, typ string) event.Payload {
	p := startOf(id, typ).(event.CycleStarted)
	p.Interrupts = of
	return p
}

func pauseOf(id event.CycleID) event.Payload  { return event.CyclePaused{CycleID: id} }
func resumeOf(id event.CycleID) event.Payload { return event.CycleResumed{CycleID: id} }
func stopOf(id event.CycleID) event.Payload   { return event.CycleStopped{CycleID: id} }

// newIDs draws fresh ids of the 'N' family, the way the engine's generator
// would.
func newIDs() func() event.ID {
	var n uint32
	return func() event.ID {
		n++
		return idOf('N', n)
	}
}

// envOf is the environment of a request against the stored log b, read at now
// with the example configuration.
func envOf(t *testing.T, b *logb, now time.Time) command.Env {
	t.Helper()
	return command.Env{Model: b.model(), Config: testutil.LoadConfig(t, nil), Now: now, NewID: newIDs()}
}

// then is env after the plan's events are adopted, read at now.
func then(t *testing.T, env command.Env, p command.Plan, now time.Time) command.Env {
	t.Helper()
	if p.Candidate == nil {
		t.Fatal("the plan has no candidate model to adopt")
	}
	env.Model, env.Now = p.Candidate, now
	return env
}

// mustPlan builds a command that MUST succeed.
func mustPlan(t *testing.T, env command.Env, c command.Command) command.Plan {
	t.Helper()
	p, err := command.Build(env, c)
	if err != nil {
		t.Fatalf("Build(%T): %v", c, err)
	}
	return p
}

// mustReject builds a command that MUST be rejected with reason want, and
// checks that nothing was planned.
func mustReject(t *testing.T, env command.Env, c command.Command, want command.Reason) command.Rejection {
	t.Helper()
	p, err := command.Build(env, c)
	if len(p.Events) != 0 || p.Candidate != nil || p.NoOp {
		t.Errorf("a rejected %T planned %d events (NoOp %v)", c, len(p.Events), p.NoOp)
	}
	return testutil.RejectionOf(t, err, want)
}

// mustNoOp builds a command that MUST be a no-op and returns its note.
func mustNoOp(t *testing.T, env command.Env, c command.Command) string {
	t.Helper()
	p := mustPlan(t, env, c)
	if !p.NoOp || len(p.Events) != 0 || p.Candidate != nil {
		t.Fatalf("Build(%T) = NoOp %v with %d events, want a no-op with none", c, p.NoOp, len(p.Events))
	}
	if p.Note == "" {
		t.Errorf("Build(%T): a no-op has no note", c)
	}
	return p.Note
}

// only is the one event of a plan, which MUST have exactly one.
func only(t *testing.T, p command.Plan) event.Event {
	t.Helper()
	if len(p.Events) != 1 {
		t.Fatalf("plan has %d events, want 1", len(p.Events))
	}
	return p.Events[0]
}

// cycleIDsOf lists the ids of the cycles a rejection names.
func cycleIDsOf(r command.Rejection) []event.CycleID {
	var out []event.CycleID
	for _, c := range r.Cycles {
		out = append(out, c.ID)
	}
	return out
}

func sameIDs[T comparable](got []T, want ...T) bool { return slices.Equal(got, want) }

// status is the status of a cycle in a model at an instant.
func status(t *testing.T, m *projection.Model, id event.CycleID, when time.Time) projection.CycleStatus {
	t.Helper()
	c, ok := m.Cycle(id)
	if !ok {
		t.Fatalf("cycle %s not in the model", id)
	}
	return c.StatusAt(when)
}

// idOf is the nth id of a family: the stored log ('L'), the ids the command
// layer draws ('N'). Ids of different families never collide.
func idOf(family byte, n uint32) event.ID { return testutil.IDOf(t0, family, n) }
