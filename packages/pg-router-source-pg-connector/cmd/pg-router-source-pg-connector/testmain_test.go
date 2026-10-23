// testmain_test.go: the in-process wire double for pg-connector — this
// packet's own "fake pg-connector binary or a stubbed exec" acceptance
// requirement, implemented as a stubbed exec. It is a cmdruntest.Fake (the
// shared in-process command runner from x, ADR 0001) whose one catch-all
// rule impersonates pg-connector by inspecting the real CLI args this adapter
// passed it (Call.Args) and the helper settings that select which canned wire
// response/exit code to answer with. The Fake records every Call, so a test
// asserts on the exact argv and env this adapter built through Fake.Calls().
//
// It used to re-execute the race-built test binary once per call, and a
// race-built child sleeps 1 s at exit (atexit_sleep_ms), so every fake call
// cost about 1 s under -race. The behavior names and the GO_HELPER_* setting
// names are kept, so call sites in the tests did not change; withFactory's
// extra "KEY=value" strings are settings of the double rather than
// environment of a child. The real exec path (cmdrun.Exec) is covered by
// x's own cmdruntest.RunContract and by exec_test.go's one real-process
// check of this adapter's wiring.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/phillipgreenii/x/cmdrun"
	"github.com/phillipgreenii/x/cmdruntest"
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

// reply is one canned pg-connector answer, in the string form the wire
// fixtures below are written in.
type reply struct {
	Code           int
	Stdout, Stderr string
}

func (r reply) result() cmdrun.Result {
	return cmdrun.Result{Code: r.Code, Stdout: []byte(r.Stdout), Stderr: []byte(r.Stderr)}
}

// helperRun answers one Call as pg-connector would for behavior.
func helperRun(behavior string, s helperSettings, c cmdrun.Call) reply {
	childArgs := c.Args
	ok := func(stdout string) reply { return reply{Stdout: stdout} }

	switch behavior {
	case "changes_degraded":
		// One succeeded backend, one degraded, one disabled — proving
		// degraded_sources names only the degraded one, never the
		// succeeded or disabled ones [design: section 6.1].
		return reply{Code: 2, Stdout: `{"sources":[` +
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
		return reply{Code: 2, Stdout: `{"sources":[` +
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
		return reply{
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
		return reply{Code: 3, Stdout: `{"sources":[{"backend":"b1","status":"degraded","version":1,"truncated":false,"reason":"rate limited: too many requests"}],"changes":[]}`}
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
		return reply{Code: 3, Stdout: `{"sources":[{"backend":"pg-connector-github","status":"degraded","version":1,"truncated":false,` +
			`"reason":"cursor_expired: position fell outside retention; re-run with --reset"}],"changes":[]}`}
	case "invalid_argument":
		return reply{
			Code:   1,
			Stdout: `{"error":{"code":"invalid_argument","message":"query not recognized"}}`,
			Stderr: "pg-connector: invalid_argument: query \"bogus\" is not recognized by any registered backend\n",
		}
	case "reject_query_flag":
		// A capability whose pg-connector verb has no --query flag (mail's
		// list, and a mail "changes" that does not exist at all): cobra
		// answers "unknown flag: --query" on stderr and exits 1, with
		// nothing on stdout. Observed against the real pg-connector binary
		// (packages/pg-connector, `mail changes --query q ...` and
		// `mail list --query q ...`), so this double reproduces what the
		// adapter really sees for type "mail" today.
		return reply{Code: 1, Stderr: "unknown flag: --query\n"}
	case "backend_not_registered":
		return reply{Code: 1, Stderr: `dispatch: --backend "not-a-backend" is not registered for connector.issue (registered: [pg-connector-issue-beads])` + "\n"}
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
			return reply{Code: 98, Stderr: "unexpected --since value: " + since + "\n"}
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
		return reply{Code: 99, Stderr: "unknown GO_HELPER_BEHAVIOR: " + behavior + "\n"}
	}
}

// withFactory installs a cmdruntest.Fake as the adapter's command runner for
// the test: every call is answered as pg-connector would for behavior,
// configured by the GO_HELPER_* "KEY=value" settings in extra. The returned
// Fake exposes the recorded calls (Calls) for argv and env assertions.
func withFactory(t *testing.T, behavior string, extra ...string) *cmdruntest.Fake {
	t.Helper()
	s := helperSettings(extra)
	fake := cmdruntest.New(t)
	fake.On(cmdruntest.MatchName(pgConnectorBinary)).Respond(func(c cmdrun.Call) cmdrun.Result {
		return helperRun(behavior, s, c).result()
	})
	orig := connectorRunner
	connectorRunner = fake.Runner()
	t.Cleanup(func() { connectorRunner = orig })
	return fake
}

// onlyCall returns the single call the fake saw, failing the test otherwise.
func onlyCall(t *testing.T, fake *cmdruntest.Fake) cmdrun.Call {
	t.Helper()
	calls := fake.Calls()
	if len(calls) != 1 {
		t.Fatalf("pg-connector calls = %d (%v), want exactly 1", len(calls), calls)
	}
	return calls[0]
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

var _ io.Writer = (*writerBuf)(nil)
