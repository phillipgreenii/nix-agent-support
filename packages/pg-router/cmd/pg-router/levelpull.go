package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/phillipgreenii/pg-router/internal/config"
	"github.com/phillipgreenii/pg-router/internal/discover"
)

// The level-source pull loop and the start pass (the router's side of sensors).
//
// Exported names: none (package main). Names: levelPullTick, wakeDetector,
// startLevelPulls, runLevelPullLoop.
//
// Level-source pulls are exempt from every level, router pause, the log
// soft-halt, log_full / log_unwritable and the load backoff: this loop checks
// none of them, and it runs on its own goroutine, so neither a hung probe nor a
// busy producer pass delays it, and it delays neither dispatch nor another
// sensor (each pull has its own goroutine and deadline).

// levelPullTick is how often the loop looks for a due level source and for a
// wake from sleep. A variable so a test can shorten it.
var levelPullTick = 5 * time.Second

// wakeDetector spots a wake from sleep: the wall clock advances while the
// monotonic clock does not, so wall elapsed minus monotonic elapsed beyond the
// threshold means the host slept.
type wakeDetector struct {
	threshold time.Duration
	prev      time.Time
}

// Observe records a tick time (it must carry a monotonic reading, as a
// time.Now() or ticker value does) and reports whether a wake happened since
// the previous call.
func (w *wakeDetector) Observe(now time.Time) bool {
	woke := false
	if !w.prev.IsZero() {
		woke = wakeBetween(now.Round(0).Sub(w.prev.Round(0)), now.Sub(w.prev), w.threshold)
	}
	w.prev = now
	return woke
}

// wakeBetween is the wake rule over one interval: the wall clock advanced more
// than threshold beyond the monotonic clock.
func wakeBetween(wall, mono, threshold time.Duration) bool {
	return wall-mono > threshold
}

// startLevelPulls runs the initial pull pass: every level source starts at once
// and the router waits until all have answered or cfg.Levels.StartWait passes,
// before the first dispatch pass. A resource that has not answered reads
// unknown. It then starts the pull loop and returns. With no level source it
// does nothing.
func startLevelPulls(ctx context.Context, cfg config.Config, p *discover.LevelPuller) {
	if len(p.Sources()) == 0 {
		return
	}
	if all := p.StartPass(ctx, cfg.Levels.StartWait); !all {
		slog.Warn("level pull start pass: not every level source answered within the start wait; unanswered resources read unknown", "wait", cfg.Levels.StartWait)
	}
	wd := &wakeDetector{threshold: cfg.Levels.WakeThreshold}
	wd.Observe(time.Now())
	go runLevelPullLoop(ctx, cfg, p, levelPullTick, wd.Observe)
}

// runLevelPullLoop starts due level sources every tick and every level source at
// once after a wake (woke reports one for a tick time), until ctx ends. cfg is
// not consulted for the load backoff: the sensors are what report that load.
func runLevelPullLoop(ctx context.Context, cfg config.Config, p *discover.LevelPuller, tick time.Duration, woke func(time.Time) bool) {
	_ = cfg
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			if woke(now) {
				slog.Info("level pull: woke from sleep; pulling every level source now")
				p.PullAll(ctx)
				continue
			}
			p.Tick(ctx)
		}
	}
}
