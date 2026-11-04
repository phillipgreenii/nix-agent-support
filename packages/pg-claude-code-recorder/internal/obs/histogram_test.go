package obs

import (
	"slices"
	"strings"
	"sync"
	"testing"
)

func TestObserveLKeepsLabelSetsIndependent(t *testing.T) {
	r := NewRegistry()
	r.RegisterHistL("h", []float64{1, 10}, []map[string]string{{"op": "a"}, {"op": "b"}})
	r.ObserveL("h", map[string]string{"op": "a"}, 0.5)
	r.ObserveL("h", map[string]string{"op": "a"}, 5)
	r.ObserveL("h", map[string]string{"op": "b"}, 50)
	a, b := r.HistL("h", map[string]string{"op": "a"}), r.HistL("h", map[string]string{"op": "b"})
	if a.Samples != 2 || !slices.Equal(a.Counts, []uint64{1, 1, 0}) || a.Sum != 5.5 {
		t.Fatalf("a = %+v", a)
	}
	if b.Samples != 1 || !slices.Equal(b.Counts, []uint64{0, 0, 1}) {
		t.Fatalf("b = %+v", b)
	}
	if r.Hist("h") != nil {
		t.Fatal("the unlabelled series must not exist")
	}
}

func TestObserveIsObserveLWithNilLabels(t *testing.T) {
	r := NewRegistry()
	r.Observe("h", 0.3)
	r.ObserveL("h", nil, 0.3)
	h := r.Hist("h")
	if h == nil || h.Samples != 2 {
		t.Fatalf("hist = %+v", h)
	}
	if _, _, hs := r.Snapshot(); len(hs) != 1 || hs["h"] == nil {
		t.Fatalf("snapshot keys: %v", hs)
	}
}

func TestObserveLUnregisteredUsesLagBuckets(t *testing.T) {
	r := NewRegistry()
	r.ObserveL("h", map[string]string{"op": "x"}, 1)
	h := r.HistL("h", map[string]string{"op": "x"})
	if !slices.Equal(h.Bounds, LagBuckets) {
		t.Fatalf("bounds %v", h.Bounds)
	}
}

func TestObserveLNewLabelSetInheritsRegisteredBounds(t *testing.T) {
	r := NewRegistry()
	r.RegisterHistL("h", []float64{2, 4}, []map[string]string{{"op": "a"}})
	r.ObserveL("h", map[string]string{"op": "late"}, 3)
	if h := r.HistL("h", map[string]string{"op": "late"}); !slices.Equal(h.Bounds, []float64{2, 4}) {
		t.Fatalf("bounds %v", h.Bounds)
	}
}

func TestObserveLConcurrentIsRaceFree(t *testing.T) {
	r := NewRegistry()
	ops := []string{"a", "b", "c"}
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				r.ObserveL("h", map[string]string{"op": ops[(g+i)%3]}, 0.2)
			}
		}(g)
	}
	wg.Wait()
	var total uint64
	for _, op := range ops {
		total += r.HistL("h", map[string]string{"op": op}).Samples
	}
	if total != 8*500 {
		t.Fatalf("total %d", total)
	}
}

func TestOnLockCountsAcquisitionsAndObserveLTakesAtMostTwo(t *testing.T) {
	r := NewRegistry()
	if r.OnLock != nil {
		t.Fatal("OnLock must be nil in production")
	}
	n := 0
	r.OnLock = func() { n++ }
	r.ObserveL("h", map[string]string{"op": "x"}, 1)
	if n < 1 || n > 2 {
		t.Fatalf("one ObserveL took the registry lock %d times", n)
	}
	before := n
	r.Add("c", nil, 1)
	if n != before+1 {
		t.Fatalf("Add took %d acquisitions", n-before)
	}
}

func TestEveryStandardMetricHasHelp(t *testing.T) {
	o := New(nil)
	help := o.Reg.HelpTexts()
	counters, gauges, hists := o.Reg.Snapshot()
	seen := map[string]bool{}
	for _, m := range []map[string]float64{counters, gauges} {
		for k := range m {
			seen[strings.SplitN(k, "{", 2)[0]] = true
		}
	}
	for k := range hists {
		seen[strings.SplitN(k, "{", 2)[0]] = true
	}
	if len(seen) == 0 {
		t.Fatal("no standard metrics")
	}
	for name := range seen {
		if strings.TrimSpace(help[name]) == "" {
			t.Errorf("metric %s has no help text", name)
		}
	}
}
