package query

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestTimerQuery_EmitsOneTickOfItsDeclaredType(t *testing.T) {
	q := TimerQuery{Meta: Meta{EmitTypes: []string{"timer.5m"}, Trig: PeriodTrigger{Every: 5 * time.Minute}}}
	if err := q.Validate(); err != nil {
		t.Fatal(err)
	}
	if q.BackingCommand() != "" {
		t.Errorf("a timer needs no executable; BackingCommand = %q", q.BackingCommand())
	}
	evts, err := q.Run(context.Background(), Env{})
	if err != nil || len(evts) != 1 {
		t.Fatalf("Run = (%v, %v), want exactly one tick event", evts, err)
	}
	if evts[0].Type != "timer.5m" || !strings.HasPrefix(evts[0].ID, "timer:timer.5m:") {
		t.Errorf("tick event = %+v", evts[0])
	}
	// Two firings must not collide on id (the dedup key).
	again, _ := q.Run(context.Background(), Env{})
	if again[0].ID == evts[0].ID {
		t.Errorf("two firings produced the same id %q", evts[0].ID)
	}
}

func TestTimerQuery_WithoutAnEmitTypeFailsLoudly(t *testing.T) {
	if _, err := (TimerQuery{}).Run(context.Background(), Env{}); err == nil {
		t.Fatal("a timer with no declared emit type must error, not emit an untyped event")
	}
}

// IsTimer is true for the timer emitter alone: gates never block it, and a
// command source is gated like any other emitter.
func TestIsTimer(t *testing.T) {
	if !IsTimer(TimerQuery{}) {
		t.Error("TimerQuery must be the timer emitter")
	}
	if IsTimer(CommandQuery{}) {
		t.Error("CommandQuery must not be the timer emitter")
	}
	if IsTimer(nil) {
		t.Error("nil is not the timer emitter")
	}
}
