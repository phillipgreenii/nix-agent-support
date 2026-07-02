package daemon

import (
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/service"
	"github.com/phillipgreenii/pa-monitor/internal/store"
)

// convertSessionWithContribution must carry LastError.FromSubagent through from
// the persisted column so the gRPC GetState/WatchState path (and the TUI it
// feeds) shows the '(in subagent)' provenance after a daemon restart. See
// pg2-kg8u.
func TestConvertSessionWithContribution_PreservesLastErrorFromSubagent(t *testing.T) {
	sc := &store.SessionWithContribution{
		Session: store.Session{
			SessionID:             "sid-1",
			LastErrorKind:         "server_error",
			LastErrorText:         "API Error: Stream idle timeout",
			LastErrorAt:           time.Now().UTC(),
			LastErrorTerminal:     true,
			LastErrorFromSubagent: true,
		},
	}

	sv := convertSessionWithContribution(sc)
	if sv == nil {
		t.Fatal("convertSessionWithContribution returned nil")
	}
	if sv.SessionEnrichment.LastError == nil {
		t.Fatal("LastError is nil; expected reconstructed error record")
	}
	if !sv.SessionEnrichment.LastError.FromSubagent {
		t.Error("LastError.FromSubagent = false; want true (provenance dropped on the DB->client path)")
	}
}

// convertStateToAggregateTree must carry the block's status-line rate_limits
// windows (ADR 0021 §6) onto the tree so they can be threaded to the wire.
// A present value round-trips; an unset (nil / NULL) value stays unknown —
// never 0 and never 1970.
func TestConvertStateToAggregateTree_CarriesRateLimits(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)

	t.Run("present", func(t *testing.T) {
		fivePct := 34.0
		sevPct := 0.0 // real "0% used", not unknown
		sevRst := now.Add(6 * 24 * time.Hour)
		capAt := now
		st := &service.State{
			Now: now,
			Block: &store.Block{
				BlockID:          "b1",
				StartedAt:        now.Add(-time.Hour),
				EndedAt:          now.Add(4 * time.Hour),
				FiveHourPct:      &fivePct,
				SevenDayPct:      &sevPct,
				SevenDayResetsAt: &sevRst,
				LimitsCapturedAt: &capAt,
			},
		}
		tree := convertStateToAggregateTree(st)
		if tree == nil {
			t.Fatal("convertStateToAggregateTree returned nil")
		}
		if tree.FiveHourPct == nil || *tree.FiveHourPct != fivePct {
			t.Errorf("FiveHourPct = %v, want %v", tree.FiveHourPct, fivePct)
		}
		if tree.SevenDayPct == nil || *tree.SevenDayPct != sevPct {
			t.Errorf("SevenDayPct = %v, want %v (real 0%%, not nil)", tree.SevenDayPct, sevPct)
		}
		if !tree.SevenDayResetsAt.Equal(sevRst) {
			t.Errorf("SevenDayResetsAt = %v, want %v", tree.SevenDayResetsAt, sevRst)
		}
		if !tree.LimitsCapturedAt.Equal(capAt) {
			t.Errorf("LimitsCapturedAt = %v, want %v", tree.LimitsCapturedAt, capAt)
		}
	})

	t.Run("unset stays unknown", func(t *testing.T) {
		st := &service.State{
			Now: now,
			Block: &store.Block{
				BlockID:   "b2",
				StartedAt: now.Add(-time.Hour),
				EndedAt:   now.Add(4 * time.Hour),
				// limits all nil.
			},
		}
		tree := convertStateToAggregateTree(st)
		if tree.FiveHourPct != nil {
			t.Errorf("FiveHourPct = %v, want nil (unknown, not 0)", *tree.FiveHourPct)
		}
		if tree.SevenDayPct != nil {
			t.Errorf("SevenDayPct = %v, want nil (unknown, not 0)", *tree.SevenDayPct)
		}
		if !tree.SevenDayResetsAt.IsZero() {
			t.Errorf("SevenDayResetsAt = %v, want zero (never 1970)", tree.SevenDayResetsAt)
		}
		if !tree.LimitsCapturedAt.IsZero() {
			t.Errorf("LimitsCapturedAt = %v, want zero (never 1970)", tree.LimitsCapturedAt)
		}
	})
}
