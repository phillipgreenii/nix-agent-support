package calstate

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var t0 = time.Date(2027, 6, 10, 6, 0, 0, 0, time.UTC)

func TestWriteRead_roundTripAndShape(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", FileName)
	in := map[string]Record{
		"a": {LastSlot: t0, Outcome: OutcomeFired, DecidedAt: t0.Add(time.Second)},
		"b": {LastSlot: t0, Outcome: OutcomeSkipped, Reason: ReasonHostAsleep, DecidedAt: t0},
	}
	if err := Write(path, in); err != nil {
		t.Fatal(err)
	}
	out, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || !out["a"].LastSlot.Equal(t0) || out["b"].Reason != ReasonHostAsleep || out["a"].Outcome != "fired" {
		t.Fatalf("round trip = %+v", out)
	}
	// Fix the file shape: a versioned wrapper with snake_case record keys.
	raw, _ := os.ReadFile(path)
	for _, want := range []string{`"version": 1`, `"queries"`, `"last_slot"`, `"outcome"`, `"decided_at"`, `"reason"`} {
		if !contains(string(raw), want) {
			t.Fatalf("file lacks %s:\n%s", want, raw)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestRead_missingIsNoStateTruncatedIsError(t *testing.T) {
	dir := t.TempDir()
	if m, err := Read(filepath.Join(dir, "none.json")); m != nil || err != nil {
		t.Fatalf("missing file: %v %v", m, err)
	}
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte(`{"version":1,"queries":{"a":{"last_sl`), 0o644); err != nil {
		t.Fatal(err)
	}
	if m, err := Read(bad); m != nil || err == nil {
		t.Fatalf("truncated file: %v %v, want nil map and an error", m, err)
	}
}

func TestWrite_atomicLeavesNoTempAndReplaces(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	for i := 0; i < 3; i++ {
		if err := Write(path, map[string]Record{"a": {LastSlot: t0.Add(time.Duration(i) * time.Hour), Outcome: OutcomeFired}}); err != nil {
			t.Fatal(err)
		}
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 || ents[0].Name() != FileName {
		t.Fatalf("directory = %v, want only the state file", ents)
	}
	m, _ := Read(path)
	if !m["a"].LastSlot.Equal(t0.Add(2 * time.Hour)) {
		t.Fatalf("last write did not win: %v", m)
	}
}

func TestPrune(t *testing.T) {
	m := map[string]Record{"keep": {}, "gone": {}, "gone2": {}}
	removed := Prune(m, map[string]bool{"keep": true})
	if len(m) != 1 || len(removed) != 2 || removed[0] != "gone" || removed[1] != "gone2" {
		t.Fatalf("m=%v removed=%v", m, removed)
	}
}

func rd(wall time.Time, mono time.Duration) Reading { return Reading{Wall: wall, Mono: mono} }

func TestSampler_recordsSleepSpanOnly(t *testing.T) {
	s := NewSampler(10*time.Second, nil)
	s.Observe(rd(t0, 0))
	s.Observe(rd(t0.Add(10*time.Second), 10*time.Second)) // awake
	s.Observe(rd(t0.Add(15*time.Second), 15*time.Second+4*time.Second))
	if len(s.Spans()) != 0 {
		t.Fatalf("a gap under one poll interval is not a sleep: %v", s.Spans())
	}
	// Wall jumps 2h while monotonic moves 10s: a sleep.
	s.Observe(rd(t0.Add(2*time.Hour), 29*time.Second))
	sp := s.Spans()
	if len(sp) != 1 || !sp[0].Start.Equal(t0.Add(15*time.Second)) || !sp[0].End.Equal(t0.Add(2*time.Hour)) {
		t.Fatalf("spans = %v", sp)
	}
	if !s.AsleepAt(t0.Add(time.Hour)) || s.AsleepAt(t0.Add(3*time.Hour)) || s.AsleepAt(t0) {
		t.Fatal("AsleepAt wrong")
	}
	s.Drop(t0.Add(3 * time.Hour))
	if len(s.Spans()) != 0 {
		t.Fatal("Drop must forget spans ended before the cutoff")
	}
}

func TestSampler_runUsesInjectedSource(t *testing.T) {
	var n int
	base := t0
	src := func() Reading {
		n++
		// The second reading jumps the wall clock by an hour.
		wall := base.Add(time.Duration(n) * time.Millisecond)
		if n >= 2 {
			wall = wall.Add(time.Hour)
		}
		return Reading{Wall: wall, Mono: time.Duration(n) * time.Millisecond}
	}
	s := NewSampler(time.Millisecond, src)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	deadline := time.After(5 * time.Second)
	for len(s.Spans()) == 0 {
		select {
		case <-deadline:
			t.Fatal("sampler goroutine recorded no span")
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	<-done
}
