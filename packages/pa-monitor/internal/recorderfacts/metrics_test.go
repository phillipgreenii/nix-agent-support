package recorderfacts

import (
	"context"
	"testing"
	"time"
)

// fakeRec records every metric call the Facts makes.
type fakeRec struct {
	lags      map[string][]time.Duration
	forced    map[string]int
	exhausted map[string]int
}

func newFakeRec() *fakeRec {
	return &fakeRec{lags: map[string][]time.Duration{}, forced: map[string]int{}, exhausted: map[string]int{}}
}

func (r *fakeRec) RecordSessionLag(kind string, d time.Duration) {
	r.lags[kind] = append(r.lags[kind], d)
}
func (r *fakeRec) RecordDiscoveryForced(reason string)        { r.forced[reason]++ }
func (r *fakeRec) RecordDiscoveryBudgetExhausted(kind string) { r.exhausted[kind]++ }

// A pid dead locally but alive for the recorder forces maxGoneTries discoveries
// and then reports its budget exhausted exactly once.
func TestMetricsGoneBudget(t *testing.T) {
	_, f, _ := scanEnv(t)
	rec := newFakeRec()
	f.SetRecorder(rec)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	f.now = func() time.Time { return now }
	f.SetDiscoveryHeartbeat(60 * time.Second)
	f.pidAlive = func(pid int) bool { return pid != 501 }
	for i := 0; i < maxGoneTries+6; i++ {
		if _, err := f.Registrations(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if rec.forced["gone"] != maxGoneTries || rec.exhausted["gone"] != 1 {
		t.Errorf("gone forced=%d exhausted=%d, want %d and 1", rec.forced["gone"], rec.exhausted["gone"], maxGoneTries)
	}
	if rec.forced["empty"] != 0 || rec.exhausted["empty"] != 0 {
		t.Errorf("empty counted without a sessionless Process: %v %v", rec.forced, rec.exhausted)
	}
}

// An alive Process that never gets a session id forces maxEmptyTries discoveries
// and then reports its budget exhausted exactly once.
func TestMetricsEmptyBudget(t *testing.T) {
	e, f, _ := scanEnv(t)
	rec := newFakeRec()
	f.SetRecorder(rec)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	f.now = func() time.Time { return now }
	f.SetDiscoveryHeartbeat(60 * time.Second)
	f.pidAlive = func(int) bool { return true }
	e.Procs.Add(506, time.UnixMilli(startMs), nil)
	writeRegistry(t, e, 506, "", "busy", "")
	f.ResyncEnded()
	for i := 0; i < maxEmptyTries+8; i++ {
		if _, err := f.Registrations(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if rec.forced["empty"] != maxEmptyTries || rec.exhausted["empty"] != 1 {
		t.Errorf("empty forced=%d exhausted=%d, want %d and 1", rec.forced["empty"], rec.exhausted["empty"], maxEmptyTries)
	}
	if rec.forced["gone"] != 0 || rec.exhausted["gone"] != 0 {
		t.Errorf("gone counted for live pids: %v %v", rec.forced, rec.exhausted)
	}
}

// A new session records its appear lag (clock minus Process start) once; the
// sessions the first discovery lists record none.
func TestMetricsAppearLag(t *testing.T) {
	e, f, _ := scanEnv(t)
	rec := newFakeRec()
	f.SetRecorder(rec)
	ctx := context.Background()
	f.pidAlive = func(int) bool { return true }
	now := time.UnixMilli(startMs).Add(7 * time.Second)
	f.now = func() time.Time { return now }
	f.SetDiscoveryHeartbeat(60 * time.Second)
	f.ResyncEnded()
	if _, err := f.Registrations(ctx); err != nil {
		t.Fatal(err)
	}
	if len(rec.lags["appear"]) != 0 {
		t.Fatalf("the first discovery recorded appear lags: %v", rec.lags)
	}
	e.Procs.Add(507, time.UnixMilli(startMs), nil)
	writeRegistry(t, e, 507, "example-new", "busy", "")
	var got []time.Duration
	deadline := time.Now().Add(10 * time.Second)
	for len(got) == 0 {
		if _, err := f.Registrations(ctx); err != nil {
			t.Fatal(err)
		}
		got = rec.lags["appear"]
		if time.Now().After(deadline) {
			t.Fatal("no appear lag recorded")
		}
		time.Sleep(5 * time.Millisecond)
	}
	for i := 0; i < 3; i++ { // later discoveries must not record it again
		f.ResyncEnded()
		if _, err := f.Registrations(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(rec.lags["appear"]); n != 1 || rec.lags["appear"][0] != 7*time.Second {
		t.Errorf("appear lags = %v, want one of 7s", rec.lags["appear"])
	}
}

// A session whose pid the probe finds dead and the recorder later reports ended
// records its leave lag: clock at the discovery minus clock at the first probe.
func TestMetricsLeaveLag(t *testing.T) {
	e, f, _ := scanEnv(t)
	rec := newFakeRec()
	f.SetRecorder(rec)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	f.now = func() time.Time { return now }
	f.SetDiscoveryHeartbeat(60 * time.Second)
	dead := map[int]bool{}
	f.pidAlive = func(pid int) bool { return !dead[pid] }
	for i := 0; i < 3; i++ { // seed and drain
		if _, err := f.Registrations(ctx); err != nil {
			t.Fatal(err)
		}
	}
	dead[501] = true
	if _, err := f.Registrations(ctx); err != nil { // the probe sees the pid dead; the recorder still lists it
		t.Fatal(err)
	}
	if len(rec.lags["leave"]) != 0 {
		t.Fatalf("leave lag before the recorder reported the exit: %v", rec.lags)
	}
	now = now.Add(5 * time.Second)
	e.Procs.Exit(501, time.UnixMilli(startMs+30000))
	e.Settle()
	deadline := time.Now().Add(10 * time.Second)
	for len(rec.lags["leave"]) == 0 {
		if _, err := f.Registrations(ctx); err != nil {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("no leave lag recorded")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if l := rec.lags["leave"]; len(l) != 1 || l[0] != 5*time.Second {
		t.Errorf("leave lags = %v, want one of 5s", l)
	}
}
