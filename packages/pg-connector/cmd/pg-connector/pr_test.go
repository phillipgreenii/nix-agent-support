package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	prprov "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/pr"
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
	// Phase 14 (bead pg2-2j5ac.42.2): "pr show"/"pr list" now read/write
	// this docket's umbrella entity cache on every call (default-on,
	// no opt-out configured here), which resolves under
	// $XDG_STATE_HOME/pg-connector/cache/ — isolate it to this test's own
	// temp dir so a test never touches the real
	// ~/.local/state/pg-connector/cache/ on whatever machine runs `go
	// test` [code-file-standards' unit-test-isolation rule].
	t.Setenv("XDG_STATE_HOME", dir)
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

// TestPrShowCacheFallback_ServesStaleOnBackendUnavailable exercises this
// packet's own wiring in newPrShowCmd end to end through the real CLI
// entry point (dispatchShowWithCache_test.go's own tests cover the helper
// directly; this proves pr.go actually calls it and Puts a live success
// into the cache): a live "pr show" success, then a subsequent call
// against the same backend now answering unavailable is served from that
// cache with stale: true and exit 0 (this docket's own Binding decision).
func TestPrShowCacheFallback_ServesStaleOnBackendUnavailable(t *testing.T) {
	liveAsOf := time.Now().UTC().Format(time.RFC3339)
	writeOpAwareFakeBackend(t, "backend-pr-cache-fallback", map[string]string{
		"show": fmt.Sprintf(`{"protocolVersion":1,"schemaVersion":1,"result":{"id":"pr-1","repo":"o/r","number":1,"title":"t","state":"open","branch":"b","base":"main","author":"a","url":"u","draft":false,"merged":false,"as_of":%q,"stale":false}}`, liveAsOf),
	}, `{"protocolVersion":1,"schemaVersions":{"pr":1},"ops":["capabilities","show"]}`)
	writeConfigFor(t, "backend-pr-cache-fallback")

	stdout, _, code := executePr(t, []string{"pr", "show", "pr-1"})
	if code != 0 {
		t.Fatalf("live show exit code = %d, want 0; stdout=%s", code, stdout)
	}
	var resp scriptout.Response
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	var pr schema.PR
	if err := scriptout.Decode(resp.Result, &pr); err != nil {
		t.Fatalf("decode PR: %v", err)
	}
	if pr.Stale {
		t.Fatal("live pr.Stale = true, want false")
	}

	// Re-point the same registered backend name at a script now answering
	// unavailable for "show".
	writeOpAwareFakeBackend(t, "backend-pr-cache-fallback", map[string]string{
		"show": `{"protocolVersion":1,"schemaVersion":1,"error":{"code":"unavailable","message":"backend down"}}`,
	}, `{"protocolVersion":1,"schemaVersions":{"pr":1},"ops":["capabilities","show"]}`)

	stdout2, _, code2 := executePr(t, []string{"pr", "show", "pr-1"})
	if code2 != 0 {
		t.Fatalf("cache-fallback show exit code = %d, want 0 (a stale-but-served read); stdout=%s", code2, stdout2)
	}
	var resp2 scriptout.Response
	if err := json.Unmarshal([]byte(stdout2), &resp2); err != nil {
		t.Fatalf("decode response 2: %v", err)
	}
	var pr2 schema.PR
	if err := scriptout.Decode(resp2.Result, &pr2); err != nil {
		t.Fatalf("decode PR 2: %v", err)
	}
	if !pr2.Stale {
		t.Fatal("cache-fallback pr.Stale = false, want true")
	}
	if pr2.AsOf != liveAsOf {
		t.Fatalf("cache-fallback pr.AsOf = %q, want the ORIGINAL live as_of %q", pr2.AsOf, liveAsOf)
	}
	if pr2.ID != "pr-1" {
		t.Fatalf("cache-fallback pr.ID = %q, want pr-1", pr2.ID)
	}
}

// TestPrListCacheFallback_DegradedSourceServesStaleEntities covers this
// docket's own acceptance criterion for "pr list": a per-backend fallback
// reports that backend's sources[] row as degraded (never "succeeded" —
// this docket's own Binding decision: never claim "ok" for a
// fallback-served backend) with a reason noting the fallback, while still
// serving the cached, stale-stamped entities in Entities — alongside a
// second, healthy backend, so the aggregate exit code is 2 (degraded: one
// healthy, one failed), not 3 (which a solo fallback-only backend would
// report under the EXISTING, unmodified exit-code scheme).
func TestPrListCacheFallback_DegradedSourceServesStaleEntities(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-pr-list-cache-fallback", map[string]string{
		"list": `{"protocolVersion":1,"schemaVersion":1,"error":{"code":"unavailable","message":"backend down"}}`,
	}, `{"protocolVersion":1,"schemaVersions":{"pr":1},"ops":["capabilities","list"]}`)
	writeOpAwareFakeBackend(t, "backend-pr-list-healthy", map[string]string{
		"list": `{"protocolVersion":1,"schemaVersion":1,"result":{"entities":[],"present_ids":[],"cursor":null,"truncated":false}}`,
	}, `{}`)

	dir := t.TempDir()
	cfg := dir + "/config.yaml"
	if err := os.WriteFile(cfg, []byte("connector:\n  pr:\n    - backend-pr-list-cache-fallback\n    - backend-pr-list-healthy\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("PG_PR_CONFIG", cfg)
	t.Setenv("XDG_STATE_HOME", dir)

	seedCache(t, CacheKey{Type: "pr", Backend: "backend-pr-list-cache-fallback"}, &Cache{Entries: map[string]CacheEntry{
		"o/r#1": {
			Content:    json.RawMessage(`{"id":"o/r#1","repo":"o/r","number":1,"title":"t","state":"open","branch":"b","base":"main","author":"a","url":"u","draft":false,"merged":false,"as_of":"2026-01-01T00:00:00Z","stale":false}`),
			AsOf:       time.Now(),
			LastAccess: time.Now(),
		},
	}})

	stdout, _, code := executePr(t, []string{"pr", "list", "--query", "mine"})
	if code != 2 {
		t.Fatalf("exit code = %d, want 2 (degraded: one healthy, one fallback-served-but-still-degraded); stdout=%s", code, stdout)
	}
	var outcome prListOutcome
	if err := json.Unmarshal([]byte(stdout), &outcome); err != nil {
		t.Fatalf("decode outcome: %v (stdout=%s)", err, stdout)
	}
	if len(outcome.Entities) != 1 || outcome.Entities[0].ID != "o/r#1" || !outcome.Entities[0].Stale {
		t.Fatalf("outcome.Entities = %+v, want exactly one stale-stamped o/r#1", outcome.Entities)
	}
	if len(outcome.Sources) != 2 {
		t.Fatalf("outcome.Sources = %+v, want one row per backend", outcome.Sources)
	}
	var fallbackRow *SourceResult
	for i := range outcome.Sources {
		if outcome.Sources[i].Source == "backend-pr-list-cache-fallback" {
			fallbackRow = &outcome.Sources[i]
		}
	}
	if fallbackRow == nil || fallbackRow.Status != SourceDegraded || fallbackRow.Reason == "" {
		t.Fatalf("fallback backend's own source row = %+v, want a degraded row with a reason noting the fallback", fallbackRow)
	}
}

// reviewSubmitResp is a well-formed review_submit wire response.
const reviewSubmitOKResp = `{"protocolVersion":1,"schemaVersion":4,"result":{"review_id":"r1","state":"pending","head_sha":"abc","as_of":"2026-01-01T00:00:00Z"}}`

func executePrWithStdin(t *testing.T, stdin string, args []string) (string, string, int) {
	t.Helper()
	root := newRootCmd()
	var outBuf, errBuf bytes.Buffer
	root.SetOut(&outBuf)
	root.SetErr(&errBuf)
	root.SetIn(strings.NewReader(stdin))
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

func TestReviewSubmitNotFoundExits4(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-rs-nf", map[string]string{
		"review_submit": `{"protocolVersion":1,"schemaVersion":4,"error":{"code":"not_found","message":"no such pr"}}`,
	}, `{}`)
	writeConfigFor(t, "backend-rs-nf")
	stdout, _, code := executePrWithStdin(t, `{"head_sha":"abc","body":"b"}`, []string{"pr", "review", "submit", "pr-1"})
	if code != 4 {
		t.Fatalf("exit code = %d, want 4; stdout=%s", code, stdout)
	}
}

func TestReviewSubmitStdinParsedIntoWireArgs(t *testing.T) {
	args, err := readReviewSubmitArgs(strings.NewReader(`{"id":"ignored","head_sha":"abc","body":"b","comments":[{"path":"a.go","line":3,"side":"RIGHT","body":"c"}],"supersede_pending":true}`), "pr-9", "")
	if err != nil {
		t.Fatal(err)
	}
	if args["id"] != "pr-9" || args["head_sha"] != "abc" || args["supersede_pending"] != true {
		t.Fatalf("args = %+v", args)
	}
	if cs, ok := args["comments"].([]any); !ok || len(cs) != 1 {
		t.Fatalf("comments = %+v", args["comments"])
	}
	if _, err := readReviewSubmitArgs(strings.NewReader(`not json`), "pr-9", ""); err == nil {
		t.Fatal("want error for non-JSON stdin")
	}
}

func TestReviewSubmitBadStdinExits1(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-rs-bad", map[string]string{"review_submit": reviewSubmitOKResp}, `{}`)
	writeConfigFor(t, "backend-rs-bad")
	_, _, code := executePrWithStdin(t, `nope`, []string{"pr", "review", "submit", "pr-1"})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
}

// TestReviewSubmitStatusesAllExit0: every status (posted, append, no_change) is
// a well-formed result, so the CLI exits 0 (INV-EXIT-1's Targeted scheme) and
// the status in the JSON output is what distinguishes them.
func TestReviewSubmitStatusesAllExit0(t *testing.T) {
	results := map[string]string{
		"posted":    `{"review_id":"r1","state":"pending","head_sha":"abc","as_of":"t","status":"posted","added":2,"already_present":0,"dismissed":0,"body":"written","extra_pending_reviews":0,"url":"https://example.invalid/r"}`,
		"append":    `{"review_id":"r1","state":"pending","head_sha":"abc","as_of":"t","status":"append","added":1,"already_present":1,"dismissed":0,"body":"kept","extra_pending_reviews":1,"last_append":{"at":"t","added":1,"head":"abc"}}`,
		"no_change": `{"review_id":"","state":"none","head_sha":"abc","as_of":"t","status":"no_change","added":0,"already_present":2,"dismissed":0,"body":"absent","extra_pending_reviews":0}`,
	}
	for status, result := range results {
		t.Run(status, func(t *testing.T) {
			name := "backend-rs-st-" + status
			writeOpAwareFakeBackend(t, name, map[string]string{
				"review_submit": `{"protocolVersion":1,"schemaVersion":4,"result":` + result + `}`,
			}, `{}`)
			writeConfigFor(t, name)
			stdout, _, code := executePrWithStdin(t, `{"head_sha":"abc","body":"b","supersede_pending":true}`, []string{"pr", "review", "submit", "pr-1"})
			if code != 0 {
				t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
			}
			var resp scriptout.Response
			if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
				t.Fatal(err)
			}
			var res prprov.ReviewSubmitResult
			if err := json.Unmarshal(resp.Result, &res); err != nil {
				t.Fatal(err)
			}
			if res.Status != status {
				t.Fatalf("status = %q, want %q", res.Status, status)
			}
			if status == "no_change" && (res.ReviewID != "" || res.State != "none") {
				t.Errorf("no_change result = %+v", res)
			}
		})
	}
}

func TestHumanizeReviewSubmitByStatus(t *testing.T) {
	cases := map[string]struct {
		raw  string
		want []string
	}{
		"posted": {
			`{"review_id":"r1","state":"pending","head_sha":"abc","as_of":"t","status":"posted","added":2,"body":"written","url":"https://example.invalid/r"}`,
			[]string{"posted", "r1", "2 added", "body: written", "https://example.invalid/r"},
		},
		"append": {
			`{"review_id":"r0","state":"pending","head_sha":"abc","as_of":"t","status":"append","added":1,"already_present":1,"body":"kept","extra_pending_reviews":1,"last_append":{"at":"t0","added":1,"head":"abc"}}`,
			[]string{"append", "r0", "1 already present", "extra pending reviews: 1", "last append: t0"},
		},
		"no_change": {
			`{"review_id":"","state":"none","head_sha":"abc","as_of":"t","status":"no_change","already_present":2,"body":"absent"}`,
			[]string{"no_change", "none created", "2 already present"},
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := humanizeReviewSubmit(json.RawMessage(c.raw))
			if err != nil {
				t.Fatal(err)
			}
			for _, w := range c.want {
				if !strings.Contains(got, w) {
					t.Errorf("%q missing %q", got, w)
				}
			}
		})
	}
}

// TestReviewSubmitFromFileOutsideWorkingDirectory: --from-file reads the
// request from a file outside the working directory, the positional id wins,
// and stdin is not read.
func TestReviewSubmitFromFileOutsideWorkingDirectory(t *testing.T) {
	dir := t.TempDir() // not the process working directory
	path := filepath.Join(dir, "request.json")
	if err := os.WriteFile(path, []byte(`{"id":"ignored","head_sha":"abc","body":"b","comments":[{"thread_id":"PRRT_1","body":"r"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	args, err := readReviewSubmitArgs(strings.NewReader(""), "pr-9", path)
	if err != nil {
		t.Fatal(err)
	}
	if args["id"] != "pr-9" || args["head_sha"] != "abc" {
		t.Fatalf("args = %+v", args)
	}
	if cs, ok := args["comments"].([]any); !ok || len(cs) != 1 {
		t.Fatalf("comments = %+v", args["comments"])
	}
}

func TestReviewSubmitFromFileEndToEnd(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-rs-ff", map[string]string{"review_submit": reviewSubmitOKResp}, `{}`)
	writeConfigFor(t, "backend-rs-ff")
	path := filepath.Join(t.TempDir(), "request.json")
	if err := os.WriteFile(path, []byte(`{"head_sha":"abc","body":"b"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, _, code := executePrWithStdin(t, ``, []string{"pr", "review", "submit", "pr-1", "--from-file", path})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}
}

// TestReviewSubmitFromFileInvalidArgument: both sources, a missing file, an
// unreadable file, a relative path and a non-object file are all
// invalid_argument (exit 1), and nothing reaches the backend.
func TestReviewSubmitFromFileInvalidArgument(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.json")
	if err := os.WriteFile(good, []byte(`{"head_sha":"abc","body":"b"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	notObject := filepath.Join(dir, "array.json")
	if err := os.WriteFile(notObject, []byte(`[1]`), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := map[string]struct {
		stdin string
		file  string
		want  string
	}{
		"both given":    {`{"head_sha":"abc"}`, good, "mutually exclusive"},
		"missing file":  {``, filepath.Join(dir, "nope.json"), "--from-file"},
		"directory":     {``, dir, "--from-file"},
		"relative path": {``, "request.json", "absolute"},
		"not an object": {``, notObject, "JSON object"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := readReviewSubmitArgs(strings.NewReader(c.stdin), "pr-9", c.file)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want one mentioning %q", err, c.want)
			}
		})
	}
	writeOpAwareFakeBackend(t, "backend-rs-ffbad", map[string]string{"review_submit": reviewSubmitOKResp}, `{}`)
	writeConfigFor(t, "backend-rs-ffbad")
	stdout, _, code := executePrWithStdin(t, `{"head_sha":"abc"}`, []string{"pr", "review", "submit", "pr-1", "--from-file", good})
	if code != 1 {
		t.Fatalf("both sources: exit code = %d, want 1; stdout=%s", code, stdout)
	}
	if !strings.Contains(stdout, "invalid_argument") {
		t.Errorf("both sources must be invalid_argument: %s", stdout)
	}
}

// TestReviewSubmitStdinStillReadWithoutFromFile: with no --from-file the
// request is read from stdin exactly as before.
func TestReviewSubmitStdinStillReadWithoutFromFile(t *testing.T) {
	args, err := readReviewSubmitArgs(strings.NewReader(`{"head_sha":"abc"}`), "pr-9", "")
	if err != nil || args["head_sha"] != "abc" {
		t.Fatalf("args = %+v, err = %v", args, err)
	}
}

func TestReviewSubmitInvalidArgumentExits1(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-rs-ia", map[string]string{
		"review_submit": `{"protocolVersion":1,"schemaVersion":4,"error":{"code":"invalid_argument","message":"head moved"}}`,
	}, `{}`)
	writeConfigFor(t, "backend-rs-ia")
	_, _, code := executePrWithStdin(t, `{"head_sha":"old","body":"b"}`, []string{"pr", "review", "submit", "pr-1"})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
}

// reviewPendingRecordResp is a well-formed review_pending wire response for a
// stale pending review with an append on record and one further pending review.
const reviewPendingRecordResp = `{"protocolVersion":1,"schemaVersion":4,"result":{"pending":true,"head_sha":"h2","as_of":"t","review":{"review_id":"PRR_1","database_id":7,"state":"pending","commit_sha":"h1","stale":true,"body":"b","comments_total":1,"comments_at_head":0,"reviewed_head":false,"extra_pending_reviews":1,"last_append":{"at":"2026-01-01T00:00:00Z","added":3,"head":"h1"},"comments":[{"id":"C1","path":"a.go","line":3,"body":"c","original_commit":"h1"}]}}}`

func TestReviewPendingPrintsRecordAndExits0(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-rp-rec", map[string]string{"review_pending": reviewPendingRecordResp}, `{}`)
	writeConfigFor(t, "backend-rp-rec")
	stdout, _, code := executePrWithStdin(t, ``, []string{"pr", "review", "pending", "pr-1"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}
	var resp scriptout.Response
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("stdout is not a JSON envelope: %v\n%s", err, stdout)
	}
	var res struct {
		Pending bool `json:"pending"`
		Review  struct {
			ReviewID            string `json:"review_id"`
			CommitSHA           string `json:"commit_sha"`
			Stale               bool   `json:"stale"`
			CommentsTotal       int    `json:"comments_total"`
			CommentsAtHead      int    `json:"comments_at_head"`
			ReviewedHead        bool   `json:"reviewed_head"`
			ExtraPendingReviews int    `json:"extra_pending_reviews"`
			LastAppend          struct {
				Added int `json:"added"`
			} `json:"last_append"`
		} `json:"review"`
	}
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		t.Fatal(err)
	}
	if !res.Pending || res.Review.ReviewID != "PRR_1" || res.Review.CommitSHA != "h1" || !res.Review.Stale ||
		res.Review.CommentsTotal != 1 || res.Review.CommentsAtHead != 0 || res.Review.ReviewedHead ||
		res.Review.ExtraPendingReviews != 1 || res.Review.LastAppend.Added != 3 {
		t.Fatalf("result = %+v", res)
	}
}

func TestReviewPendingNoneIsExplicitAndExits0(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-rp-none", map[string]string{
		"review_pending": `{"protocolVersion":1,"schemaVersion":4,"result":{"pending":false,"head_sha":"h2","as_of":"t"}}`,
	}, `{}`)
	writeConfigFor(t, "backend-rp-none")
	stdout, _, code := executePrWithStdin(t, ``, []string{"pr", "review", "pending", "pr-1"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}
	if !strings.Contains(stdout, `"pending":false`) {
		t.Fatalf("none must be an explicit pending:false result; stdout=%s", stdout)
	}
}

// A failed lookup is an error envelope and a non-zero exit, never a none
// result that a caller could mistake for "safe to post".
func TestReviewPendingLookupFailureExitsNonZeroWithError(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-rp-fail", map[string]string{
		"review_pending": `{"protocolVersion":1,"schemaVersion":4,"error":{"code":"unavailable","message":"review_pending: detection_failed: boom"}}`,
	}, `{}`)
	writeConfigFor(t, "backend-rp-fail")
	stdout, _, code := executePrWithStdin(t, ``, []string{"pr", "review", "pending", "pr-1"})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stdout=%s", code, stdout)
	}
	if !strings.Contains(stdout, "detection_failed") || strings.Contains(stdout, `"pending"`) {
		t.Fatalf("stdout must carry the error and no pending result; got %s", stdout)
	}
}

func TestReviewPendingNotFoundExits4(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-rp-nf", map[string]string{
		"review_pending": `{"protocolVersion":1,"schemaVersion":4,"error":{"code":"not_found","message":"no such pr"}}`,
	}, `{}`)
	writeConfigFor(t, "backend-rp-nf")
	_, _, code := executePrWithStdin(t, ``, []string{"pr", "review", "pending", "pr-1"})
	if code != 4 {
		t.Fatalf("exit code = %d, want 4", code)
	}
}

func TestHumanizeReviewPending(t *testing.T) {
	got, err := humanizeReviewPending(json.RawMessage(`{"pending":false,"head_sha":"h2","as_of":"t"}`))
	if err != nil || !strings.Contains(got, "no pending review") {
		t.Fatalf("none: %q %v", got, err)
	}
	var env scriptout.Response
	if err := json.Unmarshal([]byte(reviewPendingRecordResp), &env); err != nil {
		t.Fatal(err)
	}
	got, err = humanizeReviewPending(env.Result)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"STALE", "1 total, 0 at head", "reviewed head: false", "extra pending reviews: 1", "last append: 2026-01-01T00:00:00Z (+3 at h1)", "a.go:3 at h1"} {
		if !strings.Contains(got, want) {
			t.Errorf("record text missing %q: %q", want, got)
		}
	}
}
