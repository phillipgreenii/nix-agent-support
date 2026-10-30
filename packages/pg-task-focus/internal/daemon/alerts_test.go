package daemon_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/notify"
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
	eventually(e.t, "the banners to be posted", func() bool { return len(e.player.Sounds()) >= n })
	return e.player.Sounds()
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
	if got := e.player.Sounds(); len(got) != 0 {
		t.Fatalf("a sound before the time is up: %v", got)
	}

	e.pollAt(local(9, 50)) // the expiry
	if got := e.waitSounds(1); got[0] != "Hero" {
		t.Fatalf("the expiry sound = %v, want Hero (the type's own)", got)
	}
	e.pollAt(local(9, 52))
	e.settle()
	if len(e.player.Sounds()) != 1 {
		t.Fatalf("no repeat before repeat_minutes: %v", e.player.Sounds())
	}

	// Paused at 9:55 (5 minutes into overtime, 5 of 10 toward the first reminder): silent for any wall-clock time.
	e.clock.Set(local(9, 55))
	e.ok("/api/v1/cycles/pause", map[string]any{"cycle_id": c})
	e.pollAt(local(11, 0))
	e.settle()
	if len(e.player.Sounds()) != 1 {
		t.Fatalf("a paused cycle played a sound: %v", e.player.Sounds())
	}

	// Resumed at 11:00, it reminds after the 5 running minutes that remain, not after 10.
	e.ok("/api/v1/cycles/resume", map[string]any{"cycle_id": c})
	e.pollAt(local(11, 4))
	e.settle()
	if len(e.player.Sounds()) != 1 {
		t.Fatalf("the reminder fell due early: %v", e.player.Sounds())
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
	if len(e.player.Sounds()) != 1 {
		t.Fatalf("still within the boosted time: %v", e.player.Sounds())
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
	for _, n := range e.player.Notified() {
		if n.Sound != "" {
			t.Fatalf("a null sound was sent: %+v", n)
		}
	}
}

// A failed post is logged at Warn and counted once and never touches cycle
// state. The sound used to play through afplay even when the banner failed;
// now one post carries both, so a denied permission silences both (TF-7).
func TestPostFailureIsCountedOnceAndHarmless(t *testing.T) {
	e := newEnv(t, options{})
	e.bootstrap()
	e.clock.Set(local(9, 0))
	c := e.startCycle("deep-work")
	e.player.NotifyErr = errors.New("notifier unreachable")
	e.pollAt(local(9, 50))
	eventually(t, "the failure to be counted", func() bool {
		v, _ := sampleValue(e.scrape()["pg_task_focus_alert_failures_total"], nil)
		return v >= 1
	})
	e.settle()
	if v, _ := sampleValue(e.scrape()["pg_task_focus_alert_failures_total"], nil); v != 1 {
		t.Errorf("alert_failures_total = %v, want 1", v)
	}
	for _, want := range []string{"notification failed", `"kind":"expiry"`, `"cycle_id"`} {
		if !strings.Contains(e.log.String(), want) {
			t.Errorf("the failure log lacks %q:\n%s", want, e.log.String())
		}
	}
	focus, _ := e.state()["focus"].(map[string]any)
	if focus == nil || focus["id"] != c || focus["status"] != "running" {
		t.Fatalf("a post failure changed the cycle: %v", focus)
	}
}

// A panic in the notifier is recovered and counted; the daemon keeps serving.
func TestNotifierPanicIsRecovered(t *testing.T) {
	e := newEnv(t, options{})
	e.bootstrap()
	e.clock.Set(local(9, 0))
	c := e.startCycle("deep-work")
	e.player.Panic = "boom"
	e.pollAt(local(9, 50))
	eventually(t, "the panic to be counted", func() bool {
		v, _ := sampleValue(e.scrape()["pg_task_focus_alert_failures_total"], nil)
		return v >= 1
	})
	if !strings.Contains(e.log.String(), "alert playback panicked") {
		t.Errorf("the panic was not logged:\n%s", e.log.String())
	}
	focus, _ := e.state()["focus"].(map[string]any)
	if focus == nil || focus["id"] != c {
		t.Fatalf("a panic changed the cycle: %v", focus)
	}
}

// Expiry uses Sound and the reminder uses ReminderSound, each with the source
// token; the request carries nothing else the alerter chose (TF-1, TF-5).
func TestExpiryAndReminderUseTheirOwnSounds(t *testing.T) {
	e := newEnv(t, options{editConfig: func(c map[string]any) {
		c["cycles"].(map[string]any)["deep-work"].(map[string]any)["alert"] = map[string]any{"sound": "Hero", "reminder_sound": "Glass", "repeat_minutes": 10}
	}})
	e.bootstrap()
	e.clock.Set(local(9, 0))
	e.startCycle("deep-work")
	e.pollAt(local(9, 50))
	e.waitSounds(1)
	e.pollAt(local(10, 0))
	if got := e.waitSounds(2); got[0] != "Hero" || got[1] != "Glass" {
		t.Fatalf("sounds = %v, want Hero then Glass", got)
	}
	for _, n := range e.player.Notified() {
		if n.Source != "pg-task-focus" {
			t.Errorf("source = %q", n.Source)
		}
	}
}

// A title over the notifier's cap is cut on a rune boundary (TF-12).
func TestOvertimeTitleIsCutToTheContractCap(t *testing.T) {
	long := strings.Repeat("é", 200) // 400 bytes
	e := newEnv(t, options{editConfig: func(c map[string]any) {
		c["cycles"].(map[string]any)["deep-work"].(map[string]any)["title"] = long
	}})
	e.bootstrap()
	e.clock.Set(local(9, 0))
	e.startCycle("deep-work")
	e.pollAt(local(9, 50))
	eventually(t, "the notification", func() bool { return len(e.player.Notified()) >= 1 })
	title := e.player.Notified()[0].Title
	if len(title) != 256 || !utf8.ValidString(title) || title != strings.Repeat("é", 128) {
		t.Errorf("title is %d bytes: %q", len(title), title)
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
	if got := e.player.Sounds(); len(got) != 1 {
		t.Fatalf("a burst of %d alerts after a restart, want one catch-up: %v", len(got), got)
	}
}

// overtimeNotifications runs a deep-work cycle (50 minutes, a reminder every
// 10) with the given note, or none, through its expiry and its first
// reminder, and returns the two notifications.
func overtimeNotifications(t *testing.T, note string) (expiry, reminder notifyRecord) {
	t.Helper()
	e := newEnv(t, options{})
	e.bootstrap()
	e.clock.Set(local(9, 0))
	c := e.startCycle("deep-work")
	if note != "" {
		e.ok("/api/v1/cycles/annotate", map[string]any{"cycle_id": c, "note": note})
	}
	e.pollAt(local(9, 50))
	eventually(t, "the expiry notification", func() bool { return len(e.player.Notified()) >= 1 })
	e.pollAt(local(10, 0))
	eventually(t, "the reminder notification", func() bool { return len(e.player.Notified()) >= 2 })
	n := e.player.Notified()
	return n[0], n[1]
}

type notifyRecord = notify.Notification

// A notification names the cycle and, when it has one, the note: it says what
// the operator is over on.
func TestOvertimeNotificationsNameTheCycleAndItsNote(t *testing.T) {
	for _, tc := range []struct {
		name, note, wantExpiry, wantReminder string
	}{
		{"a note", "Reviewed the plan", "Deep work cycle: Reviewed the plan. Time is up.", "Deep work cycle: Reviewed the plan. 10 min over"},
		{"no note", "", "Deep work cycle. Time is up.", "Deep work cycle. 10 min over"},
		{"a note ending in a period", "Reviewed the plan.", "Deep work cycle: Reviewed the plan. Time is up.", "Deep work cycle: Reviewed the plan. 10 min over"},
		{"a multi-line note", "line one\n\n  line two", "Deep work cycle: line one line two. Time is up.", "Deep work cycle: line one line two. 10 min over"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expiry, reminder := overtimeNotifications(t, tc.note)
			if expiry.Title != "Deep work cycle" || expiry.Body != tc.wantExpiry {
				t.Errorf("expiry = %+v, want body %q", expiry, tc.wantExpiry)
			}
			if reminder.Title != "Deep work cycle" || reminder.Body != tc.wantReminder {
				t.Errorf("reminder = %+v, want body %q", reminder, tc.wantReminder)
			}
		})
	}
}

// A long note is cut to a bounded body that ends in the truncation marker
// where the note was cut, and a note that fits is left whole.
func TestOvertimeNotificationBoundsALongNote(t *testing.T) {
	long := strings.Repeat("word ", 200)
	expiry, reminder := overtimeNotifications(t, long)
	for _, n := range []notifyRecord{expiry, reminder} {
		if got := utf8.RuneCountInString(n.Body); got > 200 {
			t.Errorf("the body is %d runes, want at most 200: %q", got, n.Body)
		}
		if !strings.HasPrefix(n.Body, "Deep work cycle: word word") || !strings.Contains(n.Body, "…. ") {
			t.Errorf("the body does not show the cut note and its marker: %q", n.Body)
		}
	}
	if !strings.HasSuffix(expiry.Body, "…. Time is up.") || !strings.HasSuffix(reminder.Body, "…. 10 min over") {
		t.Errorf("the status must follow the cut note: %q, %q", expiry.Body, reminder.Body)
	}

	exact := strings.Repeat("n", 100)
	expiry, _ = overtimeNotifications(t, exact)
	if want := "Deep work cycle: " + exact + ". Time is up."; expiry.Body != want {
		t.Errorf("a note of exactly the limit was cut: %q", expiry.Body)
	}
}

// A note with quotes, backslashes and AppleScript syntax reaches the notifier
// as the plain text it is: the notifier daemon receives it as data.
func TestOvertimeNotificationKeepsQuotesInTheNote(t *testing.T) {
	note := `say "hi" \ 'x' & do shell script "id"`
	expiry, reminder := overtimeNotifications(t, note)
	if want := "Deep work cycle: " + note + ". Time is up."; expiry.Body != want {
		t.Errorf("expiry body = %q, want %q", expiry.Body, want)
	}
	if want := "Deep work cycle: " + note + ". 10 min over"; reminder.Body != want {
		t.Errorf("reminder body = %q, want %q", reminder.Body, want)
	}
}

// With the notifier stuck, the 16-entry queue drops further alerts with a Warn
// (TF-8); the drop names the kind and the cycle.
func TestFullAlertQueueDropsWithAWarn(t *testing.T) {
	e := newEnv(t, options{})
	e.bootstrap()
	e.clock.Set(local(9, 0))
	e.startCycle("deep-work")
	e.player.Block = make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-e.player.Block:
		default:
			close(e.player.Block)
		}
	})
	for k := 0; k < 22; k++ { // one alert per repeat interval: 1 in flight + 16 queued + drops
		e.pollAt(local(9, 50).Add(time.Duration(k) * 10 * time.Minute))
		e.d.Alerter().Poll()
		time.Sleep(5 * time.Millisecond)
	}
	if !strings.Contains(e.log.String(), "alert queue full") || !strings.Contains(e.log.String(), `"cycle_id"`) {
		t.Errorf("no queue-full Warn:\n%s", e.log.String())
	}
}
