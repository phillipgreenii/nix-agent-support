package daemon_test

import (
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/command"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
)

// A fresh daemon's first scrape carries one zero series per member of each
// closed label set, so the first rejection or event is visible to increase()
// and a dashboard can tell "zero" from "not emitting". Iterating the exported
// sets makes this fail when a Reason or Type is added without pre-init.
func TestFirstScrapePreinitialisesClosedSetCounters(t *testing.T) {
	e := newEnv(t, options{})
	fams := e.scrape()

	rej := fams["pg_task_focus_rejections_total"]
	if rej == nil {
		t.Fatal("rejections_total is absent on the first scrape")
	}
	for _, r := range command.Reasons() {
		if v, ok := sampleValue(rej, map[string]string{"reason": string(r)}); !ok || v != 0 {
			t.Errorf("rejections_total{reason=%q} is %v, present %v on the first scrape, want 0", r, v, ok)
		}
	}
	if got, want := len(rej.Metric), len(command.Reasons()); got != want {
		t.Errorf("rejections_total has %d series, want %d", got, want)
	}

	ev := fams["pg_task_focus_events_appended_total"]
	if ev == nil {
		t.Fatal("events_appended_total is absent on the first scrape")
	}
	for _, ty := range event.Types() {
		if v, ok := sampleValue(ev, map[string]string{"type": string(ty)}); !ok || v != 0 {
			t.Errorf("events_appended_total{type=%q} is %v, present %v on the first scrape, want 0", ty, v, ok)
		}
	}
	if got, want := len(ev.Metric), len(event.Types()); got != want {
		t.Errorf("events_appended_total has %d series, want %d", got, want)
	}
}
