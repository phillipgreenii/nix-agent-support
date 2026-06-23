package otel

import (
	"context"
	"testing"
)

func TestConnectionEmitterDisabledWhenNoEndpoint(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	e, err := NewConnectionEmitter(context.Background(), ConnOptions{
		ServiceName: "pa-monitor", Component: "cmux-bridge",
	})
	if err != nil {
		t.Fatal(err)
	}
	if e != nil {
		t.Fatalf("want nil emitter when endpoint unset, got %#v", e)
	}
	e.RecordDaemonConnected(false)
	e.LogEvent("daemon.disconnect", map[string]string{"error": "x"})
	if err := e.Shutdown(context.Background()); err != nil {
		t.Fatalf("nil Shutdown: %v", err)
	}
}

func TestConnectionEmitterConstructsAndRecords(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:4317")
	e, err := NewConnectionEmitter(context.Background(), ConnOptions{
		ServiceName: "pa-monitor", ServiceVersion: "test", Component: "tui",
	})
	if err != nil {
		t.Fatalf("construct: %v", err)
	}
	if e == nil {
		t.Fatal("want non-nil emitter when endpoint set")
	}
	defer e.Shutdown(context.Background())
	e.RecordDaemonConnected(true)
	e.RecordDaemonConnected(false)
	if got := e.connectedValue(); got != 0 {
		t.Errorf("connectedValue = %d, want 0", got)
	}
}
