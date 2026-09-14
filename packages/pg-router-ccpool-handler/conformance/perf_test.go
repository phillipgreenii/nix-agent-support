package conformance

import (
	"encoding/json"
	"sort"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/conformance"
)

// This file carries docket pg2-oju6w's Task 5.11 Step 3 perf-baseline
// re-measurement (ADR 0065's Acceptance section: "Perf baselines are
// re-measured; the extraction introduces a real wire hop where there was
// an in-process call, and this obligation exists precisely to catch a
// daemon regression from that, not to rubber-stamp it"; [design: Task 5.11
// Test, Step 3]).
//
// Pre-extraction in-process baseline (the comparison point this obligation
// names): packages/pg-router/internal/eventqueue's own Task 3.11 perf gate
// (perf_test.go) measures a pure in-process Dispatch call, with NO process
// boundary crossed, at ~18 allocs/event and sub-microsecond-to-low-
// millisecond wall time even under a concurrent ring reader
// (TestDispatchOverheadUnderRingReader's <=40% overhead budget on top of
// that). This module's own dispatch round trip, post-extraction, crosses a
// REAL process boundary (fork+exec+pipe, per DEC-WIRE-1's CLI transport) on
// every single call — a categorically different cost, not a percentage
// regression on the same operation, so this gate is NOT "wire hop within
// N% of the in-process call" (that would fail by design and would be
// re-litigating ADR 0065's already-approved architecture, not testing
// anything). It is an ABSOLUTE ceiling on the wire hop itself, following
// Task 3.11's own freedom-boundary pattern (perf_test.go's own doc: "run
// once against the landed implementation, observe the actual count, freeze
// a small rounded-up margin as the asserted budget") — so a FUTURE
// regression in the wire boundary (an accidental retry loop, an added
// synchronous sleep, a second subprocess spawn per dispatch) fails this
// gate even though the extraction's own one-time cost does not.
//
// dispatchRoundTripBudgetMs was set by running this test once against the
// landed Task 5.4 wire client + this module's real compiled binary (a
// "true"-backed command role, the same trivial real subprocess
// TestLiveDispatch_command already uses): median round trip measured
// ~7.5ms, worst-of-20 ~16ms, on this machine. 200ms is a ~27x rounded-up
// margin over the measured median — generous enough to absorb ordinary
// process-scheduling jitter and a slower/loaded/shared-machine CI run
// (this workspace's own perf tests — internal/eventqueue's
// TestDispatchOverheadUnderRingReader — document exactly this kind of
// ambient-load variance), while still catching an actual multi-x
// regression in the wire path (an accidental retry loop, an added
// synchronous sleep, a second subprocess spawn per dispatch).
const dispatchRoundTripBudgetMs = 200.0

// TestPerfBaseline_DispatchRoundTripThroughWire measures one full
// handler.dispatch round trip through the wire boundary Task 5.4
// introduced: this module's REAL compiled binary, invoked exactly as the
// core's wireclient.Client invokes a registered participant (a "command"
// role, argv ["true"] — the same trivial always-succeeding real subprocess
// TestLiveDispatch_command already uses, so this measures the wire
// adapter's own overhead, not ccpool session cost), over `rounds`
// independent round trips.
func TestPerfBaseline_DispatchRoundTripThroughWire(t *testing.T) {
	const rounds = 20

	bin := buildHandlerBinary(t)
	roleConfig := writeRoleConfig(t, `{"name":"conformance-perf","type":"command","command":{"argv":["true"]}}`)

	req, err := conformance.Golden("handler.dispatch")
	if err != nil {
		t.Fatalf("read handler.dispatch golden: %v", err)
	}
	reqBytes, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	// One untimed warm-up round: excludes the binary's first-exec page-cache
	// cost from the measured samples, matching this module's own
	// buildHandlerBinary/runBinary helpers' steady-state assumption.
	if _, code, err := runBinary(t, bin, []string{"dispatch", "--role-config", roleConfig}, reqBytes); err != nil || code != conformance.ExitOK {
		t.Fatalf("warm-up dispatch: err=%v code=%d", err, code)
	}

	samples := make([]time.Duration, rounds)
	for i := 0; i < rounds; i++ {
		start := time.Now()
		reply, code, err := runBinary(t, bin, []string{"dispatch", "--role-config", roleConfig}, reqBytes)
		samples[i] = time.Since(start)
		if err != nil {
			t.Fatalf("round %d: run dispatch: %v", i, err)
		}
		if code != conformance.ExitOK {
			t.Fatalf("round %d: dispatch exit=%d, want %d; reply=%s", i, code, conformance.ExitOK, reply)
		}
	}

	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	median := samples[len(samples)/2]
	worst := samples[len(samples)-1]
	medianMs := float64(median) / float64(time.Millisecond)

	t.Logf("dispatch round trip through the wire (n=%d): median=%v max=%v samples=%v", rounds, median, worst, samples)
	if medianMs > dispatchRoundTripBudgetMs {
		t.Fatalf("dispatch round trip through the wire: median=%.2fms, want <= %.1fms (samples=%v)",
			medianMs, dispatchRoundTripBudgetMs, samples)
	}
}
