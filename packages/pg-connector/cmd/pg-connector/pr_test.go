package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// executePr runs the root command with args, capturing stdout/stderr in a
// buffer (unlike run(), which writes to the real os.Stdout/os.Stderr) so
// tests can assert on the wire-response body pg-connector printed, not just
// the process exit code.
func executePr(t *testing.T, args []string) (stdout string, exitCode int) {
	t.Helper()
	root := newRootCmd()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs(args)

	err := root.Execute()
	if err == nil {
		return buf.String(), 0
	}
	var ee *exitError
	if errors.As(err, &ee) {
		return buf.String(), ee.code
	}
	return buf.String(), 1
}

func writeConfigFor(t *testing.T, backend string) {
	t.Helper()
	dir := t.TempDir()
	cfg := dir + "/config.yaml"
	if err := os.WriteFile(cfg, []byte("connector:\n  pr:\n    - "+backend+"\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("PG_PR_CONFIG", cfg)
}

func TestRun_PrShow_Success(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-show", map[string]string{
		"show": `{"protocolVersion":1,"schemaVersion":1,"result":{"id":"pr-1","repo":"o/r","number":1,"title":"t","state":"open","branch":"b","base":"main","author":"a","url":"u","draft":false,"merged":false,"comments":[{"id":"c1","author":"a","body":"body","resolved":false,"disposition":"open"}],"reviews":[{"id":"r1","author":"rev","state":"CHANGES_REQUESTED","comments":[{"id":"c2","author":"rev","body":"fix","thread_id":"th1","disposition":"will-fix"}]}]}}`,
	}, `{}`)
	writeConfigFor(t, "backend-show")

	stdout, code := executePr(t, []string{"pr", "show", "pr-1"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}

	var resp scriptout.Response
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("decode response: %v (stdout=%s)", err, stdout)
	}
	var pr schema.PR
	if err := scriptout.Decode(resp.Result, &pr); err != nil {
		t.Fatalf("decode PR: %v", err)
	}
	if pr.ID != "pr-1" {
		t.Fatalf("pr.ID = %q, want pr-1", pr.ID)
	}
	if len(pr.Comments) != 1 || pr.Comments[0].ID != "c1" || pr.Comments[0].Disposition != schema.DispositionOpen {
		t.Fatalf("pr.Comments = %+v", pr.Comments)
	}
	if len(pr.Reviews) != 1 || len(pr.Reviews[0].Comments) != 1 ||
		pr.Reviews[0].Comments[0].ID != "c2" || pr.Reviews[0].Comments[0].Disposition != schema.DispositionWillFix {
		t.Fatalf("pr.Reviews = %+v", pr.Reviews)
	}
}

func TestRun_PrCategorize_Success(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-categorize", map[string]string{
		"categorize": `{"protocolVersion":1,"schemaVersion":1,"result":{"id":"pr-1","category":"focus"}}`,
	}, `{}`)
	writeConfigFor(t, "backend-categorize")

	stdout, code := executePr(t, []string{"pr", "categorize", "pr-1", "--category", "focus"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}

	var resp scriptout.Response
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("decode response: %v (stdout=%s)", err, stdout)
	}
	var result schema.CategorizeResult
	if err := scriptout.Decode(resp.Result, &result); err != nil {
		t.Fatalf("decode CategorizeResult: %v", err)
	}
	if result.ID != "pr-1" || result.Category != "focus" {
		t.Fatalf("result = %+v", result)
	}
}

func TestRun_PrFeedbackSet_Success(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-feedback-ok", map[string]string{
		"feedback_set": `{"protocolVersion":1,"schemaVersion":1,"result":{"id":"pr-1","comment_id":"c1","disposition":"wont-fix"}}`,
	}, `{}`)
	writeConfigFor(t, "backend-feedback-ok")

	stdout, code := executePr(t, []string{"pr", "feedback-set", "pr-1", "c1", "--disposition", "wont-fix"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}

	var resp scriptout.Response
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("decode response: %v (stdout=%s)", err, stdout)
	}
	var result schema.FeedbackSetResult
	if err := scriptout.Decode(resp.Result, &result); err != nil {
		t.Fatalf("decode FeedbackSetResult: %v", err)
	}
	if result.ID != "pr-1" || result.CommentID != "c1" || result.Disposition != schema.DispositionWontFix {
		t.Fatalf("result = %+v", result)
	}
}

func TestRun_PrFeedbackSet_NotFound_Exit4(t *testing.T) {
	// A not_found response (e.g. the comment id no longer exists) is a
	// well-formed negative answer under the targeted-op scheme (CLI exit
	// 4), not a broken call [design: §4.5, §6.1].
	writeOpAwareFakeBackend(t, "backend-feedback-notfound", map[string]string{
		"feedback_set": `{"protocolVersion":1,"schemaVersion":1,"error":{"code":"not_found","message":"comment c1 not found"}}`,
	}, `{}`)
	writeConfigFor(t, "backend-feedback-notfound")

	stdout, code := executePr(t, []string{"pr", "feedback-set", "pr-1", "c1", "--disposition", "open"})
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

func TestRun_PrFeedbackSet_InvalidDisposition_IsGenericFailure(t *testing.T) {
	// An invalid --disposition is caught before ever dispatching to a
	// backend, so no config/backend is needed; it is the generic exit-1
	// CLI failure path, never one of the targeted-op taxonomy codes.
	_, code := executePr(t, []string{"pr", "feedback-set", "pr-1", "c1", "--disposition", "bogus"})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
}

func TestRun_PrShow_HumanOutput(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-show-human", map[string]string{
		"show": `{"protocolVersion":1,"schemaVersion":1,"result":{"id":"pr-1","repo":"o/r","number":1,"title":"t","state":"open","branch":"b","base":"main","author":"a","url":"u","draft":false,"merged":false,"category":"focus","labels":["x","y"],"comments":[{"id":"c1","author":"a","body":"body","resolved":false,"disposition":"open"}],"reviews":[{"id":"r1","author":"rev","state":"CHANGES_REQUESTED","comments":[{"id":"c2","author":"rev","body":"fix","thread_id":"th1","disposition":"will-fix"}]}]}}`,
	}, `{}`)
	writeConfigFor(t, "backend-show-human")

	stdout, code := executePr(t, []string{"--output", "human", "pr", "show", "pr-1"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}
	if strings.Contains(stdout, "{") {
		t.Fatalf("human output must not contain raw JSON; stdout=%s", stdout)
	}
	for _, want := range []string{"PR pr-1", "o/r#1", "\"t\"", "[open]", "branch: b -> main", "category: focus", "labels: x, y", "c1", "will-fix"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("human output missing %q; stdout=%s", want, stdout)
		}
	}
}

func TestRun_PrCategorize_HumanOutput(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-categorize-human", map[string]string{
		"categorize": `{"protocolVersion":1,"schemaVersion":1,"result":{"id":"pr-1","category":"focus"}}`,
	}, `{}`)
	writeConfigFor(t, "backend-categorize-human")

	stdout, code := executePr(t, []string{"--output", "human", "pr", "categorize", "pr-1", "--category", "focus"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}
	if !strings.Contains(stdout, "PR pr-1: category set to \"focus\"") {
		t.Fatalf("human output = %q", stdout)
	}
}

func TestRun_PrFeedbackSet_HumanOutput(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-feedback-human", map[string]string{
		"feedback_set": `{"protocolVersion":1,"schemaVersion":1,"result":{"id":"pr-1","comment_id":"c1","disposition":"wont-fix"}}`,
	}, `{}`)
	writeConfigFor(t, "backend-feedback-human")

	stdout, code := executePr(t, []string{"--output", "human", "pr", "feedback-set", "pr-1", "c1", "--disposition", "wont-fix"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}
	if !strings.Contains(stdout, "PR pr-1: comment c1 disposition set to \"wont-fix\"") {
		t.Fatalf("human output = %q", stdout)
	}
}

func TestRun_PrFeedbackSet_HumanOutput_NotFoundPrintsErrorLineNotJSON(t *testing.T) {
	// A wire-level error must still render as human text in human mode —
	// never the raw JSON error envelope.
	writeOpAwareFakeBackend(t, "backend-feedback-human-notfound", map[string]string{
		"feedback_set": `{"protocolVersion":1,"schemaVersion":1,"error":{"code":"not_found","message":"comment c1 not found"}}`,
	}, `{}`)
	writeConfigFor(t, "backend-feedback-human-notfound")

	stdout, code := executePr(t, []string{"--output", "human", "pr", "feedback-set", "pr-1", "c1", "--disposition", "open"})
	if code != 4 {
		t.Fatalf("exit code = %d, want 4; stdout=%s", code, stdout)
	}
	if strings.Contains(stdout, "{") {
		t.Fatalf("human output must not contain raw JSON; stdout=%s", stdout)
	}
	if !strings.Contains(stdout, "not_found") || !strings.Contains(stdout, "comment c1 not found") {
		t.Fatalf("human error output = %q", stdout)
	}
}

func TestRun_PrShow_JSONOutput_DefaultUnchangedWithOutputFlagExplicit(t *testing.T) {
	// --output json must be byte-identical to the pre-existing (no flag)
	// default behavior [bead pg2-ox1k6's backward-compatibility requirement].
	writeOpAwareFakeBackend(t, "backend-show-json-explicit", map[string]string{
		"show": `{"protocolVersion":1,"schemaVersion":1,"result":{"id":"pr-1","repo":"o/r","number":1,"title":"t","state":"open","branch":"b","base":"main","author":"a","url":"u","draft":false,"merged":false}}`,
	}, `{}`)
	writeConfigFor(t, "backend-show-json-explicit")

	stdoutDefault, codeDefault := executePr(t, []string{"pr", "show", "pr-1"})
	stdoutExplicit, codeExplicit := executePr(t, []string{"--output", "json", "pr", "show", "pr-1"})
	if codeDefault != codeExplicit || stdoutDefault != stdoutExplicit {
		t.Fatalf("default and --output json diverge: default=(%d,%q) explicit=(%d,%q)", codeDefault, stdoutDefault, codeExplicit, stdoutExplicit)
	}
}

func TestRun_PrShow_NoBackendRegistered_IsGenericFailure(t *testing.T) {
	dir := t.TempDir()
	cfg := dir + "/config.yaml"
	if err := os.WriteFile(cfg, []byte("connector: {}\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("PG_PR_CONFIG", cfg)

	stdout, code := executePr(t, []string{"pr", "show", "pr-1"})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	// Regression for bug pg2-njx27: this Tier-1 "no backend registered"
	// failure (the umbrella's own Dispatch, before ever reaching a
	// backend) must still produce a JSON error envelope on stdout — the
	// same shape a backend-reported failure already uses — not an empty
	// stdout with the message only as stderr prose.
	var resp scriptout.Response
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("stdout is not a JSON envelope: %v; stdout=%q", err, stdout)
	}
	if resp.Error == nil || !strings.Contains(resp.Error.Message, "no backend registered") {
		t.Fatalf("resp.Error = %+v, want a message naming the no-backend-registered failure", resp.Error)
	}
}

func TestRun_PrShow_ConfigFileDoesNotExist_EmitsJSONEnvelope(t *testing.T) {
	// Regression for bug pg2-njx27's "missing config" case: $PG_PR_CONFIG
	// pointing at a file that doesn't exist fails inside LoadRegistry,
	// before Dispatch is ever reached — a different code path from
	// TestRun_PrShow_NoBackendRegistered_IsGenericFailure above (which
	// exercises Dispatch's own "no backend registered" branch against a
	// config file that DOES exist but registers nothing). Both must emit
	// the same JSON-envelope-on-stdout shape.
	t.Setenv("PG_PR_CONFIG", t.TempDir()+"/does-not-exist.yaml")

	stdout, code := executePr(t, []string{"pr", "show", "pr-1"})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stdout=%s", code, stdout)
	}
	var resp scriptout.Response
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("stdout is not a JSON envelope: %v; stdout=%q", err, stdout)
	}
	if resp.Error == nil || !strings.Contains(resp.Error.Message, "does not exist") {
		t.Fatalf("resp.Error = %+v, want a message naming the missing config file", resp.Error)
	}
}

func TestRun_PrShow_NonExecutableBackendBinary_EmitsJSONEnvelope(t *testing.T) {
	// Regression for bug pg2-njx27's "non-executable backend binary"
	// case: the registered backend is an absolute path (scriptout's exec
	// helper accepts a bare $PATH name or an absolute path) to a file that
	// exists but lacks the executable bit, so exec fails with a
	// permission error before the backend process ever runs — a Tier-1
	// failure the umbrella detects around dispatch, not a backend-
	// reported error envelope.
	dir := t.TempDir()
	backend := dir + "/backend-not-executable"
	if err := os.WriteFile(backend, []byte("#!/bin/sh\necho unreachable\n"), 0o644); err != nil {
		t.Fatalf("write non-executable backend: %v", err)
	}

	cfgDir := t.TempDir()
	cfg := cfgDir + "/config.yaml"
	if err := os.WriteFile(cfg, []byte("connector:\n  pr:\n    - "+backend+"\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("PG_PR_CONFIG", cfg)

	stdout, code := executePr(t, []string{"pr", "show", "pr-1"})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stdout=%s", code, stdout)
	}
	var resp scriptout.Response
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("stdout is not a JSON envelope: %v; stdout=%q", err, stdout)
	}
	if resp.Error == nil || resp.Error.Message == "" {
		t.Fatalf("resp.Error = %+v, want a populated error for a non-executable backend binary", resp.Error)
	}
}
