package otel

import (
	"context"
	"testing"
)

func TestNew_NilWhenEndpointEmpty(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	e, err := New(context.Background(), Options{ServiceName: "test"})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	if e != nil {
		t.Fatal("expected nil emitter when endpoint unset")
	}
}

func TestEmitter_NilSafeMethods(t *testing.T) {
	var e *Emitter
	// Methods MUST not panic on nil receiver.
	e.RecordSessionsCount(map[string]int{"working": 1}, nil)
	e.RecordCaffeinateActive(false, nil)
	e.RecordBlockCost(3.14, map[string]string{"plan_tier": "max_5x"})
	e.RecordWeekCost(42.0, nil)
	e.RecordBlockLimitHit(map[string]string{"plan_tier": "max_5x"})
	e.RecordWeekLimitHit(nil)
	e.RecordCaffeinateRound(nil)
	e.RecordCaffeinateGraceExpired(nil)
	e.RecordContextLimitHit(nil)
	e.RecordNudgeSent(nil)
	e.RecordNudgeSuppressed(nil)
	e.RecordNudgeQueued(nil)
	e.RecordApiErrorObserved(nil)
	e.RecordSessionsErrored(nil)
	e.LogEvent("test.event", nil)
	if err := e.Shutdown(context.Background()); err != nil {
		t.Errorf("nil Shutdown returned error: %v", err)
	}
}

func TestEmitter_RecordSessionsErrored_NilSafe(t *testing.T) {
	var e *Emitter
	// nil map should not panic
	e.RecordSessionsErrored(map[string]int{"rate_limit": 2})
}

func TestEmitter_RecordApiErrorObserved_NilSafe(t *testing.T) {
	var e *Emitter
	e.RecordApiErrorObserved(map[string]string{
		"session_id":  "sid-1",
		"kind":        "rate_limit",
		"is_terminal": "true",
	})
}

func TestEmitter_RecordNudgeSuppressed_NilSafe(t *testing.T) {
	var e *Emitter
	e.RecordNudgeSuppressed(map[string]string{
		"session_id": "sid-1",
		"sources":    "disrupted",
		"cause":      "session_active",
	})
}

func TestEmitter_RecordNudgeSent_WithLabels_NilSafe(t *testing.T) {
	var e *Emitter
	e.RecordNudgeSent(map[string]string{
		"session_id": "sid-1",
		"sources":    "disrupted,window_reset",
		"error_kind": "server_error",
		"escalated":  "false",
	})
}
