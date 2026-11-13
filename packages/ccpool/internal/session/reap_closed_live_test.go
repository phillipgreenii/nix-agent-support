package session

import (
	"context"
	"testing"
	"time"

	"github.com/phillipgreenii/ccpool/internal/store"
)

// closedLiveRow is one live row that already carries a close stamp (the close
// was decided and then interrupted before the session ended).
type closedLiveRow struct {
	id    string
	state store.State
	// activityOffsetSec is last_activity_at relative to the close stamp (now):
	// negative means the last activity predates the close.
	activityOffsetSec int64
}

func closedLiveFixture(t *testing.T, now time.Time, rows ...closedLiveRow) (*Service, map[string]bool, *store.Store) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(":memory:", &mutClock{t: now})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	live := map[string]bool{}
	closed := map[string]bool{}
	tm := &reapTmux{live: live, closed: closed}
	for _, r := range rows {
		if err := st.Insert(ctx, store.Session{
			ExternalID: r.id, ClaudeSessionID: "csid-" + r.id, State: r.state,
			TmuxSession: "cc-" + r.id, LastActivityAt: now.Unix() + r.activityOffsetSec,
		}); err != nil {
			t.Fatalf("Insert %s: %v", r.id, err)
		}
		if err := st.SetCloseReason(ctx, r.id, "handler"); err != nil {
			t.Fatalf("SetCloseReason %s: %v", r.id, err)
		}
		live["cc-"+r.id] = true
	}
	s := New(Deps{Tmux: tm, Trust: &fakeTrust{}, Store: st, Prefix: "cc-", Exister: fakeExister{ok: true}, Now: func() time.Time { return now }})
	return s, closed, st
}

// A row closed with reason handler whose session is still live (the close was
// interrupted after /exit) is finished by the next sweep even though it is far
// inside idle_ttl and the pool is under its cap.
func TestReap_finishesInterruptedCloseOfLiveClosedRow(t *testing.T) {
	withReapSpies(t, func(closures *[]string, _ *int, _ *[]int64) {
		now := time.Unix(10_000, 0)
		for _, state := range []store.State{store.Idle, store.Errored} {
			*closures = nil
			s, closed, st := closedLiveFixture(t, now, closedLiveRow{id: "stuck", state: state, activityOffsetSec: -60})
			if err := s.Reap(context.Background(), 6, 150*time.Minute); err != nil {
				t.Fatalf("Reap: %v", err)
			}
			if !closed["cc-stuck"] {
				t.Errorf("state %s: the live closed row's session was not ended", state)
			}
			got, _, _ := st.GetByExternalID(context.Background(), "stuck")
			if got.CloseReason != "handler" {
				t.Errorf("state %s: close_reason = %q, want the original handler kept", state, got.CloseReason)
			}
			if len(*closures) != 1 || (*closures)[0] != "closed_still_live" {
				t.Errorf("state %s: closures = %v, want [closed_still_live]", state, *closures)
			}
		}
	})
}

// The new pass spares what the sweep never closes: a needs_input row and a
// starting, ready or working row, and a row with activity after the close.
func TestReap_closedLivePassSparesProtectedRows(t *testing.T) {
	withReapSpies(t, func(closures *[]string, _ *int, _ *[]int64) {
		now := time.Unix(10_000, 0)
		s, closed, _ := closedLiveFixture(t, now,
			closedLiveRow{id: "asking", state: store.NeedsInput, activityOffsetSec: -60},
			closedLiveRow{id: "booting", state: store.Starting, activityOffsetSec: -60},
			closedLiveRow{id: "readyrow", state: store.Ready, activityOffsetSec: -60},
			closedLiveRow{id: "busy", state: store.Working, activityOffsetSec: -60},
			closedLiveRow{id: "resumed", state: store.Idle, activityOffsetSec: 10})
		if err := s.Reap(context.Background(), 10, 150*time.Minute); err != nil {
			t.Fatalf("Reap: %v", err)
		}
		if len(closed) != 0 {
			t.Errorf("sessions were torn down: %v", closed)
		}
		if len(*closures) != 0 {
			t.Errorf("closures recorded: %v", *closures)
		}
	})
}
