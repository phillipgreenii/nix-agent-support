package signal_test

import (
	"errors"
	"testing"

	"github.com/phillipgreenii/pa-monitor/internal/signal"
)

// TestClassifyCmuxFailure_StringFallback locks the substring classification: a
// delivery failure reaches the daemon as plain error text, so it is classified
// by substring matching. This is the single source of truth the daemon's
// classifySendFailure delegates to.
func TestClassifyCmuxFailure_StringFallback(t *testing.T) {
	cases := []struct {
		err        string
		wantReason signal.CmuxFailureReason
		wantTimed  bool
	}{
		{"no cmux surface found for pid 123", signal.ReasonNoSurface, false},
		{"cmux send-key failed: exit status 1", signal.ReasonSendKey, false},
		{"cmux --json top enumerate failed", signal.ReasonEnumerate, false},
		{"context deadline exceeded", signal.ReasonTimeout, true},
		{"cmux send: signal: killed", signal.ReasonTimeout, true},
		{"cmux send-key: signal: killed", signal.ReasonSendKey, true},
		{"cmux enumerate: signal: killed", signal.ReasonEnumerate, true},
		{"cmux enumerate failed: exit status 1", signal.ReasonEnumerate, false},
		{"operation timeout", signal.ReasonTimeout, true},
		{"dial unix: connection refused", signal.ReasonConnection, false},
		{"", signal.ReasonUnknown, false},
		{"something totally unexpected", signal.ReasonOther, false},
	}
	for _, tc := range cases {
		gotReason, gotTimed := signal.ClassifyCmuxFailure(errors.New(tc.err))
		if gotReason != tc.wantReason {
			t.Errorf("ClassifyCmuxFailure(%q) reason = %q, want %q", tc.err, gotReason, tc.wantReason)
		}
		if gotTimed != tc.wantTimed {
			t.Errorf("ClassifyCmuxFailure(%q) timed_out = %v, want %v", tc.err, gotTimed, tc.wantTimed)
		}
	}
}

// TestClassifyCmuxFailure_Nil verifies a nil error classifies as unknown.
func TestClassifyCmuxFailure_Nil(t *testing.T) {
	if r, timed := signal.ClassifyCmuxFailure(nil); r != signal.ReasonUnknown || timed {
		t.Errorf("ClassifyCmuxFailure(nil) = (%q, %v), want (unknown, false)", r, timed)
	}
}
