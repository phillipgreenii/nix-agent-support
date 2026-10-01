package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/conformance"
	"github.com/phillipgreenii/pg-router/internal/config"
	"github.com/phillipgreenii/pg-router/internal/core"
	"github.com/phillipgreenii/pg-router/internal/eventqueue"
	"github.com/phillipgreenii/pg-router/internal/orchestrator"
	"github.com/phillipgreenii/pg-router/internal/roles"
)

// seedGate writes one active gate into logDir's event log (queue.jsonl) the way
// a previous daemon would have, so a freshly booted core finds it on replay.
func seedGate(t *testing.T, logDir string, req eventqueue.GateRequest) {
	t.Helper()
	store, err := eventqueue.NewFileStore(filepath.Join(logDir, "queue.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	q, err := eventqueue.New(store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.SetGate(req); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

// INV-LIFE-2's gated drain-and-exit slice: a gate persisted in the event log
// is found on replay; run-until-idle still boots, stays reachable (a push still
// succeeds and is durably enqueued), but produces and dispatches nothing.
func TestRunUntilIdleBody_gatedReachableAnswersIngestNoDispatch(t *testing.T) {
	fh := &fakeHandlerClient{}
	logDir := shortDir(t)
	seedGate(t, logDir, eventqueue.GateRequest{Type: "SYSTEM_PAUSE", Owner: "operator"})
	cfg := config.Config{
		LogDir: logDir,
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
		t.Fatal("gated run-until-idle never became discoverable; it must still boot the core (INV-LIFE-1)")
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
		t.Fatalf("ingest-event while gated: exit = %d, want 0; stderr=%s", code, stderr.String())
	}
	var reply map[string]any
	if err := json.Unmarshal([]byte(stdout.String()), &reply); err != nil {
		t.Fatalf("stdout %q is not JSON: %v", stdout.String(), err)
	}
	if reply["accepted"] != float64(1) {
		t.Fatalf("accepted = %v, want 1 — pushed events are still ACCEPTED under a gate", reply["accepted"])
	}

	if exitCode := <-done; exitCode != exitOK {
		t.Fatalf("gated run-until-idle exit = %d, want %d", exitCode, exitOK)
	}
	if len(fh.dispatchedCalls()) != 0 {
		t.Fatalf("gated run-until-idle must dispatch nothing; dispatched = %v", fh.dispatchedCalls())
	}
	if got := fh.preShutdownCalls(); len(got) != 1 || got[0] != "r1" {
		t.Fatalf("preShutdown calls = %v, want exactly [r1]", got)
	}
}

// INV-LIFE-2: expiry MUST continue while a gate is up — a tick under a gate
// still runs the whole body, including q.Expire().
func TestRunOneTick_expiryContinuesWhileGated(t *testing.T) {
	svc := &core.Service{}
	q, err := eventqueue.New(eventqueue.NewMemStore())
	if err != nil {
		t.Fatalf("eventqueue.New: %v", err)
	}
	past := time.Now().Add(-time.Hour)
	if _, err := q.Enqueue(eventqueue.Event{ID: "ev1", Type: "orphan", ExpiresAt: past}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if _, err := q.SetGate(eventqueue.GateRequest{Type: "SYSTEM_PAUSE"}); err != nil {
		t.Fatal(err)
	}
	o := &orchestrator.Orchestrator{}

	var stderr strings.Builder
	runOneTick(context.Background(), config.Config{}, o, svc, q, "", &stderr)

	if depth := q.DepthByType()["orphan"]; depth != 0 {
		t.Fatalf("a gated tick must still run q.Expire(): depth = %d, want 0", depth)
	}
	if svc.CurrentTick() == nil {
		t.Fatal("a gated tick must still publish its tick snapshot: gates act on participants, not on the loop")
	}
}

// The stderr notice fires exactly when the SET of active gate TYPEs changes —
// including once at startup while already gated — stays silent on repeat ticks
// and on a lease renewal of an already-active TYPE, and says so when the last
// gate clears.
func TestRunOneTick_gateNoticeOncePerChange(t *testing.T) {
	svc := &core.Service{}
	q, err := eventqueue.New(eventqueue.NewMemStore())
	if err != nil {
		t.Fatalf("eventqueue.New: %v", err)
	}
	o := &orchestrator.Orchestrator{}
	var buf strings.Builder
	tick := func(prev string) string {
		return runOneTick(context.Background(), config.Config{}, o, svc, q, prev, &buf)
	}

	if _, err := q.SetGate(eventqueue.GateRequest{Type: "SYSTEM_PAUSE", Owner: "operator"}); err != nil {
		t.Fatal(err)
	}
	sig := ""
	for i := 0; i < 3; i++ {
		sig = tick(sig)
	}
	if n := strings.Count(buf.String(), "pg-router: gated by SYSTEM_PAUSE"); n != 1 {
		t.Fatalf("notice count across 3 gated ticks = %d, want exactly 1; output=%q", n, buf.String())
	}
	if !strings.Contains(buf.String(), "owner operator") {
		t.Errorf("notice must carry the (debug-only) owner; output=%q", buf.String())
	}

	// A renewal changes nothing an operator must be told.
	if _, err := q.SetGate(eventqueue.GateRequest{Type: "SYSTEM_PAUSE", Owner: "operator"}); err != nil {
		t.Fatal(err)
	}
	sig = tick(sig)
	if n := strings.Count(buf.String(), "gated by"); n != 1 {
		t.Fatalf("a renewal re-printed the notice; output=%q", buf.String())
	}

	// A second gate changes the set: notice again, naming both.
	if _, err := q.SetGate(eventqueue.GateRequest{Type: "LOW_DISK_USAGE"}); err != nil {
		t.Fatal(err)
	}
	sig = tick(sig)
	if !strings.Contains(buf.String(), "LOW_DISK_USAGE (") || strings.Count(buf.String(), "gated by") != 2 {
		t.Fatalf("a changed gate set must re-notice; output=%q", buf.String())
	}

	// Clearing everything reports the all-clear.
	for _, ty := range []string{"SYSTEM_PAUSE", "LOW_DISK_USAGE"} {
		if _, _, err := q.ClearGate(ty, "op"); err != nil {
			t.Fatal(err)
		}
	}
	sig = tick(sig)
	if sig != "" || !strings.Contains(buf.String(), "no gate active") {
		t.Fatalf("clearing every gate: sig=%q output=%q, want the all-clear notice", sig, buf.String())
	}
}

func TestGateNotice_formatsEveryGate(t *testing.T) {
	if got := gateNotice(nil); got != "" {
		t.Fatalf("gateNotice(nil) = %q, want empty", got)
	}
	set := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	got := gateNotice([]eventqueue.Gate{
		{Type: "LOW_DISK_USAGE", Owner: "disk-watchdog", SetAt: set, ExpiresAt: set.Add(5 * time.Minute)},
		{Type: "SYSTEM_PAUSE", SetAt: set},
	})
	for _, want := range []string{"gated by LOW_DISK_USAGE (owner disk-watchdog, set 2026-10-01T12:00:00Z, lease until 2026-10-01T12:05:00Z)", "SYSTEM_PAUSE (set 2026-10-01T12:00:00Z)", "gate clear"} {
		if !strings.Contains(got, want) {
			t.Errorf("gateNotice missing %q in %q", want, got)
		}
	}
}
