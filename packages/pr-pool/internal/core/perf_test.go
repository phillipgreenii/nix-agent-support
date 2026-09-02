package core

import (
	"strings"
	"testing"

	"github.com/phillipgreenii/pr-pool/conformance"
	"github.com/phillipgreenii/pr-pool/internal/activity"
	"github.com/phillipgreenii/pr-pool/internal/eventqueue"
)

// This file carries Task 3.11's core-side HARD perf/allocation-budget gate:
// an ordinary TestX function, run under plain `go test` (see
// internal/eventqueue/perf_test.go's package doc for why that split matters
// and is deliberate, not an oversight — bench_test.go in this package is
// this file's purely-informational Benchmark* twin).

// countingStore wraps an eventqueue.Store and counts every call to any of
// its methods — the "counting fake wrapper... incrementing on any I/O
// method" TestStatusVerbAllocBudget's zero-syscall assertion needs (Task
// 3.11 Step 2). This repo has no existing syscall-counting perf-test
// convention (per the packet's own Binding decisions); this is deliberately
// the minimal one: eventqueue.Store is ALREADY the one seam through which
// the `status` verb could reach real I/O (a WAL file, in production), so
// wrapping it directly counts the thing that matters rather than
// intercepting actual syscalls one layer down.
type countingStore struct {
	inner eventqueue.Store
	calls int
}

func (c *countingStore) Append(rec eventqueue.Record) error {
	c.calls++
	return c.inner.Append(rec)
}

func (c *countingStore) AppendBatch(recs []eventqueue.Record) error {
	c.calls++
	return c.inner.AppendBatch(recs)
}

func (c *countingStore) Replay() ([]eventqueue.Record, error) {
	c.calls++
	return c.inner.Replay()
}

func (c *countingStore) Close() error {
	c.calls++
	return c.inner.Close()
}

// TestStatusVerbAllocBudget is the Step 2 red-first test: a FIXED
// allocation budget for a full, 512-entry `activity` reply (the
// activityReadWindow this package's handleStatus always allocates,
// regardless of how many entries a given request actually needs — see
// composeStatusReply's own const doc), PLUS a zero-syscall assertion
// through the Store seam (composeStatusReply's own doc already claims
// `status` is read-only and takes no q.mu; this is that same invariant
// checked from the OTHER side — no durable-store I/O either).
//
// The numeric allocation budget is this packet's own choice (Task 3.11's
// Freedom boundary): run once against the landed Tasks 3.1/3.2/3.4/3.8
// implementations, observe the actual count (8284 allocs/op, measured while
// authoring this test), and freeze a small rounded-up margin (9000) as the
// asserted budget — matching Step 1's own "confirm the current... then
// assert the budget" discovery pattern.
func TestStatusVerbAllocBudget(t *testing.T) {
	const budget = 9000.0

	ring := activity.New(activity.DefaultSize)
	for i := 0; i < 2*activity.DefaultSize; i++ {
		ring.Append(activity.Entry{Type: "review-requested", Outcome: "delivered"})
	}

	cs := &countingStore{inner: eventqueue.NewMemStore()}
	q, err := eventqueue.New(cs)
	if err != nil {
		t.Fatalf("eventqueue.New: %v", err)
	}

	svc := &Service{
		state:        conformance.Started, // INV-INTF-1: Serve only answers in `started`.
		q:            q,
		bindings:     testBindings(),
		reg:          NewRegistry(nil),
		command:      "pr-pool",
		activityRing: ring,
		configPath:   "/repo/.pr-pool/config.toml",
	}

	// since=1 with a 512-capacity ring holding 1024 appended entries makes
	// Ring.Read return the full 512-entry window (gap 1023 > capacity 512),
	// which is the worst case handleStatus's fixed-size buf actually
	// exercises — see activityReadWindow's own doc.
	const req = `{"schemaVersion":"1","since":1}`

	// Sanity: confirm this request shape really does return 512 activity
	// entries before budget-gating it, so a future schema/handler change
	// that silently shrinks the reply does not leave this test measuring a
	// smaller, easier case than the one it claims to.
	var probe strings.Builder
	if code := svc.Serve(SubcommandStatus, strings.NewReader(req), &probe); code != 0 {
		t.Fatalf("Serve(status) probe exit = %d, want 0; body=%s", code, probe.String())
	}
	if !strings.Contains(probe.String(), `"seq":513`) {
		t.Fatalf("probe reply does not look like a full 512-entry window (want the oldest kept seq, 513, present): %s", probe.String())
	}

	preCalls := cs.calls
	allocs := testing.AllocsPerRun(200, func() {
		var out strings.Builder
		svc.Serve(SubcommandStatus, strings.NewReader(req), &out)
	})
	t.Logf("Serve(status) allocs/op (512-entry activity reply) = %v", allocs)
	if allocs > budget {
		t.Fatalf("Serve(status) allocs/op = %v, want <= %v", allocs, budget)
	}

	if got := cs.calls - preCalls; got != 0 {
		t.Fatalf("Store calls during Serve(status) = %d, want 0 (status must be read-only, no durable-store I/O)", got)
	}

	gatesBefore, _ := svc.GateSnapshot()
	if len(gatesBefore) != 0 {
		t.Fatalf("gates = %v, want empty — nothing in this test ever wrote a gate, so GateSnapshot's own in-memory cache (never file I/O; see status.go's package doc) has nothing to report either", gatesBefore)
	}
}
