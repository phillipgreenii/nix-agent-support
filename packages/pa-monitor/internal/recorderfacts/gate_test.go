package recorderfacts

import (
	"context"
	"testing"
	"time"
)

// TestDiscoveryGateRunsOnlyOnRegistryChangeHeartbeatOrFailure pins the request
// shape of a gated Scan against a real recorder through a recording proxy: a
// quiet registry feed means no process.list and no registry lookup, a registry
// feed entry triggers both, the heartbeat still fires on a quiet feed (the
// clock is injected, nothing sleeps), and a failed fetch is retried on the next
// call.
func TestDiscoveryGateRunsOnlyOnRegistryChangeHeartbeatOrFailure(t *testing.T) {
	e, f, since := scanEnv(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	f.now = func() time.Time { return now }
	f.SetDiscoveryHeartbeat(60 * time.Second)
	f.pidAlive = func(int) bool { return true } // the pids are the recorder's fakes

	want := 3
	quiet := func(label string) {
		t.Helper()
		got, err := f.Registrations(ctx)
		if err != nil || len(got) != want {
			t.Fatalf("%s: %v %v", label, got, err)
		}
		reqs := since()
		if n := len(ops(reqs, "process.list")); n != 0 {
			t.Errorf("%s: process.list = %d, want 0", label, n)
		}
		if n := len(ops(reqs, "query")); n != 0 {
			t.Errorf("%s: registry lookups = %d, want 0", label, n)
		}
	}

	// The first call has no snapshot: it runs a full discovery and seeds the cursor.
	if got, err := f.Registrations(ctx); err != nil || len(got) != want {
		t.Fatalf("first: %v %v", got, err)
	}
	if n := len(ops(since(), "process.list")); n == 0 {
		t.Fatal("first call sent no process.list")
	}

	quiet("no registry change")
	quiet("no registry change, again")
	now = now.Add(59 * time.Second)
	quiet("just under the heartbeat")

	// A registry feed entry triggers process.list and the lookups.
	e.Procs.Add(504, time.UnixMilli(startMs), nil)
	writeRegistry(t, e, 504, "example-new", "busy", "")
	deadline := time.Now().Add(10 * time.Second)
	var reqs []wireRequest
	for {
		got, err := f.Registrations(ctx)
		if err != nil {
			t.Fatal(err)
		}
		reqs = append(reqs, since()...)
		if byID(got, "example-new") != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("registry change never reached discovery")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(ops(reqs, "process.list")) == 0 || len(ops(reqs, "query")) == 0 {
		t.Errorf("registry change: want process.list and a registry lookup, got %v", reqs)
	}

	want = 4
	// Drain any trailing registry records, then a quiet call sends neither.
	for i := 0; i < 3; i++ {
		if _, err := f.Registrations(ctx); err != nil {
			t.Fatal(err)
		}
	}
	since()
	if got, err := f.Registrations(ctx); err != nil || len(got) != 4 {
		t.Fatalf("after change: %v %v", got, err)
	}
	if reqs := since(); len(ops(reqs, "process.list")) != 0 || len(ops(reqs, "query")) != 0 {
		t.Errorf("quiet after change sent discovery requests: %v", reqs)
	}

	// The heartbeat fires although the feed is quiet.
	now = now.Add(61 * time.Second)
	if _, err := f.Registrations(ctx); err != nil {
		t.Fatal(err)
	}
	reqs = since()
	if len(ops(reqs, "process.list")) != 1 || len(ops(reqs, "query")) == 0 {
		t.Errorf("heartbeat: want one process.list and lookups, got %v", reqs)
	}
	quiet("quiet after the heartbeat")

	// A failed fetch is retried on the next call although nothing changed and
	// the heartbeat has not expired.
	now = now.Add(61 * time.Second)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := f.Registrations(cancelled); err == nil {
		t.Fatal("a cancelled call succeeded")
	}
	since()
	if _, err := f.Registrations(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(ops(since(), "process.list")); n == 0 {
		t.Errorf("retry after failure: process.list = %d, want at least 1", n)
	}
	quiet("quiet after the retry")
}

// TestDiscoveryGateOffByDefault: without SetDiscoveryHeartbeat every call reads
// the recorder, as the CLIs and the older tests expect.
func TestDiscoveryGateOffByDefault(t *testing.T) {
	_, f, since := scanEnv(t)
	for i := 0; i < 2; i++ {
		if _, err := f.Registrations(context.Background()); err != nil {
			t.Fatal(err)
		}
		if n := len(ops(since(), "process.list")); n == 0 {
			t.Fatalf("call %d sent no process.list", i)
		}
	}
}

// TestDiscoveryGateSeesAnExitWithoutARegistryWrite: a Process that exits with no
// session-registry record is shown on the next Scan after the local pid probe
// sees it dead, not at the 60 s heartbeat. The clock never advances, so the
// heartbeat cannot be what fires; nothing sleeps beyond the poll for the
// recorder's own asynchronous exit bookkeeping.
func TestDiscoveryGateSeesAnExitWithoutARegistryWrite(t *testing.T) {
	e, f, since := scanEnv(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	f.now = func() time.Time { return now }
	f.SetDiscoveryHeartbeat(60 * time.Second)
	dead := map[int]bool{}
	f.pidAlive = func(pid int) bool { return !dead[pid] }

	if _, err := f.Registrations(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Registrations(ctx); err != nil { // drain the seed's trailing feed read
		t.Fatal(err)
	}
	since()
	got, err := f.Registrations(ctx)
	if err != nil || byID(got, "example-live-a") == nil || !byID(got, "example-live-a").PidAlive {
		t.Fatalf("before the exit: %v %v", got, err)
	}
	if n := len(ops(since(), "process.list")); n != 0 {
		t.Fatalf("quiet scan sent %d process.list, want 0", n)
	}

	// The process exits: the registry file is untouched, only the recorder's
	// Process fact and the local pid change.
	e.Procs.Exit(501, time.UnixMilli(startMs+30000))
	dead[501] = true
	deadline := time.Now().Add(10 * time.Second)
	for {
		got, err = f.Registrations(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if s := byID(got, "example-live-a"); s != nil && !s.PidAlive {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the exit never reached discovery before the heartbeat")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !now.Equal(time.Unix(1_800_000_000, 0)) {
		t.Fatal("clock moved")
	}
	since()

	// Once the recorder agrees the session is not live, the probe is quiet again.
	if _, err := f.Registrations(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(ops(since(), "process.list")); n != 0 {
		t.Errorf("after the exit was seen: process.list = %d, want 0", n)
	}
}

// TestDiscoveryGateGivesUpOnAPidTheRecorderKeepsAlive: a pid dead locally but
// alive for the recorder (another pid domain) forces only a bounded number of
// discoveries.
func TestDiscoveryGateGivesUpOnAPidTheRecorderKeepsAlive(t *testing.T) {
	_, f, since := scanEnv(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	f.now = func() time.Time { return now }
	f.SetDiscoveryHeartbeat(60 * time.Second)
	f.pidAlive = func(pid int) bool { return pid != 501 }
	for i := 0; i < 3; i++ { // seed and drain
		if _, err := f.Registrations(ctx); err != nil {
			t.Fatal(err)
		}
	}
	since()
	for i := 0; i < maxGoneTries+6; i++ {
		if _, err := f.Registrations(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(ops(since(), "process.list")); n > maxGoneTries {
		t.Errorf("process.list = %d, want at most %d forced discoveries", n, maxGoneTries)
	}
}
