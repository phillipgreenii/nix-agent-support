// Package anchor is the small autonomous Watchdog process that the keeper's
// caffeinate follows (caffeinate -w <anchor pid>): when the anchor exits, the
// assertion drops.
//
// The anchor takes every parameter from argv, never reads config.toml, and
// keeps no connection to the keeper. It has no hold level, no hysteresis and
// no linger; those live in the keeper. Every Interval it does two checks:
//
//  1. Battery. It reads the power source (policy.NewReader, which honors the
//     hidden PG_KEEP_AWAKE_BATTERY_SOURCE=file:<path> injection). On battery at
//     or below ReleasePct it exits with reason release_threshold. After
//     policy.BlindReads consecutive failed reads, when the last good reading
//     was on battery at or below BlindPct, it exits with blind_threshold. A
//     ReleasePct of 0 never exits for battery; a BlindPct of 0 is never blind.
//  2. Heartbeat. It stats the keeper's heartbeat file and tracks the awake time
//     since the mtime last changed, on a clock that excludes sleep. When that
//     reaches Watchdog it exits with reason watchdog. Mtime values are
//     compared, never ages, so a wall-clock step is harmless; a missing file
//     counts as unchanged.
//
// Before exiting it writes anchor-exit.json (atomically, under the state
// directory) and returns it; the keeper reads it on its next tick. When several
// conditions hold on one tick the precedence is release_threshold, then
// blind_threshold, then watchdog.
//
// The Percent of anchor-exit.json is the battery percentage that decided a
// battery exit, otherwise the last good reading's percentage, or
// policy.NoPercent (-1) when no good reading was ever seen.
package anchor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/lease"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/policy"
)

// The reasons written to anchor-exit.json.
const (
	ReasonRelease  = "release_threshold"
	ReasonBlind    = "blind_threshold"
	ReasonWatchdog = "watchdog"
)

// Params are the anchor's argv parameters.
type Params struct {
	// ReleasePct is the battery level at or below which (on battery) the anchor
	// exits; 0 never exits for battery.
	ReleasePct int
	// BlindPct is the keeper's hold level: after BlindReads failed reads, a last
	// good reading on battery at or below it makes the anchor exit; 0 disables.
	BlindPct int
	// Watchdog is how long the heartbeat mtime may stay unchanged, in awake
	// time, before the anchor exits.
	Watchdog time.Duration
	// Heartbeat is the keeper's heartbeat file.
	Heartbeat string
	// Interval is the polling period (the keeper's heartbeat_interval).
	Interval time.Duration
}

// Validate reports a malformed parameter set.
func (p Params) Validate() error {
	switch {
	case p.ReleasePct < 0 || p.ReleasePct > 100:
		return fmt.Errorf("--release-pct %d must be in 0..100", p.ReleasePct)
	case p.BlindPct < 0 || p.BlindPct > 100:
		return fmt.Errorf("--blind-pct %d must be in 0..100", p.BlindPct)
	case p.Watchdog <= 0:
		return fmt.Errorf("--watchdog %s must be positive", p.Watchdog)
	case p.Interval <= 0:
		return fmt.Errorf("--interval %s must be positive", p.Interval)
	case p.Heartbeat == "":
		return errors.New("--heartbeat must not be empty")
	}
	return nil
}

// Clock is the anchor's time source, injectable for tests.
type Clock interface {
	// Awake is a monotonic reading that excludes time the machine slept.
	Awake() time.Duration
	// Now is the wall-clock time, used only for the "at" field.
	Now() time.Time
	// Sleep waits for d of awake time, or returns ctx.Err() when ctx ends.
	Sleep(ctx context.Context, d time.Duration) error
}

// RealClock is the production Clock. Go's monotonic reading (time.Since) is
// mach_absolute_time on darwin, which does not advance while the machine
// sleeps (per Apple's documentation; not measured here).
type RealClock struct{ start time.Time }

// NewRealClock returns a RealClock whose Awake starts at zero now.
func NewRealClock() *RealClock { return &RealClock{start: time.Now()} }

// Awake implements Clock.
func (c *RealClock) Awake() time.Duration { return time.Since(c.start) }

// Now implements Clock.
func (c *RealClock) Now() time.Time { return time.Now() }

// Sleep implements Clock.
func (c *RealClock) Sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Env is everything Run touches outside Params. Zero fields take production
// values, except Dir, which is required.
type Env struct {
	// Dir is the state directory anchor-exit.json is written under.
	Dir *lease.Dir
	// Reader is the battery source; nil means policy.NewReader(os.Getenv).
	Reader policy.Reader
	// Clock is the time source; nil means NewRealClock().
	Clock Clock
	// Stat returns the heartbeat file's mtime; nil means os.Stat. An error
	// (typically a missing file) counts as "unchanged".
	Stat func(path string) (time.Time, error)
	// PID is recorded in anchor-exit.json; zero means os.Getpid().
	PID int
}

func osStat(path string) (time.Time, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return time.Time{}, err
	}
	return fi.ModTime(), nil
}

// Watcher is the anchor's per-tick decision logic, free of I/O and sleeping.
type Watcher struct {
	p Params

	failures     int
	lastGood     policy.Reading
	haveLastGood bool

	haveMtime bool
	mtime     time.Time
	changedAt time.Duration
}

// NewWatcher returns a Watcher whose heartbeat countdown starts at awake time
// now with baseline mtime (haveMtime false when the file is missing).
func NewWatcher(p Params, now time.Duration, mtime time.Time, haveMtime bool) *Watcher {
	return &Watcher{p: p, haveMtime: haveMtime, mtime: mtime, changedAt: now}
}

// Tick feeds one observation: the battery read outcome, the heartbeat stat
// outcome and the awake time. It returns the exit reason and percent when the
// anchor must exit, or "" to keep going.
func (w *Watcher) Tick(r policy.Reading, readErr error, mtime time.Time, statErr error, now time.Duration) (reason string, percent int) {
	if readErr != nil || (r.OnBattery && r.Percent == policy.NoPercent) {
		w.failures++
	} else {
		w.failures = 0
		w.lastGood, w.haveLastGood = r, true
		if w.p.ReleasePct > 0 && r.OnBattery && r.Percent <= w.p.ReleasePct {
			return ReasonRelease, r.Percent
		}
	}
	if policy.Blind(w.failures, w.lastGood, w.haveLastGood, w.p.BlindPct) {
		return ReasonBlind, w.lastGood.Percent
	}
	if statErr == nil && (!w.haveMtime || !mtime.Equal(w.mtime)) {
		w.haveMtime, w.mtime, w.changedAt = true, mtime, now
	}
	if now-w.changedAt >= w.p.Watchdog {
		return ReasonWatchdog, w.percent()
	}
	return "", 0
}

func (w *Watcher) percent() int {
	if w.haveLastGood && w.lastGood.Percent != policy.NoPercent {
		return w.lastGood.Percent
	}
	return policy.NoPercent
}

// Run polls until an exit condition holds, writes anchor-exit.json and returns
// it. It returns ctx.Err() (and writes nothing) when ctx ends first, and the
// write error when the file cannot be written.
func Run(ctx context.Context, p Params, env Env) (*lease.AnchorExitFile, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	if env.Dir == nil {
		return nil, errors.New("anchor: no state directory")
	}
	if env.Reader == nil {
		env.Reader = policy.NewReader(os.Getenv)
	}
	if env.Clock == nil {
		env.Clock = NewRealClock()
	}
	if env.Stat == nil {
		env.Stat = osStat
	}
	if env.PID == 0 {
		env.PID = os.Getpid()
	}
	mtime, err := env.Stat(p.Heartbeat)
	w := NewWatcher(p, env.Clock.Awake(), mtime, err == nil)
	for {
		r, readErr := env.Reader.Read()
		mt, statErr := env.Stat(p.Heartbeat)
		if reason, pct := w.Tick(r, readErr, mt, statErr, env.Clock.Awake()); reason != "" {
			exit := &lease.AnchorExitFile{At: env.Clock.Now().UTC(), Reason: reason, Percent: pct, PID: env.PID}
			if err := env.Dir.WriteAnchorExit(exit); err != nil {
				return nil, fmt.Errorf("anchor: write anchor-exit.json: %w", err)
			}
			exit.SchemaVersion = lease.SchemaVersion
			return exit, nil
		}
		if err := env.Clock.Sleep(ctx, p.Interval); err != nil {
			return nil, err
		}
	}
}
