package store

import "testing"

// BenchmarkStepClockRead is the cost of one read of the step clock (a
// monotonic time.Since), the unit the step timers are priced in. It is a
// benchmark, so `go test` does not run it; use `go test -bench StepClockRead
// ./internal/store/`.
func BenchmarkStepClockRead(b *testing.B) {
	now := monotonicNow
	var sink int64
	for i := 0; i < b.N; i++ {
		sink += int64(now())
	}
	if sink == 0 {
		b.Fatal("clock never advanced")
	}
}
