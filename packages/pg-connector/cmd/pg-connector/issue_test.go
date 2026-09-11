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

	stdout, _, code := executePr(t, []string{"issue", "show", "issue-1"})
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

	stdout, _, code := executePr(t, []string{"issue", "show", "issue-404"})
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

	stdout, _, code := executePr(t, []string{"issue", "create", "--title", "new issue"})
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

// TestRun_IssueCreate_WithDescription_Success locks in the CLI half of
// finding 33's fix: `pg-connector issue create` must accept a
// --description flag at all (it previously had none), reaching exit 0
// [review finding A-33].
func TestRun_IssueCreate_WithDescription_Success(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-issue-create-desc", map[string]string{
		"create": `{"protocolVersion":1,"schemaVersion":1,"result":{"id":"issue-2","title":"new issue","state":"open","description":"a desc"}}`,
	}, `{}`)
	writeIssueConfigFor(t, "backend-issue-create-desc")

	stdout, _, code := executePr(t, []string{"issue", "create", "--title", "new issue", "--description", "a desc"})
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
	if issue.Description != "a desc" {
		t.Fatalf("issue.Description = %q, want %q", issue.Description, "a desc")
	}
}

// TestRun_IssueCreate_WithMetadataAndParent_Success locks in bead
// pg2-2j5ac.28.3's own IssueInput widening at the CLI layer.
func TestRun_IssueCreate_WithMetadataAndParent_Success(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-issue-create-meta", map[string]string{
		"create": `{"protocolVersion":1,"schemaVersion":1,"result":{"id":"issue-2","title":"new issue","state":"open","parent":"issue-0","metadata":{"foo":"bar"}}}`,
	}, `{}`)
	writeIssueConfigFor(t, "backend-issue-create-meta")

	stdout, _, code := executePr(t, []string{"issue", "create", "--title", "new issue", "--metadata", "foo=bar", "--parent", "issue-0"})
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
	if issue.Parent != "issue-0" || issue.Metadata["foo"] != "bar" {
		t.Fatalf("issue = %+v", issue)
	}
}

func TestRun_IssueUpdate_Success(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-issue-update", map[string]string{
		"update": `{"protocolVersion":1,"schemaVersion":1,"result":{"id":"issue-1","title":"new title","state":"open","priority":"P1"}}`,
	}, `{}`)
	writeIssueConfigFor(t, "backend-issue-update")

	stdout, _, code := executePr(t, []string{"issue", "update", "issue-1", "--title", "new title", "--priority", "P1", "--metadata", "foo=bar", "--add-label", "urgent", "--remove-label", "stale"})
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
	if issue.Title != "new title" || issue.Priority != "P1" {
		t.Fatalf("issue = %+v", issue)
	}
}

func TestRun_IssueUpdate_NotFound_Exit4(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-issue-update-notfound", map[string]string{
		"update": `{"protocolVersion":1,"schemaVersion":1,"error":{"code":"not_found","message":"issue issue-404 not found"}}`,
	}, `{}`)
	writeIssueConfigFor(t, "backend-issue-update-notfound")

	stdout, _, code := executePr(t, []string{"issue", "update", "issue-404", "--title", "x"})
	if code != 4 {
		t.Fatalf("exit code = %d, want 4; stdout=%s", code, stdout)
	}
}

func TestRun_IssueClose_Success(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-issue-close", map[string]string{
		"close": `{"protocolVersion":1,"schemaVersion":1,"result":null}`,
	}, `{}`)
	writeIssueConfigFor(t, "backend-issue-close")

	stdout, _, code := executePr(t, []string{"issue", "close", "issue-1", "--reason", "done"})
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

func TestRun_IssueClose_HumanOutput(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-issue-close-human", map[string]string{
		"close": `{"protocolVersion":1,"schemaVersion":1,"result":null}`,
	}, `{}`)
	writeIssueConfigFor(t, "backend-issue-close-human")

	stdout, _, code := executePr(t, []string{"--output", "human", "issue", "close", "issue-1", "--reason", "done"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}
	if !strings.Contains(stdout, "Issue issue-1 closed") {
		t.Fatalf("human output = %q", stdout)
	}
}

func TestRun_IssueDeps_Success(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-issue-deps", map[string]string{
		"deps": `{"protocolVersion":1,"schemaVersion":1,"result":{"ids":["issue-2","issue-3"]}}`,
	}, `{}`)
	writeIssueConfigFor(t, "backend-issue-deps")

	stdout, _, code := executePr(t, []string{"issue", "deps", "issue-1"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}
	var resp scriptout.Response
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("decode response: %v (stdout=%s)", err, stdout)
	}
	var result schema.IssueDepsResult
	if err := scriptout.Decode(resp.Result, &result); err != nil {
		t.Fatalf("decode IssueDepsResult: %v", err)
	}
	if len(result.IDs) != 2 || result.IDs[0] != "issue-2" || result.IDs[1] != "issue-3" {
		t.Fatalf("IDs = %+v", result.IDs)
	}
}

func TestRun_IssueDeps_Full_HumanOutput(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-issue-deps-full", map[string]string{
		"deps": `{"protocolVersion":1,"schemaVersion":1,"result":{"ids":["issue-2"],"entities":[{"id":"issue-2","title":"blocker","state":"open"}]}}`,
	}, `{}`)
	writeIssueConfigFor(t, "backend-issue-deps-full")

	stdout, _, code := executePr(t, []string{"--output", "human", "issue", "deps", "issue-1", "--full"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}
	for _, want := range []string{"deps (1): issue-2", `[issue-2] "blocker" [open]`} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("human output missing %q; stdout=%s", want, stdout)
		}
	}
}

func TestRun_IssueComment_Success(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-issue-comment", map[string]string{
		"comment": `{"protocolVersion":1,"schemaVersion":1,"result":null}`,
	}, `{}`)
	writeIssueConfigFor(t, "backend-issue-comment")

	stdout, _, code := executePr(t, []string{"issue", "comment", "issue-1", "--body", "a comment"})
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

	stdout, _, code := executePr(t, []string{"issue", "transition", "issue-1", "--state", "Done"})
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

	stdout, _, code := executePr(t, []string{"issue", "transition", "issue-404", "--state", "Done"})
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
	// fixed Go enum, so there is no client-side --state validation. The
	// request must actually reach the backend, and an unrecognized
	// target state is reported by the backend as a well-formed (non
	// not_found) error, which maps to the generic exit-1 failure code —
	// never exit 4 (not_found is reserved for a genuinely missing entity,
	// not an invalid vocabulary value).
	writeOpAwareFakeBackend(t, "backend-issue-transition-vocab", map[string]string{
		"transition": `{"protocolVersion":1,"schemaVersion":1,"error":{"code":"unavailable","message":"target state \"bogus\" not in vocabulary"}}`,
	}, `{}`)
	writeIssueConfigFor(t, "backend-issue-transition-vocab")

	stdout, _, code := executePr(t, []string{"issue", "transition", "issue-1", "--state", "bogus"})
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

	stdout, _, code := executePr(t, []string{"--output", "human", "issue", "show", "issue-1"})
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

// TestRun_IssueShow_HumanOutput_IncludesDescriptionAssigneeParentDeps locks
// in the CLI-rendering half of finding 33's fix: once Show's response
// carries description/assignee/parent/deps (schema.Issue side, verified in
// pkg/schema and cmd/pg-connector-issue-beads/internal), formatIssue must
// actually display them in human mode rather than silently dropping them
// again at this last rendering step [review finding A-33].
func TestRun_IssueShow_HumanOutput_IncludesDescriptionAssigneeParentDeps(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-issue-show-human-fields", map[string]string{
		"show": `{"protocolVersion":1,"schemaVersion":1,"result":{"id":"issue-1","title":"t","state":"open",` +
			`"priority":"P1","assignee":"someone@example.com","parent":"issue-0",` +
			`"deps":[{"id":"issue-0","type":"parent-child"}],"description":"a desc"}}`,
	}, `{}`)
	writeIssueConfigFor(t, "backend-issue-show-human-fields")

	stdout, _, code := executePr(t, []string{"--output", "human", "issue", "show", "issue-1"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}
	for _, want := range []string{
		"assignee: someone@example.com",
		"parent: issue-0",
		"deps: issue-0 (parent-child)",
		"description: a desc",
	} {
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

	stdout, _, code := executePr(t, []string{"--output", "human", "issue", "create", "--title", "new issue"})
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

	stdout, _, code := executePr(t, []string{"--output", "human", "issue", "comment", "issue-1", "--body", "a comment"})
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

	stdout, _, code := executePr(t, []string{"--output", "human", "issue", "transition", "issue-1", "--state", "Done"})
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

	stdout, _, code := executePr(t, []string{"issue", "show", "issue-1"})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	// Regression for bug pg2-njx27: a Tier-1 "no backend registered"
	// failure must still produce a JSON error envelope on stdout, not an
	// empty stdout with the message only as stderr prose.
	var resp scriptout.Response
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("stdout is not a JSON envelope: %v; stdout=%q", err, stdout)
	}
	if resp.Error == nil || !strings.Contains(resp.Error.Message, "no backend registered") {
		t.Fatalf("resp.Error = %+v, want a message naming the no-backend-registered failure", resp.Error)
	}
}

// TestRun_IssueCreate_AmbiguousMultipleBackends_IsGenericFailure is the
// regression this packet's own acceptance criteria requires: issue create
// is the one id-less write routed through the same dispatch path as the
// id-keyed ops (issue show/comment/transition, ci get_logs/rerun_failed,
// pr show/files/commits) — the multi-instance try-each resolution
// policy is scoped to id-keyed ops only, by this phase's own operator
// ruling, so create must be BYTE-FOR-BYTE unchanged at N > 1: it stays
// on Dispatch (never DispatchTargeted), keeps hard-failing with the
// exact pre-existing error message and CLI exit code, and never
// attempts a fan-out/try-each across the two registered backends. This
// is the same generic exit-1 CLI failure path
// TestRun_CiLogs_AmbiguousMultipleBackends_IsGenericFailure used to
// assert for ci logs before this packet's change — that assertion moved
// to ci logs' own new multi-backend resolution tests in ci_test.go; this
// test keeps it alive for create, the one op it still applies to.
func TestRun_IssueCreate_AmbiguousMultipleBackends_IsGenericFailure(t *testing.T) {
	dir := t.TempDir()
	cfg := dir + "/config.yaml"
	if err := os.WriteFile(cfg, []byte("connector:\n  issue:\n    - backend-issue-create-a\n    - backend-issue-create-b\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("PG_PR_CONFIG", cfg)

	// Deliberately no fake backend binaries are written for
	// backend-issue-create-a/-b: Dispatch's hard-fail-at-N>1 check must
	// reject this registration before ever attempting to exec either
	// one, exactly as it always has — mirroring the pre-existing (now
	// superseded for ci logs) ambiguous-backends test's own fixture
	// style.
	stdout, _, code := executePr(t, []string{"issue", "create", "--title", "new issue"})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1 (unchanged generic CLI failure); stdout=%s", code, stdout)
	}
	var resp scriptout.Response
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("stdout is not a JSON envelope: %v; stdout=%q", err, stdout)
	}
	if resp.Error == nil || !strings.Contains(resp.Error.Message, "backends registered") {
		t.Fatalf("resp.Error = %+v, want the same ambiguous-registration message Dispatch has always produced", resp.Error)
	}
}
