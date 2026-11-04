package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// state is the Memento kept between one-shot runs.
type state struct {
	LastRun time.Time `json:"lastRun"`
	// Silence is the AWAKE time with no Prometheus sample for any job.
	Silence time.Duration `json:"silence"`
	// PagedAt is the last time a page was sent; zero when not paged.
	PagedAt time.Time `json:"pagedAt"`
}

// params are the decision thresholds.
type params struct {
	threshold time.Duration // awake silence that pages
	maxGap    time.Duration // a tick gap above this is sleep (or no heartbeat): counts zero
	renotify  time.Duration // re-page interval while the outage lasts
}

// action is what a step tells the caller to do.
type action int

const (
	actNone action = iota
	actPage
	actClear
)

// step is the pure decision function: no clock, network or filesystem.
//
// Awake accounting: a gap between ticks above maxGap (or not positive) is
// treated as sleep and adds nothing, so a laptop asleep for hours never
// accumulates silence.
func step(prev state, now time.Time, samples bool, p params) (state, action) {
	next := prev
	next.LastRun = now
	if samples {
		wasPaged := !prev.PagedAt.IsZero()
		next.Silence = 0
		next.PagedAt = time.Time{}
		if wasPaged {
			return next, actClear
		}
		return next, actNone
	}
	if !prev.LastRun.IsZero() {
		if gap := now.Sub(prev.LastRun); gap > 0 && gap <= p.maxGap {
			next.Silence += gap
		}
	}
	if next.Silence < p.threshold {
		return next, actNone
	}
	if prev.PagedAt.IsZero() || now.Sub(prev.PagedAt) >= p.renotify {
		next.PagedAt = now
		return next, actPage
	}
	return next, actNone
}

func loadState(path string) state {
	b, err := os.ReadFile(path)
	if err != nil {
		return state{}
	}
	var s state
	if json.Unmarshal(b, &s) != nil {
		return state{}
	}
	return s
}

func saveState(path string, s state) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// deps are the injected effects.
type deps struct {
	now      func() time.Time
	probe    func() bool // true when any sample is present; failure is false
	notify   func(msg string) error
	stateDir string
}

// runOnce performs one heartbeat tick.
func runOnce(d deps, p params) error {
	statePath := filepath.Join(d.stateDir, "state.json")
	alertPath := filepath.Join(d.stateDir, "alert")
	prev := loadState(statePath)
	next, act := step(prev, d.now(), d.probe(), p)
	var notifyErr error
	switch act {
	case actPage:
		msg := fmt.Sprintf("observability stack silent: no Prometheus sample for %s of awake time", next.Silence.Round(time.Minute))
		_ = os.MkdirAll(d.stateDir, 0o755)
		_ = os.WriteFile(alertPath, []byte(msg+"\n"), 0o644)
		if err := d.notify(msg); err != nil {
			notifyErr = err
			next.PagedAt = prev.PagedAt // retry on the next tick
		}
	case actClear:
		_ = os.Remove(alertPath)
	}
	if err := saveState(statePath, next); err != nil {
		return err
	}
	return notifyErr
}
