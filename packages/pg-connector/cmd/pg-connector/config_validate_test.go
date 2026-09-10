package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

func TestFanOutConfigValidate_Succeeded(t *testing.T) {
	// The fixture's declared "pr" schemaVersion is interpolated from
	// schema.PRSchemaVersion itself (not a hardcoded literal) so this
	// "matching, healthy backend" scenario cannot silently start exercising
	// TestFanOutConfigValidate_DegradedOnSchemaVersionMismatch's path
	// instead the next time schema.PRSchemaVersion is bumped (as bead
	// pg2-681xo's 1 -> 2 bump for AsOf/Stale already did once).
	writeOpAwareFakeBackend(t, "backend-ok", map[string]string{
		"auth_status":  `{"protocolVersion":1,"schemaVersion":1,"result":{"state":"OK"}}`,
		"capabilities": fmt.Sprintf(`{"protocolVersion":1,"schemaVersions":{"pr":%d},"ops":["get_pr","auth_status","capabilities"]}`, schema.PRSchemaVersion),
	}, `{"protocolVersion":1,"error":{"code":"unknown_op","message":"unknown op"}}`)
	outcome := FanOutConfigValidate(context.Background(), nil, []string{"backend-ok"})
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
	outcome := FanOutConfigValidate(context.Background(), nil, []string{"backend-bad-auth"})
	got := outcome.Sources[0]
	if got.Status != SourceDegraded {
		t.Fatalf("source = %+v, want degraded", got)
	}
}

func TestFanOutConfigValidate_CountReflectsChecksPassed(t *testing.T) {
	// Count must be the number of this source's two checks (auth_status,
	// capabilities) that actually came back healthy — not the hardcoded 0
	// that made a fully-degraded backend indistinguishable from one that
	// failed only one of its two checks [bug A16].
	t.Run("both checks healthy -> 2", func(t *testing.T) {
		writeOpAwareFakeBackend(t, "backend-both-ok", map[string]string{
			"auth_status":  `{"protocolVersion":1,"schemaVersion":1,"result":{"state":"OK"}}`,
			"capabilities": fmt.Sprintf(`{"protocolVersion":1,"schemaVersions":{"pr":%d},"ops":["get_pr","auth_status","capabilities"]}`, schema.PRSchemaVersion),
		}, `{"protocolVersion":1,"error":{"code":"unknown_op","message":"unknown op"}}`)
		outcome := FanOutConfigValidate(context.Background(), nil, []string{"backend-both-ok"})
		got := outcome.Sources[0]
		if got.Status != SourceSucceeded {
			t.Fatalf("source = %+v, want succeeded", got)
		}
		if got.Count != 2 {
			t.Fatalf("count = %d, want 2 (both checks healthy)", got.Count)
		}
	})

	t.Run("auth fails, capabilities healthy -> 1", func(t *testing.T) {
		writeOpAwareFakeBackend(t, "backend-auth-only-fails", map[string]string{
			"auth_status":  `{"protocolVersion":1,"error":{"code":"unauthenticated","message":"bad token"}}`,
			"capabilities": fmt.Sprintf(`{"protocolVersion":1,"schemaVersions":{"pr":%d},"ops":["get_pr","auth_status","capabilities"]}`, schema.PRSchemaVersion),
		}, `{"protocolVersion":1,"error":{"code":"unknown_op","message":"unknown op"}}`)
		outcome := FanOutConfigValidate(context.Background(), nil, []string{"backend-auth-only-fails"})
		got := outcome.Sources[0]
		if got.Status != SourceDegraded {
			t.Fatalf("source = %+v, want degraded", got)
		}
		if got.Count != 1 {
			t.Fatalf("count = %d, want 1 (capabilities healthy, auth_status failed)", got.Count)
		}
	})

	t.Run("both checks fail -> 0", func(t *testing.T) {
		writeFakeBackend(t, "backend-both-fail", `{"protocolVersion":1,"error":{"code":"unauthenticated","message":"bad token"}}`)
		outcome := FanOutConfigValidate(context.Background(), nil, []string{"backend-both-fail"})
		got := outcome.Sources[0]
		if got.Status != SourceDegraded {
			t.Fatalf("source = %+v, want degraded", got)
		}
		if got.Count != 0 {
			t.Fatalf("count = %d, want 0 (both checks failed)", got.Count)
		}
	})
}

func TestFanOutConfigValidate_NoBackends_SourcesIsEmptyArrayNotNull(t *testing.T) {
	// A misconfigured host with zero backends registered must still
	// marshal sources as [] — a nil slice marshals as null, which makes
	// `jq '.sources[]'` exit 5 exactly on the host that's misconfigured
	// [bug A15].
	outcome := FanOutConfigValidate(context.Background(), nil, nil)
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
	// "pr" at schema.PRSchemaVersion (2 as of bead pg2-681xo's AsOf/Stale
	// addition); this fake backend declares 999 for it, simulating a
	// Tier-2 backend built at a stale/newer commit than the umbrella.
	writeOpAwareFakeBackend(t, "backend-schema-skew", map[string]string{
		"auth_status":  `{"protocolVersion":1,"schemaVersion":1,"result":{"state":"OK"}}`,
		"capabilities": `{"protocolVersion":1,"schemaVersions":{"pr":999},"ops":["get_pr","auth_status","capabilities"]}`,
	}, `{"protocolVersion":1,"error":{"code":"unknown_op","message":"unknown op"}}`)
	outcome := FanOutConfigValidate(context.Background(), nil, []string{"backend-schema-skew"})
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
	outcome := FanOutConfigValidate(context.Background(), nil, []string{"backend-protocol-skew"})
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
	// standalone plugin capability schema.CurrentSchemaVersions has no
	// entry for yet) is skipped, not flagged — this build has no opinion
	// on a capability it doesn't itself know about. "attention" is no
	// longer a usable stand-in for "unknown" here — pg2-2j5ac.15.1 gave it
	// its own CurrentSchemaVersions entry, so a genuinely-unrecognized
	// fictional key is used instead.
	resp := &scriptout.CapabilitiesResponse{
		ProtocolVersion: scriptout.ProtocolVersion,
		SchemaVersions:  map[string]int{"not-a-real-capability": 42},
	}
	if err := checkSchemaVersions(resp); err != nil {
		t.Fatalf("checkSchemaVersions: expected nil for an unrecognized capability, got %v", err)
	}
}

func TestFanOutConfigValidate_NeverCollapsesSources(t *testing.T) {
	writeFakeBackend(t, "backend-a", `{"protocolVersion":1,"schemaVersions":{"pr":1},"ops":["capabilities"]}`)
	writeFakeBackend(t, "backend-b", `{"protocolVersion":1,"schemaVersions":{"pr":1},"ops":["capabilities"]}`)
	outcome := FanOutConfigValidate(context.Background(), nil, []string{"backend-a", "backend-b"})
	if len(outcome.Sources) != 2 {
		t.Fatalf("expected one row per source, got %+v", outcome.Sources)
	}
}

// --- query coverage (bead pg2-2j5ac.28.1) ---------------------------------

func TestQueryCoverageCheck_NotApplicable_NilRegistry(t *testing.T) {
	applicable, gaps, err := queryCoverageCheck(nil, "any-backend")
	if err != nil {
		t.Fatalf("queryCoverageCheck: %v", err)
	}
	if applicable {
		t.Fatal("expected not applicable for a backend registered under nothing")
	}
	if gaps != nil {
		t.Fatalf("gaps = %v, want nil", gaps)
	}
}

func TestQueryCoverageCheck_NotApplicable_CIOnlyBackend(t *testing.T) {
	reg, err := parseRegistry([]byte(`
connector:
  ci:
    - backend-ci-only
`), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}
	applicable, _, err := queryCoverageCheck(reg, "backend-ci-only")
	if err != nil {
		t.Fatalf("queryCoverageCheck: %v", err)
	}
	if applicable {
		t.Fatal("expected not applicable for a ci-only backend (list.go's own two exemptions)")
	}
}

func TestQueryCoverageCheck_NoGap_IdenticalQueries(t *testing.T) {
	reg, err := parseRegistry([]byte(`
connector:
  pr:
    - backend-a
    - backend-b
backends:
  backend-a:
    queries:
      team: "is:open author:@me"
  backend-b:
    queries:
      team: "is:open assignee:@me"
`), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}
	applicable, gaps, err := queryCoverageCheck(reg, "backend-a")
	if err != nil {
		t.Fatalf("queryCoverageCheck: %v", err)
	}
	if !applicable {
		t.Fatal("expected applicable for a pr-registered backend")
	}
	if len(gaps) != 0 {
		t.Fatalf("gaps = %v, want none (both backends declare \"team\")", gaps)
	}
}

func TestQueryCoverageCheck_Gap_MissingNameOnOneBackend(t *testing.T) {
	reg, err := parseRegistry([]byte(`
connector:
  pr:
    - backend-a
    - backend-b
backends:
  backend-a:
    queries:
      team: "is:open author:@me"
      focus: "is:open label:focus"
  backend-b:
    queries:
      team: "is:open assignee:@me"
`), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}
	applicable, gaps, err := queryCoverageCheck(reg, "backend-b")
	if err != nil {
		t.Fatalf("queryCoverageCheck: %v", err)
	}
	if !applicable {
		t.Fatal("expected applicable")
	}
	if len(gaps) != 1 || gaps[0] != "focus" {
		t.Fatalf("gaps = %v, want [\"focus\"] (backend-a declares it, backend-b does not)", gaps)
	}
	// The backend that DOES declare "focus" has no gap of its own.
	applicableA, gapsA, err := queryCoverageCheck(reg, "backend-a")
	if err != nil {
		t.Fatalf("queryCoverageCheck: %v", err)
	}
	if !applicableA || len(gapsA) != 0 {
		t.Fatalf("backend-a: applicable=%v gaps=%v, want applicable with no gaps", applicableA, gapsA)
	}
}

func TestFanOutConfigValidate_QueryCoverageGap_IsDegraded(t *testing.T) {
	writeOpAwareFakeBackend(t, "qc-backend-a", map[string]string{
		"auth_status":  `{"protocolVersion":1,"schemaVersion":1,"result":{"state":"OK"}}`,
		"capabilities": fmt.Sprintf(`{"protocolVersion":1,"schemaVersions":{"pr":%d},"ops":["show","auth_status","capabilities"]}`, schema.PRSchemaVersion),
	}, `{"protocolVersion":1,"error":{"code":"unknown_op","message":"unknown op"}}`)
	writeOpAwareFakeBackend(t, "qc-backend-b", map[string]string{
		"auth_status":  `{"protocolVersion":1,"schemaVersion":1,"result":{"state":"OK"}}`,
		"capabilities": fmt.Sprintf(`{"protocolVersion":1,"schemaVersions":{"pr":%d},"ops":["show","auth_status","capabilities"]}`, schema.PRSchemaVersion),
	}, `{"protocolVersion":1,"error":{"code":"unknown_op","message":"unknown op"}}`)

	reg, err := parseRegistry([]byte(`
connector:
  pr:
    - qc-backend-a
    - qc-backend-b
backends:
  qc-backend-a:
    queries:
      team: "is:open author:@me"
      focus: "is:open label:focus"
  qc-backend-b:
    queries:
      team: "is:open assignee:@me"
`), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}

	outcome := FanOutConfigValidate(context.Background(), reg, []string{"qc-backend-a", "qc-backend-b"})
	if len(outcome.Sources) != 2 {
		t.Fatalf("sources = %+v", outcome.Sources)
	}
	byName := map[string]SourceResult{}
	for _, s := range outcome.Sources {
		byName[s.Source] = s
	}
	if byName["qc-backend-a"].Status != SourceSucceeded {
		t.Fatalf("qc-backend-a = %+v, want succeeded (it declares every query name)", byName["qc-backend-a"])
	}
	b := byName["qc-backend-b"]
	if b.Status != SourceDegraded {
		t.Fatalf("qc-backend-b = %+v, want degraded (missing \"focus\")", b)
	}
	if !strings.Contains(b.Reason, "focus") {
		t.Fatalf("reason = %q, want it to mention the missing query name", b.Reason)
	}
}
