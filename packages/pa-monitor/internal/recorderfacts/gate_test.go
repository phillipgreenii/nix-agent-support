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
