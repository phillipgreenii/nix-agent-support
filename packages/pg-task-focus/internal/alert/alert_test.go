package alert_test

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/alert"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/projection"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/testutil"
)

// The tests build their logs from payload structs, pass every event through
// the real encoder and decoder, and replay them. A scheduler is polled with the
// log as it was recorded at the read instant, as the daemon would see it, and
// the read instant is always a plain time.Time: nothing sleeps.
//
// In the fixture configuration, deep-work cycles are 50 minutes and remind
// every 10 with the sound "Hero" and the default reminder sound "Tink";
// notifications cycles are 25 minutes and take defaults.alert whole: "Glass",
// then "Tink" every 5 minutes.

const (
	cycleA = event.CycleID("01J9ZZZZZZZZZZZZZZZZZZZZZA")
	cycleB = event.CycleID("01J9ZZZZZZZZZZZZZZZZZZZZZB")
)

// hm is the instant h:m on 2026-10-07, in UTC.
func hm(h, m int) time.Time { return time.Date(2026, time.October, 7, h, m, 0, 0, time.UTC) }

// logb builds a log in the order its events are recorded.
type logb struct {
	t      *testing.T
	events []event.Event
	ids    uint32
	models map[int]*projection.Model
}

func newLog(t *testing.T) *logb {
	t.Helper()
	return &logb{t: t, models: map[int]*projection.Model{}}
}

func (b *logb) id() event.ID {
	b.ids++
	var entropy [10]byte
	binary.BigEndian.PutUint32(entropy[6:], b.ids)
	return event.NewID(hm(0, 0), bytes.NewReader(entropy[:]))
}

// addAt appends p, recorded at recorded and effective at effective.
func (b *logb) addAt(recorded, effective time.Time, p event.Payload) event.ID {
	b.t.Helper()
	line, err := event.Encode(event.Event{
		Envelope: event.Envelope{V: event.SchemaVersion, ID: b.id(), At: event.At(recorded), EffectiveAt: event.At(effective), Type: p.EventType()},
		Payload:  p,
	})
	if err != nil {
		b.t.Fatalf("Encode %T: %v", p, err)
	}
	e, err := event.Decode(line)
	if err != nil {
		b.t.Fatalf("Decode %T: %v", p, err)
	}
	e.Line = len(b.events) + 1
	b.events = append(b.events, e)
	return e.ID
}

func (b *logb) add(when time.Time, p event.Payload) event.ID { return b.addAt(when, when, p) }

func (b *logb) start(when time.Time, id event.CycleID, typ string, planned int) {
	b.add(when, event.CycleStarted{CycleID: id, Type: typ, Title: "Snapshot " + typ, PlannedMinutes: planned})
}

func (b *logb) pause(when time.Time, id event.CycleID) { b.add(when, event.CyclePaused{CycleID: id}) }

func (b *logb) resume(when time.Time, id event.CycleID) { b.add(when, event.CycleResumed{CycleID: id}) }

func (b *logb) stop(when time.Time, id event.CycleID) event.ID {
	return b.add(when, event.CycleStopped{CycleID: id})
}

func (b *logb) boost(when time.Time, id event.CycleID, minutes int) {
	b.add(when, event.CycleBoosted{CycleID: id, Minutes: minutes})
}

// switchTo pauses from, the running cycle, and resumes to, in one batch.
func (b *logb) switchTo(when time.Time, from, to event.CycleID) {
	bt := b.id()
	b.add(when, event.CyclePaused{CycleID: from, Batch: bt})
	b.add(when, event.CycleResumed{CycleID: to, Batch: bt})
	b.add(when, event.BatchCommitted{Batch: bt})
}

// backfill records at recorded a break of cycle id from from to to.
func (b *logb) backfill(recorded, from, to time.Time, id event.CycleID) {
	bt := b.id()
	b.addAt(recorded, from, event.CyclePaused{CycleID: id, Batch: bt})
	b.addAt(recorded, to, event.CycleResumed{CycleID: id, Batch: bt})
	b.add(recorded, event.BatchCommitted{Batch: bt})
}

// modelAt is the replay of the events recorded at or before now.
func (b *logb) modelAt(now time.Time) *projection.Model {
	b.t.Helper()
	n := 0
	for n < len(b.events) && !b.events[n].At.Time().After(now) {
		n++
	}
	if m, ok := b.models[n]; ok {
		return m
	}
	m, err := projection.Replay(b.events[:n:n])
	if err != nil {
		b.t.Fatalf("Replay of the log at %s: %v", now.Format(time.RFC3339), err)
	}
	b.models[n] = m
	return m
}

// commits lists the instants events were recorded at, once each, in order.
func (b *logb) commits() []time.Time {
	var out []time.Time
	for _, e := range b.events {
		if t := e.At.Time(); len(out) == 0 || !out[len(out)-1].Equal(t) {
			out = append(out, t)
		}
	}
	return out
}

// withoutDeepWork removes the deep-work cycle type from the configuration.
func withoutDeepWork(c map[string]any) {
	delete(c["cycles"].(map[string]any), "deep-work")
	for _, p := range c["profiles"].(map[string]any) {
		p.(map[string]any)["cycles"] = []any{"notifications", "review", "page-response"}
	}
}

// heard is an alert and the instant the poll that returned it ran at.
type heard struct {
	at time.Time
	alert.Alert
}

// String is "15:04 kind X", X the last letter of the cycle id.
func (h heard) String() string {
	return fmt.Sprintf("%s %s %s", h.at.Format("15:04"), h.Kind, h.CycleID[len(h.CycleID)-1:])
}

func lines(hs []heard) []string {
	out := []string{}
	for _, h := range hs {
		out = append(out, h.String())
	}
	return out
}

func poll(s *alert.Scheduler, b *logb, cfg *config.Config, now time.Time) *alert.Alert {
	return s.Poll(b.modelAt(now), cfg, now)
}

func nextAt(s *alert.Scheduler, b *logb, cfg *config.Config, now time.Time) (time.Time, bool) {
	return s.NextAt(b.modelAt(now), cfg, now)
}

// pollEveryMinute polls at every whole minute from from to to, both included.
func pollEveryMinute(s *alert.Scheduler, b *logb, cfg *config.Config, from, to time.Time) []heard {
	var out []heard
	for now := from; !now.After(to); now = now.Add(time.Minute) {
		if a := poll(s, b, cfg, now); a != nil {
			out = append(out, heard{now, *a})
		}
	}
	return out
}

// drive polls as the daemon does: at every instant the log changed, and at the
// instant NextAt names after each poll, from from to to.
func drive(t *testing.T, s *alert.Scheduler, b *logb, cfg *config.Config, from, to time.Time) []heard {
	t.Helper()
	commits := b.commits()
	var out []heard
	for now := from; !now.After(to); {
		if a := poll(s, b, cfg, now); a != nil {
			out = append(out, heard{now, *a})
		}
		next, ok := nextAt(s, b, cfg, now)
		if ok && !next.After(now) {
			t.Fatalf("NextAt(%s) = %s right after a poll: not later than the poll", now.Format("15:04:05"), next.Format("15:04:05"))
		}
		for _, c := range commits {
			if c.After(now) {
				if !ok || c.Before(next) {
					next, ok = c, true
				}
				break
			}
		}
		if !ok {
			break
		}
		now = next
	}
	return out
}

// assertHeard checks the alerts against "15:04 kind X" lines.
func assertHeard(t *testing.T, got []heard, want ...string) {
	t.Helper()
	if want == nil {
		want = []string{}
	}
	if g := lines(got); !slices.Equal(g, want) {
		t.Errorf("alerts\n got  %q\n want %q", g, want)
	}
}

// assertBothWays runs a fresh scheduler over the log twice, polling every
// minute and driven by NextAt, and checks both against want.
func assertBothWays(t *testing.T, b *logb, cfg *config.Config, from, to time.Time, want ...string) {
	t.Helper()
	t.Run("polled every minute", func(t *testing.T) {
		assertHeard(t, pollEveryMinute(alert.NewScheduler(), b, cfg, from, to), want...)
	})
	t.Run("driven by NextAt and the commits", func(t *testing.T) {
		assertHeard(t, drive(t, alert.NewScheduler(), b, cfg, from, to), want...)
	})
}

func mustCycle(t *testing.T, b *logb, now time.Time, id event.CycleID) projection.Cycle {
	t.Helper()
	c, ok := b.modelAt(now).Cycle(id)
	if !ok {
		t.Fatalf("cycle %s not in the log at %s", id, now)
	}
	return c
}

// overAt is how far cycle id has run past its time at now, read from the
// model: what a client shows beside an alert.
func overAt(t *testing.T, b *logb, now time.Time, id event.CycleID) time.Duration {
	t.Helper()
	return -mustCycle(t, b, now, id).Remaining(now)
}

// notificationsLog is a 25-minute notifications cycle started at 09:00: time
// is up at 09:25 and it reminds every 5 minutes.
func notificationsLog(t *testing.T) *logb {
	b := newLog(t)
	b.start(hm(9, 0), cycleA, "notifications", 25)
	return b
}

// deepWorkLog is a 50-minute deep-work cycle started at 09:00: time is up at
// 09:50 and it reminds every 10 minutes.
func deepWorkLog(t *testing.T) *logb {
	b := newLog(t)
	b.start(hm(9, 0), cycleA, "deep-work", 50)
	return b
}

func TestNoAlertBeforeTimeUp(t *testing.T) {
	b, cfg, s := notificationsLog(t), testutil.LoadConfig(t, nil), alert.NewScheduler()
	assertHeard(t, pollEveryMinute(s, b, cfg, hm(9, 0), hm(9, 24)))
	if a := poll(s, b, cfg, hm(9, 25).Add(-time.Millisecond)); a != nil {
		t.Errorf("Poll a millisecond before time-up = %+v, want none", *a)
	}
	if got, ok := nextAt(s, b, cfg, hm(9, 10)); !ok || !got.Equal(hm(9, 25)) {
		t.Errorf("NextAt(09:10) = %s, %v, want 09:25 (the time-up instant)", got, ok)
	}
}

func TestExpiryOnceAtTimeUp(t *testing.T) {
	b, cfg, s := notificationsLog(t), testutil.LoadConfig(t, nil), alert.NewScheduler()
	assertHeard(t, pollEveryMinute(s, b, cfg, hm(9, 0), hm(9, 24)))

	c := mustCycle(t, b, hm(9, 25), cycleA)
	if !c.TimeUp(hm(9, 25)) || c.Overtime(hm(9, 25)) {
		t.Fatalf("at 09:25 TimeUp = %v and Overtime = %v, want true and false", c.TimeUp(hm(9, 25)), c.Overtime(hm(9, 25)))
	}
	a := poll(s, b, cfg, hm(9, 25))
	want := alert.Alert{Kind: alert.Expiry, CycleID: cycleA, Sound: "Glass"}
	if a == nil || *a != want {
		t.Fatalf("Poll(09:25) = %+v, want %+v", a, want)
	}
	// What a client shows beside the sound, it reads from the model by id.
	if c := mustCycle(t, b, hm(9, 25), a.CycleID); c.Title != "Snapshot notifications" || c.Remaining(hm(9, 25)) != 0 {
		t.Errorf("the alerting cycle reads title %q and remaining %s, want the snapshot title and 0", c.Title, c.Remaining(hm(9, 25)))
	}
	if a := poll(s, b, cfg, hm(9, 25)); a != nil {
		t.Errorf("a second Poll at 09:25 = %+v, want none: the expiry plays once", *a)
	}
	if a := poll(s, b, cfg, hm(9, 25).Add(30*time.Second)); a != nil {
		t.Errorf("Poll(09:25:30) = %+v, want none", *a)
	}
	assertHeard(t, pollEveryMinute(s, b, cfg, hm(9, 26), hm(9, 29)))
}

func TestRemindersEveryRepeatMinutesOfRunningTime(t *testing.T) {
	type want struct {
		at    time.Time
		kind  alert.Kind
		sound string
		over  time.Duration // read from the model at the alert's instant
	}
	tests := []struct {
		name  string
		log   func(*testing.T) *logb
		until time.Time
		want  []want
	}{
		{
			name: "notifications, every 5 minutes", log: notificationsLog, until: hm(9, 39),
			want: []want{
				{hm(9, 25), alert.Expiry, "Glass", 0},
				{hm(9, 30), alert.Reminder, "Tink", 5 * time.Minute},
				{hm(9, 35), alert.Reminder, "Tink", 10 * time.Minute},
			},
		},
		{
			name: "deep work, every 10 minutes", log: deepWorkLog, until: hm(10, 19),
			want: []want{
				{hm(9, 50), alert.Expiry, "Hero", 0},
				{hm(10, 0), alert.Reminder, "Tink", 10 * time.Minute},
				{hm(10, 10), alert.Reminder, "Tink", 20 * time.Minute},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := tt.log(t)
			got := pollEveryMinute(alert.NewScheduler(), b, testutil.LoadConfig(t, nil), hm(9, 0), tt.until)
			if len(got) != len(tt.want) {
				t.Fatalf("alerts %q, want %d", lines(got), len(tt.want))
			}
			for i, w := range tt.want {
				g := got[i]
				over := overAt(t, b, g.at, g.CycleID)
				if !g.at.Equal(w.at) || g.Kind != w.kind || g.Sound != w.sound || over != w.over || g.CycleID != cycleA {
					t.Errorf("alert %d = %s %+v over %s, want %s %+v", i, g.at.Format("15:04"), g.Alert, over, w.at.Format("15:04"), w)
				}
			}
		})
	}
}

func TestRemindersContinueIndefinitely(t *testing.T) {
	b, cfg, s := notificationsLog(t), testutil.LoadConfig(t, nil), alert.NewScheduler()
	expiry, end := hm(9, 25), hm(9, 25).Add(6*time.Hour)
	got := drive(t, s, b, cfg, hm(9, 0), end)
	// One expiry, then one reminder per 5 minutes over 6 hours: 6*60/5 = 72.
	if len(got) != 73 {
		t.Fatalf("got %d alerts over 6 hours of overtime, want 1 expiry and 72 reminders", len(got))
	}
	for i, h := range got {
		wantAt, wantKind := expiry.Add(time.Duration(i)*5*time.Minute), alert.Reminder
		if i == 0 {
			wantKind = alert.Expiry
		}
		if !h.at.Equal(wantAt) || h.Kind != wantKind || overAt(t, b, h.at, h.CycleID) != h.at.Sub(expiry) {
			t.Fatalf("alert %d = %s %+v, want %s at %s", i, h.at.Format("15:04"), h.Alert, wantKind, wantAt.Format("15:04"))
		}
	}
	if next, ok := nextAt(s, b, cfg, end); !ok || !next.Equal(end.Add(5*time.Minute)) {
		t.Errorf("after 6 hours NextAt = %s, %v, want a further reminder at %s: there is no cap", next, ok, end.Add(5*time.Minute))
	}
}

// pausedDeepWorkLog is a deep-work cycle started at 09:00 that expires at
// 09:50 and reminds at 10:00; it is paused at 10:05, half-way through its next
// interval, and resumed at 10:30.
func pausedDeepWorkLog(t *testing.T) *logb {
	b := deepWorkLog(t)
	b.pause(hm(10, 5), cycleA)
	b.resume(hm(10, 30), cycleA)
	return b
}

func TestPauseSilencesAndResumeContinuesTheCount(t *testing.T) {
	b, cfg := pausedDeepWorkLog(t), testutil.LoadConfig(t, nil)
	// Five running minutes were left of the interval at the pause, so the next
	// reminder is five minutes after the resume, at 10:35, never 10:40.
	assertBothWays(t, b, cfg, hm(9, 0), hm(11, 0),
		"09:50 expiry A", "10:00 reminder A", "10:35 reminder A", "10:45 reminder A", "10:55 reminder A")

	s := alert.NewScheduler()
	pollEveryMinute(s, b, cfg, hm(9, 0), hm(10, 4))
	for _, now := range []time.Time{hm(10, 5), hm(10, 15), hm(10, 20), hm(10, 29)} {
		if a := poll(s, b, cfg, now); a != nil {
			t.Errorf("Poll(%s) while paused = %+v, want none", now.Format("15:04"), *a)
		}
		if next, ok := nextAt(s, b, cfg, now); ok {
			t.Errorf("NextAt(%s) while paused = %s, want none", now.Format("15:04"), next)
		}
	}
	if a := poll(s, b, cfg, hm(10, 30)); a != nil {
		t.Errorf("Poll(10:30) at the resume = %+v, want none: a resume never replays the expiry", *a)
	}
	if next, ok := nextAt(s, b, cfg, hm(10, 30)); !ok || !next.Equal(hm(10, 35)) {
		t.Errorf("NextAt(10:30) = %s, %v, want 10:35", next, ok)
	}

	t.Run("a pause at the instant time is up holds the expiry until the resume", func(t *testing.T) {
		b := notificationsLog(t)
		b.pause(hm(9, 25), cycleA)
		b.resume(hm(9, 40), cycleA)
		assertBothWays(t, b, cfg, hm(9, 0), hm(9, 50), "09:40 expiry A", "09:45 reminder A", "09:50 reminder A")
	})

	t.Run("a pause at the instant a reminder falls due holds it until the resume", func(t *testing.T) {
		b := notificationsLog(t)
		b.pause(hm(9, 40), cycleA)
		b.resume(hm(10, 0), cycleA)
		assertBothWays(t, b, cfg, hm(9, 0), hm(10, 5),
			"09:25 expiry A", "09:30 reminder A", "09:35 reminder A", "10:00 reminder A", "10:05 reminder A")
	})
}

func TestPauseBeforeTimeUpMovesExpiryByThePausedSpan(t *testing.T) {
	b := deepWorkLog(t)
	b.pause(hm(9, 20), cycleA)
	b.resume(hm(9, 30), cycleA)
	assertBothWays(t, b, testutil.LoadConfig(t, nil), hm(9, 0), hm(10, 5), "10:00 expiry A")
}

func TestBackfilledBreakInOvertimeDoesNotSilenceReminders(t *testing.T) {
	b := newLog(t)
	b.start(hm(11, 0), cycleA, "deep-work", 50)
	// At 13:12 a break from 12:00 to 13:00 is back-filled: the cycle has then
	// run 72 minutes, 22 of them in overtime, against 130 when it last played.
	b.backfill(hm(13, 12), hm(12, 0), hm(13, 0), cycleA)
	cfg := testutil.LoadConfig(t, nil)

	assertBothWays(t, b, cfg, hm(11, 0), hm(13, 30),
		"11:50 expiry A", "12:00 reminder A", "12:10 reminder A", "12:20 reminder A", "12:30 reminder A",
		"12:40 reminder A", "12:50 reminder A", "13:00 reminder A", "13:10 reminder A",
		"13:20 reminder A", "13:30 reminder A")

	s := alert.NewScheduler()
	pollEveryMinute(s, b, cfg, hm(11, 0), hm(13, 11))
	if a := poll(s, b, cfg, hm(13, 12)); a != nil {
		t.Errorf("Poll(13:12) at the back-fill = %+v, want none: the reset is silent", *a)
	}
	if next, ok := nextAt(s, b, cfg, hm(13, 12)); !ok || !next.Equal(hm(13, 20)) {
		t.Errorf("NextAt(13:12) = %s, %v, want 13:20 (over 30 on the grid), not 14:20", next, ok)
	}
}

func TestCycleFirstSeenLaterInOvertimePlaysTheExpiry(t *testing.T) {
	b := newLog(t)
	// At 10:00 a start is back-dated to 08:00: the 25-minute cycle is already
	// 95 minutes into overtime the first time the scheduler sees it.
	b.addAt(hm(10, 0), hm(8, 0), event.CycleStarted{CycleID: cycleA, Type: "notifications", Title: "Late entry", PlannedMinutes: 25})
	cfg := testutil.LoadConfig(t, nil)

	s := alert.NewScheduler()
	if a := poll(s, b, cfg, hm(9, 0)); a != nil {
		t.Fatalf("Poll(09:00) of an empty log = %+v", *a)
	}
	a := poll(s, b, cfg, hm(10, 0))
	if a == nil || a.Kind != alert.Expiry || a.Sound != "Glass" || overAt(t, b, hm(10, 0), a.CycleID) != 95*time.Minute {
		t.Fatalf("Poll(10:00) = %+v, want the expiry, 95 minutes over", a)
	}
	assertHeard(t, pollEveryMinute(s, b, cfg, hm(10, 1), hm(10, 10)), "10:05 reminder A", "10:10 reminder A")
}

func TestBoostOutOfOvertimeThenExpiresAgain(t *testing.T) {
	// The cycle expires at 09:25 and reminds at 09:30; a 10-minute boost at
	// 09:32 leaves 3 minutes, so time is up again at 09:35.
	b := notificationsLog(t)
	b.boost(hm(9, 32), cycleA, 10)
	cfg := testutil.LoadConfig(t, nil)

	assertBothWays(t, b, cfg, hm(9, 0), hm(9, 45),
		"09:25 expiry A", "09:30 reminder A", "09:35 expiry A", "09:40 reminder A", "09:45 reminder A")

	// The daemon polls after the boost commit and then not again until the
	// time-up NextAt names: that one poll is what unsets the anchor, and the
	// daemon contract requires it.
	t.Run("no poll between the boost commit and the next time-up", func(t *testing.T) {
		s := alert.NewScheduler()
		assertHeard(t, pollEveryMinute(s, b, cfg, hm(9, 0), hm(9, 31)), "09:25 expiry A", "09:30 reminder A")
		if a := poll(s, b, cfg, hm(9, 32)); a != nil {
			t.Fatalf("Poll(09:32) at the boost = %+v, want none", *a)
		}
		next, ok := nextAt(s, b, cfg, hm(9, 32))
		if !ok || !next.Equal(hm(9, 35)) {
			t.Fatalf("NextAt(09:32) = %s, %v, want 09:35", next, ok)
		}
		if a := poll(s, b, cfg, next); a == nil || a.Kind != alert.Expiry || a.Sound != "Glass" || overAt(t, b, next, a.CycleID) != 0 {
			t.Errorf("Poll(09:35) = %+v, want the expiry again", a)
		}
	})
}

func TestBoostInsideOvertimeKeepsTheCadence(t *testing.T) {
	// A reminder plays at 10:50, 60 minutes over; a boost at 10:53, 63
	// minutes over, leaves the cycle in overtime whatever its size.
	for _, minutes := range []int{1, 25, 62} {
		t.Run(fmt.Sprintf("boost of %d minutes", minutes), func(t *testing.T) {
			b := deepWorkLog(t)
			b.boost(hm(10, 53), cycleA, minutes)
			cfg := testutil.LoadConfig(t, nil)
			assertBothWays(t, b, cfg, hm(9, 0), hm(11, 10),
				"09:50 expiry A", "10:00 reminder A", "10:10 reminder A", "10:20 reminder A", "10:30 reminder A",
				"10:40 reminder A", "10:50 reminder A", "11:00 reminder A", "11:10 reminder A")

			s := alert.NewScheduler()
			pollEveryMinute(s, b, cfg, hm(9, 0), hm(10, 53))
			if next, ok := nextAt(s, b, cfg, hm(10, 53)); !ok || !next.Equal(hm(11, 0)) {
				t.Errorf("NextAt(10:53) after the boost = %s, %v, want 11:00, 7 minutes later", next, ok)
			}
			if a := poll(s, b, cfg, hm(11, 0)); a == nil || a.Kind != alert.Reminder || overAt(t, b, hm(11, 0), a.CycleID) != time.Duration(70-minutes)*time.Minute {
				t.Errorf("Poll(11:00) = %+v, want a reminder %d minutes over", a, 70-minutes)
			}
		})
	}
}

func TestNextAtAfterABoostWithoutPoll(t *testing.T) {
	// The cycle expires at 09:25 and reminds at 09:30; at 09:32, 7 minutes
	// over, NextAt is read straight after a boost commit, with no Poll.
	tests := []struct {
		name    string
		minutes int
		want    time.Time
	}{
		{"a boost that ends overtime gives the time-up instant", 20, hm(9, 45)},
		{"a boost that leaves overtime keeps the reminder instant", 2, hm(9, 35)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, cfg, s := notificationsLog(t), testutil.LoadConfig(t, nil), alert.NewScheduler()
			pollEveryMinute(s, b, cfg, hm(9, 0), hm(9, 31))
			if before, ok := nextAt(s, b, cfg, hm(9, 32)); !ok || !before.Equal(hm(9, 35)) {
				t.Fatalf("NextAt(09:32) before the boost = %s, %v, want 09:35", before, ok)
			}
			b.boost(hm(9, 32), cycleA, tt.minutes)
			if got, ok := nextAt(s, b, cfg, hm(9, 32)); !ok || !got.Equal(tt.want) {
				t.Errorf("NextAt(09:32) after the boost = %s, %v, want %s", got, ok, tt.want.Format("15:04"))
			}
		})
	}
}

func TestSleepYieldsOneCatchUp(t *testing.T) {
	cfg := testutil.LoadConfig(t, nil)

	t.Run("over a reminder", func(t *testing.T) {
		b, s := deepWorkLog(t), alert.NewScheduler()
		assertHeard(t, pollEveryMinute(s, b, cfg, hm(9, 0), hm(10, 1)), "09:50 expiry A", "10:00 reminder A")
		// Asleep from 10:01 to 12:01, over twelve reminder instants. Before
		// the catch-up poll the reminder is overdue, and NextAt says now.
		if next, ok := nextAt(s, b, cfg, hm(12, 1)); !ok || !next.Equal(hm(12, 1)) {
			t.Errorf("NextAt(12:01) before the catch-up poll = %s, %v, want 12:01, never earlier than now", next, ok)
		}
		a := poll(s, b, cfg, hm(12, 1))
		if a == nil || a.Kind != alert.Reminder || overAt(t, b, hm(12, 1), a.CycleID) != 131*time.Minute {
			t.Fatalf("Poll(12:01) after the sleep = %+v, want one reminder, 131 minutes over", a)
		}
		if a := poll(s, b, cfg, hm(12, 1)); a != nil {
			t.Errorf("a second Poll at 12:01 = %+v, want none: one catch-up, never a burst", *a)
		}
		if next, ok := nextAt(s, b, cfg, hm(12, 1)); !ok || !next.Equal(hm(12, 11)) {
			t.Errorf("NextAt(12:01) = %s, %v, want 12:11", next, ok)
		}
		assertHeard(t, pollEveryMinute(s, b, cfg, hm(12, 2), hm(12, 21)), "12:11 reminder A", "12:21 reminder A")
	})

	t.Run("over the expiry", func(t *testing.T) {
		b, s := deepWorkLog(t), alert.NewScheduler()
		assertHeard(t, pollEveryMinute(s, b, cfg, hm(9, 0), hm(9, 40)))
		a := poll(s, b, cfg, hm(11, 40))
		if a == nil || a.Kind != alert.Expiry || a.Sound != "Hero" || overAt(t, b, hm(11, 40), a.CycleID) != 110*time.Minute {
			t.Fatalf("Poll(11:40) after the sleep = %+v, want the expiry, 110 minutes over", a)
		}
		assertHeard(t, pollEveryMinute(s, b, cfg, hm(11, 40), hm(11, 50)), "11:50 reminder A")
	})
}

func TestFreshSchedulerFirstPoll(t *testing.T) {
	cfg := testutil.LoadConfig(t, nil)

	// The deep-work cycle started at 09:00 is time-up at 09:50 and its grid of
	// reminders is 10:00, 10:10 and so on.
	running := []struct {
		name  string
		first time.Time
		want  *alert.Kind
		next  time.Time
	}{
		{"not time-up plays nothing", hm(9, 30), nil, hm(9, 50)},
		{"exactly at time-up plays the expiry", hm(9, 50), testutil.Ptr(alert.Expiry), hm(10, 0)},
		{"inside the first interval plays the expiry", hm(9, 55), testutil.Ptr(alert.Expiry), hm(10, 0)},
		{"at the end of the first interval plays a reminder", hm(10, 0), testutil.Ptr(alert.Reminder), hm(10, 10)},
		{"later in overtime plays one reminder on the grid", hm(10, 13), testutil.Ptr(alert.Reminder), hm(10, 20)},
	}
	for _, tt := range running {
		t.Run("running: "+tt.name, func(t *testing.T) {
			b, s := deepWorkLog(t), alert.NewScheduler()
			a := poll(s, b, cfg, tt.first)
			switch {
			case tt.want == nil && a != nil:
				t.Errorf("first Poll(%s) = %+v, want none", tt.first.Format("15:04"), *a)
			case tt.want != nil && (a == nil || a.Kind != *tt.want):
				t.Errorf("first Poll(%s) = %+v, want %s", tt.first.Format("15:04"), a, *tt.want)
			}
			if a := poll(s, b, cfg, tt.first); a != nil {
				t.Errorf("a second Poll(%s) = %+v, want none", tt.first.Format("15:04"), *a)
			}
			if next, ok := nextAt(s, b, cfg, tt.first); !ok || !next.Equal(tt.next) {
				t.Errorf("NextAt(%s) = %s, %v, want %s", tt.first.Format("15:04"), next, ok, tt.next.Format("15:04"))
			}
			if a := poll(s, b, cfg, tt.next); a == nil {
				t.Errorf("Poll(%s) = none, want the next alert", tt.next.Format("15:04"))
			}
		})
	}

	t.Run("paused in overtime plays nothing and resumes on the grid", func(t *testing.T) {
		// The restart happens during the 10:05 to 10:30 pause.
		b, s := pausedDeepWorkLog(t), alert.NewScheduler()
		if a := poll(s, b, cfg, hm(10, 20)); a != nil {
			t.Errorf("first Poll(10:20) of a paused cycle = %+v, want none", *a)
		}
		if next, ok := nextAt(s, b, cfg, hm(10, 20)); ok {
			t.Errorf("NextAt(10:20) of a paused cycle = %s, want none", next)
		}
		assertHeard(t, pollEveryMinute(s, b, cfg, hm(10, 21), hm(11, 0)), "10:35 reminder A", "10:45 reminder A", "10:55 reminder A")
	})
}

func TestSwitchedAwayCycleIsSilentAndSwitchingBackContinuesItsCount(t *testing.T) {
	b := newLog(t)
	// B, a notifications cycle, ran 10 of its 25 minutes before 09:00.
	b.start(hm(8, 30), cycleB, "notifications", 25)
	b.pause(hm(8, 40), cycleB)
	// A, deep work, expires at 09:50 and reminds at 10:00; it has run 65
	// minutes when it is switched away for B at 10:05.
	b.start(hm(9, 0), cycleA, "deep-work", 50)
	b.switchTo(hm(10, 5), cycleA, cycleB)
	// B is time-up at 10:20 and reminds at 10:25 and 10:30; it has run 37
	// minutes when A comes back at 10:32, 5 running minutes short of its next
	// reminder at 70, so A reminds at 10:37 and 10:47.
	b.switchTo(hm(10, 32), cycleB, cycleA)
	// Back to B at 10:50, 3 running minutes short of its next reminder at 40.
	b.switchTo(hm(10, 50), cycleA, cycleB)

	assertBothWays(t, b, testutil.LoadConfig(t, nil), hm(8, 30), hm(11, 0),
		"09:50 expiry A", "10:00 reminder A",
		"10:20 expiry B", "10:25 reminder B", "10:30 reminder B",
		"10:37 reminder A", "10:47 reminder A",
		"10:53 reminder B", "10:58 reminder B")
}

func TestPerTypeAlertOverridesAndFallbackToDefaults(t *testing.T) {
	tests := []struct {
		name           string
		edit           func(c map[string]any)
		typ            string
		planned        int
		expiry, remind string
		repeat         time.Duration
	}{
		{"a type with its own sound and repeat takes the default reminder sound", nil, "deep-work", 50, "Hero", "Tink", 10 * time.Minute},
		{"a type with no alert takes defaults.alert", nil, "notifications", 25, "Glass", "Tink", 5 * time.Minute},
		{
			"a type with its own reminder sound", func(c map[string]any) {
				dw := c["cycles"].(map[string]any)["deep-work"].(map[string]any)
				dw["alert"].(map[string]any)["reminder_sound"] = "Ping"
			}, "deep-work", 50, "Hero", "Ping", 10 * time.Minute,
		},
		{
			"no reminder sound anywhere reminds with the resolved sound", func(c map[string]any) {
				delete(c["defaults"].(map[string]any)["alert"].(map[string]any), "reminder_sound")
			}, "deep-work", 50, "Hero", "Hero", 10 * time.Minute,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := newLog(t)
			b.start(hm(9, 0), cycleA, tt.typ, tt.planned)
			deadline := hm(9, 0).Add(time.Duration(tt.planned) * time.Minute)
			got := drive(t, alert.NewScheduler(), b, testutil.LoadConfig(t, tt.edit), hm(9, 0), deadline.Add(2*tt.repeat))
			want := []heard{
				{deadline, alert.Alert{Kind: alert.Expiry, Sound: tt.expiry}},
				{deadline.Add(tt.repeat), alert.Alert{Kind: alert.Reminder, Sound: tt.remind}},
				{deadline.Add(2 * tt.repeat), alert.Alert{Kind: alert.Reminder, Sound: tt.remind}},
			}
			assertSounds(t, got, want)
		})
	}
}

// assertSounds compares the instant, kind and sound of each alert.
func assertSounds(t *testing.T, got, want []heard) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("alerts %q, want %d", lines(got), len(want))
	}
	for i, w := range want {
		if g := got[i]; !g.at.Equal(w.at) || g.Kind != w.Kind || g.Sound != w.Sound {
			t.Errorf("alert %d = %s %s %q, want %s %s %q", i, g.at.Format("15:04"), g.Kind, g.Sound, w.at.Format("15:04"), w.Kind, w.Sound)
		}
	}
}

func TestRemovedCycleTypeFallsBackToDefaults(t *testing.T) {
	removed := testutil.LoadConfig(t, withoutDeepWork)
	if _, ok := removed.CycleType("deep-work"); ok {
		t.Fatal("the edited configuration still defines deep-work")
	}

	t.Run("from the start", func(t *testing.T) {
		got := drive(t, alert.NewScheduler(), deepWorkLog(t), removed, hm(9, 0), hm(10, 0))
		assertSounds(t, got, []heard{
			{hm(9, 50), alert.Alert{Kind: alert.Expiry, Sound: "Glass"}},
			{hm(9, 55), alert.Alert{Kind: alert.Reminder, Sound: "Tink"}},
			{hm(10, 0), alert.Alert{Kind: alert.Reminder, Sound: "Tink"}},
		})
	})

	t.Run("after a reload mid-overtime", func(t *testing.T) {
		b, s := deepWorkLog(t), alert.NewScheduler()
		assertHeard(t, pollEveryMinute(s, b, testutil.LoadConfig(t, nil), hm(9, 0), hm(10, 1)), "09:50 expiry A", "10:00 reminder A")
		// The reload at 10:02 keeps the anchor at 60 running minutes; the
		// repeat is now the default 5.
		got := pollEveryMinute(s, b, removed, hm(10, 2), hm(10, 10))
		assertSounds(t, got, []heard{
			{hm(10, 5), alert.Alert{Kind: alert.Reminder, Sound: "Tink"}},
			{hm(10, 10), alert.Alert{Kind: alert.Reminder, Sound: "Tink"}},
		})
	})
}

func TestNextAtMatchesPoll(t *testing.T) {
	cfg := testutil.LoadConfig(t, nil)
	tests := []struct {
		name string
		log  func(*testing.T) *logb
		from time.Time
		now  time.Time
	}{
		{"before time-up", notificationsLog, hm(9, 0), hm(9, 10)},
		{"right after the expiry", notificationsLog, hm(9, 0), hm(9, 25)},
		{"inside an interval", notificationsLog, hm(9, 0), hm(9, 27)},
		{"right after a resume half-way through an interval", pausedDeepWorkLog, hm(9, 0), hm(10, 30)},
		{"first poll of a fresh scheduler later in overtime", deepWorkLog, hm(10, 13), hm(10, 13)},
		{"after a boost that ends overtime", func(t *testing.T) *logb {
			b := notificationsLog(t)
			b.boost(hm(9, 32), cycleA, 10)
			return b
		}, hm(9, 0), hm(9, 32)},
		{"after a boost inside overtime", func(t *testing.T) *logb {
			b := notificationsLog(t)
			b.boost(hm(9, 32), cycleA, 2)
			return b
		}, hm(9, 0), hm(9, 32)},
		{"after a back-filled break", func(t *testing.T) *logb {
			b := newLog(t)
			b.start(hm(11, 0), cycleA, "deep-work", 50)
			b.backfill(hm(13, 12), hm(12, 0), hm(13, 0), cycleA)
			return b
		}, hm(11, 0), hm(13, 12)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, s := tt.log(t), alert.NewScheduler()
			pollEveryMinute(s, b, cfg, tt.from, tt.now.Add(-time.Minute))
			poll(s, b, cfg, tt.now)
			next, ok := nextAt(s, b, cfg, tt.now)
			if !ok {
				t.Fatalf("NextAt(%s) = none, want an instant", tt.now.Format("15:04"))
			}
			if !next.After(tt.now) {
				t.Fatalf("NextAt(%s) = %s, not after the poll", tt.now.Format("15:04"), next.Format("15:04:05"))
			}
			for at := tt.now.Add(time.Second); at.Before(next); at = at.Add(time.Second) {
				if a := poll(s, b, cfg, at); a != nil {
					t.Fatalf("Poll(%s) = %+v, before NextAt %s", at.Format("15:04:05"), *a, next.Format("15:04:05"))
				}
			}
			if a := poll(s, b, cfg, next.Add(-time.Millisecond)); a != nil {
				t.Fatalf("Poll a millisecond before NextAt %s = %+v", next.Format("15:04:05"), *a)
			}
			if a := poll(s, b, cfg, next); a == nil {
				t.Errorf("Poll(%s), the instant NextAt named, = none, want an alert", next.Format("15:04:05"))
			}
		})
	}
}

func TestAPIHasNoAcknowledgeMuteSnoozeOrCap(t *testing.T) {
	banned := []string{"ack", "mute", "snooze", "cap", "max"}
	var names []string
	for _, typ := range []reflect.Type{reflect.TypeFor[alert.Scheduler](), reflect.TypeFor[alert.Alert](), reflect.TypeFor[config.Alert]()} {
		ptrType := reflect.PointerTo(typ)
		for i := range ptrType.NumMethod() {
			names = append(names, typ.String()+"."+ptrType.Method(i).Name)
		}
		for i := range typ.NumField() {
			if f := typ.Field(i); f.IsExported() {
				names = append(names, typ.String()+"."+f.Name)
			}
		}
	}
	for _, must := range []string{"alert.Scheduler.Poll", "alert.Scheduler.NextAt", "alert.Alert.Sound", "config.Alert.RepeatMinutes"} {
		if !slices.Contains(names, must) {
			t.Fatalf("the reflected names %q lack %s: the check would be vacuous", names, must)
		}
	}
	for _, n := range names {
		member := strings.ToLower(n[strings.LastIndex(n, ".")+1:])
		for _, word := range banned {
			if strings.Contains(member, word) {
				t.Errorf("%s contains %q: there is no acknowledge, mute, snooze or cap", n, word)
			}
		}
	}
}

func TestStoppedCycleNeverAlerts(t *testing.T) {
	cfg := testutil.LoadConfig(t, nil)

	t.Run("stopped before time-up", func(t *testing.T) {
		b := deepWorkLog(t)
		b.stop(hm(9, 30), cycleA)
		assertBothWays(t, b, cfg, hm(9, 0), hm(12, 0))
	})

	t.Run("stopped in overtime", func(t *testing.T) {
		b := deepWorkLog(t)
		b.stop(hm(10, 3), cycleA)
		assertBothWays(t, b, cfg, hm(9, 0), hm(12, 0), "09:50 expiry A", "10:00 reminder A")
		s := alert.NewScheduler()
		pollEveryMinute(s, b, cfg, hm(9, 0), hm(10, 3))
		if next, ok := nextAt(s, b, cfg, hm(10, 3)); ok {
			t.Errorf("NextAt(10:03) of a stopped cycle = %s, want none", next)
		}
	})

	t.Run("a retracted stop finds the anchor it left", func(t *testing.T) {
		b := deepWorkLog(t)
		stop := b.stop(hm(10, 3), cycleA)
		b.add(hm(10, 20), event.EventRetracted{Target: stop, Reason: "stopped by mistake"})
		s := alert.NewScheduler()
		assertHeard(t, pollEveryMinute(s, b, cfg, hm(9, 0), hm(10, 19)), "09:50 expiry A", "10:00 reminder A")
		// Running again from its start, the cycle has run 80 minutes: one
		// catch-up reminder, not a second expiry.
		a := poll(s, b, cfg, hm(10, 20))
		if a == nil || a.Kind != alert.Reminder {
			t.Fatalf("Poll(10:20) after the retraction = %+v, want one reminder", a)
		}
		assertHeard(t, pollEveryMinute(s, b, cfg, hm(10, 21), hm(10, 30)), "10:30 reminder A")
	})
}

func TestNoRunningCycleNeverAlerts(t *testing.T) {
	cfg := testutil.LoadConfig(t, nil)

	t.Run("an empty log", func(t *testing.T) {
		b, s := newLog(t), alert.NewScheduler()
		assertHeard(t, pollEveryMinute(s, b, cfg, hm(9, 0), hm(10, 0)))
		if next, ok := nextAt(s, b, cfg, hm(10, 0)); ok {
			t.Errorf("NextAt of an empty log = %s, want none", next)
		}
	})

	t.Run("only a paused cycle in overtime", func(t *testing.T) {
		b := notificationsLog(t)
		b.pause(hm(9, 40), cycleA)
		s := alert.NewScheduler()
		assertHeard(t, pollEveryMinute(s, b, cfg, hm(9, 41), hm(12, 0)))
		if next, ok := nextAt(s, b, cfg, hm(12, 0)); ok {
			t.Errorf("NextAt with only a paused cycle = %s, want none", next)
		}
	})

	t.Run("a cycle first seen while paused in overtime", func(t *testing.T) {
		b := newLog(t)
		// At 10:00 a cycle that ran from 08:00 to 08:40 is back-filled: it is
		// paused, 15 minutes over, with no expiry ever played.
		b.addAt(hm(10, 0), hm(8, 0), event.CycleStarted{CycleID: cycleA, Type: "notifications", Title: "Late entry", PlannedMinutes: 25})
		b.addAt(hm(10, 0), hm(8, 40), event.CyclePaused{CycleID: cycleA})
		s := alert.NewScheduler()
		assertHeard(t, pollEveryMinute(s, b, cfg, hm(9, 0), hm(12, 0)))
		if next, ok := nextAt(s, b, cfg, hm(12, 0)); ok {
			t.Errorf("NextAt with only a paused cycle = %s, want none", next)
		}
	})
}
