package engine_test

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/civil"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/clock"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/command"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/due"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/engine"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/projection"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/store/storefault"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/testutil"
)

// The engine tests run a real store in a temporary directory, through the
// fault FS, with a fake clock and the example configuration. Every request
// goes through Engine.Do, so the tests see what a daemon would.

const (
	newYork = "America/New_York"
	logName = "events.jsonl"

	deepWork      = "deep-work"
	notifications = "notifications"
	review        = "review"
)

var newYorkLoc = func() *time.Location {
	loc, err := time.LoadLocation(newYork)
	if err != nil {
		panic(err)
	}
	return loc
}()

// dayOf is the civil date of October 2026 numbered d.
func dayOf(d int) civil.Date { return civil.Date{Year: 2026, Month: time.October, Day: d} }

// localOn is the instant of a wall-clock time in New York on day d of October
// 2026.
func localOn(d, h, m int) time.Time {
	return time.Date(2026, time.October, d, h, m, 0, 0, newYorkLoc).UTC()
}

// local is the instant of a wall-clock time in New York on 2026-10-07.
func local(h, m int) time.Time { return localOn(7, h, m) }

// taskOf is the id of the daily task def of day d.
func taskOf(d int, def string) event.TaskID { return event.NewTaskID(due.Daily, dayOf(d), def) }

// idOf is the nth id of a family: the ids clients send ('C') and the ids the
// engine draws ('N'). Ids of different families never collide.
func idOf(family byte, n uint32) event.ID { return testutil.IDOf(local(0, 0), family, n) }

// clientIDs hands out fresh client ids.
var clientIDs atomic.Uint32

func clientID() event.ID { return idOf('C', clientIDs.Add(1)) }

// fakeObserver records every call.
type fakeObserver struct {
	mu           sync.Mutex
	replayed     []int
	recovered    []store.Recovery
	appended     []event.Type
	appendFailed []string
	rejected     map[command.Reason]int
	corrected    []string
	stats        []store.AppendStats
}

func newObserver() *fakeObserver { return &fakeObserver{rejected: map[command.Reason]int{}} }

func (o *fakeObserver) Replayed(n int, _ time.Duration) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.replayed = append(o.replayed, n)
}

func (o *fakeObserver) Recovered(r store.Recovery) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.recovered = append(o.recovered, r)
}

func (o *fakeObserver) Appended(t event.Type, s store.AppendStats) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.appended = append(o.appended, t)
	o.stats = append(o.stats, s)
}

func (o *fakeObserver) AppendFailed(stage string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.appendFailed = append(o.appendFailed, stage)
}

func (o *fakeObserver) Rejected(r command.Reason) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.rejected[r]++
}

func (o *fakeObserver) Corrected(kind string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.corrected = append(o.corrected, kind)
}

// rejections is the number of rejections recorded, of every reason.
func (o *fakeObserver) rejections() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	n := 0
	for _, c := range o.rejected {
		n += c
	}
	return n
}

func (o *fakeObserver) appendedCount() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.appended)
}

// harness is one data directory and the engine open on it.
type harness struct {
	t     *testing.T
	dir   string
	clock *clock.Fake
	cfg   *config.Config
	gen   int64
	obs   *fakeObserver
	fs    *storefault.FS
	adopt func() error
	e     *engine.Engine
	ids   atomic.Uint32
}

// newHarness opens an engine on a fresh directory at 08:50 in New York on
// 2026-10-07, with the example configuration unless cfg is given.
func newHarness(t *testing.T, cfg *config.Config) *harness {
	t.Helper()
	if cfg == nil {
		cfg = testutil.LoadConfig(t, nil)
	}
	h := &harness{t: t, dir: t.TempDir(), clock: clock.NewFake(local(8, 50)), cfg: cfg, gen: 1000}
	h.open()
	t.Cleanup(func() {
		if h.e != nil {
			if err := h.e.Close(); err != nil {
				t.Errorf("Close: %v", err)
			}
		}
	})
	return h
}

// newID is the engine's id source: ids of the 'N' family, which go on across
// reopens so that no id is drawn twice.
func (h *harness) newID() event.ID { return idOf('N', h.ids.Add(1)) }

func (h *harness) options() engine.Options {
	return engine.Options{
		Dir: h.dir, Config: h.cfg, ConfigGeneration: h.gen, Clock: h.clock,
		NewID: h.newID, Observer: h.obs, FS: h.fs, AdoptFault: h.adoptFault,
	}
}

// adoptFault is the AdoptFault seam: h.adopt when set.
func (h *harness) adoptFault() error {
	if h.adopt != nil {
		return h.adopt()
	}
	return nil
}

// open opens a new engine on the directory with a fresh observer and fault FS.
func (h *harness) open() {
	h.t.Helper()
	h.obs = newObserver()
	h.fs = storefault.New(nil)
	e, err := engine.Open(h.options())
	if err != nil {
		h.t.Fatalf("Open: %v", err)
	}
	h.e = e
}

// reopen closes the engine and opens a new one on the same directory, as a
// restart does.
func (h *harness) reopen() {
	h.t.Helper()
	if err := h.e.Close(); err != nil {
		h.t.Fatalf("Close: %v", err)
	}
	h.e = nil
	h.open()
}

// path is the log file.
func (h *harness) path() string { return filepath.Join(h.dir, logName) }

// logBytes is the content of the log file.
func (h *harness) logBytes() []byte {
	h.t.Helper()
	b, err := os.ReadFile(h.path())
	if err != nil {
		h.t.Fatal(err)
	}
	return b
}

// lines is the number of lines in the log file.
func (h *harness) lines() int { return bytes.Count(h.logBytes(), []byte("\n")) }

// fileEvents decodes every line of the log file.
func (h *harness) fileEvents() []event.Event {
	h.t.Helper()
	var out []event.Event
	sc := bufio.NewScanner(bytes.NewReader(h.logBytes()))
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		e, err := event.Decode(sc.Bytes())
		if err != nil {
			h.t.Fatalf("line %d: %v", len(out)+1, err)
		}
		e.Line = len(out) + 1
		out = append(out, e)
	}
	if err := sc.Err(); err != nil {
		h.t.Fatal(err)
	}
	return out
}

// try sends a request.
func (h *harness) try(c command.Command) (engine.Result, error) {
	return h.e.Do(context.Background(), c)
}

// do sends a request that MUST succeed.
func (h *harness) do(c command.Command) engine.Result {
	h.t.Helper()
	r, err := h.try(c)
	if err != nil {
		h.t.Fatalf("Do(%T): %v", c, err)
	}
	return r
}

// at sets the clock and sends a request that MUST succeed.
func (h *harness) at(t time.Time, c command.Command) engine.Result {
	h.t.Helper()
	h.clock.Set(t)
	return h.do(c)
}

// reject sends a request that MUST be refused with reason want, and checks
// that nothing reached the log. It returns the rejection by value, so the
// caller may ignore it.
func (h *harness) reject(c command.Command, want command.Reason) command.Rejection {
	h.t.Helper()
	before := h.logBytes()
	_, err := h.try(c)
	r := testutil.RejectionOf(h.t, err, want)
	if !bytes.Equal(before, h.logBytes()) {
		h.t.Errorf("a refused %T changed the log", c)
	}
	return r
}

// bootstrapCmd sets up the day 2026-10-07, the week 2026-10-05 to 2026-10-11
// and the sprint 2026-10-05 to 2026-10-18 in New York.
func bootstrapCmd(id event.ID) command.ChangePeriods {
	return command.ChangePeriods{ID: id, Changes: []command.PeriodChange{
		{Kind: projection.Day, Start: dayOf(7), TZ: newYork},
		{Kind: projection.Week, Start: dayOf(5), End: testutil.Ptr(dayOf(11)), TZ: newYork},
		{Kind: projection.Sprint, Start: dayOf(5), End: testutil.Ptr(dayOf(18)), TZ: newYork},
	}}
}

// rolloverCmd rolls the day over to day d of October 2026.
func rolloverCmd(id event.ID, d int) command.ChangePeriods {
	return command.ChangePeriods{ID: id, Changes: []command.PeriodChange{{Kind: projection.Day, Start: dayOf(d), TZ: newYork}}}
}

// bootstrap runs bootstrapCmd at the present clock reading.
func (h *harness) bootstrap() engine.Result {
	h.t.Helper()
	return h.do(bootstrapCmd(clientID()))
}

// start starts a cycle of type typ at t and returns its id.
func (h *harness) start(t time.Time, typ string) event.CycleID {
	h.t.Helper()
	h.at(t, command.StartCycle{ID: clientID(), Type: typ})
	focus, ok := h.e.Snapshot().Model.Running()
	if !ok || focus.Type != typ {
		h.t.Fatalf("after the start of %s the focus is %+v", typ, focus)
	}
	return focus.ID
}

// cycle is a cycle of the present model.
func (h *harness) cycle(id event.CycleID) projection.Cycle {
	h.t.Helper()
	c, ok := h.e.Snapshot().Model.Cycle(id)
	if !ok {
		h.t.Fatalf("cycle %s is not in the model", id)
	}
	return c
}

// task is a task of the present model.
func (h *harness) task(id event.TaskID) projection.Task {
	h.t.Helper()
	task, ok := h.e.Snapshot().Model.Task(id)
	if !ok {
		h.t.Fatalf("task %s is not in the model", id)
	}
	return task
}

// readOnlySentence is the sentence every client shows for a reason.
func readOnlySentence(r store.ReadOnlyReason) string {
	return "READ-ONLY: " + string(r) + ". Restart pg-task-focus to recover"
}

// The guidance the engine adds to the READ-ONLY sentence.
const (
	unknownOutcome  = "the outcome is unknown: retry with the same id"
	unknownReadOnly = "The outcome is unknown; if the request carried an id, retry with it after the restart"
	storedReadOnly  = "The change is stored; if the request carried an id, retry with it after the restart to receive the result"
)

// injectOnLog adds fault rules on the log file.
func (h *harness) injectOnLog(ops ...storefault.Op) {
	for _, op := range ops {
		h.fs.Inject(storefault.Rule{Op: op, Name: logName})
	}
}

// requireNoPending fails when an injected fault was never reached.
func (h *harness) requireNoPending() {
	h.t.Helper()
	if n := h.fs.Pending(); n != 0 {
		h.t.Fatalf("%d injected faults were never reached", n)
	}
}

// hasPrefixAndGuidance reports whether a message is the READ-ONLY sentence of
// reason followed by guidance.
func hasPrefixAndGuidance(msg string, reason store.ReadOnlyReason, guidance string) bool {
	return strings.HasPrefix(msg, readOnlySentence(reason)) && strings.Contains(msg, guidance)
}
