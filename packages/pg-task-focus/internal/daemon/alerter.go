package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/alert"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/clock"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/engine"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/notify"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/obs"
)

// maxWait is the longest the alerter sleeps between polls. The timer of a
// sleeping laptop does not advance, so a poll at least this often bounds how
// late the single catch-up alert after a wake can be; the scheduler itself
// plays at most one alert per poll (INV-CYCLE-20).
const maxWait = 30 * time.Second

// minWait keeps a poll that plays nothing from becoming a busy loop.
const minWait = 500 * time.Millisecond

// Alerter polls the alert scheduler and plays what is due: the sound and the
// notification, through the SoundPlayer and Notifier. The scheduler is
// poll-driven, so the daemon polls after every commit and every reload (the
// callbacks call Poll) and at the instant the scheduler names. A missed or an
// extra sound under rare circumstances is accepted (operator ruling,
// 2026-10-09). Alerts are derived from the projection and are never events.
type Alerter struct {
	eng     *engine.Engine
	clock   clock.Clock
	sched   *alert.Scheduler
	player  notify.SoundPlayer
	notifer notify.Notifier
	log     *slog.Logger
	metrics *obs.Metrics

	queue chan alert.Alert
	kick  chan struct{}
	wg    sync.WaitGroup
}

// NewAlerter builds the alerter for an open engine.
func NewAlerter(eng *engine.Engine, clk clock.Clock, p notify.SoundPlayer, n notify.Notifier, log *slog.Logger, m *obs.Metrics) *Alerter {
	return &Alerter{
		eng: eng, clock: clk, sched: alert.NewScheduler(), player: p, notifer: n, log: log, metrics: m,
		queue: make(chan alert.Alert, 16), kick: make(chan struct{}, 1),
	}
}

// Poll asks the scheduler what is due now and queues it for playback. It is
// quick, never blocks and never calls the engine's write path, so it is safe
// to call from an engine callback.
func (a *Alerter) Poll() {
	snap := a.eng.Snapshot()
	if al := a.sched.Poll(snap.Model, snap.Config, a.clock.Now()); al != nil {
		select {
		case a.queue <- *al:
		default:
			a.log.Warn("alert dropped: the playback queue is full", "kind", string(al.Kind))
		}
	}
	select {
	case a.kick <- struct{}{}:
	default:
	}
}

// Reading is what /healthz shows: the running cycle and the instant of its
// next alert, if one runs.
func (a *Alerter) Reading() (cycleID string, next time.Time, ok bool) {
	snap := a.eng.Snapshot()
	now := a.clock.Now()
	c, running := snap.Model.RunningAt(now)
	if !running {
		return "", time.Time{}, false
	}
	at, ok := a.sched.NextAt(snap.Model, snap.Config, now)
	return string(c.ID), at, ok
}

// Run plays queued alerts and polls on a timer until ctx ends. It returns when
// the playback in progress has finished.
func (a *Alerter) Run(ctx context.Context) {
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case al := <-a.queue:
				a.play(ctx, al)
			}
		}
	}()
	for {
		wait := maxWait
		snap := a.eng.Snapshot()
		now := a.clock.Now()
		if at, ok := a.sched.NextAt(snap.Model, snap.Config, now); ok {
			if d := at.Sub(now); d < wait {
				wait = d
			}
		}
		wait = max(wait, minWait)
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			a.wg.Wait()
			return
		case <-a.kick:
			t.Stop()
		case <-t.C:
			a.Poll()
		}
	}
}

// play plays one alert: the sound, when the type has one, and the
// notification. A failure of either is logged and counted and never touches
// cycle state.
func (a *Alerter) play(ctx context.Context, al alert.Alert) {
	defer func() {
		if r := recover(); r != nil {
			a.log.Error("alert playback panicked", "panic", fmt.Sprint(r))
			a.metrics.AlertFailures.Inc()
		}
	}()
	snap := a.eng.Snapshot()
	title, body := "Cycle", "Time is up."
	if c, ok := snap.Model.Cycle(al.CycleID); ok {
		title = c.Title
		if al.Kind == alert.Reminder {
			body = fmt.Sprintf("%d min over", int(-c.Remaining(a.clock.Now())/time.Minute))
		}
	}
	a.metrics.AlertsPlayed.WithLabelValues(string(al.Kind)).Inc()
	if al.Sound != "" {
		if err := a.player.Play(ctx, al.Sound); err != nil {
			a.log.Warn("sound playback failed", "kind", string(al.Kind), "cycle_id", string(al.CycleID), "error", err.Error())
			a.metrics.AlertFailures.Inc()
		}
	}
	if err := a.notifer.Notify(ctx, title, body); err != nil {
		a.log.Warn("notification failed", "kind", string(al.Kind), "cycle_id", string(al.CycleID), "error", err.Error())
		a.metrics.AlertFailures.Inc()
	}
	a.log.Info("alert played", "kind", string(al.Kind), "cycle_id", string(al.CycleID))
}

// Drain waits for queued alerts to be handled; for tests.
func (a *Alerter) Drain(ctx context.Context) {
	for len(a.queue) > 0 {
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Millisecond):
		}
	}
	time.Sleep(20 * time.Millisecond)
}
