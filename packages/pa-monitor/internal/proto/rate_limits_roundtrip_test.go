package proto

import (
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
)

// TestDaemonStateRateLimits_UnsetRoundTripsToStale is the proto-layer
// counterpart of the store's NULL-not-1970 guarantee (ADR 0021 §6 / Test
// Strategy). A DaemonState with the status-line rate_limits fields left unset
// must round-trip through protobuf marshal/unmarshal, and be reconstructed
// into an aggregate.Tree, with every limits field reading back "unknown/stale"
// — never a value of 0 and never a 1970 timestamp.
func TestDaemonStateRateLimits_UnsetRoundTripsToStale(t *testing.T) {
	in := &DaemonState{
		// FiveHourPct / SevenDayPct / SevenDayResetsAt / LimitsCapturedAt unset.
	}
	b, err := proto.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var out DaemonState
	if err := proto.Unmarshal(b, &out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	// Unset proto timestamps must be nil on the wire, not a 1970 sentinel.
	if out.GetSevenDayResetsAt() != nil {
		t.Errorf("SevenDayResetsAt = %v, want nil (never 1970)", out.GetSevenDayResetsAt())
	}
	if out.GetLimitsCapturedAt() != nil {
		t.Errorf("LimitsCapturedAt = %v, want nil (never 1970)", out.GetLimitsCapturedAt())
	}

	// Reconstruct the tree; unknown percentages must be nil, unknown times zero.
	tree := ToTree(&out)
	if tree.FiveHourPct != nil {
		t.Errorf("tree.FiveHourPct = %v, want nil (unknown, not 0)", *tree.FiveHourPct)
	}
	if tree.SevenDayPct != nil {
		t.Errorf("tree.SevenDayPct = %v, want nil (unknown, not 0)", *tree.SevenDayPct)
	}
	if !tree.SevenDayResetsAt.IsZero() {
		t.Errorf("tree.SevenDayResetsAt = %v (Unix=%d), want zero Time (never 1970)",
			tree.SevenDayResetsAt, tree.SevenDayResetsAt.Unix())
	}
	if !tree.LimitsCapturedAt.IsZero() {
		t.Errorf("tree.LimitsCapturedAt = %v (Unix=%d), want zero Time (never 1970)",
			tree.LimitsCapturedAt, tree.LimitsCapturedAt.Unix())
	}
}

// TestTreeRateLimits_FullRoundTrip proves a Tree carrying present limits
// values survives FromTree -> Marshal -> Unmarshal -> ToTree exactly,
// including a real 0% seven_day reading (distinct from unknown/nil).
func TestTreeRateLimits_FullRoundTrip(t *testing.T) {
	fivePct := 34.0
	sevPct := 0.0 // real "0% used", NOT unknown
	sevRst := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	capAt := time.Date(2026, 7, 1, 9, 30, 0, 0, time.UTC)

	in := &aggregate.Tree{
		FiveHourPct:      &fivePct,
		SevenDayPct:      &sevPct,
		SevenDayResetsAt: sevRst,
		LimitsCapturedAt: capAt,
	}
	ds := FromTree(in)

	b, err := proto.Marshal(ds)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var out DaemonState
	if err := proto.Unmarshal(b, &out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	got := ToTree(&out)

	if got.FiveHourPct == nil || *got.FiveHourPct != fivePct {
		t.Errorf("FiveHourPct = %v, want %v", got.FiveHourPct, fivePct)
	}
	if got.SevenDayPct == nil || *got.SevenDayPct != sevPct {
		t.Errorf("SevenDayPct = %v, want %v (real 0%%, not nil)", got.SevenDayPct, sevPct)
	}
	if !got.SevenDayResetsAt.Equal(sevRst) {
		t.Errorf("SevenDayResetsAt = %v, want %v", got.SevenDayResetsAt, sevRst)
	}
	if !got.LimitsCapturedAt.Equal(capAt) {
		t.Errorf("LimitsCapturedAt = %v, want %v", got.LimitsCapturedAt, capAt)
	}
}

// TestDaemonStateRateLimits_ZeroPctIsDistinctFromUnknown pins the load-bearing
// distinction: on the wire, a present percentage of 0 must NOT be
// indistinguishable from "unknown". FromTree encodes a nil pointer as absent
// and a 0.0 pointer as present-zero via a dedicated presence flag.
func TestDaemonStateRateLimits_ZeroPctIsDistinctFromUnknown(t *testing.T) {
	// Unknown five_hour, present-zero seven_day.
	zero := 0.0
	in := &aggregate.Tree{
		FiveHourPct: nil,
		SevenDayPct: &zero,
	}
	ds := FromTree(in)
	b, err := proto.Marshal(ds)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var out DaemonState
	if err := proto.Unmarshal(b, &out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	got := ToTree(&out)
	if got.FiveHourPct != nil {
		t.Errorf("FiveHourPct = %v, want nil (unknown)", *got.FiveHourPct)
	}
	if got.SevenDayPct == nil {
		t.Fatal("SevenDayPct = nil, want present 0 (real reading, not unknown)")
	}
	if *got.SevenDayPct != 0 {
		t.Errorf("SevenDayPct = %v, want 0", *got.SevenDayPct)
	}
}
