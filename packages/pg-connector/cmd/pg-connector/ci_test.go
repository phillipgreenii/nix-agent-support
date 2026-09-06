package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// writeCiConfigFor writes a connector.ci registry listing backends (in
// order) and points $PG_PR_CONFIG at it. connector.ci is list-valued
// [design: §4.1], so this always writes a YAML list even for a single
// backend, mirroring writeConfigFor's own convention for connector.pr.
func writeCiConfigFor(t *testing.T, backends ...string) {
	t.Helper()
	dir := t.TempDir()
	cfg := dir + "/config.yaml"
	var sb strings.Builder
	sb.WriteString("connector:\n  ci:\n")
	for _, b := range backends {
		sb.WriteString("    - " + b + "\n")
	}
	if err := os.WriteFile(cfg, []byte(sb.String()), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("PG_PR_CONFIG", cfg)
}

func TestRun_CiList_Success_SingleBackend(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-ci-list", map[string]string{
		"list_runs": `{"protocolVersion":1,"schemaVersion":1,"result":[{"id":"run-1","name":"build","status":"completed","conclusion":"success","url":"u","provider":"github-actions","head_sha":"deadbeef","pr_id":"pr-1"}]}`,
	}, `{}`)
	writeCiConfigFor(t, "backend-ci-list")

	stdout, code := executePr(t, []string{"ci", "list", "pr-1"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}

	var outcome ciListOutcome
	if err := json.Unmarshal([]byte(stdout), &outcome); err != nil {
		t.Fatalf("decode outcome: %v (stdout=%s)", err, stdout)
	}
	if len(outcome.Runs) != 1 || outcome.Runs[0].ID != "run-1" || outcome.Runs[0].PRID != "pr-1" {
		t.Fatalf("outcome.Runs = %+v", outcome.Runs)
	}
	if len(outcome.Sources) != 1 || outcome.Sources[0].Status != SourceSucceeded || outcome.Sources[0].Count != 1 {
		t.Fatalf("outcome.Sources = %+v", outcome.Sources)
	}
}

func TestRun_CiList_FanOut_ConcatenatesAcrossBackends(t *testing.T) {
	// The design's own "runs concatenates" merge strategy [design: §4.5]:
	// every backend's runs land in one Runs slice, one sources[] row per
	// backend, never collapsed.
	writeOpAwareFakeBackend(t, "backend-ci-a", map[string]string{
		"list_runs": `{"protocolVersion":1,"schemaVersion":1,"result":[{"id":"run-a","pr_id":"pr-1"}]}`,
	}, `{}`)
	writeOpAwareFakeBackend(t, "backend-ci-b", map[string]string{
		"list_runs": `{"protocolVersion":1,"schemaVersion":1,"result":[{"id":"run-b","pr_id":"pr-1"}]}`,
	}, `{}`)
	writeCiConfigFor(t, "backend-ci-a", "backend-ci-b")

	stdout, code := executePr(t, []string{"ci", "list", "pr-1"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}

	var outcome ciListOutcome
	if err := json.Unmarshal([]byte(stdout), &outcome); err != nil {
		t.Fatalf("decode outcome: %v (stdout=%s)", err, stdout)
	}
	if len(outcome.Runs) != 2 {
		t.Fatalf("outcome.Runs = %+v, want 2 concatenated runs", outcome.Runs)
	}
	if len(outcome.Sources) != 2 {
		t.Fatalf("outcome.Sources = %+v, want one row per backend, never collapsed", outcome.Sources)
	}
}

func TestRun_CiList_FanOut_PartialFailure_Exit2(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-ci-ok", map[string]string{
		"list_runs": `{"protocolVersion":1,"schemaVersion":1,"result":[{"id":"run-1","pr_id":"pr-1"}]}`,
	}, `{}`)
	writeFakeBackend(t, "backend-ci-down", `{"protocolVersion":1,"error":{"code":"unauthenticated","message":"bad token"}}`)
	writeCiConfigFor(t, "backend-ci-ok", "backend-ci-down")

	stdout, code := executePr(t, []string{"ci", "list", "pr-1"})
	if code != 2 {
		t.Fatalf("exit code = %d, want 2 (degraded/partial); stdout=%s", code, stdout)
	}

	var outcome ciListOutcome
	if err := json.Unmarshal([]byte(stdout), &outcome); err != nil {
		t.Fatalf("decode outcome: %v (stdout=%s)", err, stdout)
	}
	if len(outcome.Sources) != 2 {
		t.Fatalf("outcome.Sources = %+v, want one row per backend queried, never collapsed", outcome.Sources)
	}
}

func TestRun_CiList_FanOut_NoBackendsRegistered_Exit3(t *testing.T) {
	// Zero sources queried is the total-failure case [design: §4.5] — a
	// fan-out op with nothing registered under connector.ci is not the
	// generic CLI-level failure a targeted op with no backend hits (see
	// pr_test.go's TestRun_PrShow_NoBackendRegistered_IsGenericFailure); it
	// still produces a well-formed outcome envelope.
	dir := t.TempDir()
	cfg := dir + "/config.yaml"
	if err := os.WriteFile(cfg, []byte("connector: {}\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("PG_PR_CONFIG", cfg)

	stdout, code := executePr(t, []string{"ci", "list", "pr-1"})
	if code != 3 {
		t.Fatalf("exit code = %d, want 3; stdout=%s", code, stdout)
	}
	var outcome ciListOutcome
	if err := json.Unmarshal([]byte(stdout), &outcome); err != nil {
		t.Fatalf("decode outcome: %v (stdout=%s)", err, stdout)
	}
	if len(outcome.Sources) != 0 || len(outcome.Runs) != 0 {
		t.Fatalf("outcome = %+v, want an empty-but-well-formed envelope", outcome)
	}
	// The Go-level len()==0 check above passes whether the wire bytes said
	// [] or null — round-tripping through json.Unmarshal loses that
	// distinction. Check the raw bytes directly: runs/sources MUST be []
	// so `jq '.sources[]'`/`jq '.runs[]'` don't exit 5 on exactly the host
	// that's misconfigured [bug A15].
	if want := `"runs":[]`; !strings.Contains(stdout, want) {
		t.Fatalf("stdout = %s, want it to contain %s (not runs:null)", stdout, want)
	}
	if want := `"sources":[]`; !strings.Contains(stdout, want) {
		t.Fatalf("stdout = %s, want it to contain %s (not sources:null)", stdout, want)
	}
}

func TestFanOutCIList_NoBackends_RunsAndSourcesAreEmptyArraysNotNull(t *testing.T) {
	outcome := fanOutCIList(context.Background(), nil, "pr-1")
	raw, err := json.Marshal(outcome)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := string(raw); got != `{"runs":[],"sources":[]}` {
		t.Fatalf("json = %s, want runs/sources to marshal as [] not null", got)
	}
}

func TestRun_CiLogs_Success(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte("log output"))
	writeOpAwareFakeBackend(t, "backend-ci-logs", map[string]string{
		"get_logs": `{"protocolVersion":1,"schemaVersion":1,"result":"` + encoded + `"}`,
	}, `{}`)
	writeCiConfigFor(t, "backend-ci-logs")

	stdout, code := executePr(t, []string{"ci", "logs", "run-1"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}

	var resp scriptout.Response
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("decode response: %v (stdout=%s)", err, stdout)
	}
	var logs []byte
	if err := scriptout.Decode(resp.Result, &logs); err != nil {
		t.Fatalf("decode logs: %v", err)
	}
	if string(logs) != "log output" {
		t.Fatalf("logs = %q, want %q", logs, "log output")
	}
}

func TestRun_CiLogs_NotFound_Exit4(t *testing.T) {
	// A not_found response (e.g. the run id no longer exists) is a
	// well-formed negative answer under the targeted-op scheme (CLI exit
	// 4), not a broken call [design: §4.5].
	writeOpAwareFakeBackend(t, "backend-ci-logs-notfound", map[string]string{
		"get_logs": `{"protocolVersion":1,"schemaVersion":1,"error":{"code":"not_found","message":"run run-1 not found"}}`,
	}, `{}`)
	writeCiConfigFor(t, "backend-ci-logs-notfound")

	stdout, code := executePr(t, []string{"ci", "logs", "run-1"})
	if code != 4 {
		t.Fatalf("exit code = %d, want 4; stdout=%s", code, stdout)
	}
	var resp scriptout.Response
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("decode response: %v (stdout=%s)", err, stdout)
	}
	if resp.Error == nil || resp.Error.Code != "not_found" {
		t.Fatalf("resp.Error = %+v", resp.Error)
	}
}

func TestRun_CiRerunFailed_Success(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-ci-rerun", map[string]string{
		"rerun_failed": `{"protocolVersion":1,"schemaVersion":1,"result":null}`,
	}, `{}`)
	writeCiConfigFor(t, "backend-ci-rerun")

	stdout, code := executePr(t, []string{"ci", "rerun-failed", "pr-1"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}
	var resp scriptout.Response
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("decode response: %v (stdout=%s)", err, stdout)
	}
}

func TestRun_CiRerunFailed_NotFound_Exit4(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-ci-rerun-notfound", map[string]string{
		"rerun_failed": `{"protocolVersion":1,"schemaVersion":1,"error":{"code":"not_found","message":"pr pr-1 not found"}}`,
	}, `{}`)
	writeCiConfigFor(t, "backend-ci-rerun-notfound")

	stdout, code := executePr(t, []string{"ci", "rerun-failed", "pr-1"})
	if code != 4 {
		t.Fatalf("exit code = %d, want 4; stdout=%s", code, stdout)
	}
	var resp scriptout.Response
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("decode response: %v (stdout=%s)", err, stdout)
	}
	if resp.Error == nil || resp.Error.Code != "not_found" {
		t.Fatalf("resp.Error = %+v", resp.Error)
	}
}

func TestRun_CiList_HumanOutput(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-ci-list-human", map[string]string{
		"list_runs": `{"protocolVersion":1,"schemaVersion":1,"result":[{"id":"run-1","name":"build","status":"completed","conclusion":"success","url":"u","provider":"github-actions","head_sha":"deadbeef","pr_id":"pr-1"}]}`,
	}, `{}`)
	writeCiConfigFor(t, "backend-ci-list-human")

	stdout, code := executePr(t, []string{"--output", "human", "ci", "list", "pr-1"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}
	if strings.Contains(stdout, "{") {
		t.Fatalf("human output must not contain raw JSON; stdout=%s", stdout)
	}
	for _, want := range []string{"ci runs (1)", "run-1", "build", "completed/success", "backend-ci-list-human: succeeded"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("human output missing %q; stdout=%s", want, stdout)
		}
	}
}

func TestRun_CiLogs_HumanOutput(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte("log output"))
	writeOpAwareFakeBackend(t, "backend-ci-logs-human", map[string]string{
		"get_logs": `{"protocolVersion":1,"schemaVersion":1,"result":"` + encoded + `"}`,
	}, `{}`)
	writeCiConfigFor(t, "backend-ci-logs-human")

	stdout, code := executePr(t, []string{"--output", "human", "ci", "logs", "run-1"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}
	if strings.TrimSpace(stdout) != "log output" {
		t.Fatalf("human logs output = %q, want the decoded log text verbatim", stdout)
	}
}

func TestRun_CiRerunFailed_HumanOutput(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-ci-rerun-human", map[string]string{
		"rerun_failed": `{"protocolVersion":1,"schemaVersion":1,"result":null}`,
	}, `{}`)
	writeCiConfigFor(t, "backend-ci-rerun-human")

	stdout, code := executePr(t, []string{"--output", "human", "ci", "rerun-failed", "pr-1"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}
	if !strings.Contains(stdout, "CI rerun triggered for PR pr-1") {
		t.Fatalf("human output = %q", stdout)
	}
}

func TestRun_CiLogs_AmbiguousMultipleBackends_IsGenericFailure(t *testing.T) {
	// Targeted-op backend resolution needs exactly one registered backend
	// (mirroring Dispatch's own convention for connector.pr) — with two
	// backends registered under connector.ci, "ci logs"/"ci rerun-failed"
	// cannot resolve unambiguously, so this is the generic exit-1 CLI
	// failure path, never one of the targeted-op taxonomy codes.
	writeCiConfigFor(t, "backend-ci-a", "backend-ci-b")

	_, code := executePr(t, []string{"ci", "logs", "run-1"})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
}
