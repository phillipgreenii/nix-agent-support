package corpus

import (
	"reflect"
	"testing"

	"github.com/phillipgreenii/pa-monitor/internal/core/limits"
)

func lrec(ts int64, pct float64, reset int64) limits.Record {
	return limits.Record{TS: &ts, FiveHourPct: &pct, FiveHourResetsAt: &reset}
}

// TestLimits_CurrentMatchesFold: the observer's Current is exactly
// limits.Current over the flattened record set, and surfaces a peak that lives in
// a different file than the newest-ts record (ADR-0029 peak-across-files).
func TestLimits_CurrentMatchesFold(t *testing.T) {
	o := NewLimitsObserver()
	recsA := []limits.Record{lrec(500, 40, 1000)} // newest ts, low pct
	recsB := []limits.Record{lrec(200, 90, 1000)} // earlier ts, peak
	o.setRecords("proj-a/x.status.jsonl", recsA)
	o.setRecords("proj-b/y.status.jsonl", recsB)

	got := o.Current()
	want := limits.Current(append(append([]limits.Record{}, recsA...), recsB...))
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Current = %+v, want %+v", got, want)
	}
	if got == nil || got.FiveHourPct == nil || *got.FiveHourPct != 90 {
		t.Fatalf("FiveHourPct = %v, want peak 90 across files", got)
	}
}

// TestLimits_DeterministicNoWindowTiebreak: with no reset anywhere the fold falls
// back to newestPct (first record at the newest ts) — order-sensitive. Sorting the
// flatten by path makes Current deterministic across repeated calls (guards the
// Go map-iteration flake).
func TestLimits_DeterministicNoWindowTiebreak(t *testing.T) {
	o := NewLimitsObserver()
	ts := int64(100)
	pctA, pctZ := 10.0, 90.0
	o.setRecords("z.status.jsonl", []limits.Record{{TS: &ts, FiveHourPct: &pctZ}})
	o.setRecords("a.status.jsonl", []limits.Record{{TS: &ts, FiveHourPct: &pctA}})

	first := o.Current()
	for i := 0; i < 20; i++ {
		if got := o.Current(); !reflect.DeepEqual(got, first) {
			t.Fatalf("Current not deterministic: %+v vs %+v", got, first)
		}
	}
	// sorted-path order puts "a..." first, so newestPct returns a's pct (10).
	if first == nil || first.FiveHourPct == nil || *first.FiveHourPct != 10 {
		t.Fatalf("FiveHourPct = %v, want 10 (sorted-path-first at newest ts)", first)
	}
}

func TestLimits_PrunePaths(t *testing.T) {
	o := NewLimitsObserver()
	o.setRecords("a.status.jsonl", []limits.Record{lrec(100, 50, 1000)})
	o.setRecords("b.status.jsonl", []limits.Record{lrec(100, 99, 2000)})
	o.prunePaths(map[string]bool{"a.status.jsonl": true})
	got := o.Current()
	want := limits.Current([]limits.Record{lrec(100, 50, 1000)})
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("after prune Current = %+v, want only a's records %+v", got, want)
	}
}
