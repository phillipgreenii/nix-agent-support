package daemon_test

import (
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/store/storefault"
)

const logFile = "events.jsonl"

// Read-only mode: a failed append whose outcome cannot be trusted makes every
// mutation 503 store_unavailable until a restart, the mode is carried by
// /state, /healthz, the attention feed and the metrics, reads keep working,
// and a restart clears it.
func TestReadOnlyModeIsObviousEverywhereAndClearsOnRestart(t *testing.T) {
	fs := storefault.New(nil)
	e := newEnv(t, options{fs: fs})
	e.bootstrap()
	e.clock.Set(local(9, 0))
	c := e.startCycle("deep-work")

	stream := e.openStream(t)
	defer stream.close()
	if ev := stream.next(t); ev.name != "state" {
		t.Fatalf("the stream starts with a state event, got %q", ev.name)
	}

	// The fsync of the next append fails: the state of the file is unknown.
	fs.Inject(storefault.Rule{Op: storefault.OpSync, Name: logFile})
	e.clock.Set(local(9, 10))
	body := map[string]any{"id": e.id(), "cycle_id": c}
	p := e.refused("/api/v1/cycles/pause", body, 503, "store_unavailable")
	store := p["store"].(map[string]any)
	if store["state"] != "read_only" || store["reason"] != "the append fsync failed" || store["since"] == nil {
		t.Fatalf("the problem body carries the store object: %v", store)
	}
	if !strings.HasPrefix(p["detail"].(string), "READ-ONLY: the append fsync failed. Restart pg-task-focus to recover") {
		t.Errorf("the detail is the READ-ONLY sentence: %q", p["detail"])
	}

	// The stream announced the mode at once, before any refresh.
	var sawStore bool
	for i := 0; i < 4 && !sawStore; i++ {
		if ev := stream.next(t); ev.name == "store" {
			sawStore = strings.Contains(ev.data, `"read_only"`)
		}
	}
	if !sawStore {
		t.Error("/stream sent no store event when read-only mode began")
	}

	// Every later mutation is refused with the same sentence; reads keep working.
	for _, req := range []struct {
		path string
		body map[string]any
	}{
		{"/api/v1/cycles/resume", map[string]any{"cycle_id": c}},
		{"/api/v1/tasks/day:2026-10-07:post-plan/complete", map[string]any{}},
		{"/api/v1/cycles/start", map[string]any{"type": "review"}},
		{"/api/v1/profile/change", map[string]any{"profile": "on-call"}},
	} {
		r := e.refused(req.path, req.body, 503, "store_unavailable")
		if !strings.HasPrefix(r["detail"].(string), "READ-ONLY: the append fsync failed") {
			t.Errorf("%s: %q", req.path, r["detail"])
		}
	}
	s := e.state()
	if st := s["store"].(map[string]any); st["state"] != "read_only" || st["reason"] != "the append fsync failed" {
		t.Errorf("/state store = %v", st)
	}
	var h map[string]any
	r := e.get("/healthz")
	r.json(t, &h)
	hs := h["store"].(map[string]any)
	if hs["state"] != "read_only" || hs["reason"] == nil || hs["since"] == nil || hs["writable"] != true {
		t.Errorf("/healthz store = %v", hs)
	}
	if h["status"] != "degraded" || h["ready"] != true {
		t.Errorf("a read-only daemon is ready and degraded: %v", h["status"])
	}
	if r := e.get("/readyz"); r.Status != 200 {
		t.Errorf("a failing store does not gate reads: /readyz %d", r.Status)
	}
	var att struct {
		Items []struct{ Type, ID, Summary, Severity string }
	}
	e.get("/api/v1/attention").json(t, &att)
	var found bool
	for _, it := range att.Items {
		if it.Type == "store" && it.Severity == "high" && strings.Contains(it.Summary, "read-only: the append fsync failed") {
			found = true
		}
	}
	if !found {
		t.Errorf("the attention feed has no high store item: %+v", att.Items)
	}
	fams := e.scrape()
	if v, _ := sampleValue(fams["pg_task_focus_store_read_only"], nil); v != 1 {
		t.Errorf("store_read_only = %v, want 1", v)
	}
	if v, _ := sampleValue(fams["pg_task_focus_append_failures_total"], map[string]string{"stage": "fsync"}); v != 1 {
		t.Errorf("append_failures_total{fsync} = %v, want 1", v)
	}
	if !strings.Contains(e.log.String(), "store is read-only; restart pg-task-focus to recover") {
		t.Error("the daemon did not log the mode at Error")
	}

	// Only a restart clears it, and the unknown outcome of the failed pause is resolved by retrying its id.
	e.restart(nil)
	if st := e.state()["store"].(map[string]any); st["state"] != "ok" {
		t.Fatalf("after a restart the store is %v", st)
	}
	if r := e.get("/api/v1/attention"); strings.Contains(string(r.Body), `"type":"store"`) {
		t.Error("a restart that clears read-only mode removes the store item")
	}
	again := e.ok("/api/v1/cycles/pause", body)
	if again["changed"] != true && again["replayed"] != true {
		t.Errorf("the retry with the same id after the restart: %v", again)
	}
}

// A write that fails and is rolled back is a 503 with an unknown outcome, but the store stays writable.
func TestRolledBackAppendIsRetryableWithTheSameID(t *testing.T) {
	fs := storefault.New(nil)
	e := newEnv(t, options{fs: fs})
	e.bootstrap()
	e.clock.Set(local(9, 0))
	fs.Inject(storefault.Rule{Op: storefault.OpWrite, Name: logFile})
	id := e.id()
	p := e.refused("/api/v1/tasks/day:2026-10-07:plan-day/complete", map[string]any{"id": id}, 503, "store_unavailable")
	if st := p["store"].(map[string]any); st["state"] != "ok" {
		t.Errorf("a rolled-back append leaves the store writable: %v", st)
	}
	if !strings.Contains(p["detail"].(string), "retry with the same id") {
		t.Errorf("the detail says to retry: %q", p["detail"])
	}
	if r := e.ok("/api/v1/tasks/day:2026-10-07:plan-day/complete", map[string]any{"id": id}); r["changed"] != true {
		t.Errorf("the retry with the same id succeeded: %v", r)
	}
	fams := e.scrape()
	if v, _ := sampleValue(fams["pg_task_focus_append_failures_total"], map[string]string{"stage": "write"}); v != 1 {
		t.Errorf("append_failures_total{write} = %v", v)
	}
	if v, _ := sampleValue(fams["pg_task_focus_store_read_only"], nil); v != 0 {
		t.Errorf("store_read_only = %v, want 0", v)
	}
}
