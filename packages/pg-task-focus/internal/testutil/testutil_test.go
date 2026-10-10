package testutil_test

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/command"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/testutil"
)

// failing is a testing.TB whose Fatalf records the failure and ends the
// goroutine that called it, as a real one does.
type failing struct {
	testing.TB
	message string
}

func (f *failing) Helper() {}

func (f *failing) Fatalf(format string, args ...any) {
	f.message = fmt.Sprintf(format, args...)
	runtime.Goexit()
}

// failureOf runs f against a failing TB and returns what it reported, or ""
// when f returned normally.
func failureOf(f func(t testing.TB)) string {
	tb := &failing{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		f(tb)
	}()
	<-done
	return tb.message
}

func TestRejectionOf(t *testing.T) {
	rejection := &command.Rejection{Reason: command.ReasonUnknownTask, Message: "no such task"}
	if got := testutil.RejectionOf(t, fmt.Errorf("wrapped: %w", rejection), command.ReasonUnknownTask); got.Message != "no such task" {
		t.Errorf("RejectionOf = %+v, want the wrapped rejection", got)
	}
	if msg := failureOf(func(tb testing.TB) { testutil.RejectionOf(tb, rejection, command.ReasonInvalidRequest) }); !strings.Contains(msg, "no such task") {
		t.Errorf("a wrong reason reported %q, want the rejection's message", msg)
	}
	if msg := failureOf(func(tb testing.TB) { testutil.RejectionOf(tb, errors.New("boom"), command.ReasonUnknownTask) }); !strings.Contains(msg, "not a *command.Rejection") {
		t.Errorf("a plain error reported %q", msg)
	}
	if msg := failureOf(func(tb testing.TB) { testutil.RejectionOf(tb, nil, command.ReasonUnknownTask) }); msg == "" {
		t.Error("a nil error was accepted as a rejection")
	}
}

func TestIDOfIsStableAndDistinct(t *testing.T) {
	base := time.Date(2026, time.October, 7, 12, 0, 0, 0, time.UTC)
	a := testutil.IDOf(base, 'C', 1)
	if a != testutil.IDOf(base, 'C', 1) {
		t.Error("the same arguments gave two ids")
	}
	seen := map[string]bool{string(a): true}
	for _, id := range []string{
		string(testutil.IDOf(base, 'C', 2)), string(testutil.IDOf(base, 'N', 1)), string(testutil.IDOf(base.Add(time.Hour), 'C', 1)),
	} {
		if seen[id] {
			t.Errorf("id %s repeats", id)
		}
		seen[id] = true
	}
}

func TestLoadConfigAppliesTheEdit(t *testing.T) {
	if cfg := testutil.LoadConfig(t, nil); cfg.Defaults().MaxFutureSkewSeconds == 0 {
		t.Error("the example configuration has no future skew")
	}
	cfg := testutil.LoadConfig(t, func(c map[string]any) {
		c["defaults"].(map[string]any)["max_future_skew_seconds"] = 7
	})
	if got := cfg.Defaults().MaxFutureSkewSeconds; got != 7 {
		t.Errorf("max_future_skew_seconds = %d, want the edited 7", got)
	}
}

func TestPtr(t *testing.T) {
	v := 3
	p := testutil.Ptr(v)
	v = 4
	if *p != 3 {
		t.Errorf("*Ptr(3) = %d after the original changed, want a copy", *p)
	}
}
