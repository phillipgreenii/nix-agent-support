package testgen

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"time"

	"pgregory.net/rapid"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
)

// The generators build every event through event.Encode and event.Decode, so
// a generated event is exactly what a store hands to replay. The period and
// task payloads are written as JSON, which keeps this package's imports to
// event and rapid.

// zoneNames are IANA zones the standard library resolves in their exact
// spelling, including zones with half-hour and skipped-date history.
var zoneNames = []string{
	"America/New_York", "America/Los_Angeles", "America/St_Johns", "Europe/London",
	"Europe/Berlin", "Asia/Kolkata", "Asia/Tokyo", "Australia/Lord_Howe", "Pacific/Apia", "UTC",
}

// ZoneName draws the name of an IANA zone.
func ZoneName() *rapid.Generator[string] { return rapid.SampledFrom(zoneNames) }

var (
	firstInstant = time.Date(2020, time.January, 1, 0, 0, 0, 0, time.UTC)
	lastInstant  = time.Date(2035, time.December, 31, 0, 0, 0, 0, time.UTC)
)

// Instant draws a UTC instant with millisecond precision, from 2020 to 2035.
func Instant() *rapid.Generator[time.Time] {
	return rapid.Custom(func(t *rapid.T) time.Time {
		ms := rapid.Int64Range(firstInstant.UnixMilli(), lastInstant.UnixMilli()).Draw(t, "unix_ms")
		return time.UnixMilli(ms).UTC()
	})
}

const dateLayout = "2006-01-02"

// builder appends events with fresh, unique ids.
type builder struct {
	events []event.Event
	ids    uint32
}

// id is a ULID at instant at whose entropy is a counter, so it is unique and
// the same on every run.
func (b *builder) id(at time.Time) event.ID {
	b.ids++
	var entropy [10]byte
	binary.BigEndian.PutUint32(entropy[6:], b.ids)
	return event.NewID(at, bytes.NewReader(entropy[:]))
}

// emit appends the event recorded at recorded, effective at effective, with
// payload p or, when p is nil, the data of type typ.
func (b *builder) emit(recorded, effective time.Time, p event.Payload, typ event.Type, data map[string]any) event.ID {
	env := event.Envelope{V: event.SchemaVersion, ID: b.id(recorded), At: event.At(recorded), EffectiveAt: event.At(effective), Type: typ}
	if p == nil {
		raw, err := json.Marshal(data)
		if err != nil {
			panic(fmt.Sprintf("testgen: %v", err))
		}
		env.Data = raw
	}
	line, err := event.Encode(event.Event{Envelope: env, Payload: p})
	if err != nil {
		panic(fmt.Sprintf("testgen built an event the codec refuses: %v", err))
	}
	e, err := event.Decode(line)
	if err != nil {
		panic(fmt.Sprintf("testgen built an event the codec does not read back: %v", err))
	}
	e.Line = len(b.events) + 1
	b.events = append(b.events, e)
	return e.ID
}

// add appends p, recorded and effective at at.
func (b *builder) add(at time.Time, p event.Payload) event.ID { return b.emit(at, at, p, "", nil) }

func (b *builder) commit(at time.Time, batch event.ID) {
	b.add(at, event.BatchCommitted{Batch: batch})
}

func (b *builder) profile(at time.Time, batch event.ID) {
	b.add(at, event.ProfileChanged{Profile: "work", Batch: batch})
}

func (b *builder) day(at, start time.Time, tz string, batch event.ID) {
	b.emit(at, at, nil, event.TypePeriodChanged, map[string]any{
		"kind": "day", "start": start.Format(dateLayout), "tz": tz, "batch": batch,
	})
}

// task materializes the daily task of definition def for the day starting on
// start, due at 17:00 in zone tz.
func (b *builder) task(at, start time.Time, def, tz string, batch event.ID) event.TaskID {
	id := event.TaskID("day:" + start.Format(dateLayout) + ":" + def)
	b.emit(at, at, nil, event.TypeTaskMaterialized, map[string]any{
		"task_id": id, "definition": def, "cadence": "daily", "period": start.Format(dateLayout),
		"title": "Task " + def, "link": "https://example.test/" + def,
		"due": event.At(start.Add(21 * time.Hour)), "due_rule": map[string]any{"at": "17:00", "tz": tz},
		"batch": batch,
	})
	return id
}

// cycleState is where the walk left one cycle.
type cycleState struct {
	id     event.CycleID
	status string    // "running", "paused" or "stopped"
	since  time.Time // the instant of its latest start or resume
}

// walk is a random walk over the task and cycle state machines that only ever
// takes a step the state allows, so every log it writes is valid by
// construction.
type walk struct {
	builder
	now    time.Time
	tz     string
	day    time.Time // the start date of the current day period, at 00:00 UTC
	cycles []*cycleState
	open   []event.TaskID // the open tasks of the current day
	defs   int
}

// Log draws a valid log: a first batch that sets the profile, the day and its
// tasks, then up to maxSteps steps over the task and cycle state machines
// (completions, skips and rollovers; starts, interrupts, pauses, resumes,
// switches, boosts, stops, annotations and back-filled breaks), each a moment
// after the one before. A failing log shrinks to fewer and simpler steps.
func Log(maxSteps int) *rapid.Generator[[]event.Event] {
	return rapid.Custom(func(t *rapid.T) []event.Event {
		w := &walk{now: Instant().Draw(t, "start"), tz: ZoneName().Draw(t, "zone")}
		w.day = w.now.Truncate(24 * time.Hour)
		w.rollover(t, true)
		steps := rapid.IntRange(0, maxSteps).Draw(t, "steps")
		for range steps {
			w.now = w.now.Add(time.Duration(rapid.IntRange(1, 3_600_000).Draw(t, "advance_ms")) * time.Millisecond)
			w.step(t, rapid.SampledFrom(w.ops()).Draw(t, "op"))
		}
		return w.events
	})
}

func (w *walk) running() *cycleState {
	for _, c := range w.cycles {
		if c.status == "running" {
			return c
		}
	}
	return nil
}

func (w *walk) with(statuses ...string) []*cycleState {
	var out []*cycleState
	for _, c := range w.cycles {
		for _, s := range statuses {
			if c.status == s {
				out = append(out, c)
			}
		}
	}
	return out
}

// ops lists the steps the state allows now.
func (w *walk) ops() []string {
	ops := []string{"start"}
	run, paused := w.running(), w.with("paused")
	if run != nil {
		ops = append(ops, "pause")
		if w.now.Sub(run.since) >= 2*time.Millisecond {
			ops = append(ops, "break")
		}
		if len(paused) > 0 {
			ops = append(ops, "switch")
		}
	} else if len(paused) > 0 {
		ops = append(ops, "resume")
	}
	if len(w.with("running", "paused")) > 0 {
		ops = append(ops, "boost", "stop")
	} else {
		ops = append(ops, "rollover")
	}
	if len(w.cycles) > 0 {
		ops = append(ops, "annotate")
	}
	if len(w.open) > 0 {
		ops = append(ops, "complete", "skip")
	}
	return ops
}

func pick[T any](t *rapid.T, label string, from []T) T {
	return from[rapid.IntRange(0, len(from)-1).Draw(t, label)]
}

var cycleTypes = []struct {
	typ, title string
}{{"deep-work", "Deep work"}, {"notifications", "Notifications"}, {"review", "Review"}}

func (w *walk) step(t *rapid.T, op string) {
	run := w.running()
	switch op {
	case "start":
		typ := pick(t, "type", cycleTypes)
		p := event.CycleStarted{
			CycleID: event.CycleID(w.id(w.now)), Type: typ.typ, Title: typ.title,
			PlannedMinutes: rapid.IntRange(1, 90).Draw(t, "planned_minutes"),
		}
		if run != nil { // starting while another runs interrupts it
			p.Interrupts, run.status = run.id, "paused"
		}
		w.add(w.now, p)
		w.cycles = append(w.cycles, &cycleState{id: p.CycleID, status: "running", since: w.now})
	case "pause":
		w.add(w.now, event.CyclePaused{CycleID: run.id})
		run.status = "paused"
	case "resume":
		c := pick(t, "cycle", w.with("paused"))
		w.add(w.now, event.CycleResumed{CycleID: c.id})
		c.status, c.since = "running", w.now
	case "switch":
		c := pick(t, "cycle", w.with("paused"))
		batch := w.id(w.now)
		w.add(w.now, event.CyclePaused{CycleID: run.id, Batch: batch})
		w.add(w.now, event.CycleResumed{CycleID: c.id, Batch: batch})
		w.commit(w.now, batch)
		run.status = "paused"
		c.status, c.since = "running", w.now
	case "break":
		// The running cycle ran without a pause from since to now, so any
		// break strictly inside that span, ending by now, is valid.
		span := int(w.now.Sub(run.since) / time.Millisecond)
		from := rapid.IntRange(1, span-1).Draw(t, "break_from_ms")
		to := rapid.IntRange(from+1, span).Draw(t, "break_to_ms")
		at := func(ms int) time.Time { return run.since.Add(time.Duration(ms) * time.Millisecond) }
		batch := w.id(w.now)
		w.emit(w.now, at(from), event.CyclePaused{CycleID: run.id, Batch: batch}, "", nil)
		w.emit(w.now, at(to), event.CycleResumed{CycleID: run.id, Batch: batch}, "", nil)
		w.commit(w.now, batch)
		run.since = at(to)
	case "boost":
		c := pick(t, "cycle", w.with("running", "paused"))
		w.add(w.now, event.CycleBoosted{CycleID: c.id, Minutes: rapid.IntRange(1, 60).Draw(t, "boost_minutes")})
	case "stop":
		c := pick(t, "cycle", w.with("running", "paused"))
		w.add(w.now, event.CycleStopped{CycleID: c.id})
		c.status = "stopped"
	case "annotate":
		c := pick(t, "cycle", w.cycles)
		p := event.CycleAnnotated{CycleID: c.id, Note: rapid.SampledFrom([]string{"", "Reviewed the plan", "Paired on a fix"}).Draw(t, "note")}
		for range rapid.IntRange(0, 3).Draw(t, "pairs") {
			p.KV = append(p.KV, event.KV{
				Key:   rapid.SampledFrom([]string{"pr", "topic"}).Draw(t, "key"),
				Value: rapid.SampledFrom([]string{"https://example.test/pr/1", "https://example.test/pr/2", "planning"}).Draw(t, "value"),
			})
		}
		w.add(w.now, p)
	case "complete", "skip":
		i := rapid.IntRange(0, len(w.open)-1).Draw(t, "task")
		id := w.open[i]
		w.open = append(w.open[:i:i], w.open[i+1:]...)
		if op == "complete" {
			w.add(w.now, event.TaskCompleted{TaskID: id})
		} else {
			w.add(w.now, event.TaskSkipped{TaskID: id, Reason: "Not needed today"})
		}
	case "rollover":
		w.rollover(t, false)
	}
}

// rollover writes one batch: the first sets the profile and the day; a later
// one marks the open tasks missed and starts a later day. Each materializes
// up to three tasks for its day. It runs only while no cycle is active.
func (w *walk) rollover(t *rapid.T, first bool) {
	batch := w.id(w.now)
	if first {
		w.profile(w.now, batch)
	} else {
		for _, id := range w.open {
			w.add(w.now, event.TaskMissed{TaskID: id, Batch: batch})
		}
		w.day = w.day.AddDate(0, 0, rapid.IntRange(1, 3).Draw(t, "days"))
	}
	w.open = nil
	w.builder.day(w.now, w.day, w.tz, batch)
	for range rapid.IntRange(0, 3).Draw(t, "tasks") {
		w.defs++
		w.open = append(w.open, w.task(w.now, w.day, fmt.Sprintf("task-%d", w.defs), w.tz, batch))
	}
	w.commit(w.now, batch)
}

// Days builds n scripted days of exactly 50 events each, with no randomness,
// starting on 2026-10-05 in America/New_York. Each day opens with one batch
// (the profile on the first day, else the missed task of the day before),
// completes one task, skips another and leaves the third open; it runs two
// blocks of an interrupt, two switches, a resume, a boost, a back-filled
// break, a stop and an annotation, then two shorter cycles, and every cycle
// is stopped before the next day rolls over.
func Days(n int) []event.Event {
	const tz = "America/New_York"
	b := &builder{}
	first := time.Date(2026, time.October, 5, 12, 0, 0, 0, time.UTC) // 08:00 in New York
	var left event.TaskID
	for d := range n {
		base := first.AddDate(0, 0, d)
		date := base.Truncate(24 * time.Hour)
		at := func(min int) time.Time { return base.Add(time.Duration(min) * time.Minute) }

		batch := b.id(base)
		if d == 0 {
			b.profile(base, batch)
		} else {
			b.add(base, event.TaskMissed{TaskID: left, Batch: batch})
		}
		b.day(base, date, tz, batch)
		plan := b.task(base, date, "plan-day", tz, batch)
		post := b.task(base, date, "post-plan", tz, batch)
		left = b.task(base, date, "end-of-day-summary", tz, batch)
		b.commit(base, batch)
		b.add(at(30), event.TaskCompleted{TaskID: plan})
		b.add(at(35), event.TaskSkipped{TaskID: post, Reason: "Not needed today"})

		for _, start := range []int{60, 360} {
			deep := event.CycleID(b.id(at(start)))
			b.add(at(start), event.CycleStarted{CycleID: deep, Type: "deep-work", Title: "Deep work", PlannedMinutes: 50})
			notif := event.CycleID(b.id(at(start + 30)))
			b.add(at(start+30), event.CycleStarted{CycleID: notif, Type: "notifications", Title: "Notifications", PlannedMinutes: 15, Interrupts: deep})
			b.switchCycles(at(start+40), notif, deep)
			b.switchCycles(at(start+45), deep, notif)
			b.add(at(start+48), event.CycleStopped{CycleID: notif})
			b.add(at(start+48), event.CycleResumed{CycleID: deep})
			b.add(at(start+50), event.CycleBoosted{CycleID: deep, Minutes: 10})
			brk := b.id(at(start + 220))
			b.emit(at(start+220), at(start+180), event.CyclePaused{CycleID: deep, Batch: brk}, "", nil)
			b.emit(at(start+220), at(start+210), event.CycleResumed{CycleID: deep, Batch: brk}, "", nil)
			b.commit(at(start+220), brk)
			b.add(at(start+240), event.CycleStopped{CycleID: deep})
			b.add(at(start+241), event.CycleAnnotated{CycleID: deep, Note: "Reviewed the plan", KV: []event.KV{
				{Key: "pr", Value: "https://example.test/pr/1"}, {Key: "pr", Value: "https://example.test/pr/2"},
			}})
		}

		review := event.CycleID(b.id(at(660)))
		b.add(at(660), event.CycleStarted{CycleID: review, Type: "review", Title: "Review", PlannedMinutes: 25})
		b.add(at(670), event.CyclePaused{CycleID: review})
		b.add(at(675), event.CycleResumed{CycleID: review})
		b.add(at(680), event.CycleBoosted{CycleID: review, Minutes: 5})
		b.add(at(705), event.CycleStopped{CycleID: review})
		b.add(at(706), event.CycleAnnotated{CycleID: review, Note: "Read the review"})

		late := event.CycleID(b.id(at(750)))
		b.add(at(750), event.CycleStarted{CycleID: late, Type: "review", Title: "Review", PlannedMinutes: 25})
		b.add(at(760), event.CycleBoosted{CycleID: late, Minutes: 5})
		b.add(at(770), event.CycleAnnotated{CycleID: late, KV: []event.KV{{Key: "topic", Value: "planning"}}})
		b.add(at(780), event.CycleStopped{CycleID: late})
	}
	return b.events
}

// switchCycles appends a switch at at: from, the running cycle, pauses and to
// resumes, in one batch.
func (b *builder) switchCycles(at time.Time, from, to event.CycleID) {
	batch := b.id(at)
	b.add(at, event.CyclePaused{CycleID: from, Batch: batch})
	b.add(at, event.CycleResumed{CycleID: to, Batch: batch})
	b.commit(at, batch)
}
