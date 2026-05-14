package cmuxstatus_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/claude-agents-tui/internal/cmuxstatus"
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

func TestCmuxPushEmitsFourSubprocessCalls(t *testing.T) {
	var calls []string
	r := cmuxstatus.NewReporter(cmuxstatus.Options{
		Enable:    true,
		RunCmd:    recordingRun(&calls),
		LookupEnv: inCmuxEnv(),
	})
	r.Push(cmuxstatus.Snapshot{
		CaffeinateOn:  true,
		NudgeOn:       false,
		State:         cmuxstatus.StateWorking,
		Progress:      0.5,
		ProgressLabel: "5h block 50% used",
		HasProgress:   true,
	})
	if len(calls) != 4 {
		t.Fatalf("expected 4 cmux calls (3 set-status + 1 set-progress), got %d: %v", len(calls), calls)
	}
	if !strings.Contains(calls[0], "set-status caffeinate on") {
		t.Errorf("call[0] = %q, want set-status caffeinate on", calls[0])
	}
	if !strings.Contains(calls[1], "set-status nudge off") {
		t.Errorf("call[1] = %q, want set-status nudge off", calls[1])
	}
	if !strings.Contains(calls[2], "set-status state working") {
		t.Errorf("call[2] = %q, want set-status state working", calls[2])
	}
	if !strings.Contains(calls[3], "set-progress 0.50 --label 5h block 50% used") {
		t.Errorf("call[3] = %q, want set-progress 0.50 --label '5h block 50%% used'", calls[3])
	}
}

func TestCmuxPushSkipsProgressWhenHasProgressFalse(t *testing.T) {
	var calls []string
	r := cmuxstatus.NewReporter(cmuxstatus.Options{
		Enable:    true,
		RunCmd:    recordingRun(&calls),
		LookupEnv: inCmuxEnv(),
	})
	r.Push(cmuxstatus.Snapshot{
		State:       cmuxstatus.StateIdle,
		HasProgress: false,
	})
	if len(calls) != 3 {
		t.Fatalf("expected 3 cmux calls (no progress), got %d: %v", len(calls), calls)
	}
	for _, c := range calls {
		if strings.Contains(c, "set-progress") {
			t.Errorf("unexpected set-progress call: %q", c)
		}
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
		if len(calls) < 4 {
			t.Fatalf("in=%v: expected 4 calls, got %d: %v", tc.in, len(calls), calls)
		}
		if !strings.Contains(calls[3], tc.wanted) {
			t.Errorf("in=%v: call[3]=%q, want substring %q", tc.in, calls[3], tc.wanted)
		}
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
	if len(calls) < 3 {
		t.Fatalf("expected ≥ 3 calls, got %d", len(calls))
	}
	if !strings.Contains(calls[2], "paused") {
		t.Errorf("state call = %q, want it to mention paused", calls[2])
	}
	// Wall-clock formatting: just check the hour-minute renders into the value.
	if !strings.Contains(calls[2], "15:30") {
		t.Errorf("state call = %q, want it to mention the reset time 15:30", calls[2])
	}
}

func TestCmuxNotifyEmitsCmuxNotify(t *testing.T) {
	var calls []string
	r := cmuxstatus.NewReporter(cmuxstatus.Options{
		Enable:    true,
		RunCmd:    recordingRun(&calls),
		LookupEnv: inCmuxEnv(),
	})
	r.Notify("claude-agents-tui", "5h reset, nudged 3 sessions")
	if len(calls) != 1 {
		t.Fatalf("expected 1 cmux notify call, got %d: %v", len(calls), calls)
	}
	if !strings.Contains(calls[0], "notify --title claude-agents-tui --body 5h reset, nudged 3 sessions") {
		t.Errorf("call = %q, want cmux notify with title+body", calls[0])
	}
}

func TestCmuxClearIssuesFourCalls(t *testing.T) {
	var calls []string
	r := cmuxstatus.NewReporter(cmuxstatus.Options{
		Enable:    true,
		RunCmd:    recordingRun(&calls),
		LookupEnv: inCmuxEnv(),
	})
	r.Clear()
	if len(calls) != 4 {
		t.Fatalf("expected 4 clear calls, got %d: %v", len(calls), calls)
	}
	want := []string{
		"clear-status caffeinate",
		"clear-status nudge",
		"clear-status state",
		"clear-progress",
	}
	for i, w := range want {
		if !strings.Contains(calls[i], w) {
			t.Errorf("call[%d] = %q, want substring %q", i, calls[i], w)
		}
	}
}

func TestCmuxPushPartialFailureContinuesAndLogs(t *testing.T) {
	var calls []string
	var logs []string
	// Fail the SECOND call (nudge); the third and fourth must still attempt.
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, "cmux "+strings.Join(args, " "))
		if len(calls) == 2 {
			return nil, fmt.Errorf("simulated nudge failure")
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
	if len(calls) != 4 {
		t.Errorf("expected 4 attempts despite failure, got %d: %v", len(calls), calls)
	}
	if len(logs) != 1 {
		t.Errorf("expected 1 log line for the failed call, got %d: %v", len(logs), logs)
	}
	if len(logs) >= 1 && !strings.Contains(logs[0], "simulated nudge failure") {
		t.Errorf("log[0] = %q, want it to mention the failure", logs[0])
	}
}
