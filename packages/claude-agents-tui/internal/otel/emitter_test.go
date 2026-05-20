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
	e.LogEvent("test.event", nil)
	if err := e.Shutdown(context.Background()); err != nil {
		t.Errorf("nil Shutdown returned error: %v", err)
	}
}
