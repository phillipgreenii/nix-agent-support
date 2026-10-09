package engine_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/civil"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/clock"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/command"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/due"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/engine"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/projection"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/view"
)

// The golden replay suite. Each scenario drives a real engine (a store in a
// temporary directory, a fake clock and the example configuration) through a
// realistic stretch of use; the log it writes is committed under
// testdata/logs/golden as <name>.jsonl, and the state a client reads from it
// at a pinned instant as <name>.state.json. The committed files are claims
// about the behavior: a change that alters them is reviewed by hand against
// the behavior docs before `go test ./internal/engine -run TestGoldenReplay
// -update` rewrites them. Later clients reuse these logs as realistic inputs.
//
// Every id comes from a deterministic source and every instant from the fake
// clock, so a scenario writes the same bytes on every run. The one log that
// no engine writes is corrupt-tail: the normal day with the start of one
// more batch torn off at the end, as a crash in the middle of an append
// leaves it.

var update = flag.Bool("update", false, "rewrite the golden logs and states under testdata/logs/golden")

const goldenDir = "../../testdata/logs/golden"

// goldenScenario is one golden log: how to write it and the instant its
// state is read at.
type goldenScenario struct {
	name string
	now  time.Time
	log  func(t *testing.T) []byte
}

// scripted is a scenario log written by driving an engine with script.
func scripted(script func(g *goldenRun)) func(t *testing.T) []byte {
	return func(t *testing.T) []byte {
		t.Helper()
		g := newGoldenRun(t)
		script(g)
		return g.logBytes()
	}
}

var goldenScenarios = []goldenScenario{
	{name: "normal-day", now: oct(7, 11, 45), log: scripted(normalDay)},
	{name: "dst-week", now: time.Date(2026, time.November, 1, 8, 20, 0, 0, time.UTC), log: scripted(dstWeek)},
	{name: "sprint", now: oct(19, 9, 10), log: scripted(sprint)},
	{name: "on-call-switch", now: oct(8, 9, 40), log: scripted(onCallSwitch)},
	{name: "interrupt-chain", now: oct(7, 9, 50), log: scripted(interruptChain)},
	{name: "forgotten-lunch", now: oct(7, 13, 30), log: scripted(forgottenLunch)},
	{name: "corrections", now: oct(7, 10, 0), log: scripted(corrections)},
	{name: "corrupt-tail", now: oct(7, 11, 45), log: corruptTail},
}

// goldenRun is one scenario's engine with its own source of client ids, so
// a scenario's ids do not depend on which tests ran before it.
type goldenRun struct {
	*harness
	clientN uint32
}

func newGoldenRun(t *testing.T) *goldenRun {
	t.Helper()
	return &goldenRun{harness: newHarness(t, nil)}
}

// id is the next client id of the scenario.
func (g *goldenRun) id() event.ID {
	g.clientN++
	return idOf('G', g.clientN)
}

// commit sends a request at t that MUST append.
func (g *goldenRun) commit(t time.Time, c command.Command) engine.Result {
	g.t.Helper()
	r := g.at(t, c)
	if !r.Changed {
		g.t.Fatalf("%T at %s changed nothing: %s", c, t, r.Note)
	}
	return r
}

// focus is the id of the running cycle, which MUST exist.
func (g *goldenRun) focus() event.CycleID {
	g.t.Helper()
	c, ok := g.e.Snapshot().Model.Running()
	if !ok {
		g.t.Fatal("no cycle is running")
	}
	return c.ID
}

// startCycle starts a cycle of type typ at t and returns its id and the id of
// its cycle.started event.
func (g *goldenRun) startCycle(t time.Time, typ string) (event.CycleID, event.ID) {
	g.t.Helper()
	r := g.commit(t, command.StartCycle{ID: g.id(), Type: typ})
	id := g.focus()
	if got := g.cycle(id).Type; got != typ {
		g.t.Fatalf("the cycle started at %s is a %s, want %s", t, got, typ)
	}
	return id, r.EventIDs[0]
}

func (g *goldenRun) complete(t time.Time, task event.TaskID) engine.Result {
	g.t.Helper()
	return g.commit(t, command.CompleteTask{ID: g.id(), TaskID: task})
}

func (g *goldenRun) stop(t time.Time, c event.CycleID) {
	g.t.Helper()
	g.commit(t, command.StopCycle{ID: g.id(), CycleID: c})
}

// rollDay rolls the day over to d, leaving the other periods.
func (g *goldenRun) rollDay(t time.Time, d civil.Date) engine.Result {
	g.t.Helper()
	return g.commit(t, command.ChangePeriods{ID: g.id(), Changes: []command.PeriodChange{dayChange(d)}})
}

// oct is the instant of a wall-clock time in New York on day d of October
// 2026.
func oct(d, h, m int) time.Time { return localOn(d, h, m) }

// nov is the instant of a wall-clock time in New York on day d of November
// 2026; it MUST NOT name a time the zone repeats or skips.
func nov(d, h, m int) time.Time {
	return time.Date(2026, time.November, d, h, m, 0, 0, newYorkLoc).UTC()
}

func date(m time.Month, d int) civil.Date { return civil.Date{Year: 2026, Month: m, Day: d} }

func dayChange(d civil.Date) command.PeriodChange {
	return command.PeriodChange{Kind: projection.Day, Start: d, TZ: newYork}
}

func weekChange(start civil.Date) command.PeriodChange {
	return command.PeriodChange{Kind: projection.Week, Start: start, End: ptr(start.AddDays(6)), TZ: newYork}
}

func sprintChange(start civil.Date) command.PeriodChange {
	return command.PeriodChange{Kind: projection.Sprint, Start: start, End: ptr(start.AddDays(13)), TZ: newYork}
}

func daily(d civil.Date, def string) event.TaskID { return event.NewTaskID(due.Daily, d, def) }

func weekly(start civil.Date, def string) event.TaskID {
	return event.NewTaskID(due.Weekly, start, def)
}

func sprintTask(start civil.Date, def string) event.TaskID {
	return event.NewTaskID(due.Sprint, start, def)
}

// rawJSON is v as a correction field value.
func rawJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// normalDay is an ordinary Wednesday: the routine's tasks, a notification
// cycle, a deep-work cycle that runs out, is boosted and is annotated, the
// overdue sprint task done late, and a review cycle that was paused and is in
// overtime at 11:45.
func normalDay(g *goldenRun) {
	d := dayOf(7)
	g.commit(oct(7, 8, 50), bootstrapCmd(g.id()))
	g.complete(oct(7, 8, 55), daily(d, "plan-day"))
	notif, _ := g.startCycle(oct(7, 9, 5), notifications)
	g.stop(oct(7, 9, 25), notif)
	g.complete(oct(7, 9, 28), daily(d, "post-plan"))
	deep, _ := g.startCycle(oct(7, 9, 30), deepWork)
	g.commit(oct(7, 10, 22), command.BoostCycle{ID: g.id(), CycleID: deep, Minutes: 10})
	g.stop(oct(7, 10, 35), deep)
	g.commit(oct(7, 10, 36), command.AnnotateCycle{ID: g.id(), CycleID: deep, Note: "Parser refactor", KV: []event.KV{
		{Key: "ticket", Value: "TASK-1"}, {Key: "pr", Value: "https://example.test/pr/1"},
	}})
	g.complete(oct(7, 10, 40), sprintTask(dayOf(5), "capacity-check"))
	rev, _ := g.startCycle(oct(7, 11, 0), review)
	g.commit(oct(7, 11, 20), command.PauseCycle{ID: g.id(), CycleID: rev})
	g.commit(oct(7, 11, 30), command.ResumeCycle{ID: g.id(), CycleID: rev})
}

// dstWeek is a week of New York days across the end of daylight saving time,
// 2026-11-01 at 02:00 EDT, when the clocks go back to 01:00 EST. The week runs
// from Monday 2026-10-26 to Sunday 2026-11-01. A deep-work cycle starts at
// 00:20 EDT on the Sunday and is still running at 03:20 EST: three hours on
// the wall clock, four of running time. The Sunday's tasks are due at 09:00
// EST (14:00Z) where the Saturday's were due at 09:00 EDT (13:00Z).
func dstWeek(g *goldenRun) {
	week := date(time.October, 26)
	g.commit(oct(26, 8, 50), command.ChangePeriods{ID: g.id(), Changes: []command.PeriodChange{
		dayChange(week), weekChange(week), sprintChange(date(time.October, 19)),
	}})
	g.complete(oct(26, 8, 55), daily(week, "plan-day"))
	g.complete(oct(26, 9, 0), sprintTask(date(time.October, 19), "capacity-check"))
	for i := range 6 {
		d := week.AddDays(i)
		if i > 0 {
			g.rollDay(oct(26+i, 8, 50), d)
			g.complete(oct(26+i, 8, 55), daily(d, "plan-day"))
		}
		switch d.Weekday() {
		case time.Thursday:
			g.complete(oct(26+i, 9, 0), weekly(week, "weekly-update"))
		case time.Friday:
			g.commit(oct(26+i, 9, 20), command.SkipTask{ID: g.id(), TaskID: daily(d, "post-plan"), Reason: "Nothing new to post"})
		}
		if d.Weekday() != time.Friday {
			g.complete(oct(26+i, 9, 25), daily(d, "post-plan"))
		}
		deep, _ := g.startCycle(oct(26+i, 9, 30), deepWork)
		g.stop(oct(26+i, 10, 20), deep)
		if d.Weekday() != time.Saturday {
			g.complete(oct(26+i, 17, 20), daily(d, "end-of-day-summary"))
		}
	}
	sunday := date(time.November, 1)
	g.rollDay(nov(1, 0, 10), sunday)
	deep, _ := g.startCycle(nov(1, 0, 20), deepWork)
	// 01:30 happens twice on this day: first at 05:30Z (EDT), then at 06:30Z
	// (EST).
	g.commit(time.Date(2026, time.November, 1, 5, 30, 0, 0, time.UTC), command.BoostCycle{ID: g.id(), CycleID: deep, Minutes: 25})
	g.commit(time.Date(2026, time.November, 1, 6, 30, 0, 0, time.UTC), command.AnnotateCycle{ID: g.id(), CycleID: deep, Note: "Worked through the repeated hour"})
}

// sprint is a whole two-week sprint of workdays, Monday 2026-10-05 to Friday
// 2026-10-16, ending with one change that rolls the day, the week and the
// sprint together on Monday 2026-10-19. Each workday plans, posts, runs a
// deep-work and a review cycle and, Monday to Thursday, writes the summary.
// Wednesdays skip the post with a reason; the Monday rollover skips Friday's
// summary with an override; the second week's update is never written and is
// marked missed by the sprint rollover.
func sprint(g *goldenRun) {
	first := date(time.October, 5)
	g.commit(oct(5, 8, 50), command.ChangePeriods{ID: g.id(), Changes: []command.PeriodChange{
		dayChange(first), weekChange(first), sprintChange(first),
	}})
	g.complete(oct(5, 9, 0), sprintTask(first, "capacity-check"))
	for _, n := range []int{5, 6, 7, 8, 9, 12, 13, 14, 15, 16} {
		d := date(time.October, n)
		switch {
		case n == 12:
			g.commit(oct(n, 8, 50), command.ChangePeriods{
				ID:        g.id(),
				Changes:   []command.PeriodChange{dayChange(d), weekChange(d)},
				Overrides: []command.Override{{TaskID: daily(date(time.October, 9), "end-of-day-summary"), Reason: "Left early on Friday"}},
			})
		case n > 5:
			g.rollDay(oct(n, 8, 50), d)
		}
		g.complete(oct(n, 8, 55), daily(d, "plan-day"))
		if d.Weekday() == time.Wednesday {
			g.commit(oct(n, 9, 20), command.SkipTask{ID: g.id(), TaskID: daily(d, "post-plan"), Reason: "Posted in the standup instead"})
		} else {
			g.complete(oct(n, 9, 20), daily(d, "post-plan"))
		}
		if n == 8 {
			g.complete(oct(n, 9, 25), weekly(first, "weekly-update"))
		}
		deep, _ := g.startCycle(oct(n, 9, 30), deepWork)
		g.stop(oct(n, 10, 20), deep)
		rev, _ := g.startCycle(oct(n, 10, 30), review)
		g.stop(oct(n, 10, 55), rev)
		if d.Weekday() != time.Friday {
			g.complete(oct(n, 17, 20), daily(d, "end-of-day-summary"))
		}
	}
	next := date(time.October, 19)
	g.commit(oct(19, 8, 50), command.ChangePeriods{ID: g.id(), Changes: []command.PeriodChange{
		dayChange(next), weekChange(next), sprintChange(next),
	}})
	g.complete(oct(19, 8, 55), daily(next, "plan-day"))
}

// onCallSwitch goes on call for part of Wednesday with a profile change of
// its own, back to normal at the end of the day, and on call again on
// Thursday with the profile change riding inside the day's rollover. On
// Thursday a deep-work cycle, which the on-call profile does not list,
// starts and is interrupted by a page response at 09:20. The example
// configuration gives both profiles the same tasks, so the changes add,
// withdraw and reinstate no task.
func onCallSwitch(g *goldenRun) {
	d := dayOf(7)
	g.commit(oct(7, 8, 50), bootstrapCmd(g.id()))
	g.complete(oct(7, 8, 55), daily(d, "plan-day"))
	deep, _ := g.startCycle(oct(7, 9, 0), deepWork)
	g.stop(oct(7, 9, 50), deep)
	g.commit(oct(7, 10, 0), command.ChangeProfile{ID: g.id(), Profile: "on-call"})
	page, _ := g.startCycle(oct(7, 10, 5), "page-response")
	g.stop(oct(7, 10, 40), page)
	g.commit(oct(7, 10, 45), command.AnnotateCycle{ID: g.id(), CycleID: page, Note: "Restarted the worker", KV: []event.KV{
		{Key: "incident", Value: "https://example.test/incidents/1"},
	}})
	g.commit(oct(7, 17, 0), command.ChangeProfile{ID: g.id(), Profile: "normal"})
	g.complete(oct(7, 17, 25), daily(d, "end-of-day-summary"))

	next := dayOf(8)
	g.commit(oct(8, 8, 50), command.ChangePeriods{ID: g.id(), Changes: []command.PeriodChange{dayChange(next)}, Profile: "on-call"})
	g.complete(oct(8, 8, 55), daily(next, "plan-day"))
	g.startCycle(oct(8, 9, 0), deepWork)
	g.startCycle(oct(8, 9, 20), "page-response")
}

// interruptChain is a morning of interruptions. Deep work (A) starts at 09:00;
// a review (B) interrupts it at 09:20; at 09:25 the operator switches back to
// A, which dims B as displaced by A; a notification cycle (C) interrupts A at
// 09:30 and stops at 09:40, so A is offered back; at 09:45 a second
// notification cycle (D) starts while nothing runs. At 09:50 D is the focus,
// A and B are dimmed, and A is still offered, now as a switch.
func interruptChain(g *goldenRun) {
	g.commit(oct(7, 8, 50), bootstrapCmd(g.id()))
	g.complete(oct(7, 8, 55), daily(dayOf(7), "plan-day"))
	a, _ := g.startCycle(oct(7, 9, 0), deepWork)
	g.startCycle(oct(7, 9, 20), review)
	g.commit(oct(7, 9, 25), command.SwitchCycle{ID: g.id(), To: a})
	c, _ := g.startCycle(oct(7, 9, 30), notifications)
	g.stop(oct(7, 9, 40), c)
	g.startCycle(oct(7, 9, 45), notifications)
}

// forgottenLunch is a deep-work cycle started at 09:00 that ran through a
// lunch from 12:00 to 13:00 nobody paused for, fixed at 13:10 by back-filling
// the break. At 13:30 it has run 210 minutes, not 270.
func forgottenLunch(g *goldenRun) {
	d := dayOf(7)
	g.commit(oct(7, 8, 50), bootstrapCmd(g.id()))
	g.complete(oct(7, 8, 55), daily(d, "plan-day"))
	deep, _ := g.startCycle(oct(7, 9, 0), deepWork)
	g.complete(oct(7, 9, 20), daily(d, "post-plan"))
	g.commit(oct(7, 13, 10), command.BackfillBreak{ID: g.id(), CycleID: deep, From: oct(7, 12, 0), To: oct(7, 13, 0)})
}

// corrections exercises every kind of fix on one morning: a completion's time
// corrected; a skip reason corrected, the correction undone, and the undo
// undone (the corrected reason is back); a rollover made by mistake,
// retracted as a batch, the retraction retracted (the rollover is back in
// force) and that retraction retracted too (the rollover is undone again);
// and a review cycle whose type is corrected to deep work, which takes the
// configured title and nothing else.
func corrections(g *goldenRun) {
	d := dayOf(7)
	g.commit(oct(7, 8, 50), bootstrapCmd(g.id()))
	done := g.complete(oct(7, 9, 5), daily(d, "plan-day"))
	g.commit(oct(7, 9, 6), command.Correct{ID: g.id(), Target: done.EventIDs[0], Reason: "Finished before the stand-up", Fields: map[string]json.RawMessage{
		"effective_at": rawJSON(g.t, event.At(oct(7, 8, 57))),
	}})

	skip := g.commit(oct(7, 9, 10), command.SkipTask{ID: g.id(), TaskID: daily(d, "post-plan"), Reason: "Posted in the standup instead"})
	fix := g.commit(oct(7, 9, 12), command.Correct{ID: g.id(), Target: skip.EventIDs[0], Fields: map[string]json.RawMessage{
		"reason": rawJSON(g.t, "Shared in the standup instead"),
	}})
	undo := g.commit(oct(7, 9, 14), command.Retract{ID: g.id(), Target: fix.EventIDs[0], Reason: "Wrong fix"})
	g.commit(oct(7, 9, 16), command.Retract{ID: g.id(), Target: undo.EventIDs[0], Reason: "The fix was right"})

	roll := g.rollDay(oct(7, 9, 20), dayOf(8))
	r1 := g.commit(oct(7, 9, 21), command.Retract{ID: g.id(), TargetBatch: roll.BatchID, Reason: "Rolled over by mistake"})
	r2 := g.commit(oct(7, 9, 22), command.Retract{ID: g.id(), Target: r1.EventIDs[0]})
	g.commit(oct(7, 9, 23), command.Retract{ID: g.id(), Target: r2.EventIDs[0]})

	_, started := g.startCycle(oct(7, 9, 30), review)
	g.commit(oct(7, 9, 40), command.Correct{ID: g.id(), Target: started, Fields: map[string]json.RawMessage{
		"type": rawJSON(g.t, deepWork),
	}})
}

// corruptTail is the normal day's log followed by the start of one more
// batch, a back-filled break, that a crash cut short: its first event is
// whole and its second stops half-way through its line. The cut line ends in
// a newline: a final line that ends in a newline but does not parse is a torn
// tail like any other, and the newline keeps the repository's end-of-file
// fixer from changing the fixture. The store test also recovers the file with
// that newline removed, as a crash in the middle of a write leaves it.
func corruptTail(t *testing.T) []byte {
	t.Helper()
	g := newGoldenRun(t)
	normalDay(g)
	good := g.logBytes()
	rev := g.focus()
	g.commit(oct(7, 11, 45), command.BackfillBreak{ID: g.id(), CycleID: rev, From: oct(7, 11, 35), To: oct(7, 11, 40)})
	batch := g.logBytes()[len(good):]
	lines := bytes.SplitAfter(batch, []byte("\n"))
	if len(lines) < 3 {
		t.Fatalf("the break appended %d lines, want a batch of 3", len(lines))
	}
	torn := append(slices.Clone(lines[0]), lines[1][:len(lines[1])/2]...)
	return append(append(good, torn...), '\n')
}

// goldenState is what a state file holds: the pinned read instant and the
// state at it.
type goldenState struct {
	Now   time.Time  `json:"now"`
	State view.State `json:"state"`
}

// stateOf opens an engine on a copy of a log and reads its state at now, as a
// daemon starting on that log would.
func stateOf(t *testing.T, log []byte, now time.Time) []byte {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, logName), log, 0o600); err != nil {
		t.Fatal(err)
	}
	e, err := engine.Open(engine.Options{Dir: dir, Config: loadConfig(t, nil), ConfigGeneration: 1, Clock: clock.NewFake(now)})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	st := e.State(now)
	if err := e.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	b, err := json.MarshalIndent(goldenState{Now: now, State: st}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(b, '\n')
}

func goldenPaths(name string) (log, state string) {
	return filepath.Join(goldenDir, name+".jsonl"), filepath.Join(goldenDir, name+".state.json")
}

func TestGoldenReplay(t *testing.T) {
	if *update {
		if err := os.MkdirAll(goldenDir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, s := range goldenScenarios {
		t.Run(s.name, func(t *testing.T) {
			logPath, statePath := goldenPaths(s.name)
			written := s.log(t)
			if *update {
				writeGolden(t, logPath, written)
				writeGolden(t, statePath, stateOf(t, written, s.now))
			}
			committed := readGolden(t, logPath)
			if !bytes.Equal(committed, written) {
				t.Errorf("the scenario no longer writes the committed %s; review the change against the behavior docs, then rerun with -update", logPath)
			}
			if got, want := stateOf(t, committed, s.now), readGolden(t, statePath); !bytes.Equal(got, want) {
				t.Errorf("the state of %s at %s differs from %s; review the change against the behavior docs, then rerun with -update\n got: %s", logPath, s.now.Format(time.RFC3339), statePath, got)
			}
		})
	}

	// Recovery loses nothing that was acknowledged: the torn log reads as the
	// normal day it was cut from.
	_, normalState := goldenPaths("normal-day")
	_, tornState := goldenPaths("corrupt-tail")
	if !bytes.Equal(readGolden(t, normalState), readGolden(t, tornState)) {
		t.Errorf("%s differs from %s", tornState, normalState)
	}

	// Every golden file belongs to a scenario, so none goes stale unseen.
	want := map[string]bool{}
	for _, s := range goldenScenarios {
		want[s.name+".jsonl"], want[s.name+".state.json"] = true, true
	}
	entries, err := os.ReadDir(goldenDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !want[e.Name()] {
			t.Errorf("%s in %s belongs to no scenario", e.Name(), goldenDir)
		}
	}
}

func writeGolden(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func readGolden(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (generate the goldens with -update)", err)
	}
	return b
}

// decodeLog decodes every line of a log that has no torn tail.
func decodeLog(t *testing.T, log []byte) []event.Event {
	t.Helper()
	var out []event.Event
	for i, line := range bytes.Split(bytes.TrimSuffix(log, []byte("\n")), []byte("\n")) {
		e, err := event.Decode(line)
		if err != nil {
			t.Fatalf("line %d: %v", i+1, err)
		}
		e.Line = i + 1
		out = append(out, e)
	}
	return out
}

// openStore opens a store on dir at a fixed instant.
func openStore(t *testing.T, dir string, now time.Time) (*store.Store, []event.Event, store.Recovery) {
	t.Helper()
	st, events, rec, err := store.Open(store.Options{Dir: dir, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	return st, events, rec
}

func closeStore(t *testing.T, st *store.Store) {
	t.Helper()
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestGoldenLogsRoundTripThroughStore(t *testing.T) {
	for _, s := range goldenScenarios {
		if s.name == "corrupt-tail" {
			continue
		}
		t.Run(s.name, func(t *testing.T) {
			logPath, _ := goldenPaths(s.name)
			golden := readGolden(t, logPath)
			want := decodeLog(t, golden)

			dir := t.TempDir()
			st, none, _ := openStore(t, dir, s.now)
			if len(none) != 0 {
				t.Fatalf("a new store holds %d events", len(none))
			}
			for _, e := range want {
				if _, err := st.Append([]event.Event{e}); err != nil {
					t.Fatalf("Append(line %d): %v", e.Line, err)
				}
			}
			closeStore(t, st)

			st, got, rec := openStore(t, dir, s.now)
			defer closeStore(t, st)
			if rec != (store.Recovery{}) {
				t.Errorf("reopening the written log recovered %+v", rec)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("the reopened store holds %d events that differ from the %d of %s", len(got), len(want), logPath)
			}
			written, err := os.ReadFile(filepath.Join(dir, logName))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(written, golden) {
				t.Errorf("the log the store wrote is not byte-for-byte %s", logPath)
			}
			if _, err := projection.Replay(got); err != nil {
				t.Errorf("the reopened log does not replay: %v", err)
			}
		})
	}

	t.Run("corrupt-tail", func(t *testing.T) {
		goodPath, _ := goldenPaths("normal-day")
		corruptPath, _ := goldenPaths("corrupt-tail")
		good, corrupt := readGolden(t, goodPath), readGolden(t, corruptPath)
		if !bytes.HasPrefix(corrupt, good) || len(corrupt) == len(good) {
			t.Fatalf("%s is not %s with a torn tail", corruptPath, goodPath)
		}
		lines := bytes.Split(bytes.TrimSuffix(corrupt, []byte("\n")), []byte("\n"))
		if _, err := event.Decode(lines[len(lines)-1]); err == nil {
			t.Fatalf("the final line of %s parses; it must be torn", corruptPath)
		}
		t.Run("as committed, with a newline", func(t *testing.T) {
			recoverTornTail(t, good, corrupt)
		})
		t.Run("without the final newline", func(t *testing.T) {
			recoverTornTail(t, good, bytes.TrimSuffix(corrupt, []byte("\n")))
		})
	})
}

// recoverTornTail opens a store on corrupt, which is good followed by a torn
// tail, and checks that recovery cuts exactly the tail, keeps it in a
// sidecar and serves the events of good.
func recoverTornTail(t *testing.T, good, corrupt []byte) {
	t.Helper()
	tail := corrupt[len(good):]
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, logName), corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	recoveredAt := oct(7, 12, 0)
	st, got, rec := openStore(t, dir, recoveredAt)
	defer closeStore(t, st)

	if !rec.TornTail || rec.UncommittedBatches != 1 || rec.TruncatedBytes != int64(len(tail)) {
		t.Errorf("Recovery %+v, want a torn tail and 1 uncommitted batch, %d bytes cut", rec, len(tail))
	}
	wantSidecar := filepath.Join(dir, logName+".recovered-"+recoveredAt.Format("20060102T150405Z")+"-1")
	if rec.Sidecar != wantSidecar {
		t.Errorf("Sidecar %q, want %q", rec.Sidecar, wantSidecar)
	}
	kept, err := os.ReadFile(rec.Sidecar)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(kept, tail) {
		t.Errorf("the sidecar holds %q, want the cut bytes %q", kept, tail)
	}
	left, err := os.ReadFile(filepath.Join(dir, logName))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(left, good) {
		t.Errorf("after recovery the log is %d bytes, want the %d of the good log", len(left), len(good))
	}
	if want := decodeLog(t, good); !reflect.DeepEqual(got, want) {
		t.Errorf("the recovered store holds %d events, want the %d of the good log", len(got), len(want))
	}
	if !strings.HasPrefix(string(tail), `{"v":1,`) {
		t.Errorf("the torn tail does not begin with an event: %q", tail)
	}
}
