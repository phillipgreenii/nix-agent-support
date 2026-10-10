package daemon_test

import (
	"errors"
	"slices"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/command"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/contract"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/wire"
)

// Route parity: a registered route missing from the spec, or a spec path with
// no route, fails.
func TestRoutesMatchTheOpenAPIDocument(t *testing.T) {
	e := newEnv(t, options{})
	got := e.d.Routes()
	sort.Strings(got)
	want := e.spec.Operations()
	if !slices.Equal(got, want) {
		var missing, extra []string
		for _, r := range got {
			if !slices.Contains(want, r) {
				extra = append(extra, r)
			}
		}
		for _, r := range want {
			if !slices.Contains(got, r) {
				missing = append(missing, r)
			}
		}
		t.Errorf("routes not in the spec: %v; spec operations with no route: %v", extra, missing)
	}
}

// The reason enum of the spec is the library's closed set, plus the reasons
// only the transport can find, plus the internal-error of a defect.
func TestReasonEnumIsTheClosedSet(t *testing.T) {
	e := newEnv(t, options{})
	want := wire.SortReasons(command.Reasons())
	for _, r := range wire.TransportReasons() {
		want = append(want, string(r))
	}
	want = append(want, string(wire.ReasonInternal))
	sort.Strings(want)
	got := e.spec.Enum(t, "Reason")
	sort.Strings(got)
	if !slices.Equal(got, want) {
		t.Errorf("the Reason enum of api/openapi.yaml and the code differ:\n spec: %v\n code: %v", got, want)
	}
	// Every status of the table is one the Problem schema allows.
	allowed := map[int]bool{400: true, 403: true, 404: true, 405: true, 409: true, 415: true, 422: true, 500: true, 503: true}
	for _, r := range want {
		if !allowed[wire.StatusOf(command.Reason(r))] {
			t.Errorf("reason %s has status %d, which the Problem schema does not allow", r, wire.StatusOf(command.Reason(r)))
		}
	}
	if slices.Contains(want, "invalid_timeline") {
		t.Error("there is no generic invalid_timeline code")
	}
}

// A negative control: the contract check really rejects a body that does not match.
func TestContractCheckRejectsAMismatch(t *testing.T) {
	spec := contract.Load(t)
	fake := &failRecorder{TB: t}
	spec.ValidateResponse(fake, "GET", "/api/v1/state", 200, "application/json", []byte(`{"read_at":"nope"}`))
	if !fake.failed {
		t.Fatal("a state document with no version and a bad instant validated")
	}
	fake = &failRecorder{TB: t}
	spec.ValidateRequest(fake, "POST", "/api/v1/cycles/start", []byte(`{"type":"x","bogus":true}`))
	if !fake.failed {
		t.Fatal("a request with an unknown field validated")
	}
	fake = &failRecorder{TB: t}
	spec.ValidateResponse(fake, "GET", "/api/v1/state", 418, "application/json", []byte(`{}`))
	if !fake.failed {
		t.Fatal("an undocumented status validated")
	}
}

type failRecorder struct {
	testing.TB
	failed bool
}

func (f *failRecorder) Errorf(string, ...any) { f.failed = true }
func (f *failRecorder) Fatalf(string, ...any) { f.failed = true }

// Item 2 of the hand-over: the append histograms observe a batch's duration
// once, not once per event.
func TestAppendDurationIsObservedOncePerAppend(t *testing.T) {
	e := newEnv(t, options{})
	e.bootstrap() // one batch of many events
	count := func(name string) uint64 {
		f := e.scrape()[name]
		return f.Metric[0].Histogram.GetSampleCount()
	}
	events, _ := sampleValue(e.scrape()["pg_task_focus_events_appended_total"], map[string]string{"type": "task.materialized"})
	if events < 3 {
		t.Fatalf("the bootstrap batch has %v tasks", events)
	}
	if n := count("pg_task_focus_append_duration_seconds"); n != 1 {
		t.Errorf("append_duration_seconds count = %d after one batch of many events, want 1", n)
	}
	if n := count("pg_task_focus_fsync_duration_seconds"); n != 1 {
		t.Errorf("fsync_duration_seconds count = %d, want 1", n)
	}
	e.ok("/api/v1/tasks/day:2026-10-07:plan-day/complete", map[string]any{})
	if n := count("pg_task_focus_append_duration_seconds"); n != 2 {
		t.Errorf("append_duration_seconds count = %d after a second append, want 2", n)
	}
}

// Item 4 of the hand-over: a panic inside an observer callback never escapes
// into the engine's commit, so the client push for that version is not missed
// and the store stays writable.
type panickingObserver struct{ panics int }

func (p *panickingObserver) Replayed(int, time.Duration) {}
func (p *panickingObserver) Recovered(store.Recovery)    {}
func (p *panickingObserver) Appended(event.Type, store.AppendStats) {
	p.panics++
	panic("a metrics label error")
}
func (p *panickingObserver) AppendFailed(string)     {}
func (p *panickingObserver) Rejected(command.Reason) {}
func (p *panickingObserver) Corrected(string)        {}

func TestAPanicInAnObserverDoesNotSkipTheCommitNotifications(t *testing.T) {
	obs := &panickingObserver{}
	e := newEnv(t, options{observer: obs})
	s := e.openStream(t)
	defer s.close()
	s.next(t)
	e.bootstrap() // Appended panics for every event
	if obs.panics == 0 {
		t.Fatal("the test observer never ran")
	}
	if ev := s.next(t); ev.name != "state" {
		t.Errorf("the stream push for the version was missed: %+v", ev)
	}
	if !strings.Contains(e.log.String(), "engine observer panicked") {
		t.Errorf("the panic was not logged")
	}
	e.clock.Set(local(9, 0))
	e.ok("/api/v1/tasks/day:2026-10-07:plan-day/complete", map[string]any{}) // the store is still writable
	// The alert poll that follows every commit ran too: a cycle past its time plays.
	e.startCycle("notifications")
	e.pollAt(local(9, 26))
	e.waitSounds(1)
}

// probeGate is a store.FS whose writability probe (the temporary file the
// store creates in its data directory) fails for as long as the gate is
// closed. Every other call passes through.
type probeGate struct {
	store.FS
	open atomic.Bool
}

var errProbeGateClosed = errors.New("probeGate: the writability probe is held failing")

func (g *probeGate) CreateTemp(dir, pattern string) (store.File, string, error) {
	if strings.HasPrefix(pattern, ".probe-") && !g.open.Load() {
		return nil, "", errProbeGateClosed
	}
	return g.FS.CreateTemp(dir, pattern)
}

// The first writability check gates readiness: until it passes /readyz and
// every /api/v1 endpoint answer 503 not_ready, while /healthz and /metrics
// answer.
func TestNotReadyUntilTheFirstStoreCheckPasses(t *testing.T) {
	// The not-ready window is held open by the gate, not by the probe interval: every probe fails
	// until the test opens the gate, so a test process starved of CPU for any length of time still
	// sees 503. (A one-shot fault rule plus a long interval left a wall-clock race, pg2-nmidq.) The
	// short interval only makes the daemon notice the open gate quickly.
	gate := &probeGate{FS: store.OS()}
	e := newEnv(t, options{fs: gate, probeEvery: 20 * time.Millisecond})
	if r := e.raw("GET", "/readyz", nil, nil); r.Status != 503 || r.Reason() != "not_ready" || !strings.Contains(string(r.Body), "store_writable") {
		t.Fatalf("/readyz before the first check: %d %s", r.Status, r.Body)
	}
	e.spec.ValidateResponse(t, "GET", "/readyz", 503, "application/problem+json", e.raw("GET", "/readyz", nil, nil).Body)
	if r := e.raw("GET", "/api/v1/state", nil, nil); r.Status != 503 || r.Reason() != "not_ready" {
		t.Fatalf("/state before the first check: %d %s", r.Status, r.Body)
	}
	var h map[string]any
	if r := e.get("/healthz"); r.Status != 200 {
		t.Fatalf("/healthz answers from process start: %d", r.Status)
	} else {
		r.json(t, &h)
	}
	if h["ready"] != false || h["status"] != "starting" {
		t.Errorf("/healthz before the first check: %v", h)
	}
	if v, _ := sampleValue(e.scrape()["pg_task_focus_ready"], nil); v != 0 {
		t.Errorf("ready = %v before the first check", v)
	}
	gate.open.Store(true)
	eventually(t, "the next probe to pass", func() bool { return e.raw("GET", "/readyz", nil, nil).Status == 200 })
	if r := e.get("/api/v1/state"); r.Status != 200 {
		t.Errorf("/state once ready: %d", r.Status)
	}
}
