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

// executePr runs the root command with args, capturing stdout and stderr in
// SEPARATE buffers (unlike run(), which writes to the real
// os.Stdout/os.Stderr) so tests can assert on the wire-response body
// pg-connector printed, not just the process exit code — and so a
// stream-discipline violation (prose landing on the wrong stream) is
// actually observable [review finding 31]. A single shared buffer would
// let stray stderr prose silently show up inside a test's "stdout" string
// (or vice versa), so no test in this package could ever catch that class
// of bug.
func executePr(t *testing.T, args []string) (stdout string, stderr string, exitCode int) {
	t.Helper()
	root := newRootCmd()
	var outBuf, errBuf bytes.Buffer
	root.SetOut(&outBuf)
	root.SetErr(&errBuf)
	root.SetArgs(args)

	err := root.Execute()
	if err == nil {
		return outBuf.String(), errBuf.String(), 0
	}
	var ee *exitError
	if errors.As(err, &ee) {
		return outBuf.String(), errBuf.String(), ee.code
	}
	return outBuf.String(), errBuf.String(), 1
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
		"show": `{"protocolVersion":1,"schemaVersion":1,"result":{"id":"pr-1","repo":"o/r","number":1,"title":"t","state":"open","branch":"b","base":"main","author":"a","url":"u","draft":false,"merged":false,"comments":[{"id":"c1","author":"a","body":"body","resolved":false}],"reviews":[{"id":"r1","author":"rev","state":"CHANGES_REQUESTED","comments":[{"id":"c2","author":"rev","body":"fix","thread_id":"th1"}]}]}}`,
	}, `{}`)
	writeConfigFor(t, "backend-show")

	stdout, _, code := executePr(t, []string{"pr", "show", "pr-1"})
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
	if len(pr.Comments) != 1 || pr.Comments[0].ID != "c1" {
		t.Fatalf("pr.Comments = %+v", pr.Comments)
	}
	if len(pr.Reviews) != 1 || len(pr.Reviews[0].Comments) != 1 ||
		pr.Reviews[0].Comments[0].ID != "c2" {
		t.Fatalf("pr.Reviews = %+v", pr.Reviews)
	}
}

func TestRun_PrShow_Success_EmitsNoStderr(t *testing.T) {
	// Regression for the test-harness fix [review finding 31]: with
	// executePr's stdout/stderr now captured in separate buffers, this
	// asserts directly on the stream-discipline invariant the wire
	// protocol requires — a successful op's ONLY output is the JSON
	// envelope on stdout, nothing on stderr. Before the fix this could not
	// even be expressed: stderr content silently landed inside "stdout"
	// (or vice versa), so no test in this package could tell the two
	// streams apart.
	writeOpAwareFakeBackend(t, "backend-show-clean-stderr", map[string]string{
		"show": `{"protocolVersion":1,"schemaVersion":1,"result":{"id":"pr-1","repo":"o/r","number":1,"title":"t","state":"open","branch":"b","base":"main","author":"a","url":"u","draft":false,"merged":false}}`,
	}, `{}`)
	writeConfigFor(t, "backend-show-clean-stderr")

	_, stderr, code := executePr(t, []string{"pr", "show", "pr-1"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty on a successful op", stderr)
	}
}

func TestRun_PrFiles_Success(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-files", map[string]string{
		"files": `{"protocolVersion":1,"schemaVersion":4,"result":{"id":"pr-1","files":[{"path":"a.go","additions":5,"deletions":1}]}}`,
	}, `{}`)
	writeConfigFor(t, "backend-files")

	stdout, _, code := executePr(t, []string{"pr", "files", "pr-1"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}

	var resp scriptout.Response
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("decode response: %v (stdout=%s)", err, stdout)
	}
	var result schema.PRFilesResult
	if err := scriptout.Decode(resp.Result, &result); err != nil {
		t.Fatalf("decode PRFilesResult: %v", err)
	}
	if result.ID != "pr-1" || len(result.Files) != 1 || result.Files[0].Path != "a.go" {
		t.Fatalf("result = %+v", result)
	}
}

func TestRun_PrFiles_HumanOutput(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-files-human", map[string]string{
		"files": `{"protocolVersion":1,"schemaVersion":4,"result":{"id":"pr-1","files":[{"path":"a.go","additions":5,"deletions":1}]}}`,
	}, `{}`)
	writeConfigFor(t, "backend-files-human")

	stdout, _, code := executePr(t, []string{"--output", "human", "pr", "files", "pr-1"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}
	if strings.Contains(stdout, "{") {
		t.Fatalf("human output must not contain raw JSON; stdout=%s", stdout)
	}
	for _, want := range []string{"PR pr-1", "a.go", "+5/-1"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("human output missing %q; stdout=%s", want, stdout)
		}
	}
}

func TestRun_PrCommits_Success(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-commits", map[string]string{
		"commits": `{"protocolVersion":1,"schemaVersion":4,"result":{"id":"pr-1","commits":[{"sha":"abc123","author":"alice","message":"fix bug"}]}}`,
	}, `{}`)
	writeConfigFor(t, "backend-commits")

	stdout, _, code := executePr(t, []string{"pr", "commits", "pr-1"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}

	var resp scriptout.Response
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("decode response: %v (stdout=%s)", err, stdout)
	}
	var result schema.PRCommitsResult
	if err := scriptout.Decode(resp.Result, &result); err != nil {
		t.Fatalf("decode PRCommitsResult: %v", err)
	}
	if result.ID != "pr-1" || len(result.Commits) != 1 || result.Commits[0].Author != "alice" {
		t.Fatalf("result = %+v", result)
	}
}

func TestRun_PrCommits_HumanOutput(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-commits-human", map[string]string{
		"commits": `{"protocolVersion":1,"schemaVersion":4,"result":{"id":"pr-1","commits":[{"sha":"abc123","author":"alice","message":"fix bug"}]}}`,
	}, `{}`)
	writeConfigFor(t, "backend-commits-human")

	stdout, _, code := executePr(t, []string{"--output", "human", "pr", "commits", "pr-1"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}
	if strings.Contains(stdout, "{") {
		t.Fatalf("human output must not contain raw JSON; stdout=%s", stdout)
	}
	for _, want := range []string{"PR pr-1", "abc123", "alice", "fix bug"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("human output missing %q; stdout=%s", want, stdout)
		}
	}
}

func TestRun_PrShow_HumanOutput(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-show-human", map[string]string{
		"show": `{"protocolVersion":1,"schemaVersion":1,"result":{"id":"pr-1","repo":"o/r","number":1,"title":"t","state":"open","branch":"b","base":"main","author":"a","url":"u","draft":false,"merged":false,"labels":["x","y"],"comments":[{"id":"c1","author":"a","body":"body","resolved":false}],"reviews":[{"id":"r1","author":"rev","state":"CHANGES_REQUESTED","comments":[{"id":"c2","author":"rev","body":"fix","thread_id":"th1"}]}]}}`,
	}, `{}`)
	writeConfigFor(t, "backend-show-human")

	stdout, _, code := executePr(t, []string{"--output", "human", "pr", "show", "pr-1"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}
	if strings.Contains(stdout, "{") {
		t.Fatalf("human output must not contain raw JSON; stdout=%s", stdout)
	}
	for _, want := range []string{"PR pr-1", "o/r#1", "\"t\"", "[open]", "branch: b -> main", "labels: x, y", "c1"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("human output missing %q; stdout=%s", want, stdout)
		}
	}
}

func TestRun_PrShow_JSONOutput_DefaultUnchangedWithOutputFlagExplicit(t *testing.T) {
	// --output json must be byte-identical to the pre-existing (no flag)
	// default behavior [bead pg2-ox1k6's backward-compatibility requirement].
	writeOpAwareFakeBackend(t, "backend-show-json-explicit", map[string]string{
		"show": `{"protocolVersion":1,"schemaVersion":1,"result":{"id":"pr-1","repo":"o/r","number":1,"title":"t","state":"open","branch":"b","base":"main","author":"a","url":"u","draft":false,"merged":false}}`,
	}, `{}`)
	writeConfigFor(t, "backend-show-json-explicit")

	stdoutDefault, _, codeDefault := executePr(t, []string{"pr", "show", "pr-1"})
	stdoutExplicit, _, codeExplicit := executePr(t, []string{"--output", "json", "pr", "show", "pr-1"})
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

	stdout, _, code := executePr(t, []string{"pr", "show", "pr-1"})
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

	stdout, _, code := executePr(t, []string{"pr", "show", "pr-1"})
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

	stdout, _, code := executePr(t, []string{"pr", "show", "pr-1"})
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

// TestRun_PrList_Success_FullEntities is a baseline sanity check for "pr
// list" without --ids-only: the pre-existing, already-correct path
// (entities populated, present_ids populated alongside it per
// PRListResult's "always populated regardless of ids_only" contract).
// There was no existing coverage of "pr list" at all before bug
// pg2-nc3iy's fix.
func TestRun_PrList_Success_FullEntities(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-pr-list", map[string]string{
		"list": `{"protocolVersion":1,"schemaVersion":1,"result":{"entities":[{"id":"o/r#1","repo":"o/r","number":1,"title":"t","state":"open","branch":"b","base":"main","author":"a","url":"u","draft":false,"merged":false}],"present_ids":["o/r#1"],"cursor":null,"truncated":false}}`,
	}, `{}`)
	writeConfigFor(t, "backend-pr-list")

	stdout, _, code := executePr(t, []string{"pr", "list", "--query", "mine"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}

	var outcome prListOutcome
	if err := json.Unmarshal([]byte(stdout), &outcome); err != nil {
		t.Fatalf("decode outcome: %v (stdout=%s)", err, stdout)
	}
	if len(outcome.Entities) != 1 || outcome.Entities[0].ID != "o/r#1" {
		t.Fatalf("outcome.Entities = %+v", outcome.Entities)
	}
	if len(outcome.PresentIDs) != 1 || outcome.PresentIDs[0] != "o/r#1" {
		t.Fatalf("outcome.PresentIDs = %+v", outcome.PresentIDs)
	}
	if len(outcome.Sources) != 1 || outcome.Sources[0].Status != SourceSucceeded || outcome.Sources[0].Count != 1 {
		t.Fatalf("outcome.Sources = %+v", outcome.Sources)
	}
}

// TestRun_PrList_IdsOnly_ReturnsPresentIDsNotEmptyEntities is the
// regression test for bug pg2-nc3iy: `pg-connector pr list --ids-only`
// always returned an empty entities array despite sources[].count
// correctly reporting the real match count. Root cause: fanOutPRList
// (pr.go) only ever forwarded a backend's result.Entities into the
// umbrella outcome; with ids_only true, a backend correctly leaves its
// own Entities empty per PRListResult's documented ids_only contract
// (pkg/schema/pr.go) and populates only PresentIDs instead, so the
// concatenation step appended nothing and the caller had no way to
// recover the matched ids at all. The fix surfaces PresentIDs on
// prListOutcome itself.
func TestRun_PrList_IdsOnly_ReturnsPresentIDsNotEmptyEntities(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-pr-list-ids", map[string]string{
		"list": `{"protocolVersion":1,"schemaVersion":1,"result":{"entities":[],"present_ids":["o/r#1","o/r#2"],"cursor":null,"truncated":false}}`,
	}, `{}`)
	writeConfigFor(t, "backend-pr-list-ids")

	stdout, _, code := executePr(t, []string{"pr", "list", "--query", "mine", "--ids-only"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}

	var outcome prListOutcome
	if err := json.Unmarshal([]byte(stdout), &outcome); err != nil {
		t.Fatalf("decode outcome: %v (stdout=%s)", err, stdout)
	}
	if len(outcome.Entities) != 0 {
		t.Fatalf("outcome.Entities = %+v, want empty (ids_only leaves entities empty by design)", outcome.Entities)
	}
	if len(outcome.PresentIDs) != 2 || outcome.PresentIDs[0] != "o/r#1" || outcome.PresentIDs[1] != "o/r#2" {
		t.Fatalf("outcome.PresentIDs = %+v, want both matched ids surfaced (the bug: this was always empty)", outcome.PresentIDs)
	}
	if len(outcome.Sources) != 1 || outcome.Sources[0].Status != SourceSucceeded || outcome.Sources[0].Count != 2 {
		t.Fatalf("outcome.Sources = %+v, want count 2 (sources[].count already worked pre-fix)", outcome.Sources)
	}
}

// TestRun_PrList_IdsOnly_FanOut_ConcatenatesPresentIDsAcrossBackends
// proves the fix also works across a multi-backend fan-out (the bug
// report's "team" query repro: a 6-clause fan-out, not just a
// single-backend "mine" query) — present_ids from every queried backend
// must concatenate, exactly like entities does in the non-ids_only case.
func TestRun_PrList_IdsOnly_FanOut_ConcatenatesPresentIDsAcrossBackends(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-pr-list-a", map[string]string{
		"list": `{"protocolVersion":1,"schemaVersion":1,"result":{"entities":[],"present_ids":["o/r#1"],"cursor":null,"truncated":false}}`,
	}, `{}`)
	writeOpAwareFakeBackend(t, "backend-pr-list-b", map[string]string{
		"list": `{"protocolVersion":1,"schemaVersion":1,"result":{"entities":[],"present_ids":["o/r#2"],"cursor":null,"truncated":false}}`,
	}, `{}`)
	// writeConfigFor only supports a single backend and writeCiConfigFor
	// hardcodes "connector.ci" -- pr needs its own list-valued
	// "connector.pr" block with two entries, so build the config directly.
	dir := t.TempDir()
	cfg := dir + "/config.yaml"
	if err := os.WriteFile(cfg, []byte("connector:\n  pr:\n    - backend-pr-list-a\n    - backend-pr-list-b\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("PG_PR_CONFIG", cfg)

	stdout, _, code := executePr(t, []string{"pr", "list", "--query", "team", "--ids-only"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}

	var outcome prListOutcome
	if err := json.Unmarshal([]byte(stdout), &outcome); err != nil {
		t.Fatalf("decode outcome: %v (stdout=%s)", err, stdout)
	}
	if len(outcome.PresentIDs) != 2 {
		t.Fatalf("outcome.PresentIDs = %+v, want 2 concatenated ids across both backends", outcome.PresentIDs)
	}
	if len(outcome.Sources) != 2 {
		t.Fatalf("outcome.Sources = %+v, want one row per backend, never collapsed", outcome.Sources)
	}
}

// TestRun_PrList_IdsOnly_HumanOutput_ShowsIDsNotNone covers the human
// (--output human) rendering path, which branched on len(Entities) alone
// before the fix and so always printed "prs: (none)" for --ids-only
// regardless of how many ids actually matched.
func TestRun_PrList_IdsOnly_HumanOutput_ShowsIDsNotNone(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-pr-list-human", map[string]string{
		"list": `{"protocolVersion":1,"schemaVersion":1,"result":{"entities":[],"present_ids":["o/r#1"],"cursor":null,"truncated":false}}`,
	}, `{}`)
	writeConfigFor(t, "backend-pr-list-human")

	stdout, _, code := executePr(t, []string{"--output", "human", "pr", "list", "--query", "mine", "--ids-only"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}
	if strings.Contains(stdout, "prs: (none)") {
		t.Fatalf("human output = %q, want it to show the matched id, not report zero matches", stdout)
	}
	if !strings.Contains(stdout, "o/r#1") {
		t.Fatalf("human output = %q, want it to contain the matched id", stdout)
	}
}
