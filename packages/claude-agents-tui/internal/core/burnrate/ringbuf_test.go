package burnrate

import (
	"testing"
	"time"
)

func TestRatePerMinuteOverWindow(t *testing.T) {
	r := New(60 * time.Second)
	start := time.Date(2026, 4, 23, 10, 0, 0, 0, time.UTC)
	r.Add(start, 100)
	r.Add(start.Add(30*time.Second), 200)
	// at t=start+60s, window covers [start, start+60s]: total 300 tokens over 60s → 300 tok/min
	got := r.Rate(start.Add(60 * time.Second))
	if got != 300 {
		t.Errorf("Rate = %v, want 300", got)
	}
}

func TestRateDropsOldSamples(t *testing.T) {
	r := New(60 * time.Second)
	start := time.Date(2026, 4, 23, 10, 0, 0, 0, time.UTC)
	r.Add(start, 100)
	r.Add(start.Add(5*time.Minute), 200)
	// at t=start+5m: only the second sample is in-window → 200 tok over 60s window → 200 tok/min
	got := r.Rate(start.Add(5 * time.Minute))
	if got != 200 {
		t.Errorf("Rate = %v, want 200", got)
	}
}

func TestRateEmptyIsZero(t *testing.T) {
	r := New(60 * time.Second)
	if got := r.Rate(time.Now()); got != 0 {
		t.Errorf("empty Rate = %v, want 0", got)
	}
}
