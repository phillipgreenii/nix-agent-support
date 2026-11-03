package obs

import (
	"slices"
	"testing"
)

func TestLoopBuckets(t *testing.T) {
	want := []float64{0.01, 0.05, 0.25, 1, 5, 15, 30, 60, 120, 300, 600, 1800}
	if !slices.Equal(LoopBuckets, want) {
		t.Fatalf("LoopBuckets %v", LoopBuckets)
	}
	for i := 1; i < len(LoopBuckets); i++ {
		if LoopBuckets[i] <= LoopBuckets[i-1] {
			t.Fatalf("not strictly increasing at %d: %v", i, LoopBuckets)
		}
	}
	if LoopBuckets[0] > 0.01 || LoopBuckets[len(LoopBuckets)-1] < 600 {
		t.Fatalf("range %v", LoopBuckets)
	}
}

func TestRegisterStandardRegistersLoopMetrics(t *testing.T) {
	o := New(nil)
	for _, name := range []string{MSettleTickDuration, MRefreshGaugesDuration} {
		h := o.Reg.Hist(name)
		if h == nil || !slices.Equal(h.Bounds, LoopBuckets) {
			t.Fatalf("%s: %+v", name, h)
		}
	}
	for _, l := range []string{LoopSettleTick, LoopRefreshGauges} {
		for _, out := range []string{OutcomeOK, OutcomeError} {
			if v, ok := o.Reg.Counter(MLoopRuns, map[string]string{"loop": l, "outcome": out}); !ok || v != 0 {
				t.Errorf("loop_runs_total{%s,%s} = %v %v", l, out, v, ok)
			}
		}
	}
	if _, ok := o.Reg.Counter(MLoopRuns, map[string]string{"loop": LoopWALCheckpoint, "outcome": OutcomeOK}); ok {
		t.Error("wal_checkpoint series belongs to the WAL checkpoint task")
	}
}
