package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
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

func TestRun_PrShow_NoBackendRegistered_IsGenericFailure(t *testing.T) {
	dir := t.TempDir()
	cfg := dir + "/config.yaml"
	if err := os.WriteFile(cfg, []byte("connector: {}\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("PG_PR_CONFIG", cfg)

	_, code := executePr(t, []string{"pr", "show", "pr-1"})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
}
