package daemon_test

import (
	"strings"
	"testing"
)

type attentionItem struct {
	Type, ID, Summary, Severity, URL string
	Group                            struct{ Key, Label string }
}

func (e *env) attention() []attentionItem {
	e.t.Helper()
	var a struct{ Items []attentionItem }
	e.get("/api/v1/attention").json(e.t, &a)
	return a.Items
}

func find(items []attentionItem, typ, idSuffix string) *attentionItem {
	for i := range items {
		if items[i].Type == typ && strings.HasSuffix(items[i].ID, idSuffix) {
			return &items[i]
		}
	}
	return nil
}

// The attention feed is stateless: an item exists while its condition holds,
// with the severities, groups and deep links of the mapping, and disappears
// when it stops.
func TestAttentionFeedMapping(t *testing.T) {
	e := newEnv(t, options{})
	e.bootstrap()

	// 09:25: plan-day (due 09:00) is overdue, post-plan (09:30) is due soon.
	e.clock.Set(local(9, 25))
	items := e.attention()
	plan := find(items, "task", ":plan-day")
	post := find(items, "task", ":post-plan")
	if plan == nil || plan.Severity != "high" || plan.Group.Key != "daily" || plan.Group.Label != "Today" || plan.Summary != "Plan the day" {
		t.Fatalf("overdue task item: %+v", plan)
	}
	if post == nil || post.Severity != "medium" {
		t.Fatalf("due-soon task item: %+v", post)
	}
	if want := "https://focus.example.test/#/tasks/day:2026-10-07:plan-day"; plan.URL != want {
		t.Errorf("the deep link is %q, want %q", plan.URL, want)
	}
	if find(items, "task", ":end-of-day-summary") != nil {
		t.Error("a task due in 8 hours is not an attention item")
	}
	if wk := find(items, "task", ":capacity-check"); wk == nil || wk.Group.Key != "sprint" || wk.Group.Label != "This sprint" {
		t.Errorf("the sprint task's group: %+v", wk)
	}

	// Completing or skipping removes the item.
	e.ok("/api/v1/tasks/day:2026-10-07:plan-day/complete", map[string]any{})
	if find(e.attention(), "task", ":plan-day") != nil {
		t.Error("a completed task is still in the feed")
	}

	// An overtime cycle is medium, high after overtime_high_minutes (15); boosting out of it removes the item.
	c := e.startCycle("notifications") // 25 minutes
	e.clock.Set(local(9, 55))          // 5 minutes over
	over := find(e.attention(), "cycle", c)
	if over == nil || over.Severity != "medium" || over.Summary != "Notification cycle: 5 min over" || over.Group.Key != "cycles" {
		t.Fatalf("overtime item: %+v", over)
	}
	e.clock.Set(local(10, 10)) // 20 minutes over
	if over = find(e.attention(), "cycle", c); over == nil || over.Severity != "high" {
		t.Fatalf("overtime past overtime_high_minutes: %+v", over)
	}
	e.ok("/api/v1/cycles/boost", map[string]any{"minutes": 25})
	if find(e.attention(), "cycle", c) != nil {
		t.Error("a boost that ends overtime leaves the item")
	}

	// A cycle paused longer than stale_pause_minutes (45) is low.
	e.ok("/api/v1/cycles/pause", map[string]any{})
	e.clock.Set(local(10, 50)) // 40 minutes paused: within the limit
	if find(e.attention(), "cycle", c) != nil {
		t.Error("a pause within stale_pause_minutes is an item")
	}
	e.clock.Set(local(11, 30))
	stale := find(e.attention(), "cycle", c)
	if stale == nil || stale.Severity != "low" || !strings.HasPrefix(stale.Summary, "Notification cycle paused ") {
		t.Fatalf("stale pause item: %+v", stale)
	}
	e.ok("/api/v1/cycles/resume", map[string]any{})
	if find(e.attention(), "cycle", c) != nil {
		t.Error("resuming removes the stale-pause item")
	}
}

// The corrected view carries the original beside a corrected event and the
// correcting events, and an original view carries the raw lines.
func TestEventsViews(t *testing.T) {
	e := newEnv(t, options{})
	e.bootstrap()
	e.clock.Set(local(9, 0))
	e.ok("/api/v1/tasks/day:2026-10-07:plan-day/complete", map[string]any{})
	var list struct{ Events []struct{ ID string } }
	e.get("/api/v1/events?type=task.completed").json(t, &list)
	id := list.Events[0].ID
	correction := e.ok("/api/v1/events/"+id+"/correct", map[string]any{"fields": map[string]any{"effective_at": "2026-10-07T12:55:00.000Z"}})
	cid := correction["event_ids"].([]any)[0].(string)

	var corrected struct {
		Events []struct {
			ID    string
			Event struct {
				EffectiveAt string `json:"effective_at"`
			}
			Original *struct {
				EffectiveAt string `json:"effective_at"`
			}
			CorrectedBy []string `json:"corrected_by"`
		}
	}
	e.get("/api/v1/events?type=task.completed").json(t, &corrected)
	ev := corrected.Events[0]
	if ev.Event.EffectiveAt != "2026-10-07T12:55:00.000Z" || ev.Original == nil || ev.Original.EffectiveAt == ev.Event.EffectiveAt || len(ev.CorrectedBy) != 1 || ev.CorrectedBy[0] != cid {
		t.Errorf("corrected view: %+v", ev)
	}
	var original struct {
		Events []struct {
			Event struct {
				EffectiveAt string `json:"effective_at"`
			}
		}
	}
	e.get("/api/v1/events?type=task.completed&view=original").json(t, &original)
	if original.Events[0].Event.EffectiveAt == "2026-10-07T12:55:00.000Z" {
		t.Error("the original view shows the correction")
	}
	// Retracting the correction restores the first time and the event shows no correcting events.
	e.ok("/api/v1/events/"+cid+"/retract", map[string]any{})
	corrected.Events = nil
	e.get("/api/v1/events?type=task.completed").json(t, &corrected)
	if len(corrected.Events[0].CorrectedBy) != 0 || corrected.Events[0].Original != nil {
		t.Errorf("after retracting the correction: %+v", corrected.Events[0])
	}
	e.refused("/api/v1/events/"+id+"/correct", map[string]any{"fields": map[string]any{"task_id": "day:2026-10-07:post-plan"}}, 422, "invalid_correction")
	if r := e.get("/api/v1/events?view=bogus"); r.Status != 400 {
		t.Errorf("a bad view: %d", r.Status)
	}
	if r := e.get("/api/v1/events?type=task.exploded"); r.Status != 400 {
		t.Errorf("a bad type: %d", r.Status)
	}
	if r := e.get("/api/v1/calendar?from=2026-10-07T00:00:00Z"); r.Status != 400 || r.Reason() != "invalid_request" {
		t.Errorf("a calendar with no to: %d", r.Status)
	}
}
