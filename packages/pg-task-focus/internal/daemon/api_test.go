package daemon_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// A scripted day through the real HTTP API; every request and response is
// checked against api/openapi.yaml by env.call.
func TestScriptedDay(t *testing.T) {
	e := newEnv(t, options{})

	// Before the first change the state is uninitialized.
	s := e.state()
	if s["initialized"] != false {
		t.Fatalf("an empty log is initialized: %v", s)
	}

	// Bootstrap is a dry run first, then for real.
	dry := e.ok("/api/v1/periods/change", map[string]any{
		"dry_run": true,
		"changes": []map[string]any{{"kind": "day", "start": "2026-10-07", "tz": "America/New_York"}},
	})
	if dry["dry_run"] != true {
		t.Fatalf("dry run: %v", dry)
	}
	res := e.bootstrap()
	if res["changed"] != true || res["batch_id"] == nil {
		t.Fatalf("bootstrap: %v", res)
	}
	s = e.state()
	if s["initialized"] != true || s["profile"] != "normal" {
		t.Fatalf("after bootstrap: %v", s)
	}
	tasks := s["tasks"].([]any)
	if len(tasks) != 5 {
		t.Fatalf("tasks = %d, want 5 (3 daily, 1 weekly, 1 sprint)", len(tasks))
	}
	if next, _ := s["next"].(map[string]any); next == nil || next["definition"] != "capacity-check" {
		t.Fatalf("next = %v", s["next"])
	}

	// A task is completed; repeating it with the same id returns the original result.
	id := e.id()
	body := map[string]any{"id": id}
	first := e.ok("/api/v1/tasks/day:2026-10-07:plan-day/complete", body)
	again := e.ok("/api/v1/tasks/day:2026-10-07:plan-day/complete", body)
	if first["changed"] != true || again["replayed"] != true {
		t.Fatalf("idempotency: %v then %v", first, again)
	}
	e.refused("/api/v1/tasks/day:2026-10-07:plan-day/complete", map[string]any{"id": e.id()}, 409, "task_already_resolved")
	e.refused("/api/v1/tasks/day:2026-10-07:nope/skip", map[string]any{"reason": "x"}, 404, "unknown_task")
	e.refused("/api/v1/tasks/day:2026-10-07:post-plan/skip", map[string]any{"reason": "   "}, 400, "invalid_request")
	e.ok("/api/v1/tasks/day:2026-10-07:post-plan/skip", map[string]any{"reason": "not needed today"})

	// A cycle: start, pause (twice: the second is a no-op), resume, boost, annotate, stop.
	e.clock.Set(local(9, 0))
	c1 := e.startCycle("deep-work")
	e.clock.Set(local(9, 10))
	if r := e.ok("/api/v1/cycles/pause", map[string]any{}); r["changed"] != true {
		t.Fatalf("pause: %v", r)
	}
	noop := e.ok("/api/v1/cycles/pause", map[string]any{"cycle_id": c1})
	if noop["changed"] != false || noop["note"] == nil || len(noop["event_ids"].([]any)) != 0 {
		t.Fatalf("a repeated pause is a no-op with a note: %v", noop)
	}
	e.clock.Set(local(9, 20))
	e.ok("/api/v1/cycles/resume", map[string]any{})
	e.ok("/api/v1/cycles/boost", map[string]any{"minutes": 10})
	e.ok("/api/v1/cycles/annotate", map[string]any{"note": "wrote the thing", "kv": []map[string]any{{"key": "ticket", "value": "ABC-1"}}})

	// An interrupting cycle dims the first; switch swaps; ambiguous verbs are refused.
	e.clock.Set(local(9, 30))
	c2 := e.startCycle("notifications")
	s = e.state()
	dimmed := s["dimmed"].([]any)
	if len(dimmed) != 1 || dimmed[0].(map[string]any)["id"] != c1 || dimmed[0].(map[string]any)["can_switch"] != true {
		t.Fatalf("dimmed = %v", dimmed)
	}
	e.clock.Set(local(9, 40))
	sw := e.ok("/api/v1/cycles/switch", map[string]any{"to": c1})
	if sw["batch_id"] == nil {
		t.Fatalf("a switch is a batch: %v", sw)
	}
	if f := e.state()["focus"].(map[string]any); f["id"] != c1 {
		t.Fatalf("after the switch the focus is %v", f["id"])
	}
	e.clock.Set(local(9, 45))
	e.ok("/api/v1/cycles/pause", map[string]any{"cycle_id": c1})
	amb := e.refused("/api/v1/cycles/resume", map[string]any{}, 400, "cycle_ambiguous")
	if cycles := amb["details"].(map[string]any)["cycles"].([]any); len(cycles) != 2 {
		t.Fatalf("cycle_ambiguous lists its candidates: %v", amb)
	}
	e.clock.Set(local(9, 46))
	e.ok("/api/v1/cycles/resume", map[string]any{"cycle_id": c1})
	e.clock.Set(local(9, 50))
	e.ok("/api/v1/cycles/stop", map[string]any{})
	if r := e.ok("/api/v1/cycles/stop", map[string]any{"cycle_id": c1}); r["changed"] != false { // a repeated named stop is a no-op
		t.Fatalf("repeated stop: %v", r)
	}
	e.ok("/api/v1/cycles/stop", map[string]any{}) // the only cycle not stopped
	_ = c2

	// The editor lists events in both views, corrects and retracts.
	ev := e.get("/api/v1/events?type=cycle.started,cycle.stopped&view=corrected")
	var list struct {
		Events []struct {
			ID    string
			Event struct{ Type string }
		}
	}
	ev.json(t, &list)
	if ev.Status != 200 || len(list.Events) < 4 {
		t.Fatalf("events: %d %s", ev.Status, ev.Body)
	}
	if r := e.get("/api/v1/events?view=original&from=2026-10-07T00:00:00Z&to=2026-10-08T00:00:00Z"); r.Status != 200 {
		t.Fatalf("original view: %d %s", r.Status, r.Body)
	}
	e.refused("/api/v1/events/01JABCDEFGHJKMNPQRSTVWXYZ0/retract", map[string]any{}, 404, "unknown_event")
	last := list.Events[len(list.Events)-1].ID
	e.ok("/api/v1/events/"+last+"/retract", map[string]any{"reason": "oops"})

	// The connector reads.
	cal := e.get("/api/v1/calendar?from=2026-10-07T00:00:00Z&to=2026-10-08T00:00:00Z")
	if cal.Status != 200 || !strings.Contains(string(cal.Body), "cycle_type: deep-work") {
		t.Fatalf("calendar: %d %s", cal.Status, cal.Body)
	}
	if r := e.get("/api/v1/calendar?from=2026-10-07T00:00:00Z&to=2026-10-08T00:00:00Z&calendar=other"); !strings.Contains(string(r.Body), `"events":[]`) {
		t.Fatalf("another calendar name returns no events: %s", r.Body)
	}
	if r := e.get("/api/v1/attention"); r.Status != 200 {
		t.Fatalf("attention: %d %s", r.Status, r.Body)
	}
	if r := e.get("/api/v1/config"); r.Status != 200 || !strings.Contains(string(r.Body), `"listen_port"`) {
		t.Fatalf("config: %d %s", r.Status, r.Body)
	}
	for _, p := range []string{"/healthz", "/readyz"} {
		if r := e.get(p); r.Status != 200 {
			t.Fatalf("%s: %d %s", p, r.Status, r.Body)
		}
	}
	r := e.raw("GET", "/metrics", nil, nil)
	if r.Status != 200 || !strings.Contains(string(r.Body), "pg_task_focus_ready 1") {
		t.Fatalf("metrics: %d", r.Status)
	}
	_ = json.RawMessage(nil)
	_ = time.Second
}
