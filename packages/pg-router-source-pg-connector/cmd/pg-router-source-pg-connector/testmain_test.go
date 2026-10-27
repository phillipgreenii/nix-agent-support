// testmain_test.go: the in-process wire double for pg-connector — this
// packet's own "fake pg-connector binary or a stubbed exec" acceptance
// requirement, implemented as a stubbed exec. Mirrors
// packages/pg-connector's own pkg/scriptout/exec_test.go double: a Runner
// (runner.go) that impersonates pg-connector by inspecting the real CLI
// args this adapter passed it (Call.Args) and the helper settings that
// select which canned wire response/exit code to answer with.
//
// It used to re-execute the race-built test binary once per call
// (GO_WANT_HELPER_PROCESS), and a race-built child sleeps 1 s at exit
// (atexit_sleep_ms), so every fake call cost about 1 s under -race. The
// behavior names and the GO_HELPER_* setting names are kept, so call sites
// in the tests did not change; withFactory's extra "KEY=value" strings are
// now settings of the double rather than environment of a child. The real
// exec path (ExecRunner) is covered by runner_test.go with sh -c.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
)

// flagValue returns the value following flag in args, or "" if flag is
// absent or has no following value.
func flagValue(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// helperSettings are the GO_HELPER_* "KEY=value" strings a test handed to
// withFactory; the last entry for a key wins, like a real environment, and
// the test process's own environment is the fallback (a child would have
// inherited it).
type helperSettings []string

func (s helperSettings) lookup(key string) (string, bool) {
	val, found := "", false
	for _, e := range s {
		if v, ok := strings.CutPrefix(e, key+"="); ok {
			val, found = v, true
		}
	}
	if found {
		return val, true
	}
	return os.LookupEnv(key)
}

func (s helperSettings) get(key string) string {
	v, _ := s.lookup(key)
	return v
}

// callEnv returns the value of key as the child would have observed it:
// the last Call.Env entry (appended to the inherited environment) wins,
// else the test process's own environment.
func callEnv(c Call, key string) (string, bool) {
	val, found := "", false
	for _, e := range c.Env {
		if v, ok := strings.CutPrefix(e, key+"="); ok {
			val, found = v, true
		}
	}
	if found {
		return val, true
	}
	return os.LookupEnv(key)
}

// recordAmbientEnvIfRequested is the "the double records its own env"
// side channel this packet's own --beads-dir acceptance criterion needs:
// when the test sets GO_HELPER_ENV_RECORD_FILE (to the path of a
// t.TempDir file) and GO_HELPER_ENV_RECORD_VAR (the env var name to
// capture), the double writes that var's own value, as the CHILD would
// have observed it (i.e. what runPgConnector's Call.Env actually
// carried), to that file — independent of which behavior was also
// selected, so any behavior can be combined with this recording.
func recordAmbientEnvIfRequested(s helperSettings, c Call) {
	file := s.get("GO_HELPER_ENV_RECORD_FILE")
	if file == "" {
		return
	}
	val, ok := callEnv(c, s.get("GO_HELPER_ENV_RECORD_VAR"))
	if !ok {
		// Distinguishable from "set to empty string" — this packet's own
		// Contract distinguishes "not given -> do not set the var at
		// all" from any actually-set value [design: section 6.1].
		val = "<unset>"
	}
	_ = os.WriteFile(file, []byte(val), 0o600)
}

// recordArgsIfRequested writes the call's argv (space-joined) to the path
// named by GO_HELPER_ARGS_RECORD_FILE, when set, so a test can assert on the
// exact pg-connector argv this adapter built.
func recordArgsIfRequested(s helperSettings, childArgs []string) {
	file := s.get("GO_HELPER_ARGS_RECORD_FILE")
	if file == "" {
		return
	}
	_ = os.WriteFile(file, []byte(strings.Join(childArgs, " ")), 0o600)
}

// helperRun answers one Call as pg-connector would for behavior.
func helperRun(behavior string, s helperSettings, c Call) Result {
	recordAmbientEnvIfRequested(s, c)
	recordArgsIfRequested(s, c.Args)

	childArgs := c.Args
	ok := func(stdout string) Result { return Result{Stdout: stdout} }

	switch behavior {
	case "changes_degraded":
		// One succeeded backend, one degraded, one disabled — proving
		// degraded_sources names only the degraded one, never the
		// succeeded or disabled ones [design: section 6.1].
		return Result{Code: 2, Stdout: `{"sources":[` +
			`{"backend":"b1","status":"succeeded","version":1,"truncated":false},` +
			`{"backend":"b2","status":"degraded","version":2,"truncated":false},` +
			`{"backend":"b3","status":"disabled","version":0,"truncated":false}` +
			`],"changes":[` +
			`{"change":"added","source":"b1","entity":{"id":"e1","title":"Entity One"}},` +
			`{"change":"changed","source":"b2","entity":{"id":"e2","title":""}}` +
			`]}`}
	case "changes_pr_rows":
		// bead pg2-1ldvy: five rows over two PRs exercising every field of
		// the retry-window change identity (change kind, source, entity id,
		// head_sha). Entities carry the volatile as_of/stale fields.
		return ok(`{"sources":[{"backend":"gh","status":"succeeded","version":1,"truncated":false}],"changes":[` +
			`{"change":"changed","source":"gh","entity":{"id":"o/r#1","title":"One","head_sha":"h1","as_of":"2026-10-07T10:00:00Z","stale":false}},` +
			`{"change":"changed","source":"gh","entity":{"id":"o/r#1","title":"One","head_sha":"h2","as_of":"2026-10-07T10:00:00Z","stale":false}},` +
			`{"change":"added","source":"gh","entity":{"id":"o/r#1","title":"One","head_sha":"h1","as_of":"2026-10-07T10:00:00Z","stale":false}},` +
			`{"change":"changed","source":"other","entity":{"id":"o/r#1","title":"One","head_sha":"h1","as_of":"2026-10-07T10:00:00Z","stale":false}},` +
			`{"change":"changed","source":"gh","entity":{"id":"o/r#2","title":"Two","head_sha":"h1","as_of":"2026-10-07T10:00:00Z","stale":false}}` +
			`]}`)
	case "changes_pr_one":
		// bead pg2-1ldvy: ONE changed row whose entity fields come from the
		// settings, so a test can vary the volatile (as_of, stale, title,
		// comment_count) and stable (head_sha, version) fields independently
		// across invocations. An empty GO_HELPER_HEAD / GO_HELPER_VERSION
		// leaves that field out of the entity entirely.
		entity := map[string]any{
			"id":            "o/r#1",
			"title":         s.get("GO_HELPER_TITLE"),
			"as_of":         s.get("GO_HELPER_ASOF"),
			"stale":         s.get("GO_HELPER_STALE") == "1",
			"comment_count": len(s.get("GO_HELPER_TITLE")),
		}
		if h := s.get("GO_HELPER_HEAD"); h != "" {
			entity["head_sha"] = h
		}
		if v := s.get("GO_HELPER_VERSION"); v != "" {
			entity["version"] = v
		}
		body, _ := json.Marshal(map[string]any{
			"sources": []any{},
			"changes": []any{map[string]any{"change": "changed", "source": "gh", "entity": entity}},
		})
		return ok(string(body))
	case "changes_body_env":
		// The whole "changes" wire answer comes from GO_HELPER_BODY, so a
		// test can drive the extended wire of a backend that owns its
		// changes and give each invocation (poll) its own canned answer.
		return ok(s.get("GO_HELPER_BODY"))
	case "changes_ok_empty":
		return ok(`{"sources":[],"changes":[]}`)
	case "changes_degraded_with_reason":
		// bead pg2-wa5uk: same shape as "changes_degraded" but the
		// degraded backend also carries a real reason (as pg-connector's
		// own changesSourceRow does since bead pg2-unqcn) — proving this
		// adapter now forwards it into each printed item's own
		// degraded_reasons metadata, keyed by backend name.
		return Result{Code: 2, Stdout: `{"sources":[` +
			`{"backend":"b1","status":"succeeded","version":1,"truncated":false},` +
			`{"backend":"b2","status":"degraded","version":2,"truncated":false,"reason":"rate limited: too many requests"},` +
			`{"backend":"b3","status":"disabled","version":0,"truncated":false}` +
			`],"changes":[` +
			`{"change":"added","source":"b1","entity":{"id":"e1","title":"Entity One"}}` +
			`]}`}
	case "total_failure":
		// A total-failure outcome still writes a well-formed wire body
		// to its OWN stdout (matching pg-connector's real
		// writeFanOutResult, which writes the JSON body before ever
		// checking exitCode) — this adapter must ignore it entirely on
		// this path and print nothing of its own [design: section 6.1].
		return Result{
			Code:   3,
			Stdout: `{"sources":[{"backend":"b1","status":"degraded","version":1,"truncated":false}],"changes":[]}`,
			Stderr: "pg-connector: total failure: no backend succeeded\n",
		}
	case "total_failure_with_reason":
		// bead pg2-wa5uk: the exact observed real-world shape — a single
		// degraded backend (rate-limited), no other healthy source, so
		// pg-connector's own exit is 3 ("total failure"). Per its own
		// exitError doc comment, pg-connector's own STDERR is always
		// empty for this outcome ("carries a specific exit code without
		// printing anything to stderr, since the JSON body on stdout is
		// the reported outcome") — only stdout carries the real reason.
		// Before this bead's fix, invokeOrFail discarded stdout entirely
		// on this path, so this adapter's own stderr was completely
		// empty too and every caller downstream only ever saw a bare
		// "exit status 1".
		return Result{Code: 3, Stdout: `{"sources":[{"backend":"b1","status":"degraded","version":1,"truncated":false,"reason":"rate limited: too many requests"}],"changes":[]}`}
	case "changes_cursor_expired":
		// The wire the real umbrella (pg-connector's own forwarded "changes",
		// changes_forward.go) answers when a daemon-backed backend that owns
		// its changes refuses a poll with invalid_argument "cursor_expired:"
		// (the consumer's cursor fell outside retention, e.g. a store moved
		// aside): ONE degraded source carrying the daemon's message verbatim as
		// its reason, no changes, nothing acknowledged, exit 3 (no healthy
		// source). Pinned to pg-connector's own
		// TestForwardedChanges_CursorExpiredReachesTheCaller, whose double
		// answers the same refusal.
		return Result{Code: 3, Stdout: `{"sources":[{"backend":"pg-connector-github","status":"degraded","version":1,"truncated":false,` +
			`"reason":"cursor_expired: position fell outside retention; re-run with --reset"}],"changes":[]}`}
	case "invalid_argument":
		return Result{
			Code:   1,
			Stdout: `{"error":{"code":"invalid_argument","message":"query not recognized"}}`,
			Stderr: "pg-connector: invalid_argument: query \"bogus\" is not recognized by any registered backend\n",
		}
	case "backend_not_registered":
		return Result{Code: 1, Stderr: `dispatch: --backend "not-a-backend" is not registered for connector.issue (registered: [pg-connector-issue-beads])` + "\n"}
	case "sweep_ids_by_query":
		// present_ids-only responses whose content depends on which
		// --query was passed, so a multi-query sweep invocation can be
		// exercised end-to-end (union + dedup across calls). entities
		// is ALWAYS EMPTY here, matching the real --ids-only shape
		// exactly [design: section 6.1] — a wire double with entities
		// populated instead would catch the OPPOSITE bug.
		var presentIDs string
		switch flagValue(childArgs, "--query") {
		case "q1":
			presentIDs = `["a","b"]`
		case "q2":
			presentIDs = `["b","c"]`
		default:
			presentIDs = `[]`
		}
		return ok(fmt.Sprintf(`{"entities":[],"present_ids":%s,"sources":[]}`, presentIDs))
	case "sweep_since_echo":
		// A ranged --ids-only response: with --since 40m only the recently
		// updated id comes back; with no --since the whole open set does.
		// Any other --since value is a test bug and fails loudly.
		switch since := flagValue(childArgs, "--since"); since {
		case "":
			return ok(`{"entities":[],"present_ids":["recent","old"],"sources":[]}`)
		case "40m":
			return ok(`{"entities":[],"present_ids":["recent"],"sources":[]}`)
		default:
			return Result{Code: 98, Stderr: "unexpected --since value: " + since + "\n"}
		}
	case "list_full_ok":
		return ok(`{"entities":[` +
			`{"id":"i1","title":"Alpha task one","issue_type":"task","metadata":{"k":"v1"}},` +
			`{"id":"i2","title":"Beta task two","issue_type":"task","metadata":{"k":"v2"}},` +
			`{"id":"i3","title":"Alpha bug three","issue_type":"bug","metadata":{"k":"v3"}}` +
			`],"present_ids":["i1","i2","i3"],"sources":[{"source":"b1","status":"succeeded","count":3}]}`)
	case "list_full_title_fallback":
		// One entity with an empty title, proving the "else the id"
		// fallback [design: section 6.1].
		return ok(`{"entities":[{"id":"i9","title":"","issue_type":"task","metadata":{}}],"present_ids":["i9"],"sources":[]}`)
	default:
		return Result{Code: 99, Stderr: "unknown GO_HELPER_BEHAVIOR: " + behavior + "\n"}
	}
}

// helperRunner returns a Runner that answers every Call with behavior,
// configured by the GO_HELPER_* "KEY=value" settings in extra.
func helperRunner(behavior string, extra ...string) Runner {
	s := helperSettings(extra)
	return func(_ context.Context, c Call) Result {
		return helperRun(behavior, s, c)
	}
}

func withFactory(t *testing.T, behavior string, extraEnv ...string) {
	t.Helper()
	orig := connectorRunner
	connectorRunner = helperRunner(behavior, extraEnv...)
	t.Cleanup(func() { connectorRunner = orig })
}

// runCLI drives newRootCmd() end-to-end (cobra arg parsing + RunE),
// returning stdout, stderr, and the same exit code main()'s run() would
// have returned — used by every _test.go file in this package rather
// than calling run() (which would want real os.Stdout/os.Stderr).
func runCLI(t *testing.T, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	var out, errOut writerBuf
	exitCode = run(args, &out, &errOut)
	return out.String(), errOut.String(), exitCode
}

// writerBuf is a minimal io.Writer + String() buffer, avoiding a direct
// bytes.Buffer import collision with per-file imports.
type writerBuf struct{ b []byte }

func (w *writerBuf) Write(p []byte) (int, error) {
	w.b = append(w.b, p...)
	return len(p), nil
}
func (w *writerBuf) String() string { return string(w.b) }

// mustUnmarshalItems decodes stdout as this adapter's own rawItem array
// output shape.
func mustUnmarshalItems(t *testing.T, stdout string) []rawItem {
	t.Helper()
	var items []rawItem
	if err := json.Unmarshal([]byte(stdout), &items); err != nil {
		t.Fatalf("unmarshal items: %v (stdout=%q)", err, stdout)
	}
	return items
}

// readFile reads path (a small test fixture/recording file), failing the
// test on any error.
func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

var _ io.Writer = (*writerBuf)(nil)
