package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

func writeAlertConfigFor(t *testing.T, backends ...string) {
	t.Helper()
	var sb strings.Builder
	sb.WriteString("connector:\n  alert:\n")
	for _, b := range backends {
		sb.WriteString("    - " + b + "\n")
	}
	cfg := t.TempDir() + "/config.yaml"
	if err := os.WriteFile(cfg, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PG_PR_CONFIG", cfg)
}

func TestNewAlertCmd_HasListShowHistoryOnly(t *testing.T) {
	names := map[string]bool{}
	for _, c := range newAlertCmd().Commands() {
		names[c.Name()] = true
	}
	if len(names) != 3 || !names["list"] || !names["show"] || !names["history"] {
		t.Fatalf("subcommands = %v, want exactly list/show/history (INV-ALERT-6: no mutation, no changes in v1)", names)
	}
}

func TestRun_AlertList_NoQuery_Succeeds(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-alert-list", map[string]string{
		"list": `{"protocolVersion":1,"schemaVersion":1,"result":{"entities":[{"id":"grafana:a","provider":"grafana","title":"HighCPU","since":"2026-10-01T00:00:00Z","as_of":"2026-10-01T00:01:00Z","stale":false}],"present_ids":["grafana:a"],"cursor":null,"truncated":false}}`,
	}, `{}`)
	writeAlertConfigFor(t, "backend-alert-list")

	stdout, _, code := executePr(t, []string{"alert", "list"})
	if code != 0 {
		t.Fatalf("exit=%d stdout=%s", code, stdout)
	}
	var o alertListOutcome
	if err := json.Unmarshal([]byte(stdout), &o); err != nil {
		t.Fatal(err)
	}
	if len(o.Entities) != 1 || o.Entities[0].ID != "grafana:a" || o.Sources[0].Status != SourceSucceeded || o.Sources[0].Count != 1 {
		t.Fatalf("outcome=%+v", o)
	}

	human, _, hcode := executePr(t, []string{"--output", "human", "alert", "list"})
	if hcode != 0 || !strings.Contains(human, "[grafana:a] HighCPU") {
		t.Fatalf("human exit=%d out=%s", hcode, human)
	}
}

func TestRun_AlertList_ZeroFiring_IsSucceededCountZero(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-alert-none", map[string]string{
		"list": `{"protocolVersion":1,"schemaVersion":1,"result":{"entities":[],"present_ids":[],"cursor":null,"truncated":false}}`,
	}, `{}`)
	writeAlertConfigFor(t, "backend-alert-none")
	stdout, _, code := executePr(t, []string{"alert", "list"})
	if code != 0 || !strings.Contains(stdout, `"entities":[]`) {
		t.Fatalf("exit=%d stdout=%s", code, stdout)
	}
}

// Unknown is NOT none (INV-ALERT-5): an unavailable backend is a degraded
// source, exit 3 for a single-source fan-out, with no cache fallback.
func TestRun_AlertList_Unavailable_IsDegradedExit3_NoCacheFallback(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-alert-down", map[string]string{
		"list": `{"protocolVersion":1,"schemaVersion":1,"error":{"code":"unavailable","message":"grafana down"}}`,
	}, `{}`)
	writeAlertConfigFor(t, "backend-alert-down")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	stdout, _, code := executePr(t, []string{"alert", "list"})
	if code != 3 {
		t.Fatalf("exit=%d stdout=%s", code, stdout)
	}
	var o alertListOutcome
	if err := json.Unmarshal([]byte(stdout), &o); err != nil {
		t.Fatal(err)
	}
	if len(o.Entities) != 0 || o.Sources[0].Status != SourceDegraded || o.Sources[0].Count != 0 {
		t.Fatalf("outcome=%+v", o)
	}
}

func TestRun_AlertList_QueryNotRecognizedByAll_IsInvalidArgument(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-alert-noq", map[string]string{
		"list": `{"protocolVersion":1,"schemaVersion":1,"error":{"code":"query_not_recognized","message":"nope"}}`,
	}, `{}`)
	writeAlertConfigFor(t, "backend-alert-noq")
	stdout, _, code := executePr(t, []string{"alert", "list", "--query", "bogus"})
	if code != 1 {
		t.Fatalf("exit=%d stdout=%s", code, stdout)
	}
	var resp scriptout.Response
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil || resp.Error == nil || resp.Error.Code != "invalid_argument" {
		t.Fatalf("resp=%+v err=%v stdout=%s", resp, err, stdout)
	}
}

func TestRun_AlertShow_SuccessAndNotFound(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-alert-show", map[string]string{
		"show": `{"protocolVersion":1,"schemaVersion":1,"result":{"id":"grafana:a","provider":"grafana","title":"HighCPU","severity":"high","since":"2026-10-01T00:00:00Z","as_of":"2026-10-01T00:01:00Z","stale":false}}`,
	}, `{}`)
	writeAlertConfigFor(t, "backend-alert-show")
	out, _, code := executePr(t, []string{"--output", "human", "alert", "show", "grafana:a"})
	if code != 0 || !strings.Contains(out, "alert grafana:a [grafana]") || !strings.Contains(out, "severity: high") || strings.Contains(out, "acknowledged") {
		t.Fatalf("exit=%d out=%s", code, out)
	}

	writeOpAwareFakeBackend(t, "backend-alert-show-nf", map[string]string{
		"show": `{"protocolVersion":1,"schemaVersion":1,"error":{"code":"not_found","message":"not firing"}}`,
	}, `{}`)
	writeAlertConfigFor(t, "backend-alert-show-nf")
	_, _, code = executePr(t, []string{"alert", "show", "grafana:gone"})
	if code != 4 {
		t.Fatalf("exit=%d, want 4", code)
	}
}

func TestRun_AlertHistory_ConcatenatesAndPassesWindow(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-alert-hist", map[string]string{
		"list_history": `{"protocolVersion":1,"schemaVersion":1,"result":{"episodes":[{"rule_id":"r1","title":"HighCPU","started_at":"2026-10-01T01:00:00Z"}],"truncated":true}}`,
	}, `{}`)
	writeAlertConfigFor(t, "backend-alert-hist")
	stdout, _, code := executePr(t, []string{"alert", "history", "--since", "2026-10-01T00:00:00Z", "--until", "2026-10-02T00:00:00Z"})
	if code != 0 {
		t.Fatalf("exit=%d stdout=%s", code, stdout)
	}
	var o alertHistoryOutcome
	if err := json.Unmarshal([]byte(stdout), &o); err != nil {
		t.Fatal(err)
	}
	if len(o.Episodes) != 1 || o.Episodes[0].RuleID != "r1" || !o.Truncated || o.Sources[0].Count != 1 {
		t.Fatalf("outcome=%+v", o)
	}

	// Required flags and RFC3339 validation.
	if _, _, code := executePr(t, []string{"alert", "history", "--since", "bad", "--until", "2026-10-02T00:00:00Z"}); code == 0 {
		t.Fatal("malformed --since must fail")
	}
	if _, _, code := executePr(t, []string{"alert", "history"}); code == 0 {
		t.Fatal("missing --since/--until must fail")
	}
}

func TestRun_AlertHistory_BackendLackingOp_IsDisabledNotApplicable(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-alert-nohist", map[string]string{
		"list_history": `{"protocolVersion":1,"schemaVersion":1,"error":{"code":"unknown_op","message":"no"}}`,
	}, `{}`)
	writeAlertConfigFor(t, "backend-alert-nohist")
	stdout, _, _ := executePr(t, []string{"alert", "history", "--since", "2026-10-01T00:00:00Z", "--until", "2026-10-02T00:00:00Z"})
	if !strings.Contains(stdout, "disabled") || !strings.Contains(stdout, "not applicable") {
		t.Fatalf("stdout=%s", stdout)
	}
}
