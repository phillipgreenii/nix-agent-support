package signal_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/phillipgreenii/pa-monitor/internal/signal"
)

// TestClassifyCmuxFailure_Typed is the typed-API expression of the pg2-qxo5 /
// pg2-zixk classification cases: given a *CmuxError (or *NoCmuxSurfaceError)
// produced by the cmux Gateway, the reason is derived from the typed Command +
// TimedOut fields via a type switch — not by substring-matching the message.
// The enumerate/send-key PATH wins over the generic timeout reason (pg2-qxo5),
// while timed_out is surfaced ORTHOGONALLY so a path-timeout keeps its path
// reason AND reports timed_out=true (pg2-zixk).
func TestClassifyCmuxFailure_Typed(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantReason signal.CmuxFailureReason
		wantTimed  bool
	}{
		{
			name:       "enumerate timeout keeps path reason, timed_out true",
			err:        &signal.CmuxError{Command: signal.CmuxEnumerate, TimedOut: true, Underlying: errors.New("cmux --json top --processes: signal: killed")},
			wantReason: signal.ReasonEnumerate,
			wantTimed:  true,
		},
		{
			name:       "enumerate genuine exit 1 stays not-timed-out",
			err:        &signal.CmuxError{Command: signal.CmuxEnumerate, TimedOut: false, Underlying: errors.New("exit status 1")},
			wantReason: signal.ReasonEnumerate,
			wantTimed:  false,
		},
		{
			name:       "send-key timeout keeps path reason, timed_out true",
			err:        &signal.CmuxError{Command: signal.CmuxSendKey, TimedOut: true, Underlying: errors.New("signal: killed")},
			wantReason: signal.ReasonSendKey,
			wantTimed:  true,
		},
		{
			name:       "send-key genuine exit 1 stays not-timed-out",
			err:        &signal.CmuxError{Command: signal.CmuxSendKey, TimedOut: false, Underlying: errors.New("exit status 1")},
			wantReason: signal.ReasonSendKey,
			wantTimed:  false,
		},
		{
			name:       "plain send timeout has no path reason -> timeout",
			err:        &signal.CmuxError{Command: signal.CmuxSend, TimedOut: true, Underlying: errors.New("signal: killed")},
			wantReason: signal.ReasonTimeout,
			wantTimed:  true,
		},
		{
			name:       "plain send connection failure -> connection",
			err:        &signal.CmuxError{Command: signal.CmuxSend, TimedOut: false, Underlying: errors.New("dial unix: connection refused")},
			wantReason: signal.ReasonConnection,
			wantTimed:  false,
		},
		{
			name:       "plain send unclassifiable failure -> other",
			err:        &signal.CmuxError{Command: signal.CmuxSend, TimedOut: false, Underlying: errors.New("exit status 1")},
			wantReason: signal.ReasonOther,
			wantTimed:  false,
		},
		{
			name:       "no surface is never a subprocess timeout",
			err:        &signal.NoCmuxSurfaceError{PID: 123},
			wantReason: signal.ReasonNoSurface,
			wantTimed:  false,
		},
		{
			// The real CmuxSignaler.Send wraps the enumerate CmuxError with
			// fmt.Errorf("cmux enumerate: %w", ...); errors.As must still find it.
			name:       "wrapped enumerate CmuxError is still classified typed",
			err:        fmt.Errorf("cmux enumerate: %w", &signal.CmuxError{Command: signal.CmuxEnumerate, TimedOut: true, Underlying: errors.New("cmux --json top --processes: signal: killed")}),
			wantReason: signal.ReasonEnumerate,
			wantTimed:  true,
		},
		{
			name:       "wrapped send-key CmuxError is still classified typed",
			err:        fmt.Errorf("cmux send-key: %w", &signal.CmuxError{Command: signal.CmuxSendKey, TimedOut: true, Underlying: errors.New("signal: killed")}),
			wantReason: signal.ReasonSendKey,
			wantTimed:  true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotReason, gotTimed := signal.ClassifyCmuxFailure(tc.err)
			if gotReason != tc.wantReason {
				t.Errorf("reason = %q, want %q", gotReason, tc.wantReason)
			}
			if gotTimed != tc.wantTimed {
				t.Errorf("timed_out = %v, want %v", gotTimed, tc.wantTimed)
			}
		})
	}
}

// TestClassifyCmuxFailure_StringFallback locks the documented fallback: an error
// that did NOT come through the Gateway as a typed value — e.g. one reconstructed
// from a string that crossed the cmux-bridge gRPC boundary (ADR 0022), where the
// daemon can only ever see text — is classified by substring matching, preserving
// the exact pg2-qxo5/pg2-zixk taxonomy. This is the single source of truth the
// daemon's classifySendFailure now delegates to.
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
		{"cmux enumerate: cmux --json top --processes: signal: killed", signal.ReasonEnumerate, true},
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

// failingCmuxRun returns a RunCmd that succeeds at enumeration (yielding one
// surface hosting failPID) but fails the named cmux subcommand ("send" or
// "send-key") with runErr, so Send exercises the typed-error path for that
// command. When failCmd is "enumerate" the enumeration itself fails.
func failingCmuxRun(failCmd string, failPID int, runErr error) func(context.Context, string, ...string) ([]byte, error) {
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "ps" {
			// Ancestry lookups: report no parent so findSurfaceForPID stops.
			return []byte(""), nil
		}
		if name != "cmux" {
			return nil, fmt.Errorf("unexpected command: %s", name)
		}
		if len(args) >= 3 && args[0] == "--json" && args[1] == "top" && args[2] == "--processes" {
			if failCmd == "enumerate" {
				return nil, runErr
			}
			body := fmt.Sprintf(`{"windows":[{"ref":"window:1","workspaces":[{"ref":"workspace:1","panes":[{"ref":"pane:1","surfaces":[{"ref":"surface:1","type":"terminal","tty":"ttysX","tty_process_pids":[%d]}]}]}]}]}`, failPID)
			return []byte(body), nil
		}
		if len(args) >= 1 && args[0] == failCmd {
			return nil, runErr
		}
		return []byte(""), nil
	}
}

// TestCmuxSignalerSend_ReturnsTypedFailures proves the Gateway is real, not a
// facade: CmuxSignaler.Send returns errors that errors.As-match the typed cmux
// failure types with the correct Command tag, and ClassifyCmuxFailure classifies
// them via the typed path in-process (the consumer the bridge can use directly).
func TestCmuxSignalerSend_ReturnsTypedFailures(t *testing.T) {
	env := func(m map[string]string) func(string) (string, bool) {
		return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
	}

	t.Run("no surface -> *NoCmuxSurfaceError", func(t *testing.T) {
		sig := &signal.CmuxSignaler{
			RunCmd:    failingCmuxRun("send", 4242, nil), // surface hosts 4242, we send to 999
			LookupEnv: env(map[string]string{"CMUX_WORKSPACE_ID": "workspace:1"}),
		}
		err := sig.Send(999, "continue")
		if err == nil {
			t.Fatal("expected error for pid with no surface")
		}
		var ns *signal.NoCmuxSurfaceError
		if !errors.As(err, &ns) {
			t.Fatalf("error %v (%T) is not a *NoCmuxSurfaceError", err, err)
		}
		if ns.PID != 999 {
			t.Errorf("NoCmuxSurfaceError.PID = %d, want 999", ns.PID)
		}
		if r, _ := signal.ClassifyCmuxFailure(err); r != signal.ReasonNoSurface {
			t.Errorf("classified reason = %q, want no_surface", r)
		}
	})

	t.Run("enumerate failure -> *CmuxError{Enumerate}", func(t *testing.T) {
		sig := &signal.CmuxSignaler{
			RunCmd:    failingCmuxRun("enumerate", 0, errors.New("exit status 1")),
			LookupEnv: env(map[string]string{"CMUX_WORKSPACE_ID": "workspace:1"}),
		}
		err := sig.Send(4242, "continue")
		var ce *signal.CmuxError
		if !errors.As(err, &ce) {
			t.Fatalf("error %v (%T) is not a *CmuxError", err, err)
		}
		if ce.Command != signal.CmuxEnumerate {
			t.Errorf("Command = %q, want enumerate", ce.Command)
		}
		if ce.TimedOut {
			t.Error("a plain exit status 1 must not be flagged timed_out")
		}
		if r, timed := signal.ClassifyCmuxFailure(err); r != signal.ReasonEnumerate || timed {
			t.Errorf("classified = (%q, %v), want (enumerate, false)", r, timed)
		}
	})

	t.Run("send-key timeout -> *CmuxError{SendKey, timed_out}", func(t *testing.T) {
		sig := &signal.CmuxSignaler{
			RunCmd:    failingCmuxRun("send-key", 4242, errors.New("signal: killed")),
			LookupEnv: env(map[string]string{"CMUX_WORKSPACE_ID": "workspace:1"}),
		}
		err := sig.Send(4242, "continue")
		var ce *signal.CmuxError
		if !errors.As(err, &ce) {
			t.Fatalf("error %v (%T) is not a *CmuxError", err, err)
		}
		if ce.Command != signal.CmuxSendKey {
			t.Errorf("Command = %q, want send-key", ce.Command)
		}
		if !ce.TimedOut {
			t.Error("a signal: killed subprocess must be flagged timed_out")
		}
		if r, timed := signal.ClassifyCmuxFailure(err); r != signal.ReasonSendKey || !timed {
			t.Errorf("classified = (%q, %v), want (send_key, true)", r, timed)
		}
		// The message must stay byte-identical to the pre-refactor wrapping so
		// the string that crosses the gRPC boundary is unchanged.
		if !strings.Contains(err.Error(), "cmux send-key: signal: killed") {
			t.Errorf("error message = %q, want it to contain 'cmux send-key: signal: killed'", err.Error())
		}
	})

	t.Run("send failure -> *CmuxError{Send}", func(t *testing.T) {
		sig := &signal.CmuxSignaler{
			RunCmd:    failingCmuxRun("send", 4242, errors.New("exit status 1")),
			LookupEnv: env(map[string]string{"CMUX_WORKSPACE_ID": "workspace:1"}),
		}
		err := sig.Send(4242, "continue")
		var ce *signal.CmuxError
		if !errors.As(err, &ce) {
			t.Fatalf("error %v (%T) is not a *CmuxError", err, err)
		}
		if ce.Command != signal.CmuxSend {
			t.Errorf("Command = %q, want send", ce.Command)
		}
		if !strings.Contains(err.Error(), "cmux send: exit status 1") {
			t.Errorf("error message = %q, want it to contain 'cmux send: exit status 1'", err.Error())
		}
	})
}
