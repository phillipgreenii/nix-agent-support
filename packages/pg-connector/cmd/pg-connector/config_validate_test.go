package main

import (
	"context"
	"encoding/json"
	"testing"
)

func TestFanOutConfigValidate_Succeeded(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-ok", map[string]string{
		"auth_status":  `{"protocolVersion":1,"schemaVersion":1,"result":{"state":"OK"}}`,
		"capabilities": `{"protocolVersion":1,"schemaVersions":{"pr":1},"ops":["get_pr","auth_status","capabilities"]}`,
	}, `{"protocolVersion":1,"error":{"code":"unknown_op","message":"unknown op"}}`)
	outcome := FanOutConfigValidate(context.Background(), []string{"backend-ok"})
	if len(outcome.Sources) != 1 {
		t.Fatalf("sources = %+v", outcome.Sources)
	}
	got := outcome.Sources[0]
	if got.Status != SourceSucceeded {
		t.Fatalf("source = %+v, want succeeded", got)
	}
}

func TestFanOutConfigValidate_DegradedOnAuthFailure(t *testing.T) {
	writeFakeBackend(t, "backend-bad-auth", `{"protocolVersion":1,"error":{"code":"unauthenticated","message":"bad token"}}`)
	outcome := FanOutConfigValidate(context.Background(), []string{"backend-bad-auth"})
	got := outcome.Sources[0]
	if got.Status != SourceDegraded {
		t.Fatalf("source = %+v, want degraded", got)
	}
}

func TestFanOutConfigValidate_NoBackends_SourcesIsEmptyArrayNotNull(t *testing.T) {
	// A misconfigured host with zero backends registered must still
	// marshal sources as [] — a nil slice marshals as null, which makes
	// `jq '.sources[]'` exit 5 exactly on the host that's misconfigured
	// [bug A15].
	outcome := FanOutConfigValidate(context.Background(), nil)
	raw, err := json.Marshal(outcome)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := string(raw); got != `{"sources":[]}` {
		t.Fatalf("json = %s, want sources to marshal as [] not null", got)
	}
}

func TestFanOutConfigValidate_NeverCollapsesSources(t *testing.T) {
	writeFakeBackend(t, "backend-a", `{"protocolVersion":1,"schemaVersions":{"pr":1},"ops":["capabilities"]}`)
	writeFakeBackend(t, "backend-b", `{"protocolVersion":1,"schemaVersions":{"pr":1},"ops":["capabilities"]}`)
	outcome := FanOutConfigValidate(context.Background(), []string{"backend-a", "backend-b"})
	if len(outcome.Sources) != 2 {
		t.Fatalf("expected one row per source, got %+v", outcome.Sources)
	}
}
