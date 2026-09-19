package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// writeThreadConfigFor writes a connector.thread registry config naming
// backend as the sole registered thread backend — mirrors
// writeIssueConfigFor (issue_test.go), a distinct helper since both live
// in this same package main test binary. No XDG_STATE_HOME isolation is
// needed here (unlike writeIssueConfigFor/writeConfigFor): thread show/
// list make no cache_dispatch.go calls at all (this packet's own
// freedom-boundary choice, thread.go's header comment).
func writeThreadConfigFor(t *testing.T, backend string) {
	t.Helper()
	dir := t.TempDir()
	cfg := dir + "/config.yaml"
	if err := os.WriteFile(cfg, []byte("connector:\n  thread:\n    - "+backend+"\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("PG_PR_CONFIG", cfg)
}

// TestNewThreadCmd_HasShowAndListOnly is this packet's own acceptance
// criterion: newThreadCmd() has exactly show/list subcommands — no
// create/comment/transition/update/close/deps (thread is read-only) —
// mirroring TestNewAgentSessionCmd_HasShowAndListOnly's identical shape
// for its own read-only capability.
func TestNewThreadCmd_HasShowAndListOnly(t *testing.T) {
	cmd := newThreadCmd()
	names := map[string]bool{}
	for _, c := range cmd.Commands() {
		names[c.Name()] = true
	}
	if !names["show"] || !names["list"] {
		t.Fatalf("expected show and list subcommands, got %v", names)
	}
	if len(names) != 2 {
		t.Fatalf("expected exactly 2 subcommands, got %v", names)
	}
	for _, forbidden := range []string{"create", "comment", "transition", "update", "close", "deps"} {
		if names[forbidden] {
			t.Errorf("thread is read-only; unexpected %q subcommand", forbidden)
		}
	}
}

func TestRun_ThreadShow_Success(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-thread-show", map[string]string{
		"show": `{"protocolVersion":1,"schemaVersion":1,"result":{"id":"thread-1","channel":"C123","permalink":"https://slack.example/p1","reply_count":3,"text":"root message","mentions_me":true,"as_of":"2026-09-19T00:00:00Z","stale":false}}`,
	}, `{}`)
	writeThreadConfigFor(t, "backend-thread-show")

	stdout, _, code := executePr(t, []string{"thread", "show", "thread-1"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}

	var resp scriptout.Response
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("decode response: %v (stdout=%s)", err, stdout)
	}
	var th schema.Thread
	if err := scriptout.Decode(resp.Result, &th); err != nil {
		t.Fatalf("decode Thread: %v", err)
	}
	if th.ID != "thread-1" || th.Channel != "C123" || th.ReplyCount != 3 || !th.MentionsMe {
		t.Fatalf("thread = %+v", th)
	}
}

func TestRun_ThreadShow_NotFound_Exit4(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-thread-show-notfound", map[string]string{
		"show": `{"protocolVersion":1,"schemaVersion":1,"error":{"code":"not_found","message":"thread thread-404 not found"}}`,
	}, `{}`)
	writeThreadConfigFor(t, "backend-thread-show-notfound")

	stdout, _, code := executePr(t, []string{"thread", "show", "thread-404"})
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

func TestRun_ThreadShow_HumanOutput(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-thread-show-human", map[string]string{
		"show": `{"protocolVersion":1,"schemaVersion":1,"result":{"id":"thread-1","channel":"C123","permalink":"https://slack.example/p1","started_by":"u1","participants":["u1","u2"],"reply_count":2,"text":"hello","mentions_me":false,"as_of":"2026-09-19T00:00:00Z","stale":false}}`,
	}, `{}`)
	writeThreadConfigFor(t, "backend-thread-show-human")

	stdout, _, code := executePr(t, []string{"--output", "human", "thread", "show", "thread-1"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}
	if strings.Contains(stdout, "{") {
		t.Fatalf("human output must not contain raw JSON; stdout=%s", stdout)
	}
	for _, want := range []string{"thread thread-1", "C123", "permalink: https://slack.example/p1", "started_by: u1", "participants: u1, u2", "reply_count: 2", "text: hello", "mentions_me: false"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("human output missing %q; stdout=%s", want, stdout)
		}
	}
}

func TestRun_ThreadShow_NoBackendRegistered_IsGenericFailure(t *testing.T) {
	dir := t.TempDir()
	cfg := dir + "/config.yaml"
	if err := os.WriteFile(cfg, []byte("connector: {}\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("PG_PR_CONFIG", cfg)

	stdout, _, code := executePr(t, []string{"thread", "show", "thread-1"})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	var resp scriptout.Response
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("stdout is not a JSON envelope: %v; stdout=%q", err, stdout)
	}
	if resp.Error == nil || !strings.Contains(resp.Error.Message, "no backend registered") {
		t.Fatalf("resp.Error = %+v, want a message naming the no-backend-registered failure", resp.Error)
	}
}

// TestRun_ThreadList_ConfigLoadFailure_IsGenericFailureViaJSONEnvelope is
// this packet's own required "every error path routes through
// writeTargetedResult/writeFanOutResult, never a bare return err"
// acceptance criterion, applied to "thread list"'s own LoadRegistry
// failure path — deliberately NOT the same bare `return err` newIssueListCmd/
// newCalendarListCmd's own identical LoadRegistry-failure branch still
// takes (thread.go's own newThreadListCmd doc comment explains why this
// packet does not reintroduce that gap). A bare `return err` here would
// print bare stderr prose with an EMPTY stdout (never a JSON envelope) —
// this proves stdout instead carries a well-formed wire error envelope.
func TestRun_ThreadList_ConfigLoadFailure_IsGenericFailureViaJSONEnvelope(t *testing.T) {
	t.Setenv("PG_PR_CONFIG", "/does/not/exist/config.yaml")

	stdout, stderr, code := executePr(t, []string{"thread", "list", "--query", "mine"})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stdout=%s stderr=%s", code, stdout, stderr)
	}
	var resp scriptout.Response
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("stdout is not a JSON envelope: %v; stdout=%q stderr=%q", err, stdout, stderr)
	}
	if resp.Error == nil || !strings.Contains(resp.Error.Message, "does not exist") {
		t.Fatalf("resp.Error = %+v, want a message naming the config-load failure", resp.Error)
	}
}

func TestRun_ThreadList_Success_FullEntities(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-thread-list", map[string]string{
		"list": `{"protocolVersion":1,"schemaVersion":1,"result":{"entities":[{"id":"thread-1","channel":"C123","reply_count":1,"as_of":"2026-09-19T00:00:00Z","stale":false}],"present_ids":["thread-1"],"cursor":null,"truncated":true}}`,
	}, `{}`)
	writeThreadConfigFor(t, "backend-thread-list")

	stdout, _, code := executePr(t, []string{"thread", "list", "--query", "mine"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}

	var outcome threadListOutcome
	if err := json.Unmarshal([]byte(stdout), &outcome); err != nil {
		t.Fatalf("decode outcome: %v (stdout=%s)", err, stdout)
	}
	if len(outcome.Entities) != 1 || outcome.Entities[0].ID != "thread-1" {
		t.Fatalf("outcome.Entities = %+v", outcome.Entities)
	}
	if len(outcome.PresentIDs) != 1 || outcome.PresentIDs[0] != "thread-1" {
		t.Fatalf("outcome.PresentIDs = %+v", outcome.PresentIDs)
	}
	if len(outcome.Sources) != 1 || outcome.Sources[0].Status != SourceSucceeded || outcome.Sources[0].Count != 1 {
		t.Fatalf("outcome.Sources = %+v", outcome.Sources)
	}
}

// TestRun_ThreadList_IdsOnly_ReturnsPresentIDsNotEmptyEntities mirrors
// TestRun_IssueList_IdsOnly_ReturnsPresentIDsNotEmptyEntities (issue_test.go):
// with --ids-only, a backend leaves Entities empty per
// ThreadListResult's documented ids_only contract and populates only
// PresentIDs.
func TestRun_ThreadList_IdsOnly_ReturnsPresentIDsNotEmptyEntities(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-thread-list-ids", map[string]string{
		"list": `{"protocolVersion":1,"schemaVersion":1,"result":{"entities":[],"present_ids":["thread-1","thread-2"],"cursor":null,"truncated":true}}`,
	}, `{}`)
	writeThreadConfigFor(t, "backend-thread-list-ids")

	stdout, _, code := executePr(t, []string{"thread", "list", "--query", "mine", "--ids-only"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}

	var outcome threadListOutcome
	if err := json.Unmarshal([]byte(stdout), &outcome); err != nil {
		t.Fatalf("decode outcome: %v (stdout=%s)", err, stdout)
	}
	if len(outcome.Entities) != 0 {
		t.Fatalf("outcome.Entities = %+v, want empty", outcome.Entities)
	}
	if len(outcome.PresentIDs) != 2 || outcome.PresentIDs[0] != "thread-1" || outcome.PresentIDs[1] != "thread-2" {
		t.Fatalf("outcome.PresentIDs = %+v, want both matched ids", outcome.PresentIDs)
	}
}

// TestRun_ThreadList_ZeroMatch is the bead's own required "at least one
// zero-match case": a registered backend answers "list" successfully
// with no matching threads at all, and the umbrella must still report
// exit 0 with non-null empty entities[]/present_ids[] (never null,
// mirroring issue.go's/pr.go's own "bug A15" convention) and a succeeded
// source row with count 0.
func TestRun_ThreadList_ZeroMatch(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-thread-list-empty", map[string]string{
		"list": `{"protocolVersion":1,"schemaVersion":1,"result":{"entities":[],"present_ids":[],"cursor":null,"truncated":true}}`,
	}, `{}`)
	writeThreadConfigFor(t, "backend-thread-list-empty")

	stdout, _, code := executePr(t, []string{"thread", "list", "--query", "mine"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}
	if strings.Contains(stdout, "null") {
		t.Fatalf("stdout must not contain a null entities/present_ids array; stdout=%s", stdout)
	}

	var outcome threadListOutcome
	if err := json.Unmarshal([]byte(stdout), &outcome); err != nil {
		t.Fatalf("decode outcome: %v (stdout=%s)", err, stdout)
	}
	if len(outcome.Entities) != 0 || len(outcome.PresentIDs) != 0 {
		t.Fatalf("outcome = %+v, want zero matches", outcome)
	}
	if len(outcome.Sources) != 1 || outcome.Sources[0].Status != SourceSucceeded || outcome.Sources[0].Count != 0 {
		t.Fatalf("outcome.Sources = %+v, want one succeeded row with count 0", outcome.Sources)
	}

	humanStdout, _, humanCode := executePr(t, []string{"--output", "human", "thread", "list", "--query", "mine"})
	if humanCode != 0 {
		t.Fatalf("human exit code = %d, want 0; stdout=%s", humanCode, humanStdout)
	}
	if !strings.Contains(humanStdout, "threads: (none)") {
		t.Fatalf("human output = %q, want the zero-match rendering", humanStdout)
	}
}

// TestRun_ThreadList_AllQueryNotRecognized_Exit1 is the bead's own
// required "at least one query-not-recognized case", mirroring pr.go's/
// issue.go's own allQueryNotRecognized umbrella handling
// (list.go): every registered thread backend answering
// query_not_recognized fails the whole call as invalid_argument (exit
// 1), never the ordinary fan-out exit 2/3 scheme.
func TestRun_ThreadList_AllQueryNotRecognized_Exit1(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-thread-list-badquery", map[string]string{
		"list": `{"protocolVersion":1,"schemaVersion":1,"error":{"code":"query_not_recognized","message":"query \"bogus\" is not defined"}}`,
	}, `{}`)
	writeThreadConfigFor(t, "backend-thread-list-badquery")

	stdout, _, code := executePr(t, []string{"thread", "list", "--query", "bogus"})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stdout=%s", code, stdout)
	}
	var resp scriptout.Response
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("stdout is not a JSON envelope: %v; stdout=%q", err, stdout)
	}
	if resp.Error == nil || resp.Error.Code != "invalid_argument" {
		t.Fatalf("resp.Error = %+v, want invalid_argument", resp.Error)
	}
	if !strings.Contains(resp.Error.Message, "bogus") {
		t.Fatalf("resp.Error.Message = %q, want it to name the unrecognized query", resp.Error.Message)
	}
}

// TestRun_ThreadList_IdsOnly_HumanOutput_ShowsIDsNotNone mirrors
// TestRun_IssueList_IdsOnly_HumanOutput_ShowsIDsNotNone (issue_test.go):
// the human-output branch must not misreport a non-zero --ids-only match
// count as "(none)".
func TestRun_ThreadList_IdsOnly_HumanOutput_ShowsIDsNotNone(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-thread-list-human", map[string]string{
		"list": `{"protocolVersion":1,"schemaVersion":1,"result":{"entities":[],"present_ids":["thread-1"],"cursor":null,"truncated":true}}`,
	}, `{}`)
	writeThreadConfigFor(t, "backend-thread-list-human")

	stdout, _, code := executePr(t, []string{"--output", "human", "thread", "list", "--query", "mine", "--ids-only"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}
	if strings.Contains(stdout, "threads: (none)") {
		t.Fatalf("human output = %q, want it to show the matched id, not report zero matches", stdout)
	}
	if !strings.Contains(stdout, "thread-1") {
		t.Fatalf("human output = %q, want it to contain the matched id", stdout)
	}
}

// TestHumanizeThreadShow_RoundTrip is this packet's own required
// "TestHumanizeThread"-matching coverage (bead's Validation section),
// proving humanizeThreadShow decodes and renders a raw wire result
// directly, independent of the CLI dispatch path the tests above already
// cover end to end.
func TestHumanizeThreadShow_RoundTrip(t *testing.T) {
	raw := json.RawMessage(`{"id":"thread-1","channel":"C123","permalink":"https://slack.example/p1","started_by":"u1","participants":["u1","u2"],"reply_count":4,"text":"hi there","mentions_me":true,"as_of":"2026-09-19T00:00:00Z","stale":false}`)
	out, err := humanizeThreadShow(raw)
	if err != nil {
		t.Fatalf("humanizeThreadShow: %v", err)
	}
	for _, want := range []string{"thread thread-1", "C123", "permalink: https://slack.example/p1", "started_by: u1", "participants: u1, u2", "reply_count: 4", "text: hi there", "mentions_me: true"} {
		if !strings.Contains(out, want) {
			t.Fatalf("humanizeThreadShow output missing %q; got %q", want, out)
		}
	}
}

// TestHumanizeThreadListOutcome_ZeroMatch is this packet's own required
// "TestHumanizeThread"-matching coverage for the zero-match rendering
// path, independent of the CLI dispatch path TestRun_ThreadList_ZeroMatch
// already covers end to end.
func TestHumanizeThreadListOutcome_ZeroMatch(t *testing.T) {
	out := humanizeThreadListOutcome(threadListOutcome{
		Entities:   []schema.Thread{},
		PresentIDs: []string{},
		Sources:    []SourceResult{{Source: "backend-a", Status: SourceSucceeded, Count: 0}},
	})
	if !strings.Contains(out, "threads: (none)") {
		t.Fatalf("humanizeThreadListOutcome = %q, want the zero-match rendering", out)
	}
}

// TestHumanizeThreadListOutcome_IdsOnly is this packet's own required
// "TestHumanizeThread"-matching coverage for the --ids-only rendering
// path (PresentIDs populated, Entities empty), mirroring
// humanizeIssueListOutcome's identical branch.
func TestHumanizeThreadListOutcome_IdsOnly(t *testing.T) {
	out := humanizeThreadListOutcome(threadListOutcome{
		Entities:   []schema.Thread{},
		PresentIDs: []string{"thread-1"},
		Sources:    []SourceResult{{Source: "backend-a", Status: SourceSucceeded, Count: 1}},
	})
	if strings.Contains(out, "threads: (none)") {
		t.Fatalf("humanizeThreadListOutcome = %q, want it to show the matched id, not report zero matches", out)
	}
	if !strings.Contains(out, "thread-1") {
		t.Fatalf("humanizeThreadListOutcome = %q, want it to contain the matched id", out)
	}
}
