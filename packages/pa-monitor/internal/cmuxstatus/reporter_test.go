package cmuxstatus_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/cmuxstatus"
)

func TestNewReporterReturnsNoopWhenNotInCmux(t *testing.T) {
	lookup := func(string) (string, bool) { return "", false }
	calls := 0
	run := func(context.Context, string, ...string) ([]byte, error) {
		calls++
		return nil, nil
	}
	r := cmuxstatus.NewReporter(cmuxstatus.Options{
		Enable:    true,
		RunCmd:    run,
		LookupEnv: lookup,
	})
	r.Push(cmuxstatus.Snapshot{CaffeinateOn: true, HasProgress: true, Progress: 0.5})
	r.Notify("t", "b")
	r.Clear()
	if calls != 0 {
		t.Errorf("Noop should produce 0 subprocess calls; got %d", calls)
	}
}

func TestNewReporterReturnsNoopWhenDisabled(t *testing.T) {
	lookup := func(k string) (string, bool) {
		if k == "CMUX_WORKSPACE_ID" {
			return "workspace:1", true
		}
		return "", false
	}
	calls := 0
	run := func(context.Context, string, ...string) ([]byte, error) {
		calls++
		return nil, nil
	}
	r := cmuxstatus.NewReporter(cmuxstatus.Options{
		Enable:    false,
		RunCmd:    run,
		LookupEnv: lookup,
	})
	r.Push(cmuxstatus.Snapshot{})
	if calls != 0 {
		t.Errorf("disabled reporter should produce 0 subprocess calls; got %d", calls)
	}
}

// recordingRun returns a RunCmd that appends every "cmux <args>" invocation to
// *calls. Always returns empty bytes and no error.
func recordingRun(calls *[]string) func(context.Context, string, ...string) ([]byte, error) {
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "cmux" {
			return nil, fmt.Errorf("unexpected command: %s", name)
		}
		*calls = append(*calls, "cmux "+strings.Join(args, " "))
		return []byte(""), nil
	}
}

// inCmuxEnv produces a LookupEnv stub claiming we are inside cmux.
func inCmuxEnv() func(string) (string, bool) {
	return func(k string) (string, bool) {
		if k == "CMUX_WORKSPACE_ID" {
			return "workspace:1", true
		}
		return "", false
	}
}

func TestCmuxPushEmitsOneStatusPlusProgress(t *testing.T) {
	var calls []string
	r := cmuxstatus.NewReporter(cmuxstatus.Options{
		Enable:    true,
		RunCmd:    recordingRun(&calls),
		LookupEnv: inCmuxEnv(),
	})
	r.Push(cmuxstatus.Snapshot{
		CaffeinateOn:  true,
		NudgeOn:       true,
		State:         cmuxstatus.StateWorking,
		Progress:      0.5,
		ProgressLabel: "5h block 50% of cap",
		HasProgress:   true,
	})
	if len(calls) != 2 {
		t.Fatalf("expected 2 cmux calls (1 set-status + 1 set-progress), got %d: %v", len(calls), calls)
	}
	if !strings.Contains(calls[0], "set-status claude-agents") {
		t.Errorf("call[0] = %q, want set-status claude-agents", calls[0])
	}
	if !strings.Contains(calls[0], "working • caff • nudge") {
		t.Errorf("call[0] = %q, want value 'working • caff • nudge'", calls[0])
	}
	if !strings.Contains(calls[0], "--icon play") {
		t.Errorf("call[0] = %q, want --icon play (working)", calls[0])
	}
	if !strings.Contains(calls[1], "set-progress 0.50") {
		t.Errorf("call[1] = %q, want set-progress 0.50", calls[1])
	}
}

func TestCmuxPushOmitsToggleSuffixesWhenOff(t *testing.T) {
	var calls []string
	r := cmuxstatus.NewReporter(cmuxstatus.Options{
		Enable:    true,
		RunCmd:    recordingRun(&calls),
		LookupEnv: inCmuxEnv(),
	})
	r.Push(cmuxstatus.Snapshot{
		CaffeinateOn: false,
		NudgeOn:      false,
		State:        cmuxstatus.StateIdle,
		HasProgress:  false,
	})
	if len(calls) != 1 {
		t.Fatalf("expected 1 cmux call (set-status only, no progress), got %d: %v", len(calls), calls)
	}
	// Value should be exactly "idle" with no trailing toggle text.
	if !strings.Contains(calls[0], "set-status claude-agents idle ") {
		t.Errorf("call[0] = %q, want set-status claude-agents 'idle' with no • suffix", calls[0])
	}
	if strings.Contains(calls[0], "caff") || strings.Contains(calls[0], "nudge") {
		t.Errorf("call[0] = %q, want no caff/nudge suffix when both toggles off", calls[0])
	}
}

func TestCmuxPushClampsProgress(t *testing.T) {
	cases := []struct {
		in     float64
		wanted string
	}{
		{-1, "set-progress 0.00"},
		{0.5, "set-progress 0.50"},
		{2.5, "set-progress 1.00"},
	}
	for _, tc := range cases {
		var calls []string
		r := cmuxstatus.NewReporter(cmuxstatus.Options{
			Enable:    true,
			RunCmd:    recordingRun(&calls),
			LookupEnv: inCmuxEnv(),
		})
		r.Push(cmuxstatus.Snapshot{HasProgress: true, Progress: tc.in, ProgressLabel: "x"})
		if len(calls) < 2 {
			t.Fatalf("in=%v: expected 2 calls, got %d: %v", tc.in, len(calls), calls)
		}
		if !strings.Contains(calls[1], tc.wanted) {
			t.Errorf("in=%v: call[1]=%q, want substring %q", tc.in, calls[1], tc.wanted)
		}
	}
}

func TestCmuxPushCaffOnlyShowsCaff(t *testing.T) {
	var calls []string
	r := cmuxstatus.NewReporter(cmuxstatus.Options{
		Enable:    true,
		RunCmd:    recordingRun(&calls),
		LookupEnv: inCmuxEnv(),
	})
	r.Push(cmuxstatus.Snapshot{
		CaffeinateOn: true,
		NudgeOn:      false,
		State:        cmuxstatus.StateWorking,
		HasProgress:  false,
	})
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if !strings.Contains(calls[0], "working • caff") {
		t.Errorf("call[0] = %q, want value 'working • caff'", calls[0])
	}
	if strings.Contains(calls[0], "nudge") {
		t.Errorf("call[0] = %q, want no nudge suffix when nudge off", calls[0])
	}
}

func TestCmuxPushNudgeOnlyShowsNudge(t *testing.T) {
	var calls []string
	r := cmuxstatus.NewReporter(cmuxstatus.Options{
		Enable:    true,
		RunCmd:    recordingRun(&calls),
		LookupEnv: inCmuxEnv(),
	})
	r.Push(cmuxstatus.Snapshot{
		CaffeinateOn: false,
		NudgeOn:      true,
		State:        cmuxstatus.StateWorking,
		HasProgress:  false,
	})
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if !strings.Contains(calls[0], "working • nudge") {
		t.Errorf("call[0] = %q, want value 'working • nudge'", calls[0])
	}
	if strings.Contains(calls[0], "caff") {
		t.Errorf("call[0] = %q, want no caff suffix when caff off", calls[0])
	}
}

func TestCmuxPushPausedStateIncludesResetTime(t *testing.T) {
	var calls []string
	r := cmuxstatus.NewReporter(cmuxstatus.Options{
		Enable:    true,
		RunCmd:    recordingRun(&calls),
		LookupEnv: inCmuxEnv(),
	})
	resetAt := time.Date(2026, 5, 14, 15, 30, 0, 0, time.UTC)
	r.Push(cmuxstatus.Snapshot{
		State:         cmuxstatus.StatePaused,
		PausedResetAt: resetAt,
	})
	if len(calls) < 1 {
		t.Fatalf("expected ≥ 1 call, got %d", len(calls))
	}
	if !strings.Contains(calls[0], "paused (resets 15:30)") {
		t.Errorf("call[0] = %q, want value 'paused (resets 15:30)'", calls[0])
	}
}

func TestCmuxNotifyEmitsCmuxNotify(t *testing.T) {
	var calls []string
	r := cmuxstatus.NewReporter(cmuxstatus.Options{
		Enable:    true,
		RunCmd:    recordingRun(&calls),
		LookupEnv: inCmuxEnv(),
	})
	r.Notify("pa-monitor", "5h reset, nudged 3 sessions")
	if len(calls) != 1 {
		t.Fatalf("expected 1 cmux notify call, got %d: %v", len(calls), calls)
	}
	if !strings.Contains(calls[0], "notify --title pa-monitor --body 5h reset, nudged 3 sessions") {
		t.Errorf("call = %q, want cmux notify with title+body", calls[0])
	}
}

func TestCmuxClearIssuesTwoCalls(t *testing.T) {
	var calls []string
	r := cmuxstatus.NewReporter(cmuxstatus.Options{
		Enable:    true,
		RunCmd:    recordingRun(&calls),
		LookupEnv: inCmuxEnv(),
	})
	r.Clear()
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls (1 clear-status + 1 clear-progress), got %d: %v", len(calls), calls)
	}
	if !strings.Contains(calls[0], "clear-status claude-agents") {
		t.Errorf("call[0] = %q, want clear-status claude-agents", calls[0])
	}
	if !strings.Contains(calls[1], "clear-progress") {
		t.Errorf("call[1] = %q, want clear-progress", calls[1])
	}
}

func TestCmuxPushPartialFailureContinuesAndLogs(t *testing.T) {
	var calls []string
	var logs []string
	// Fail the FIRST call (status); the second (progress) must still attempt.
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, "cmux "+strings.Join(args, " "))
		if len(calls) == 1 {
			return nil, fmt.Errorf("simulated status failure")
		}
		return []byte(""), nil
	}
	r := cmuxstatus.NewReporter(cmuxstatus.Options{
		Enable:    true,
		RunCmd:    run,
		LookupEnv: inCmuxEnv(),
		Logf:      func(s string) { logs = append(logs, s) },
	})
	r.Push(cmuxstatus.Snapshot{HasProgress: true, Progress: 0.1, ProgressLabel: "x"})
	if len(calls) != 2 {
		t.Errorf("expected 2 attempts (status fail + progress retry), got %d: %v", len(calls), calls)
	}
	if len(logs) != 1 {
		t.Errorf("expected 1 log line for the failed call, got %d: %v", len(logs), logs)
	}
}
