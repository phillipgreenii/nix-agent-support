package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// writeIssueConfigFor writes a connector.issue registry config naming
// backend as the sole registered issue backend. A distinct helper from
// pr_test.go's writeConfigFor (which is pr-specific) since both live in
// this same package main test binary.
func writeIssueConfigFor(t *testing.T, backend string) {
	t.Helper()
	dir := t.TempDir()
	cfg := dir + "/config.yaml"
	if err := os.WriteFile(cfg, []byte("connector:\n  issue:\n    - "+backend+"\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("PG_PR_CONFIG", cfg)
}

func TestRun_IssueShow_Success(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-issue-show", map[string]string{
		"show": `{"protocolVersion":1,"schemaVersion":1,"result":{"id":"issue-1","title":"t","state":"open","url":"u","priority":"High","labels":["a","b"],"issue_type":"Bug"}}`,
	}, `{}`)
	writeIssueConfigFor(t, "backend-issue-show")

	stdout, code := executePr(t, []string{"issue", "show", "issue-1"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}

	var resp scriptout.Response
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("decode response: %v (stdout=%s)", err, stdout)
	}
	var issue schema.Issue
	if err := scriptout.Decode(resp.Result, &issue); err != nil {
		t.Fatalf("decode Issue: %v", err)
	}
	if issue.ID != "issue-1" || issue.Priority != "High" || issue.IssueType != "Bug" {
		t.Fatalf("issue = %+v", issue)
	}
	if len(issue.Labels) != 2 || issue.Labels[0] != "a" {
		t.Fatalf("issue.Labels = %+v", issue.Labels)
	}
}

func TestRun_IssueShow_NotFound_Exit4(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-issue-show-notfound", map[string]string{
		"show": `{"protocolVersion":1,"schemaVersion":1,"error":{"code":"not_found","message":"issue issue-404 not found"}}`,
	}, `{}`)
	writeIssueConfigFor(t, "backend-issue-show-notfound")

	stdout, code := executePr(t, []string{"issue", "show", "issue-404"})
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

func TestRun_IssueCreate_Success(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-issue-create", map[string]string{
		"create": `{"protocolVersion":1,"schemaVersion":1,"result":{"id":"issue-2","title":"new issue","state":"open"}}`,
	}, `{}`)
	writeIssueConfigFor(t, "backend-issue-create")

	stdout, code := executePr(t, []string{"issue", "create", "--title", "new issue"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}
	var resp scriptout.Response
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("decode response: %v (stdout=%s)", err, stdout)
	}
	var issue schema.Issue
	if err := scriptout.Decode(resp.Result, &issue); err != nil {
		t.Fatalf("decode Issue: %v", err)
	}
	if issue.ID != "issue-2" || issue.Title != "new issue" {
		t.Fatalf("issue = %+v", issue)
	}
}

func TestRun_IssueComment_Success(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-issue-comment", map[string]string{
		"comment": `{"protocolVersion":1,"schemaVersion":1,"result":null}`,
	}, `{}`)
	writeIssueConfigFor(t, "backend-issue-comment")

	stdout, code := executePr(t, []string{"issue", "comment", "issue-1", "--body", "a comment"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}
	var resp scriptout.Response
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("decode response: %v (stdout=%s)", err, stdout)
	}
	if resp.Error != nil {
		t.Fatalf("resp.Error = %+v, want nil", resp.Error)
	}
}

func TestRun_IssueTransition_Success(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-issue-transition", map[string]string{
		"transition": `{"protocolVersion":1,"schemaVersion":1,"result":null}`,
	}, `{}`)
	writeIssueConfigFor(t, "backend-issue-transition")

	stdout, code := executePr(t, []string{"issue", "transition", "issue-1", "--state", "Done"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}
	var resp scriptout.Response
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("decode response: %v (stdout=%s)", err, stdout)
	}
	if resp.Error != nil {
		t.Fatalf("resp.Error = %+v, want nil", resp.Error)
	}
}

func TestRun_IssueTransition_NotFound_Exit4(t *testing.T) {
	// Transition on an unknown issue id is a well-formed negative answer
	// under the targeted-op scheme (CLI exit 4), not a broken call.
	writeOpAwareFakeBackend(t, "backend-issue-transition-notfound", map[string]string{
		"transition": `{"protocolVersion":1,"schemaVersion":1,"error":{"code":"not_found","message":"issue issue-404 not found"}}`,
	}, `{}`)
	writeIssueConfigFor(t, "backend-issue-transition-notfound")

	stdout, code := executePr(t, []string{"issue", "transition", "issue-404", "--state", "Done"})
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

func TestRun_IssueTransition_VocabularyMismatch_PassesToBackendAndIsGenericFailure(t *testing.T) {
	// transition's target-state vocabulary is backend-declared, never a
	// fixed Go enum, so there is no client-side --state validation the way
	// pr's feedback-set validates --disposition against a closed enum.
	// The request must actually reach the backend, and an unrecognized
	// target state is reported by the backend as a well-formed (non
	// not_found) error, which maps to the generic exit-1 failure code —
	// never exit 4 (not_found is reserved for a genuinely missing entity,
	// not an invalid vocabulary value).
	writeOpAwareFakeBackend(t, "backend-issue-transition-vocab", map[string]string{
		"transition": `{"protocolVersion":1,"schemaVersion":1,"error":{"code":"unavailable","message":"target state \"bogus\" not in vocabulary"}}`,
	}, `{}`)
	writeIssueConfigFor(t, "backend-issue-transition-vocab")

	stdout, code := executePr(t, []string{"issue", "transition", "issue-1", "--state", "bogus"})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stdout=%s", code, stdout)
	}
	var resp scriptout.Response
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("decode response: %v (stdout=%s)", err, stdout)
	}
	if resp.Error == nil || resp.Error.Code == "not_found" {
		t.Fatalf("resp.Error = %+v, want a non-not_found error", resp.Error)
	}
}

func TestRun_IssueShow_HumanOutput(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-issue-show-human", map[string]string{
		"show": `{"protocolVersion":1,"schemaVersion":1,"result":{"id":"issue-1","title":"t","state":"open","url":"u","priority":"High","labels":["a","b"],"issue_type":"Bug"}}`,
	}, `{}`)
	writeIssueConfigFor(t, "backend-issue-show-human")

	stdout, code := executePr(t, []string{"--output", "human", "issue", "show", "issue-1"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}
	if strings.Contains(stdout, "{") {
		t.Fatalf("human output must not contain raw JSON; stdout=%s", stdout)
	}
	for _, want := range []string{"issue issue-1", "[open]", "priority: High", "type: Bug", "labels: a, b"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("human output missing %q; stdout=%s", want, stdout)
		}
	}
}

func TestRun_IssueCreate_HumanOutput(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-issue-create-human", map[string]string{
		"create": `{"protocolVersion":1,"schemaVersion":1,"result":{"id":"issue-2","title":"new issue","state":"open"}}`,
	}, `{}`)
	writeIssueConfigFor(t, "backend-issue-create-human")

	stdout, code := executePr(t, []string{"--output", "human", "issue", "create", "--title", "new issue"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}
	if !strings.Contains(stdout, "created issue issue-2") {
		t.Fatalf("human output = %q", stdout)
	}
}

func TestRun_IssueComment_HumanOutput(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-issue-comment-human", map[string]string{
		"comment": `{"protocolVersion":1,"schemaVersion":1,"result":null}`,
	}, `{}`)
	writeIssueConfigFor(t, "backend-issue-comment-human")

	stdout, code := executePr(t, []string{"--output", "human", "issue", "comment", "issue-1", "--body", "a comment"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}
	if !strings.Contains(stdout, "Comment added to issue issue-1") {
		t.Fatalf("human output = %q", stdout)
	}
}

func TestRun_IssueTransition_HumanOutput(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-issue-transition-human", map[string]string{
		"transition": `{"protocolVersion":1,"schemaVersion":1,"result":null}`,
	}, `{}`)
	writeIssueConfigFor(t, "backend-issue-transition-human")

	stdout, code := executePr(t, []string{"--output", "human", "issue", "transition", "issue-1", "--state", "Done"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}
	if !strings.Contains(stdout, "Issue issue-1 transitioned to Done") {
		t.Fatalf("human output = %q", stdout)
	}
}

func TestRun_IssueShow_NoBackendRegistered_IsGenericFailure(t *testing.T) {
	dir := t.TempDir()
	cfg := dir + "/config.yaml"
	if err := os.WriteFile(cfg, []byte("connector: {}\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("PG_PR_CONFIG", cfg)

	_, code := executePr(t, []string{"issue", "show", "issue-1"})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
}
