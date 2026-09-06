package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
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

func TestFanOutConfigValidate_DegradedOnSchemaVersionMismatch(t *testing.T) {
	// Before this fix, configValidateOne discarded InvokeCapabilities'
	// returned *CapabilitiesResponse entirely — even a backend openly
	// declaring a schemaVersion this build doesn't recognize passed as
	// "succeeded" [bug pg2-p2z7o]. schema.CurrentSchemaVersions expects
	// "pr" at schema.SchemaVersion (1 today); this fake backend declares
	// 999 for it, simulating a Tier-2 backend built at a stale/newer
	// commit than the umbrella.
	writeOpAwareFakeBackend(t, "backend-schema-skew", map[string]string{
		"auth_status":  `{"protocolVersion":1,"schemaVersion":1,"result":{"state":"OK"}}`,
		"capabilities": `{"protocolVersion":1,"schemaVersions":{"pr":999},"ops":["get_pr","auth_status","capabilities"]}`,
	}, `{"protocolVersion":1,"error":{"code":"unknown_op","message":"unknown op"}}`)
	outcome := FanOutConfigValidate(context.Background(), []string{"backend-schema-skew"})
	got := outcome.Sources[0]
	if got.Status != SourceDegraded {
		t.Fatalf("source = %+v, want degraded", got)
	}
	if !strings.Contains(got.Reason, "version mismatch") {
		t.Fatalf("reason = %q, want it to mention version mismatch", got.Reason)
	}
	if !strings.Contains(got.Reason, `"pr"`) || !strings.Contains(got.Reason, "999") {
		t.Fatalf("reason = %q, want it to name the mismatched capability and its reported version", got.Reason)
	}
}

func TestFanOutConfigValidate_DegradedOnCapabilitiesProtocolVersionMismatch(t *testing.T) {
	// Same skew, but on protocolVersion (the wire envelope itself) rather
	// than a capability's own schemaVersion — checked inside
	// scriptout.InvokeCapabilities, surfaced here the same way as any
	// other capabilities-check failure.
	writeOpAwareFakeBackend(t, "backend-protocol-skew", map[string]string{
		"auth_status":  `{"protocolVersion":1,"schemaVersion":1,"result":{"state":"OK"}}`,
		"capabilities": `{"protocolVersion":999,"schemaVersions":{"pr":1},"ops":["get_pr","auth_status","capabilities"]}`,
	}, `{"protocolVersion":1,"error":{"code":"unknown_op","message":"unknown op"}}`)
	outcome := FanOutConfigValidate(context.Background(), []string{"backend-protocol-skew"})
	got := outcome.Sources[0]
	if got.Status != SourceDegraded {
		t.Fatalf("source = %+v, want degraded", got)
	}
	if !strings.Contains(got.Reason, "version mismatch") {
		t.Fatalf("reason = %q, want it to mention version mismatch", got.Reason)
	}
}

func TestCheckSchemaVersions_UnknownCapability_IsNotAMismatch(t *testing.T) {
	// A capability key this build doesn't recognize (e.g. a future
	// attention/search-only plugin) is skipped, not flagged — this build
	// has no opinion on a capability it doesn't itself know about.
	resp := &scriptout.CapabilitiesResponse{
		ProtocolVersion: scriptout.ProtocolVersion,
		SchemaVersions:  map[string]int{"attention": 42},
	}
	if err := checkSchemaVersions(resp); err != nil {
		t.Fatalf("checkSchemaVersions: expected nil for an unrecognized capability, got %v", err)
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
