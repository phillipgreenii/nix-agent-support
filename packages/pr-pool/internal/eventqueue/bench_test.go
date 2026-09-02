package eventqueue

import (
	"fmt"
	"sort"
	"testing"
	"time"
)

// This file is PURELY INFORMATIONAL (Task 3.11 Step 4): every function here
// is a func BenchmarkX(b *testing.B), which `go test` never runs without an
// explicit `-bench` flag — none of the assertions perf_test.go makes live
// here, and nothing here is asserted on by the pr-pool-go-tests gate. Its
// purpose is feeding packages/pr-pool/tests/perf-baselines.txt (Step 5) and
// giving a human a quick `go test -bench . -benchmem ./internal/eventqueue/...`
// read on the same code perf_test.go budget-gates.

// BenchmarkEnqueue reports steady-state Enqueue cost (new id each call, no
// listeners) via b.ReportAllocs() — the same call TestEnqueueAllocBudget
// budget-gates the observability DELTA of, here reported in full with
// timing alongside allocs.
func BenchmarkEnqueue(b *testing.B) {
	q, err := New(NewMemStore())
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := q.Enqueue(Event{ID: fmt.Sprintf("e%d", i), Type: "T"}); err != nil {
			b.Fatalf("Enqueue: %v", err)
		}
	}
}

// BenchmarkDispatch reports steady-state Dispatch cost (early eviction, one
// always-accepting listener, one pre-enqueued event consumed per call) via
// b.ReportAllocs().
func BenchmarkDispatch(b *testing.B) {
	q, err := New(NewMemStore(), WithEarlyEviction())
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	q.Register(&statelessAcceptListener{id: "h", typ: "T"})
	for i := 0; i < b.N; i++ {
		if _, err := q.Enqueue(Event{ID: fmt.Sprintf("e%d", i), Type: "T"}); err != nil {
			b.Fatalf("Enqueue: %v", err)
		}
	}
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		q.Dispatch()
	}
}

// BenchmarkDepthByType reports DepthByType's own steady-state cost (Task
// 3.2's lock-free read) at 10k retained entries, with no concurrent writer
// (TestDepthByTypeUnderContention in perf_test.go is the contended,
// budget-gated version of this same call).
func BenchmarkDepthByType(b *testing.B) {
	q, err := New(NewMemStore())
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	farFuture := time.Now().Add(time.Hour)
	for i := 0; i < 10_000; i++ {
		if _, err := q.Enqueue(Event{ID: fmt.Sprintf("e%d", i), Type: "T", ExpiresAt: farFuture}); err != nil {
			b.Fatalf("Enqueue: %v", err)
		}
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = q.DepthByType()
	}
}

// --- Step 5: q.mu hold-time p99 baseline, feeding perf-baselines.txt ------
//
// These two benchmarks are the informational source for
// packages/pr-pool/tests/perf-baselines.txt's UNGATED baseline record.
// Neither Dispatch's phases 1 ("SNAPSHOT", locked) and 3 ("RECORD", locked)
// nor Expire (a single q.mu critical section end to end) expose their own
// lock hold time as a public seam — splitting that out would mean
// instrumenting q.mu itself, which is outside this packet's Files. Instead,
// each benchmark drives its listener's Offer with a trivial, immediate
// accept (no work done unlocked in Dispatch's phase 2), so the WALL time
// per call is a close proxy for the LOCKED time: there is almost nothing
// left over for phase 2 to spend. p99 (rather than the mean `go test -bench`
// reports on its own) is computed by hand across b.N per-call samples and
// surfaced via b.ReportMetric, since ADR 0031's requirement here is a tail
// figure, not an average.

// p99Of returns the 99th-percentile duration in samples (ascending sort,
// index floor(0.99*n) — the same computation perf_test.go's
// TestDepthByTypeUnderContention uses).
func p99Of(samples []time.Duration) time.Duration {
	sorted := make([]time.Duration, len(samples))
	copy(sorted, samples)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return sorted[int(float64(len(sorted))*0.99)]
}

// BenchmarkDispatchQmuHoldTimeP99AtTenK approximates Dispatch's combined
// phase-1/phase-3 (locked) hold time at 10k retained entries: one listener,
// always-accepting, so phase 2 (unlocked Offer) does negligible work and
// each call's wall time is dominated by the two locked phases.
func BenchmarkDispatchQmuHoldTimeP99AtTenK(b *testing.B) {
	q, err := New(NewMemStore(), WithEarlyEviction())
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	q.Register(&statelessAcceptListener{id: "h", typ: "T"})
	for i := 0; i < 10_000; i++ {
		if _, err := q.Enqueue(Event{ID: fmt.Sprintf("e%d", i), Type: "T"}); err != nil {
			b.Fatalf("Enqueue: %v", err)
		}
	}
	samples := make([]time.Duration, 0, b.N)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		start := time.Now()
		q.Dispatch()
		samples = append(samples, time.Since(start))
	}
	b.ReportMetric(float64(p99Of(samples).Nanoseconds()), "p99-ns/op")
}

// BenchmarkExpireQmuHoldTimeP99AtTenK approximates Expire's q.mu hold time
// at 10k retained, already-expired-with-nothing-owed entries (no bound
// listener, so retainedLocked's second half is vacuous and every entry
// retires on the first pass) — the whole call is one q.mu critical section,
// so its wall time IS the lock hold time, not merely a proxy for it.
func BenchmarkExpireQmuHoldTimeP99AtTenK(b *testing.B) {
	samples := make([]time.Duration, 0, b.N)
	for i := 0; i < b.N; i++ {
		q, err := New(NewMemStore())
		if err != nil {
			b.Fatalf("New: %v", err)
		}
		for j := 0; j < 10_000; j++ {
			if _, err := q.Enqueue(Event{ID: fmt.Sprintf("e%d", j), Type: "T"}); err != nil {
				b.Fatalf("Enqueue: %v", err)
			}
		}
		start := time.Now()
		q.Expire()
		samples = append(samples, time.Since(start))
	}
	b.ReportMetric(float64(p99Of(samples).Nanoseconds()), "p99-ns/op")
}
