package main

import (
	"context"
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
