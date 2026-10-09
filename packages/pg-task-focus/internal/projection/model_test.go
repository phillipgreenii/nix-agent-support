package projection

import (
	"reflect"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/civil"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/due"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
)

var (
	week1      = civil.Date{Year: 2026, Month: time.October, Day: 5}
	sprint1    = civil.Date{Year: 2026, Month: time.September, Day: 28}
	weeklyTask = event.NewTaskID(due.Weekly, week1, "review")
	sprintTask = event.NewTaskID(due.Sprint, sprint1, "demo")
)

// sharingLog is a log whose model has every pointer, slice and map an accessor
// can return: a completed weekly task (ResolvedAt, DueRule.Weekday), a sprint
// task (DueRule.Day), a stopped cycle with boosts, an annotation and closed
// segments, a paused cycle, and a corrected event. Each call allocates its own
// payloads, so two logs it builds share nothing but the read-only zone rules.
func sharingLog(t *testing.T) (*logb, event.ID) {
	t.Helper()
	z := newZone(t, "America/New_York")
	weekday, day := time.Thursday, 3
	materialize := func(id event.TaskID, def string, c due.Cadence, period civil.Date, rule due.Rule) func(event.ID) event.Payload {
		return func(bt event.ID) event.Payload {
			return event.TaskMaterialized{
				TaskID: id, Definition: def, Cadence: c, Period: period, Title: "Task " + def,
				Link: exampleLink, Due: event.At(at(60)), DueRule: rule, Batch: bt,
			}
		}
	}
	sprintEnd := sprint1.AddDays(13)
	b := newLog(t)
	b.batch(-120, profileOf("work"), dayOf(day1), weekOf(week1, week1.AddDays(6)),
		periodOf("sprint", sprint1, &sprintEnd, "America/New_York", "Sprint 12"))
	b.batch(-110,
		materialize(weeklyTask, "review", due.Weekly, week1, due.Rule{At: civil.TimeOfDay{Hour: 9}, TZ: z, Weekday: &weekday}),
		materialize(sprintTask, "demo", due.Sprint, sprint1, due.Rule{At: civil.TimeOfDay{Hour: 9}, TZ: z, Day: &day}))
	b.add(-100, event.TaskCompleted{TaskID: weeklyTask})
	b.add(0, startOf(cycleA, 50))
	b.add(5, boostOf(cycleA, 10))
	b.add(6, noteOf(cycleA, "notes", "ticket", "T-1"))
	b.add(10, stopOf(cycleA))
	b.add(20, startOf(cycleB, 25))
	boosted := b.add(25, boostOf(cycleB, 5))
	b.add(26, noteOf(cycleB, "more", "ticket", "T-2"))
	b.add(30, pauseOf(cycleB))
	b.add(31, event.EventCorrected{Target: boosted, Fields: fieldsOf("minutes", 15)})
	return b, boosted
}

// scribbleTask writes through every pointer and into every field of a task.
func scribbleTask(task *Task) {
	task.Title = "scribbled"
	if task.ResolvedAt != nil {
		*task.ResolvedAt = time.Time{}
	}
	if task.DueRule.Weekday != nil {
		*task.DueRule.Weekday = time.Sunday
	}
	if task.DueRule.Day != nil {
		*task.DueRule.Day = 99
	}
}

// scribbleCycle writes through every pointer and into every slice of a cycle.
func scribbleCycle(c *Cycle) {
	c.Title = "scribbled"
	for i := range c.Boosts {
		c.Boosts[i].Minutes = -1
	}
	for i := range c.KV {
		c.KV[i].Value = "scribbled"
	}
	if c.StoppedAt != nil {
		*c.StoppedAt = time.Time{}
	}
	for i := range c.Segments {
		c.Segments[i].Start = time.Time{}
		if c.Segments[i].End != nil {
			*c.Segments[i].End = time.Time{}
		}
	}
}

func scribbleView(v *EventView) {
	for i := range v.CorrectedBy {
		v.CorrectedBy[i] = ""
	}
}

// TestAccessorsShareNothingWithTheModel pins that every value an accessor
// returns belongs to the caller: writing through each returned pointer, slice
// and map leaves a fresh read equal to that of a model no one wrote to.
func TestAccessorsShareNothingWithTheModel(t *testing.T) {
	b, corrected := sharingLog(t)
	m := mustReplay(t, b.events)
	refLog, _ := sharingLog(t)
	ref := mustReplay(t, refLog.events)

	for _, id := range []event.TaskID{weeklyTask, sprintTask} {
		task := mustTask(t, m, id)
		scribbleTask(&task)
	}
	for _, task := range m.Tasks() {
		scribbleTask(&task)
	}
	tasks := m.Tasks()
	for i := range tasks {
		scribbleTask(&tasks[i])
	}
	for _, id := range []event.CycleID{cycleA, cycleB} {
		c := mustCycle(t, m, id)
		scribbleCycle(&c)
	}
	for _, list := range [][]Cycle{m.Cycles(), m.Dimmed()} {
		for i := range list {
			scribbleCycle(&list[i])
		}
	}
	d := m.Domain()
	d.Profile = "scribbled"
	for i := range d.Tasks {
		scribbleTask(&d.Tasks[i])
	}
	for i := range d.Cycles {
		scribbleCycle(&d.Cycles[i])
	}
	d.Periods[Sprint] = Period{}
	delete(d.Periods, Day)
	views := m.Events(EventQuery{})
	for i := range views {
		scribbleView(&views[i])
	}
	if v, ok := m.Event(corrected); ok {
		scribbleView(&v)
	}

	// Each pinned state must still hold what the test writes over, or the
	// writes above prove nothing.
	if task := mustTask(t, ref, weeklyTask); task.ResolvedAt == nil || task.DueRule.Weekday == nil {
		t.Fatalf("weekly task %+v: want a resolution instant and a weekday", task)
	}
	if task := mustTask(t, ref, sprintTask); task.DueRule.Day == nil {
		t.Fatalf("sprint task %+v: want a day", task)
	}
	if a, dimmed := mustCycle(t, ref, cycleA), ref.Dimmed(); a.StoppedAt == nil || len(a.Boosts) == 0 || len(a.KV) == 0 || len(dimmed) != 1 {
		t.Fatalf("cycle A %+v and dimmed %+v: want a stopped, boosted, annotated cycle and one dimmed", a, dimmed)
	}
	if v, _ := ref.Event(corrected); len(v.CorrectedBy) != 1 {
		t.Fatalf("view %+v: want one correction", v)
	}

	reads := []struct {
		name string
		read func(*Model) any
	}{
		{"Task", func(m *Model) any { w, _ := m.Task(weeklyTask); s, _ := m.Task(sprintTask); return []Task{w, s} }},
		{"Tasks", func(m *Model) any { return m.Tasks() }},
		{"Cycle", func(m *Model) any { a, _ := m.Cycle(cycleA); b, _ := m.Cycle(cycleB); return []Cycle{a, b} }},
		{"Cycles", func(m *Model) any { return m.Cycles() }},
		{"Dimmed", func(m *Model) any { return m.Dimmed() }},
		{"Domain", func(m *Model) any { return m.Domain() }},
		{"Events", func(m *Model) any { return m.Events(EventQuery{}) }},
		{"Event", func(m *Model) any { v, _ := m.Event(corrected); return v }},
	}
	for _, r := range reads {
		if got, want := r.read(m), r.read(ref); !reflect.DeepEqual(got, want) {
			t.Errorf("%s after writing through earlier results:\n got  %+v\n want %+v", r.name, got, want)
		}
	}
}

// TestDomainEqualAcrossSeparatelyLoadedZones pins that Domain equality does
// not depend on where a zone was loaded: one model holds the zones the test
// loaded, the other the zones the codec loaded decoding the same log, and the
// replay loads each period's zone again.
func TestDomainEqualAcrossSeparatelyLoadedZones(t *testing.T) {
	b, _ := sharingLog(t)
	decoded := make([]event.Event, len(b.events))
	for i, e := range b.events {
		line, err := event.Encode(e)
		if err != nil {
			t.Fatalf("Encode event %d: %v", i+1, err)
		}
		if decoded[i], err = event.Decode(line); err != nil {
			t.Fatalf("Decode event %d: %v", i+1, err)
		}
	}
	built, read := mustReplay(t, b.events).Domain(), mustReplay(t, decoded).Domain()
	if !reflect.DeepEqual(built, read) {
		t.Errorf("Domain differs:\n built %+v\n read  %+v", built, read)
	}
	for k, p := range built.Periods {
		if p.TZ != read.Periods[k].TZ {
			t.Errorf("the %s period's zone differs: %+v and %+v", k, p.TZ, read.Periods[k].TZ)
		}
	}
	for i, task := range built.Tasks {
		if task.DueRule.TZ != read.Tasks[i].DueRule.TZ {
			t.Errorf("task %s's due zone differs: %+v and %+v", task.ID, task.DueRule.TZ, read.Tasks[i].DueRule.TZ)
		}
	}
}
