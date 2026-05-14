package cmuxstatus_test

import (
	"context"
	"testing"

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
