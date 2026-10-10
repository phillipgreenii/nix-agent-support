package daemon_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// poll moves the fake clock to t and polls the scheduler, as the daemon's own
// timer does at the instant the scheduler names.
func (e *env) pollAt(t time.Time) {
	e.t.Helper()
	e.clock.Set(t)
	e.d.Alerter().Poll()
}

func (e *env) waitSounds(n int) []string {
	e.t.Helper()
	eventually(e.t, "the sounds to be played", func() bool { return len(e.player.Played()) >= n })
	return e.player.Played()
}

func (e *env) settle() {
	e.t.Helper()
	e.d.Alerter().Drain(context.Background())
}

// The daemon plays the sound and sends the notification at expiry and at every
// reminder, counted in the cycle's running time: a paused cycle is silent and
// a resumed one continues its count.
func TestExpiryAndRemindersCountRunningTime(t *testing.T) {
	e := newEnv(t, options{})
	e.bootstrap()
	e.clock.Set(local(9, 0))
	c := e.startCycle("deep-work") // 50 minutes, sound Hero, repeat every 10 minutes

	e.pollAt(local(9, 49))
	e.settle()
	if got := e.player.Played(); len(got) != 0 {
		t.Fatalf("a sound before the time is up: %v", got)
	}

	e.pollAt(local(9, 50)) // the expiry
	if got := e.waitSounds(1); got[0] != "Hero" {
		t.Fatalf("the expiry sound = %v, want Hero (the type's own)", got)
	}
	e.pollAt(local(9, 52))
	e.settle()
	if len(e.player.Played()) != 1 {
		t.Fatalf("no repeat before repeat_minutes: %v", e.player.Played())
	}

	// Paused at 9:55 (5 minutes into overtime, 5 of 10 toward the first reminder): silent for any wall-clock time.
	e.clock.Set(local(9, 55))
	e.ok("/api/v1/cycles/pause", map[string]any{"cycle_id": c})
	e.pollAt(local(11, 0))
	e.settle()
	if len(e.player.Played()) != 1 {
		t.Fatalf("a paused cycle played a sound: %v", e.player.Played())
	}

	// Resumed at 11:00, it reminds after the 5 running minutes that remain, not after 10.
	e.ok("/api/v1/cycles/resume", map[string]any{"cycle_id": c})
	e.pollAt(local(11, 4))
	e.settle()
	if len(e.player.Played()) != 1 {
		t.Fatalf("the reminder fell due early: %v", e.player.Played())
	}
	e.pollAt(local(11, 5))
	e.waitSounds(2)
	notes := e.player.Notified()
	if len(notes) != 2 || notes[0].Title != "Deep work cycle" || !strings.Contains(notes[1].Body, "min over") {
		t.Fatalf("a notification goes with every sound: %+v", notes)
	}

	fams := e.scrape()
	if v, _ := sampleValue(fams["pg_task_focus_alerts_played_total"], map[string]string{"kind": "expiry"}); v != 1 {
		t.Errorf("alerts_played_total{expiry} = %v", v)
	}
	if v, _ := sampleValue(fams["pg_task_focus_alerts_played_total"], map[string]string{"kind": "reminder"}); v != 1 {
		t.Errorf("alerts_played_total{reminder} = %v", v)
	}
	var h struct {
		Alerts struct {
			RunningCycle string `json:"running_cycle"`
		}
	}
	e.get("/healthz").json(t, &h)
	if h.Alerts.RunningCycle != c {
		t.Errorf("/healthz alerts.running_cycle = %q, want %q", h.Alerts.RunningCycle, c)
	}
}

// A boost that ends overtime means entering overtime again plays the expiry again.
func TestBoostOutOfOvertimeReArmsTheExpiry(t *testing.T) {
	e := newEnv(t, options{})
	e.bootstrap()
	e.clock.Set(local(9, 0))
	e.startCycle("deep-work")
	e.pollAt(local(9, 51))
	e.waitSounds(1)
	e.clock.Set(local(9, 52))
	e.ok("/api/v1/cycles/boost", map[string]any{"minutes": 25}) // the commit polls: the anchor is unset
	e.pollAt(local(10, 14))
	e.settle()
	if len(e.player.Played()) != 1 {
		t.Fatalf("still within the boosted time: %v", e.player.Played())
	}
	e.pollAt(local(10, 15)) // 50 + 25 minutes of running time
	if got := e.waitSounds(2); got[1] != "Hero" {
		t.Fatalf("the expiry plays again: %v", got)
	}
}

// "no sound" is a setting: the notification is still sent and the reminders still repeat.
func TestNoSoundStillNotifies(t *testing.T) {
	e := newEnv(t, options{editConfig: func(c map[string]any) {
		c["cycles"].(map[string]any)["deep-work"].(map[string]any)["alert"] = map[string]any{"sound": nil, "repeat_minutes": 10}
	}})
	e.bootstrap()
	e.clock.Set(local(9, 0))
	e.startCycle("deep-work")
	e.pollAt(local(9, 50))
	eventually(t, "the notification", func() bool { return len(e.player.Notified()) >= 1 })
	e.pollAt(local(10, 0))
	eventually(t, "the reminder notification", func() bool { return len(e.player.Notified()) >= 2 })
	if got := e.player.Played(); len(got) != 0 {
		t.Fatalf("a null sound played %v", got)
	}
}

// A playback failure is logged and counted and never touches cycle state.
func TestPlaybackFailureIsCountedAndHarmless(t *testing.T) {
	e := newEnv(t, options{})
	e.bootstrap()
	e.clock.Set(local(9, 0))
	c := e.startCycle("deep-work")
	e.player.PlayErr = errors.New("no audio device")
	e.pollAt(local(9, 50))
	eventually(t, "the failure to be counted", func() bool {
		v, _ := sampleValue(e.scrape()["pg_task_focus_alert_failures_total"], nil)
		return v >= 1
	})
	if !strings.Contains(e.log.String(), "sound playback failed") {
		t.Errorf("the failure was not logged:\n%s", e.log.String())
	}
	focus, _ := e.state()["focus"].(map[string]any)
	if focus == nil || focus["id"] != c || focus["status"] != "running" {
		t.Fatalf("a playback failure changed the cycle: %v", focus)
	}
	if len(e.player.Notified()) == 0 {
		t.Errorf("the notification is still sent when the sound fails")
	}
}

// The scheduler is polled after every commit: a restart with a cycle already
// past its time plays at most one catch-up alert, never a burst.
func TestRestartPlaysOneCatchUpAlert(t *testing.T) {
	e := newEnv(t, options{})
	e.bootstrap()
	e.clock.Set(local(9, 0))
	e.startCycle("deep-work")
	e.d.Stop()
	e.clock.Set(local(11, 0)) // 70 minutes into overtime: seven intervals missed
	e.start(nil)
	e.waitSounds(1)
	e.settle()
	if got := e.player.Played(); len(got) != 1 {
		t.Fatalf("a burst of %d alerts after a restart, want one catch-up: %v", len(got), got)
	}
}
