package main

import (
	"context"
	"testing"
)

func TestFanOutAuthStatus_Succeeded(t *testing.T) {
	writeFakeBackend(t, "backend-ok", `{"protocolVersion":1,"schemaVersion":1,"result":{"state":"OK"}}`)
	outcome := FanOutAuthStatus(context.Background(), []string{"backend-ok"})
	if len(outcome.Sources) != 1 || outcome.Sources[0].Status != SourceSucceeded {
		t.Fatalf("sources = %+v", outcome.Sources)
	}
	if outcome.ExitCode() != 0 {
		t.Fatalf("ExitCode = %d, want 0", outcome.ExitCode())
	}
}

func TestFanOutAuthStatus_Degraded(t *testing.T) {
	writeFakeBackend(t, "backend-missing", `{"protocolVersion":1,"schemaVersion":1,"result":{"state":"MISSING"}}`)
	outcome := FanOutAuthStatus(context.Background(), []string{"backend-missing"})
	if len(outcome.Sources) != 1 || outcome.Sources[0].Status != SourceDegraded {
		t.Fatalf("sources = %+v", outcome.Sources)
	}
	if outcome.ExitCode() != 3 {
		t.Fatalf("ExitCode = %d, want 3 (total failure with one source)", outcome.ExitCode())
	}
}

func TestFanOutAuthStatus_DisabledNotApplicable(t *testing.T) {
	writeFakeBackend(t, "backend-noauth", `{"protocolVersion":1,"error":{"code":"unknown_op","message":"unknown op \"auth_status\""}}`)
	outcome := FanOutAuthStatus(context.Background(), []string{"backend-noauth"})
	if len(outcome.Sources) != 1 {
		t.Fatalf("sources = %+v", outcome.Sources)
	}
	got := outcome.Sources[0]
	if got.Status != SourceDisabled || got.Reason != "not applicable" {
		t.Fatalf("source = %+v, want disabled/not applicable", got)
	}
}

func TestFanOutAuthStatus_NeverCollapsesSources(t *testing.T) {
	writeFakeBackend(t, "backend-a", `{"protocolVersion":1,"schemaVersion":1,"result":{"state":"OK"}}`)
	writeFakeBackend(t, "backend-b", `{"protocolVersion":1,"schemaVersion":1,"result":{"state":"OK"}}`)
	outcome := FanOutAuthStatus(context.Background(), []string{"backend-a", "backend-b"})
	if len(outcome.Sources) != 2 {
		t.Fatalf("expected one row per source, got %+v", outcome.Sources)
	}
}
