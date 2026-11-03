package obs

import (
	"slices"
	"sync"
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
	for _, name := range []string{MSettleTickDuration, MRefreshGaugesDuration, MWALCheckpointDuration} {
		h := o.Reg.Hist(name)
		if h == nil || !slices.Equal(h.Bounds, LoopBuckets) {
			t.Fatalf("%s: %+v", name, h)
		}
	}
	for _, l := range []string{LoopSettleTick, LoopRefreshGauges, LoopWALCheckpoint} {
		for _, out := range []string{OutcomeOK, OutcomeError} {
			if v, ok := o.Reg.Counter(MLoopRuns, map[string]string{"loop": l, "outcome": out}); !ok || v != 0 {
				t.Errorf("loop_runs_total{%s,%s} = %v %v", l, out, v, ok)
			}
		}
	}
	for _, g := range []string{MWALFrames, MWALCheckpointedFrames, MWALCheckpointLastTime} {
		if v, ok := o.Reg.Gauge(g, nil); !ok || v != 0 {
			t.Errorf("%s = %v %v", g, v, ok)
		}
	}
}

func TestSetCounterNeverDecreasesAndIsRaceFree(t *testing.T) {
	r := NewRegistry()
	r.SetCounter("c", nil, 5)
	r.SetCounter("c", nil, 3)
	if v, ok := r.Counter("c", nil); !ok || v != 5 {
		t.Fatalf("counter %v %v", v, ok)
	}
	r.SetCounter("c", nil, 7)
	if v, _ := r.Counter("c", nil); v != 7 {
		t.Fatalf("counter %v", v)
	}
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				r.SetCounter("c", nil, float64(g*1000+i))
			}
		}(g)
	}
	wg.Wait()
	if v, _ := r.Counter("c", nil); v != 7499 {
		t.Fatalf("final %v", v)
	}
}
