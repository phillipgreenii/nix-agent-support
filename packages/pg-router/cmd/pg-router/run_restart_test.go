package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/phillipgreenii/pg-router/internal/config"
	"github.com/phillipgreenii/pg-router/internal/core"
	"github.com/phillipgreenii/pg-router/internal/event"
	"github.com/phillipgreenii/pg-router/internal/eventlog"
	"github.com/phillipgreenii/pg-router/internal/eventqueue"
	"github.com/phillipgreenii/pg-router/internal/metrics"
	"github.com/phillipgreenii/pg-router/internal/orchestrator"
	"github.com/phillipgreenii/pg-router/internal/query"
	"github.com/phillipgreenii/pg-router/internal/roles"
	"github.com/phillipgreenii/pg-router/internal/wireclient"
)

// End-to-end tests for a daemon restart that leaves a ccpool-backed dispatch
// running (this repo's ADR 0085, packages/pg-router DEC-EVENT-5, bead pg2-x3zkb):
// the old daemon exits with the handler of a surviving role still working, and the
// next daemon re-offers the event and adopts the session. The fake handler stands
// in for the ccpool handler: it blocks inside `dispatch` for as long as its
// "session" lives.

// blockingSource is a pull source whose poll blocks until released: a stand-in for
// the slow first producer tick of a fresh process, when no source has a last-fire
// time and every one of them is due.
type blockingSource struct {
	query.Meta
	entered chan struct{} // closed when the first poll begins
	release chan struct{} // closed to let the poll return
	once    sync.Once
}

func newBlockingSource() *blockingSource {
	return &blockingSource{
		Meta:    query.Meta{EmitTypes: []string{"t1"}, Trig: query.PeriodTrigger{Every: time.Hour}},
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (*blockingSource) Validate() error        { return nil }
func (*blockingSource) BackingCommand() string { return "" }
func (b *blockingSource) Run(ctx context.Context, _ query.Env) ([]event.Event, error) {
	b.once.Do(func() { close(b.entered) })
	select {
	case <-b.release:
	case <-ctx.Done():
	}
	return nil, nil
}

// seedDurableQueue writes evts into the queue log of a daemon that is not running
// (the events a previous process had queued), then releases the log's lock.
func seedDurableQueue(t *testing.T, logDir string, evts ...eventqueue.Event) {
	t.Helper()
	store, err := eventqueue.NewFileStore(filepath.Join(logDir, "queue.jsonl"))
	if err != nil {
		t.Fatalf("open queue log: %v", err)
	}
	q, err := eventqueue.New(store)
	if err != nil {
		_ = store.Close()
		t.Fatalf("queue: %v", err)
	}
	for _, e := range evts {
		if _, err := q.Enqueue(e); err != nil {
			_ = store.Close()
			t.Fatalf("enqueue %s: %v", e.ID, err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close queue log: %v", err)
	}
}

// The per-role file the deployment renders for the one role these tests run: a
// ccpool-backed role declares that its dispatch survives a daemon shutdown, a
// command role does not.
const (
	survivingRoleJSON = `{"name":"r1","type":"ccpool","survivesShutdown":true}`
	commandRoleJSON   = `{"name":"r1","type":"command"}`
)

// restartDaemon is one daemon of a restart test: runLongRunningBody over logDir
// with the one role r1, bound to t1, whose handler is runner.
type restartDaemon struct {
	logDir  string
	runner  wireclient.Runner
	roleCfg string        // the role's rendered file: survivingRoleJSON or commandRoleJSON
	drain   time.Duration // the shutdown drain timeout of a role that does not survive
	queries query.SourceSet
	mp      metric.MeterProvider // nil: the daemon's own default provider
	// eventLog opens <logDir>/events.jsonl for the daemon, as the production
	// wiring does, so a test can read the dispatch rows it writes.
	eventLog bool
}

// run starts the daemon and returns its exit code on the channel.
func (d restartDaemon) run(ctx context.Context, t *testing.T) <-chan int {
	t.Helper()
	cfg := config.Config{
		LogDir:        d.logDir,
		PollInterval:  20 * time.Millisecond,
		Roles:         roles.RoleSet{{Name: "r1", Enabled: true, Binds: []string{"t1"}}},
		Queries:       d.queries,
		MeterProvider: d.mp,
	}
	roleDirWith(t, d.roleCfg)(&cfg)
	h := &wireclient.Client{Runner: d.runner, Command: func(_ roles.Role, sub string) ([]string, error) { return []string{"fake-handler", sub}, nil }}
	o := &orchestrator.Orchestrator{Cfg: cfg, Handler: h, Bindings: core.NewBindings("t1")}
	if d.eventLog {
		lw, err := eventlog.New(filepath.Join(d.logDir, "events.jsonl"))
		if err != nil {
			t.Fatalf("open the event log: %v", err)
		}
		t.Cleanup(func() { _ = lw.Close() })
		o.Log = lw
	}
	ch := make(chan int, 1)
	go func() {
		ch <- runLongRunningBody(ctx, preparedRun{cfg: cfg, o: o, cleanup: func() {}, declaredRoles: cfg.Roles}, d.drain)
	}()
	return ch
}

func awaitClosed(t *testing.T, c <-chan struct{}, within time.Duration, why string) {
	t.Helper()
	select {
	case <-c:
	case <-time.After(within):
		t.Fatal(why)
	}
}

func awaitExit(t *testing.T, done <-chan int, why string) {
	t.Helper()
	select {
	case code := <-done:
		if code != exitOK {
			t.Fatalf("%s: exit = %d, want %d", why, code, exitOK)
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("%s: the daemon did not exit", why)
	}
}

// readInFlight reads pg_router_listener_in_flight{role=role} the way a scrape
// does. ok is false while the gauge has no such series.
func readInFlight(t *testing.T, reader *sdkmetric.ManualReader, role string) (value int64, ok bool) {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != metrics.MetricListenerInFlight {
				continue
			}
			g, isGauge := m.Data.(metricdata.Gauge[int64])
			if !isGauge {
				t.Fatalf("%s is %T, want an int64 gauge", m.Name, m.Data)
			}
			for _, dp := range g.DataPoints {
				if v, has := dp.Attributes.Value(attribute.Key("role")); has && v.AsString() == role {
					return dp.Value, true
				}
			}
		}
	}
	return 0, false
}

// A daemon that restarts over a dispatch its predecessor left running must
// re-offer that event in its FIRST dispatch pass, before its first producer tick.
// The first tick of a fresh process polls every source, none having a last-fire
// time, one after another, so the offer used to wait behind all of them: on
// 2026-10-12 the new daemon held no offer for the surviving session for 268.6 s
// (23:28:38Z to 23:33:06.6Z) and pg_router_listener_in_flight read 0 for the role
// that whole time, although its session never stopped working.
//
// Here the one source never returns, so the first tick can never finish: the
// re-offer, and the gauge reading 1, can only come from a pass that runs before it.
func TestRunLongRunning_restartReoffersTheSurvivingDispatchBeforeTheFirstProducerTick(t *testing.T) {
	logDir := shortDir(t)
	seedDurableQueue(t, logDir, eventqueue.Event{ID: "survivor-1", Type: "t1", ExpiresAt: time.Now().Add(time.Hour)})

	// Daemon one offers the event; its handler (the surviving "session") keeps
	// working through the daemon's shutdown (ADR 0085).
	r1 := newShutdownRunner()
	t.Cleanup(func() { close(r1.release) }) // let the old handler's goroutine end last
	ctx1, signal1 := context.WithCancel(context.Background())
	t.Cleanup(signal1)
	done1 := restartDaemon{logDir: logDir, runner: r1, roleCfg: survivingRoleJSON, drain: time.Minute}.run(ctx1, t)
	awaitClosed(t, r1.started, 20*time.Second, "daemon one never offered the queued event")
	signal1()
	awaitExit(t, done1, "daemon one")

	// Daemon two restarts over the same queue log with a first producer tick that
	// cannot finish.
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	slow := newBlockingSource()
	r2 := newShutdownRunner()
	ctx2, signal2 := context.WithCancel(context.Background())
	done2 := restartDaemon{
		logDir: logDir, runner: r2, roleCfg: survivingRoleJSON, drain: time.Minute,
		queries: query.SourceSet{{Name: "slow", Query: slow}}, mp: mp,
	}.run(ctx2, t)
	t.Cleanup(func() {
		close(slow.release)
		close(r2.release)
		signal2()
		awaitExit(t, done2, "daemon two")
	})
	awaitClosed(t, slow.entered, 20*time.Second, "daemon two never began its first producer tick")

	// The first producer tick is stuck inside its source. The restored event must
	// already have been re-offered to its role's handler.
	awaitClosed(t, r2.started, 10*time.Second,
		"the surviving dispatch was not re-offered although the first producer tick was already running: the first dispatch pass must precede it")
	if n, _, _ := r2.snapshot(); n != 1 {
		t.Fatalf("re-offered %d time(s), want exactly 1", n)
	}

	// And the role reads in flight from then on, not 0 until the tick ends.
	deadline := time.Now().Add(10 * time.Second)
	for {
		v, ok := readInFlight(t, reader, "r1")
		if ok && v == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s{role=r1} = %d (series present=%v) while the re-offered dispatch is running, want 1", metrics.MetricListenerInFlight, v, ok)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The startup dispatch pass is a dispatch pass: like every tick, it must offer
// nothing once shutdown was requested (a signal that lands while the daemon is
// still booting).
//
// The role does not survive a shutdown, so the daemon waits out its drain for any
// offer that was launched and only then exits: an offer the startup pass wrongly
// launched has reached the handler by the time the daemon is gone, which makes
// the count read below exact rather than a race against the launched goroutine.
func TestRunLongRunning_noStartupDispatchPassOnceShutdownWasRequested(t *testing.T) {
	logDir := shortDir(t)
	seedDurableQueue(t, logDir, eventqueue.Event{ID: "queued-1", Type: "t1", ExpiresAt: time.Now().Add(time.Hour)})

	r := newShutdownRunner()
	t.Cleanup(func() { close(r.release) })
	ctx, signal := context.WithCancel(context.Background())
	signal() // already requested before the daemon boots
	done := restartDaemon{logDir: logDir, runner: r, roleCfg: commandRoleJSON, drain: 200 * time.Millisecond}.run(ctx, t)
	awaitExit(t, done, "the daemon")
	if n, steps, _ := r.snapshot(); n != 0 {
		t.Fatalf("%d dispatch(es) offered after shutdown was requested (steps %v), want none", n, steps)
	}
}

// The adoption tests below: the new daemon's handler adopts the session the old daemon
// left running and reports the dispatch's own start (DEC-EVENT-5, bead pg2-dm6de), so a
// restart neither lengthens the wait nor shortens the run.

// restartHole is how long the surviving dispatch keeps running between daemon one's exit
// and daemon two's start: the span a re-timed start loses, long enough that scheduling
// jitter cannot hide it.
const restartHole = 500 * time.Millisecond

// instantRunner is a shutdownRunner that notes when its first dispatch began: the
// dispatch's own start, which the next daemon's adopting handler reports.
type instantRunner struct {
	*shutdownRunner
	mu    sync.Mutex
	first time.Time
}

func (r *instantRunner) Run(ctx context.Context, argv []string, stdin []byte) ([]byte, int, error) {
	if argv[len(argv)-1] == "dispatch" {
		r.mu.Lock()
		if r.first.IsZero() {
			r.first = time.Now()
		}
		r.mu.Unlock()
	}
	return r.shutdownRunner.Run(ctx, argv, stdin)
}

func (r *instantRunner) firstDispatch() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.first
}

// adoptingRunner is the second daemon's handler: it blocks in dispatch until released and
// then replies as a handler that adopted the session does, naming the dispatch's original
// start in startedAt. An empty startedAt leaves the field out, as every other handler does.
type adoptingRunner struct {
	started   chan struct{} // closed on the first dispatch call
	release   chan struct{} // close to let the dispatch reply
	startedAt string
	once      sync.Once
}

func (r *adoptingRunner) Run(ctx context.Context, argv []string, _ []byte) ([]byte, int, error) {
	if argv[len(argv)-1] != "dispatch" {
		return []byte(`{"schemaVersion":"1","id":"x","outcome":"ok"}`), 0, nil
	}
	r.once.Do(func() { close(r.started) })
	select {
	case <-r.release:
		reply := map[string]any{"schemaVersion": "1", "id": "x", "outcome": "delivered"}
		if r.startedAt != "" {
			reply["startedAt"] = r.startedAt
		}
		body, err := json.Marshal(reply)
		return body, 0, err
	case <-ctx.Done():
		return nil, 0, ctx.Err()
	}
}

// awaitJSONLine polls a JSONL file a running daemon appends to until a line satisfying
// match turns up. The file is read as bytes, not opened as a queue log, whose lock the
// daemon holds; a line still being written fails to decode and is skipped.
func awaitJSONLine(t *testing.T, path, why string, match func(map[string]any) bool) map[string]any {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		if data, err := os.ReadFile(path); err == nil {
			for _, line := range bytes.Split(data, []byte("\n")) {
				var m map[string]any
				if json.Unmarshal(line, &m) == nil && match(m) {
					return m
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: no matching line in %s", why, path)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// adoptionRestart is what a daemon restart over a surviving dispatch left behind.
type adoptionRestart struct {
	firstStart  time.Time // when daemon one's handler began the dispatch
	restarted   time.Time // just before daemon two was started
	rowSeen     time.Time // when daemon two's dispatch row was read
	acceptStart time.Time // the accept record's startedAt
	acceptAt    time.Time // the accept record's settle instant
	row         map[string]any
}

// restartOverSurvivingDispatch runs two daemons over one queue log. Daemon one offers the
// event and exits while its handler keeps working; daemon two re-offers it to a handler
// that adopts it and, when handlerReportsStart, replies with the instant daemon one's
// handler began. Daemon two's first producer tick cannot finish, so the re-offer, and the
// gauge reading 1, can only come from the startup dispatch pass.
func restartOverSurvivingDispatch(t *testing.T, handlerReportsStart bool) adoptionRestart {
	t.Helper()
	logDir := shortDir(t)
	seedDurableQueue(t, logDir, eventqueue.Event{ID: "survivor-1", Type: "t1", ExpiresAt: time.Now().Add(time.Hour)})

	r1 := &instantRunner{shutdownRunner: newShutdownRunner()}
	t.Cleanup(func() { close(r1.release) }) // let the old handler's goroutine end last
	ctx1, signal1 := context.WithCancel(context.Background())
	t.Cleanup(signal1)
	done1 := restartDaemon{logDir: logDir, runner: r1, roleCfg: survivingRoleJSON, drain: time.Minute}.run(ctx1, t)
	awaitClosed(t, r1.started, 20*time.Second, "daemon one never offered the queued event")
	firstStart := r1.firstDispatch()
	signal1()
	awaitExit(t, done1, "daemon one")
	time.Sleep(restartHole) // the dispatch keeps running while no daemon is up

	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	slow := newBlockingSource()
	r2 := &adoptingRunner{started: make(chan struct{}), release: make(chan struct{})}
	if handlerReportsStart {
		r2.startedAt = firstStart.UTC().Format(time.RFC3339Nano)
	}
	ctx2, signal2 := context.WithCancel(context.Background())
	restarted := time.Now()
	done2 := restartDaemon{
		logDir: logDir, runner: r2, roleCfg: survivingRoleJSON, drain: time.Minute,
		queries: query.SourceSet{{Name: "slow", Query: slow}}, mp: mp, eventLog: true,
	}.run(ctx2, t)
	var releaseOnce, stopOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(r2.release) }) }
	t.Cleanup(func() {
		stopOnce.Do(func() {
			close(slow.release)
			release()
			signal2()
			awaitExit(t, done2, "daemon two")
		})
	})
	awaitClosed(t, slow.entered, 20*time.Second, "daemon two never began its first producer tick")
	awaitClosed(t, r2.started, 10*time.Second,
		"the surviving dispatch was not re-offered although the first producer tick was already running: the first dispatch pass must precede it")

	// The role reads in flight from the startup pass, not 0 until the tick ends.
	deadline := time.Now().Add(10 * time.Second)
	for {
		v, ok := readInFlight(t, reader, "r1")
		if ok && v == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s{role=r1} = %d (series present=%v) while the re-offered dispatch is running, want 1", metrics.MetricListenerInFlight, v, ok)
		}
		time.Sleep(10 * time.Millisecond)
	}

	// The adopting handler finishes: the offer settles, and daemon two writes its
	// dispatch row (events.jsonl) and its accept record (queue.jsonl).
	release()
	row := awaitJSONLine(t, filepath.Join(logDir, "events.jsonl"), "daemon two never wrote the dispatch row",
		func(m map[string]any) bool { return m["kind"] == "dispatch" })
	rowSeen := time.Now()
	accept := awaitJSONLine(t, filepath.Join(logDir, "queue.jsonl"), "daemon two never wrote the accept record",
		func(m map[string]any) bool { return m["op"] == "accept" && m["eventId"] == "survivor-1" })
	instant := func(field string) time.Time {
		s, _ := accept[field].(string)
		v, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			t.Fatalf("accept record %s = %q: %v", field, s, err)
		}
		return v
	}
	return adoptionRestart{
		firstStart: firstStart, restarted: restarted, rowSeen: rowSeen,
		acceptStart: instant("startedAt"), acceptAt: instant("at"), row: row,
	}
}

func durationMs(t *testing.T, row map[string]any) time.Duration {
	t.Helper()
	ms, ok := row["duration_ms"].(float64)
	if !ok {
		t.Fatalf("the dispatch row has no duration_ms: %v", row)
	}
	return time.Duration(ms) * time.Millisecond
}

// A handler that adopted the surviving session reports the dispatch's own start, and
// the new daemon uses it: the accept record and the dispatch row carry daemon one's
// instant, and the run spans the restart instead of starting at the re-offer.
func TestRunLongRunning_restartAdoptedDispatchKeepsItsOriginalStart(t *testing.T) {
	r := restartOverSurvivingDispatch(t, true)

	if !r.acceptStart.Equal(r.firstStart) {
		t.Fatalf("accept startedAt = %v, want the reported start %v (the dispatch's own, %v before the restart ended)", r.acceptStart, r.firstStart, r.restarted.Sub(r.firstStart))
	}
	if got, want := r.row["started_at"], r.firstStart.UTC().Format(time.RFC3339Nano); got != want {
		t.Fatalf("row started_at = %v, want the reported start %v", got, want)
	}
	// duration_ms spans the restart hole, and it cannot exceed the time the dispatch has existed.
	dur := durationMs(t, r.row)
	if dur < restartHole {
		t.Fatalf("duration_ms = %v, want at least the %v restart hole it spans", dur, restartHole)
	}
	if existed := r.rowSeen.Sub(r.firstStart); dur > existed {
		t.Fatalf("duration_ms = %v is longer than the dispatch has existed (%v)", dur, existed)
	}
	// The run the metric reads (settle - start, from the accept record) spans the hole too.
	if run := r.acceptAt.Sub(r.acceptStart); run < restartHole {
		t.Fatalf("accept record run = %v, want at least the %v restart hole", run, restartHole)
	}
}

// The negative control: the same restart with a handler that reports no start, as every
// handler did before this field existed. The dispatch is timed from the re-offer, today's
// behavior, so the restart hole is read as time before the dispatch began.
func TestRunLongRunning_restartWithoutAReportedStartTimesTheDispatchFromTheReoffer(t *testing.T) {
	r := restartOverSurvivingDispatch(t, false)

	if r.acceptStart.Before(r.restarted) {
		t.Fatalf("accept startedAt = %v precedes daemon two's launch %v: it is the re-offer's instant, not daemon one's %v", r.acceptStart, r.restarted, r.firstStart)
	}
	if lost := r.acceptStart.Sub(r.firstStart); lost < restartHole {
		t.Fatalf("accept startedAt is only %v after the dispatch really began, want at least the %v restart hole it re-timed away", lost, restartHole)
	}
	if got, want := r.row["started_at"], r.acceptStart.UTC().Format(time.RFC3339Nano); got != want {
		t.Fatalf("row started_at = %v, want the offer's own start %v, the accept record's", got, want)
	}
	// duration_ms is the offer's own run: it cannot reach back before daemon two existed.
	if dur, bound := durationMs(t, r.row), r.rowSeen.Sub(r.restarted); dur > bound {
		t.Fatalf("duration_ms = %v is longer than daemon two has existed (%v): the run reached back across the restart", dur, bound)
	}
}
