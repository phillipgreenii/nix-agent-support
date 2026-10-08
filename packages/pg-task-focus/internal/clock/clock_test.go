package clock

import (
	"testing"
	"time"
)

func TestFakeClockAdvanceAndSet(t *testing.T) {
	start := time.Date(2026, 10, 7, 13, 30, 0, 0, time.UTC)
	f := NewFake(start)
	if got := f.Now(); !got.Equal(start) {
		t.Fatalf("Now() = %v, want %v", got, start)
	}

	f.Advance(90 * time.Second)
	want := start.Add(90 * time.Second)
	if got := f.Now(); !got.Equal(want) {
		t.Fatalf("after Advance(90s) Now() = %v, want %v", got, want)
	}

	f.Set(start)
	if got := f.Now(); !got.Equal(start) {
		t.Fatalf("after Set back Now() = %v, want %v", got, start)
	}
}

func TestRealClockIsUTCAgnostic(t *testing.T) {
	before := time.Now()
	got := Real().Now()
	after := time.Now()

	if got.Before(before.Add(-time.Second)) || got.After(after.Add(time.Second)) {
		t.Fatalf("Real().Now() = %v, not within one second of time.Now() (%v..%v)", got, before, after)
	}
}
