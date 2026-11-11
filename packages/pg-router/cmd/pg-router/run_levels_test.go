package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/conformance"
	"github.com/phillipgreenii/pg-router/internal/config"
	"github.com/phillipgreenii/pg-router/internal/core"
	"github.com/phillipgreenii/pg-router/internal/eventqueue"
	"github.com/phillipgreenii/pg-router/internal/levels"
	"github.com/phillipgreenii/pg-router/internal/orchestrator"
	"github.com/phillipgreenii/pg-router/internal/roles"
)

// seedPause writes an active router pause into logDir's event log (queue.jsonl)
// the way a previous daemon would have, so a freshly booted core finds it on replay.
func seedPause(t *testing.T, logDir string) {
	t.Helper()
	store, err := eventqueue.NewFileStore(filepath.Join(logDir, "queue.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	q, err := eventqueue.New(store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.AppendLevel(eventqueue.PauseRecord("operator", time.Now(), 0)); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

// routerResources is the router resource a deployment registers: without it a
// level record for "router" is ignored on replay.
func routerResources() config.ResourceSet {
	return config.ResourceSet{{Name: "router", Scale: []string{"RUNNING", "PAUSED"}, Sensor: config.RouterSensor, GraceFactor: 2}}
}

// INV-LIFE-2's paused drain-and-exit slice: a router pause persisted in the event
// log is found on replay; run-until-idle still boots, stays reachable (a push still
// succeeds and is durably enqueued), but produces and dispatches nothing.
func TestRunUntilIdleBody_pausedReachableAnswersIngestNoDispatch(t *testing.T) {
	fh := &fakeHandlerClient{}
	logDir := shortDir(t)
	seedPause(t, logDir)
	cfg := config.Config{
		LogDir:    logDir,
		Resources: routerResources(),
		Roles: roles.RoleSet{
			{Name: "r1", Enabled: true, Binds: []string{"t1"}},
			{Name: "r2", Enabled: false, Binds: []string{"t2"}}, // disabled: must never get a lifecycle hook
		},
	}
	o := &orchestrator.Orchestrator{Cfg: cfg, Handler: fh}

	done := make(chan int, 1)
	go func() {
		done <- runUntilIdleBody(context.Background(), preparedRun{cfg: cfg, o: o, cleanup: func() {}, declaredRoles: cfg.Roles})
	}()

	var ref core.Ref
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if r, err := core.Discover(logDir); err == nil {
			ref = r
			break
		}
		time.Sleep(time.Millisecond)
	}
	if ref == (core.Ref{}) {
		t.Fatal("paused run-until-idle never became discoverable; it must still boot the core (INV-LIFE-1)")
	}

	// postStartupAll fires right after bootCore succeeds (pg2-g3gqe: wait,
	// bounded, for it); exactly once, for the ENABLED role only.
	hookDeadline := time.Now().Add(5 * time.Second)
	for len(fh.postStartupCalls()) == 0 && time.Now().Before(hookDeadline) {
		time.Sleep(time.Millisecond)
	}
	if got := fh.postStartupCalls(); len(got) != 1 || got[0] != "r1" {
		t.Fatalf("postStartup calls = %v, want exactly [r1]", got)
	}

	var stdout, stderr strings.Builder
	code := callCore(&stdout, &stderr, ref, core.SubcommandIngestEvent,
		[]byte(`{"schemaVersion":"1","id":"trk-1","events":[{"id":"e1","type":"t1"}]}`))
	if code != conformance.ExitOK {
		t.Fatalf("ingest-event while paused: exit = %d, want 0; stderr=%s", code, stderr.String())
	}
	var reply map[string]any
	if err := json.Unmarshal([]byte(stdout.String()), &reply); err != nil {
		t.Fatalf("stdout %q is not JSON: %v", stdout.String(), err)
	}
	if reply["accepted"] != float64(1) {
		t.Fatalf("accepted = %v, want 1 — pushed events are still ACCEPTED under a pause", reply["accepted"])
	}

	if exitCode := <-done; exitCode != exitOK {
		t.Fatalf("paused run-until-idle exit = %d, want %d", exitCode, exitOK)
	}
	if len(fh.dispatchedCalls()) != 0 {
		t.Fatalf("paused run-until-idle must dispatch nothing; dispatched = %v", fh.dispatchedCalls())
	}
	if got := fh.preShutdownCalls(); len(got) != 1 || got[0] != "r1" {
		t.Fatalf("preShutdown calls = %v, want exactly [r1]", got)
	}
}

// INV-LIFE-2: expiry MUST continue while the router is paused — a tick under a
// pause still runs the whole body, including q.Expire().
func TestRunOneTick_expiryContinuesWhilePaused(t *testing.T) {
	svc := &core.Service{}
	q, err := eventqueue.New(eventqueue.NewMemStore(), eventqueue.WithLevelSource(pausedSource))
	if err != nil {
		t.Fatalf("eventqueue.New: %v", err)
	}
	past := time.Now().Add(-time.Hour)
	if _, err := q.Enqueue(eventqueue.Event{ID: "ev1", Type: "orphan", ExpiresAt: past}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	o := &orchestrator.Orchestrator{}

	var stderr strings.Builder
	runOneTick(context.Background(), config.Config{}, o, svc, q, "", &stderr)

	if depth := q.DepthByType()["orphan"]; depth != 0 {
		t.Fatalf("a paused tick must still run q.Expire(): depth = %d, want 0", depth)
	}
	if svc.CurrentTick() == nil {
		t.Fatal("a paused tick must still publish its tick snapshot: levels act on participants, not on the loop")
	}
}

// pausedSource holds the router PAUSED, owned by the operator.
func pausedSource() levels.Snapshot {
	now := time.Now()
	return levels.Snapshot{Now: now, Levels: map[string]levels.Level{
		eventqueue.RouterResource: {Value: eventqueue.LevelPaused, State: levels.StateHeld, Source: "operator", Owner: "operator", Since: now},
	}}
}

// The stderr notice fires exactly when the router pause CHANGES — including once
// at startup while already paused — stays silent on repeat ticks, and says so when
// the pause is cleared.
func TestRunOneTick_pauseNoticeOncePerChange(t *testing.T) {
	svc := &core.Service{}
	var cur atomic.Pointer[levels.Snapshot]
	set := func(s levels.Snapshot) { cur.Store(&s) }
	set(pausedSource())
	q, err := eventqueue.New(eventqueue.NewMemStore(), eventqueue.WithLevelSource(func() levels.Snapshot { return *cur.Load() }))
	if err != nil {
		t.Fatalf("eventqueue.New: %v", err)
	}
	o := &orchestrator.Orchestrator{}
	var buf strings.Builder
	tick := func(prev string) string {
		return runOneTick(context.Background(), config.Config{}, o, svc, q, prev, &buf)
	}
	sig := ""
	for i := 0; i < 3; i++ {
		sig = tick(sig)
	}
	if n := strings.Count(buf.String(), "pg-router: paused"); n != 1 {
		t.Fatalf("notice count across 3 paused ticks = %d, want exactly 1; output=%q", n, buf.String())
	}
	if !strings.Contains(buf.String(), "owner operator") {
		t.Errorf("notice must carry the (debug-only) owner; output=%q", buf.String())
	}
	set(levels.Snapshot{Now: time.Now(), Levels: map[string]levels.Level{}})
	sig = tick(sig)
	if sig != "" || !strings.Contains(buf.String(), "not paused") {
		t.Fatalf("clearing the pause: sig=%q output=%q, want the all-clear notice", sig, buf.String())
	}
}

func TestPauseNotice_formats(t *testing.T) {
	if got := pauseNotice(levels.Snapshot{}); got != "" {
		t.Fatalf("pauseNotice(none) = %q, want empty", got)
	}
	set := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	got := pauseNotice(levels.Snapshot{Levels: map[string]levels.Level{
		eventqueue.RouterResource: {Value: eventqueue.LevelPaused, Owner: "alice", Since: set},
	}})
	for _, want := range []string{"owner alice, set 2026-10-01T12:00:00Z", "admission is stopped", "pg-router resume"} {
		if !strings.Contains(got, want) {
			t.Errorf("pauseNotice missing %q in %q", want, got)
		}
	}
	other := pauseNotice(levels.Snapshot{Levels: map[string]levels.Level{"disk": {Value: "PAUSED"}}})
	if other != "" {
		t.Errorf("only the router resource's PAUSED level is a pause; got %q", other)
	}
}
