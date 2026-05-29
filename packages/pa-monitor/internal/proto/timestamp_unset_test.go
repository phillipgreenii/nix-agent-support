package proto

import (
	"testing"
)

// TestSessionViewUnsetRateLimitResetsAtRemainsZero is the regression test
// for an "all sessions show paused in the TUI" bug. The TUI rendered
// every session as rate-limited because timeFromTS treated a typed-nil
// *timestamppb.Timestamp as "set" -- Go interface equality means a typed
// nil pointer wrapped in an interface{ AsTime() time.Time } is NOT == nil,
// so the function fell through to ts.AsTime() on a nil receiver, which
// timestamppb implements as "return the epoch (1970)" -- a non-zero
// time.Time that IsZero() reports as false. Downstream the render code
// then took the rate-limited branch unconditionally.
//
// Locking the type as the concrete *timestamppb.Timestamp lets the nil
// check actually fire. This test exercises the round trip with the field
// deliberately left unset on the proto SessionView; the reconstructed
// aggregate.SessionView.RateLimitResetsAt must be the zero Time.
func TestSessionViewUnsetRateLimitResetsAtRemainsZero(t *testing.T) {
	sv := &SessionView{
		SessionId: "sid-1",
		Pid:       1234,
		Status:    "working",
		// RateLimitResetsAt is intentionally NOT set here.
	}
	view := sessionViewFromProto(sv)
	if view == nil {
		t.Fatal("sessionViewFromProto returned nil for a non-nil input")
	}
	if !view.RateLimitResetsAt.IsZero() {
		t.Errorf("unset RateLimitResetsAt should round-trip to zero Time; got %v (Unix=%d)",
			view.RateLimitResetsAt, view.RateLimitResetsAt.Unix())
	}
	if !view.StartedAt.IsZero() {
		t.Errorf("unset StartedAt should round-trip to zero Time; got %v", view.StartedAt)
	}
	if !view.TranscriptMTime.IsZero() {
		t.Errorf("unset TranscriptMTime should round-trip to zero Time; got %v", view.TranscriptMTime)
	}
}
