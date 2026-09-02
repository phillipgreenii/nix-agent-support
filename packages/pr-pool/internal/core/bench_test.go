package core

import (
	"strings"
	"testing"

	"github.com/phillipgreenii/pr-pool/conformance"
	"github.com/phillipgreenii/pr-pool/internal/activity"
	"github.com/phillipgreenii/pr-pool/internal/eventqueue"
)

// This file is PURELY INFORMATIONAL (Task 3.11 Step 4) — see
// internal/eventqueue/bench_test.go's package doc for why: `go test` never
// runs a Benchmark body without `-bench`, so nothing here is asserted on by
// the pr-pool-go-tests gate. perf_test.go's TestStatusVerbAllocBudget is
// this package's actual gate; this is its `go test -bench . -benchmem` read.

// BenchmarkServeStatus reports the `status` verb's steady-state cost for a
// full 512-entry activity reply — the same call TestStatusVerbAllocBudget
// budget-gates — via b.ReportAllocs(), with full timing alongside.
func BenchmarkServeStatus(b *testing.B) {
	ring := activity.New(activity.DefaultSize)
	for i := 0; i < 2*activity.DefaultSize; i++ {
		ring.Append(activity.Entry{Type: "review-requested", Outcome: "delivered"})
	}
	q, err := eventqueue.New(eventqueue.NewMemStore())
	if err != nil {
		b.Fatalf("eventqueue.New: %v", err)
	}
	svc := &Service{
		state:        conformance.Started,
		q:            q,
		bindings:     testBindings(),
		reg:          NewRegistry(nil),
		command:      "pr-pool",
		activityRing: ring,
		configPath:   "/repo/.pr-pool/config.toml",
	}
	const req = `{"schemaVersion":"1","since":1}`

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		var out strings.Builder
		svc.Serve(SubcommandStatus, strings.NewReader(req), &out)
	}
}

// BenchmarkServeStatus_DefaultWindow is BenchmarkServeStatus's cheaper
// sibling: an omitted `since` (the common, ordinary-poll case) returns only
// the newest min(64, held) entries rather than the full 512-entry worst
// case — useful as a baseline read alongside the worst-case number above.
func BenchmarkServeStatus_DefaultWindow(b *testing.B) {
	ring := activity.New(activity.DefaultSize)
	for i := 0; i < 100; i++ {
		ring.Append(activity.Entry{Type: "review-requested", Outcome: "delivered"})
	}
	q, err := eventqueue.New(eventqueue.NewMemStore())
	if err != nil {
		b.Fatalf("eventqueue.New: %v", err)
	}
	svc := &Service{
		state:        conformance.Started,
		q:            q,
		bindings:     testBindings(),
		reg:          NewRegistry(nil),
		command:      "pr-pool",
		activityRing: ring,
		configPath:   "/repo/.pr-pool/config.toml",
	}
	const req = `{"schemaVersion":"1"}`

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		var out strings.Builder
		svc.Serve(SubcommandStatus, strings.NewReader(req), &out)
	}
}
