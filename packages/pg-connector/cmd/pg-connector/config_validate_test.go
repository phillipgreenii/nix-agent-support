package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

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

func TestFanOutConfigValidate_QueryCoverageGap_IsInformationalOnly(t *testing.T) {
	// OPERATOR DECISION (2026-09-18, bead pg2-rnnfz): queryCoverageCheck's
	// gaps no longer affect configValidateOne's Status/Reason/Count — the
	// check still runs (its own applicable/gaps computation is covered by
	// the TestQueryCoverageCheck_* tests above), but a backend with a
	// real query-coverage gap and otherwise-healthy auth/capabilities now
	// reports succeeded, with no gap-related reason and a count reflecting
	// only its two checks that actually ran. Supersedes this test's
	// previous incarnation,
	// TestFanOutConfigValidate_QueryCoverageGap_IsDegraded, which asserted
	// the pre-pg2-rnnfz degraded-on-gap behavior.
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
	a := byName["qc-backend-a"]
	if a.Status != SourceSucceeded {
		t.Fatalf("qc-backend-a = %+v, want succeeded (it declares every query name)", a)
	}
	if a.Count != 2 {
		t.Fatalf("qc-backend-a count = %d, want 2 (auth_status + capabilities only)", a.Count)
	}
	// qc-backend-b has a real query-coverage gap ("focus", declared only
	// by qc-backend-a) but must still report succeeded: query coverage is
	// informational-only and must not affect Status, Reason, or Count.
	b := byName["qc-backend-b"]
	if b.Status != SourceSucceeded {
		t.Fatalf("qc-backend-b = %+v, want succeeded (query coverage is informational-only per pg2-rnnfz)", b)
	}
	if b.Reason != "" {
		t.Fatalf("qc-backend-b reason = %q, want empty (query coverage must not contribute a reason)", b.Reason)
	}
	if b.Count != 2 {
		t.Fatalf("qc-backend-b count = %d, want 2 (query coverage must not contribute to count)", b.Count)
	}
}

// writeActivityConfig writes a registry with one connector.pr backend plus
// the given activity.sources list and points $PG_PR_CONFIG at it.
func writeActivityConfig(t *testing.T, prBackend string, activitySources []string) {
	t.Helper()
	body := "connector:\n  pr:\n    - " + prBackend + "\n"
	if len(activitySources) > 0 {
		body += "activity:\n  sources:\n"
		for _, s := range activitySources {
			body += "    - " + s + "\n"
		}
	}
	cfg := t.TempDir() + "/config.yaml"
	if err := os.WriteFile(cfg, []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("PG_PR_CONFIG", cfg)
}

// writeHealthyPRBackend writes a fake connector.pr backend that passes both
// config validate checks.
func writeHealthyPRBackend(t *testing.T, name string) {
	t.Helper()
	writeOpAwareFakeBackend(t, name, map[string]string{
		"auth_status":  `{"protocolVersion":1,"schemaVersion":1,"result":{"state":"OK"}}`,
		"capabilities": fmt.Sprintf(`{"protocolVersion":1,"schemaVersions":{"pr":%d},"ops":["auth_status","capabilities"]}`, schema.PRSchemaVersion),
	}, `{"protocolVersion":1,"error":{"code":"unknown_op","message":"unknown op"}}`)
}

func writeActivityKindsBackend(t *testing.T, name, vocabularyJSON string) {
	t.Helper()
	writeOpAwareFakeBackend(t, name, map[string]string{
		"capabilities": `{"protocolVersion":1,"schemaVersions":{},"ops":["capabilities","list_activity"],"vocabulary":` + vocabularyJSON + `}`,
	}, `{"protocolVersion":1,"error":{"code":"unknown_op","message":"unknown op"}}`)
}

func TestConfigValidate_ActivityKindsUnion_SortedDeduplicated(t *testing.T) {
	writeHealthyPRBackend(t, "backend-pr-ok")
	writeActivityKindsBackend(t, "act-one", `{"activity_kinds":["pr.opened","pr.merged"]}`)
	writeActivityKindsBackend(t, "act-two", `{"activity_kinds":["pr.merged","pr.closed"]}`)
	writeActivityConfig(t, "backend-pr-ok", []string{"act-one", "act-two"})

	stdout, _, code := executePr(t, []string{"config", "validate"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}
	var got struct {
		Sources       []SourceResult `json:"sources"`
		ActivityKinds []string       `json:"activity_kinds"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("decode: %v (stdout=%s)", err, stdout)
	}
	want := []string{"pr.closed", "pr.merged", "pr.opened"}
	if strings.Join(got.ActivityKinds, ",") != strings.Join(want, ",") {
		t.Fatalf("activity_kinds = %v, want %v", got.ActivityKinds, want)
	}
	if len(got.Sources) != 1 || got.Sources[0].Source != "backend-pr-ok" {
		t.Fatalf("sources = %+v, want only the connector backend row", got.Sources)
	}

	human, _, code := executePr(t, []string{"--output", "human", "config", "validate"})
	if code != 0 {
		t.Fatalf("human exit code = %d, want 0; stdout=%s", code, human)
	}
	if !strings.Contains(human, "activity kinds: pr.closed, pr.merged, pr.opened") {
		t.Fatalf("human output = %q, want the sorted union line", human)
	}
}

func TestConfigValidate_ActivityKinds_FailingOrSilentSourceContributesNothing(t *testing.T) {
	writeHealthyPRBackend(t, "backend-pr-ok2")
	writeActivityKindsBackend(t, "act-good", `{"activity_kinds":["pr.opened"]}`)
	writeActivityKindsBackend(t, "act-silent", `{"search_attributes":["x"]}`)
	writeFakeBackend(t, "act-broken", `{"protocolVersion":1,"error":{"code":"unauthenticated","message":"bad token"}}`)
	writeActivityConfig(t, "backend-pr-ok2", []string{"act-good", "act-silent", "act-broken"})

	stdout, _, code := executePr(t, []string{"config", "validate"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (activity source failure must not degrade); stdout=%s", code, stdout)
	}
	var got struct {
		Sources       []SourceResult `json:"sources"`
		ActivityKinds []string       `json:"activity_kinds"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("decode: %v (stdout=%s)", err, stdout)
	}
	if len(got.ActivityKinds) != 1 || got.ActivityKinds[0] != "pr.opened" {
		t.Fatalf("activity_kinds = %v, want [pr.opened]", got.ActivityKinds)
	}
	if len(got.Sources) != 1 || got.Sources[0].Status != SourceSucceeded || got.Sources[0].Count != 2 {
		t.Fatalf("sources = %+v, want one succeeded count=2 row", got.Sources)
	}
}

func TestConfigValidate_ActivityKinds_NoKindsDeclaredOmitsOutput(t *testing.T) {
	writeHealthyPRBackend(t, "backend-pr-ok3")
	writeActivityKindsBackend(t, "act-silent2", `{}`)
	writeActivityConfig(t, "backend-pr-ok3", []string{"act-silent2"})
	withSource, _, code := executePr(t, []string{"config", "validate"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, withSource)
	}
	if strings.Contains(withSource, "activity_kinds") {
		t.Fatalf("stdout = %s, want no activity_kinds key", withSource)
	}
	humanOut, _, _ := executePr(t, []string{"--output", "human", "config", "validate"})
	if strings.Contains(humanOut, "activity kinds") {
		t.Fatalf("human = %q, want no activity kinds line", humanOut)
	}
}

func TestConfigValidate_ActivityKinds_NoActivitySourcesByteIdentical(t *testing.T) {
	writeHealthyPRBackend(t, "backend-pr-ok4")
	writeActivityConfig(t, "backend-pr-ok4", nil)
	stdout, _, code := executePr(t, []string{"config", "validate"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}
	want := `{"sources":[{"source":"backend-pr-ok4","status":"succeeded","count":2}]}` + "\n"
	if stdout != want {
		t.Fatalf("stdout = %q, want %q", stdout, want)
	}
	human, _, _ := executePr(t, []string{"--output", "human", "config", "validate"})
	if wantH := "config validate:\n  backend-pr-ok4: succeeded count=2\n"; human != wantH {
		t.Fatalf("human = %q, want %q", human, wantH)
	}
}

func TestConfigValidate_ActivityKinds_InvalidActivitySourcesKeySkipsUnion(t *testing.T) {
	writeHealthyPRBackend(t, "backend-pr-ok5")
	cfg := t.TempDir() + "/config.yaml"
	body := "connector:\n  pr:\n    - backend-pr-ok5\nactivity:\n  sources: []\n"
	if err := os.WriteFile(cfg, []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("PG_PR_CONFIG", cfg)
	// An explicitly-empty list is rejected by the registry; whatever the
	// command does with that (it may fail to load at all), the union print
	// itself must never panic or emit a kinds array.
	reg := &Registry{activitySources: []yaml.Node{}}
	if got := activityKindsUnion(context.Background(), reg); got != nil {
		t.Fatalf("activityKindsUnion = %v, want nil on an ActivitySources error", got)
	}
}

func TestConfigShow_ActivitySources_InvokesNoBackend(t *testing.T) {
	// No fake backend exists on PATH: config show must not try to exec the
	// registered activity source.
	writeActivityConfig(t, "backend-pr-absent", []string{"act-absent"})
	_, _, code := executePr(t, []string{"config", "show"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (config show must never invoke a backend)", code)
	}
}
