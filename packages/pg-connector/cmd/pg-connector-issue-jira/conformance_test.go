//go:build integration

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/servetest"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/socket"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout/conformance"
)

// Wire conformance of the pg-connector-issue-jira backend: the module's generic
// suite (pkg/scriptout/conformance) is driven through the REAL client binary, in
// the registered {name, command} argv form `pg-connector-issue-jira --config
// <rendered file> --instance <name>`, against a REAL daemon child (the assembled
// serve, with the counting fake pjira) on a short temp socket path. Beside it
// the suite runs each op of the backend's catalog (INTF-JIRA-WIRE) against that
// throwaway store and faked origin, each write with the daemon down, and each
// failure the daemon or the client produces.

// Fixture issues of the daemon's mine query: generic names only.
var (
	confA = servetest.Open("EXA-1")
	confB = servetest.Open("EXA-2")
)

// confDaemon starts a real daemon with fixture issues behind the mine and team
// queries and warms the mine query, so a conformance call is answered from the
// store rather than racing a cold fetch.
func confDaemon(t *testing.T) *servetest.Daemon {
	t.Helper()
	d := servetest.StartDaemon(t, servetest.Options{
		Hooks:        "poll_interval=50ms",
		ClientBudget: "3s",
		Before: func(d *servetest.Daemon) {
			d.Shim.SetIssues(servetest.MineJQL, confA, confB)
			d.Shim.SetIssues(servetest.TeamJQL, confA)
		},
	})
	if resp := d.MustCall(t, servetest.Op("list", `{"query":"`+servetest.QueryMine+`"}`), 20*time.Second); resp.Error != nil {
		t.Fatalf("warm the mine query: %+v", resp.Error)
	}
	// The team query is run by the daemon itself at startup; wait for it, so a
	// feed answer lists every configured query.
	db := d.OpenStore(t)
	deadline := time.Now().Add(60 * time.Second)
	for {
		var n int
		err := db.QueryRow(`SELECT COUNT(*) FROM query WHERE last_success_at IS NOT NULL`).Scan(&n)
		if err == nil && n >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the configured queries never both ran (%v)", err)
		}
		time.Sleep(25 * time.Millisecond)
	}
	return d
}

// confBackend is the registered argv form of the instance.
func confBackend(d *servetest.Daemon) conformance.ExecBackend {
	return conformance.ExecBackend{Binary: d.Bin(), Args: d.ExecArgs()}
}

// confReply is one backend answer decoded for assertions.
type confReply struct {
	stdout []byte
	exit   int
	resp   scriptout.Response
}

// invoke sends one raw request, requires a response envelope that
// CheckResponseBytes accepts, and requires the process exit code to be the one
// the envelope's error code maps to (zero for a success).
func invoke(t *testing.T, b conformance.Backend, request string) confReply {
	t.Helper()
	out, exit, err := b.Invoke(context.Background(), []byte(request))
	if err != nil {
		t.Fatalf("%s: invoke: %v", request, err)
	}
	if err := conformance.CheckResponseBytes(out); err != nil {
		t.Fatalf("%s: not a response envelope: %v (stdout %q)", request, err, out)
	}
	r := confReply{stdout: out, exit: exit}
	if err := json.Unmarshal(out, &r.resp); err != nil {
		t.Fatalf("%s: %v", request, err)
	}
	want := 0
	if r.resp.Error != nil {
		want = scriptout.ExitCodeForCode(r.resp.Error.Code)
		if want == 0 {
			t.Fatalf("%s: error code %q is outside the closed taxonomy", request, r.resp.Error.Code)
		}
	}
	if exit != want {
		t.Errorf("%s: exit %d, want %d for %+v", request, exit, want, r.resp.Error)
	}
	return r
}

// wantError asserts an error answer with the given taxonomy code and the exit
// code scriptout assigns it.
func (r confReply) wantError(t *testing.T, what, code string) {
	t.Helper()
	if r.resp.Error == nil || r.resp.Error.Code != code {
		t.Errorf("%s: got %+v (stdout %q), want error %q", what, r.resp.Error, r.stdout, code)
		return
	}
	if r.exit != scriptout.ExitCodeForCode(code) {
		t.Errorf("%s: exit %d, want %d (%s)", what, r.exit, scriptout.ExitCodeForCode(code), code)
	}
}

// wantResult asserts a success answer stamped with schemaVersion.
func (r confReply) wantResult(t *testing.T, what string, schemaVersion int) {
	t.Helper()
	if r.resp.Error != nil || len(r.resp.Result) == 0 || r.exit != 0 {
		t.Errorf("%s: want a success, got exit %d: %s", what, r.exit, r.stdout)
		return
	}
	if r.resp.SchemaVersion != schemaVersion || r.resp.ProtocolVersion != scriptout.ProtocolVersion {
		t.Errorf("%s: schemaVersion %d protocolVersion %d, want %d and %d", what, r.resp.SchemaVersion, r.resp.ProtocolVersion, schemaVersion, scriptout.ProtocolVersion)
	}
}

// decode unmarshals the success result into v.
func (r confReply) decode(t *testing.T, what string, v any) {
	t.Helper()
	if err := json.Unmarshal(r.resp.Result, v); err != nil {
		t.Fatalf("%s: decode %s: %v", what, r.resp.Result, err)
	}
}

func TestConformanceGenericSuiteThroughTheRealClient(t *testing.T) {
	d := confDaemon(t)
	results := conformance.Run(context.Background(), confBackend(d))
	invoking := 0
	for _, r := range results {
		if r.Err != nil {
			t.Errorf("%s: %v", r.Name, r.Err)
		}
		if r.Skipped {
			t.Errorf("%s was skipped (%s): every invoking case applies to this backend", r.Name, r.SkipReason)
		}
		if strings.HasPrefix(r.Name, "invoking/") {
			invoking++
		}
	}
	// unknown-op, malformed-stdin and capabilities all ran for real.
	if invoking != 3 {
		t.Errorf("%d invoking cases ran, want 3", invoking)
	}
}

// catalogOps are the ops of the backend's catalog (INTF-JIRA-WIRE): the five
// writes, the daemon's reads, the feed and the notices.
var catalogOps = []string{
	"show", "list", "children", "search", "list_activity",
	"create", "comment", "transition", "update", "close",
	"changes", "changes_ack", "explain", "deps", "auth_status", "status", "capabilities",
}

// TestConformanceEveryOpOfTheCatalogAnswersARealRequest runs each op of the
// catalog through the real client against the throwaway store and the faked
// origin, and checks the answer's envelope, schema version and exit code.
func TestConformanceEveryOpOfTheCatalogAnswersARealRequest(t *testing.T) {
	d := confDaemon(t)
	b := confBackend(d)
	const issueV, searchV, activityV = schema.IssueSchemaVersion, schema.SearchSchemaVersion, schema.ActivitySchemaVersion

	// capabilities: answered by the client, bespoke (non-enveloped) shape.
	out, exit, err := b.Invoke(context.Background(), []byte(`{"op":"capabilities"}`))
	if err != nil || exit != 0 {
		t.Fatalf("capabilities: exit %d err %v", exit, err)
	}
	if err := conformance.CheckBytes("capabilities-response", out); err != nil {
		t.Errorf("capabilities: %v (stdout %q)", err, out)
	}
	var caps scriptout.CapabilitiesResponse
	if err := json.Unmarshal(out, &caps); err != nil {
		t.Fatal(err)
	}
	for _, op := range catalogOps {
		if !slices.Contains(caps.Ops, op) {
			t.Errorf("capabilities: ops %v miss %q", caps.Ops, op)
		}
	}
	for _, key := range []string{"owns_changes", "cache_opt_out"} {
		if caps.Vocabulary[key] != true {
			t.Errorf("capabilities: vocabulary %v must declare %s: true", caps.Vocabulary, key)
		}
	}

	// show: the stored summary, read through.
	r := invoke(t, b, `{"op":"show","args":{"id":"EXA-1"}}`)
	r.wantResult(t, "show", issueV)
	var shown schema.Issue
	r.decode(t, "show", &shown)
	if shown.ID != "EXA-1" || shown.State != "To Do" || shown.Title == "" {
		t.Errorf("show EXA-1 = %+v", shown)
	}
	invoke(t, b, `{"op":"show","args":{"id":"EXA-404"}}`).wantError(t, "show of an issue the origin does not show", "not_found")
	invoke(t, b, `{"op":"show","args":{}}`).wantError(t, "show with no id", "invalid_argument")

	// list: the configured query, from the store.
	r = invoke(t, b, string(conformance.ListRequest(servetest.QueryMine, false, nil)))
	r.wantResult(t, "list", issueV)
	var list schema.IssueListResult
	r.decode(t, "list", &list)
	got := slices.Clone(list.PresentIDs)
	slices.Sort(got)
	if !slices.Equal(got, []string{"EXA-1", "EXA-2"}) || len(list.Entities) != 2 {
		t.Errorf("list mine = %+v", list)
	}
	r = invoke(t, b, string(conformance.ListRequest(servetest.QueryMine, true, nil)))
	r.wantResult(t, "list ids_only", issueV)

	// children: the open direct children of an issue the origin shows, by a
	// search of the parent.
	child := servetest.Open("EXA-3")
	child.Parent = "EXA-1"
	d.Shim.SetIssues(`parent = "EXA-1" AND statusCategory != Done ORDER BY key ASC`, child)
	r = invoke(t, b, `{"op":"children","args":{"id":"EXA-1"}}`)
	r.wantResult(t, "children", issueV)
	var kids schema.IssueChildrenResult
	r.decode(t, "children", &kids)
	if len(kids.Children) != 1 || kids.Children[0].ID != "EXA-3" || kids.Children[0].Parent != "EXA-1" {
		t.Errorf("children of EXA-1 = %+v", kids)
	}
	invoke(t, b, `{"op":"children","args":{"id":"EXA-404"}}`).wantError(t, "children of an unknown issue", "not_found")

	// search: live from the origin, never stored.
	d.Shim.SetIssues("text ~ example", confB)
	r = invoke(t, b, `{"op":"search","args":{"query":"text ~ example"}}`)
	r.wantResult(t, "search", searchV)
	if !strings.Contains(string(r.resp.Result), "EXA-2") {
		t.Errorf("search result %s lacks EXA-2", r.resp.Result)
	}
	invoke(t, b, `{"op":"search","args":{}}`).wantError(t, "search with no query", "invalid_argument")

	// list_activity: scoped to the operator, live from the origin.
	d.Shim.SetDefaultIssues(confA)
	since := time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339)
	before := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	r = invoke(t, b, `{"op":"list_activity","args":{"since":"`+since+`","before":"`+before+`"}}`)
	r.wantResult(t, "list_activity", activityV)
	invoke(t, b, `{"op":"list_activity","args":{"since":"`+before+`","before":"`+since+`"}}`).wantError(t, "list_activity over a reversed range", "invalid_argument")

	// deps: a backend with no dependency concept answers an empty list for an
	// issue it shows, not_found for one it does not.
	r = invoke(t, b, `{"op":"deps","args":{"id":"EXA-1"}}`)
	r.wantResult(t, "deps", issueV)
	var deps schema.IssueDepsResult
	r.decode(t, "deps", &deps)
	if len(deps.IDs) != 0 {
		t.Errorf("deps of EXA-1 = %+v, want none", deps)
	}
	invoke(t, b, `{"op":"deps","args":{"id":"EXA-404"}}`).wantError(t, "deps of an unknown issue", "not_found")

	// auth_status: a pass-through of the origin tool's own check.
	r = invoke(t, b, `{"op":"auth_status"}`)
	r.wantResult(t, "auth_status", issueV)
	var auth scriptout.AuthStatus
	r.decode(t, "auth_status", &auth)
	if auth.State != scriptout.AuthOK {
		t.Errorf("auth_status = %+v, want ok", auth)
	}

	// status and explain: the daemon's own.
	r = invoke(t, b, `{"op":"status"}`)
	r.wantResult(t, "status", issueV)
	var report map[string]any
	r.decode(t, "status", &report)
	if report["health"] == nil || report["source"] != "daemon" {
		t.Errorf("status = %v", report)
	}
	r = invoke(t, b, `{"op":"explain","args":{"id":"EXA-1"}}`)
	r.wantResult(t, "explain", issueV)
	invoke(t, b, `{"op":"explain","args":{"id":"EXA-9999"}}`).wantError(t, "explain of an issue the daemon never saw", "not_found")
	invoke(t, b, `{"op":"explain","args":{}}`).wantError(t, "explain with no id", "invalid_argument")

	// changes and changes_ack: the feed (its content is conformance_changes_test.go).
	confAwaitBaseline(t, d, servetest.QueryMine)
	r = invoke(t, b, confChangesReq("catalog", servetest.QueryMine, nil))
	r.wantResult(t, "changes", issueV)
	invoke(t, b, `{"op":"changes","args":{}}`).wantError(t, "changes with no consumer", "invalid_argument")
	invoke(t, b, `{"op":"changes_ack","args":{}}`).wantError(t, "changes_ack with no consumer", "invalid_argument")

	// write_begin and invalidate: the notices, answered null.
	invoke(t, b, `{"op":"write_begin","args":{"id":"EXA-1"}}`).wantResult(t, "write_begin", issueV)
	invoke(t, b, `{"op":"invalidate","args":{"id":"EXA-1"}}`).wantResult(t, "invalidate", issueV)
	invoke(t, b, `{"op":"write_begin","args":{}}`).wantError(t, "write_begin with neither id nor instance", "invalid_argument")
	invoke(t, b, `{"op":"write_begin","args":{"id":"not a key"}}`).wantError(t, "write_begin with a malformed key", "invalid_argument")
	invoke(t, b, `{"op":"invalidate","args":{}}`).wantError(t, "invalidate with no id", "invalid_argument")
}

// TestConformanceEveryWriteRunsWithTheDaemonDown runs each of the five writes
// through the real client with NO daemon: the write is performed by the calling
// process against the origin tool, under the caller's own environment, and the
// two notices fail without failing it or holding it up.
func TestConformanceEveryWriteRunsWithTheDaemonDown(t *testing.T) {
	d := servetest.NewDaemon(t, servetest.Options{ClientBudget: "3s", Before: func(d *servetest.Daemon) {
		d.Shim.SetIssue(confA)
		d.Shim.CreateKey("EXA-100")
		d.Shim.SetIssue(servetest.Open("EXA-100"))
	}})
	// The daemon is never started: its socket does not exist.
	b := confBackend(d)
	const issueV = schema.IssueSchemaVersion
	// The conformance backend inherits the test's environment; the write needs
	// the shim's, so the writes go through the harness's own client runner.
	run := func(request string) confReply {
		t.Helper()
		res := d.Client(t, request)
		if err := conformance.CheckResponseBytes([]byte(res.Stdout)); err != nil {
			t.Fatalf("%s: not a response envelope: %v (stdout %q stderr %q)", request, err, res.Stdout, res.Stderr)
		}
		return confReply{stdout: []byte(res.Stdout), exit: res.Exit, resp: res.Resp}
	}
	_ = b

	start := time.Now()
	r := run(`{"op":"comment","args":{"id":"EXA-1","body":"example comment"}}`)
	r.wantResult(t, "comment", issueV)
	r = run(`{"op":"transition","args":{"id":"EXA-1","target_state":"In Progress"}}`)
	r.wantResult(t, "transition", issueV)
	r = run(`{"op":"close","args":{"id":"EXA-1","reason":"done here"}}`)
	r.wantResult(t, "close", issueV)
	r = run(`{"op":"create","args":{"title":"New example","issue_type":"Task"}}`)
	r.wantResult(t, "create", issueV)
	var created schema.Issue
	r.decode(t, "create", &created)
	if created.ID != "EXA-100" {
		t.Errorf("create answered %+v, want the key the origin minted", created)
	}
	// update: pjira has no field update op; the backend answers unavailable for
	// an issue the origin shows (the documented capability gap), not_found for
	// one it does not.
	run(`{"op":"update","args":{"id":"EXA-1","fields":{"title":"renamed"}}}`).wantError(t, "update of a known issue", "unavailable")
	run(`{"op":"update","args":{"id":"EXA-404","fields":{"title":"renamed"}}}`).wantError(t, "update of an unknown issue", "not_found")
	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Errorf("the writes took %s: a missing daemon must delay each by no more than its notice timeout", elapsed)
	}

	var writes []string
	for _, c := range d.Shim.Calls() {
		if c.Kind() == "write" {
			writes = append(writes, c.Verb)
		}
	}
	if want := []string{"comment", "transition", "comment", "transition", "create"}; !slices.Equal(writes, want) {
		t.Errorf("the origin saw the writes %v, want %v (close is its comment, then its transition)", writes, want)
	}
	// Nothing reached the (absent) daemon: no state was written for it.
	if _, err := os.Stat(d.StorePath()); err == nil {
		t.Error("a write with the daemon down created the daemon's store")
	}
}

func TestConformanceFailureCodesFollowTheClosedTaxonomy(t *testing.T) {
	d := confDaemon(t)
	b := confBackend(d)

	// query_not_recognized: the daemon's own config decides, whatever the
	// request's config block says.
	lr, err := conformance.InvokeList(context.Background(), b, "no-such-query", false, json.RawMessage(`{"queries":{"no-such-query":"project = EXA"}}`))
	if err != nil || lr.ErrorCode != "query_not_recognized" || lr.ExitCode != scriptout.ExitCodeForCode("query_not_recognized") {
		t.Errorf("an unknown query: %+v err %v", lr, err)
	}
	// invalid_argument: list args that are not an object.
	invoke(t, b, `{"op":"list","args":"not-an-object"}`).wantError(t, "list with malformed args", "invalid_argument")
	// invalid_argument: a config block rendered from another config.
	invoke(t, b, `{"op":"list","args":{"query":"mine"},"config":{"config_hash":"some-other-hash","config_path":"/etc/example/other.json"}}`).
		wantError(t, "list with a stale config block", "invalid_argument")
	// A block carrying the stamped hash, one with no hash, and none at all pass.
	invoke(t, b, `{"op":"list","args":{"query":"mine"},"config":{"config_hash":"servetest-config-hash"}}`).wantResult(t, "list with the daemon's own hash", schema.IssueSchemaVersion)
	invoke(t, b, `{"op":"list","args":{"query":"mine"},"config":{"queries":{}}}`).wantResult(t, "list with a hashless block", schema.IssueSchemaVersion)

	// unknown_op for an op nobody has.
	invoke(t, b, `{"op":"no_such_op"}`).wantError(t, "an unregistered op", "unknown_op")
}

// TestConformanceUnauthenticatedFetchesAnswerUnauthenticatedAndStoredReadsAreServed
// covers the authentication row of the failure catalog: while the origin refuses
// the credential, fetches answer unauthenticated and auth_status reports it,
// while a read of stored content is still served.
func TestConformanceUnauthenticatedFetchesAnswerUnauthenticatedAndStoredReadsAreServed(t *testing.T) {
	d := confDaemon(t)
	b := confBackend(d)
	d.Shim.Fail(401)

	// Not stored, so it needs the origin: unauthenticated.
	invoke(t, b, `{"op":"show","args":{"id":"EXA-77"}}`).wantError(t, "show of an unstored issue while unauthenticated", "unauthenticated")
	// Stored (the mine query's members and their summaries), so served.
	invoke(t, b, string(conformance.ListRequest(servetest.QueryMine, false, nil))).wantResult(t, "list of a stored query", schema.IssueSchemaVersion)
	// auth_status reports the failure instead of an error.
	r := invoke(t, b, `{"op":"auth_status"}`)
	if r.resp.Error == nil && r.resp.Result != nil {
		var auth scriptout.AuthStatus
		r.decode(t, "auth_status", &auth)
		if auth.State == scriptout.AuthOK {
			t.Errorf("auth_status while unauthenticated = %+v", auth)
		}
	}
}

// The binary speaks socket protocol N; a daemon accepts N and N-1 and answers
// anything else version_mismatch (a daemon child of this build always speaks the
// same N, so the window is exercised with frames of other versions).
func TestConformanceSocketProtocolWindow(t *testing.T) {
	d := confDaemon(t)
	n := socket.ProtocolVersion
	req := servetest.Op("status", "")
	for _, protocol := range []int{n - 1, n, n + 1, n + 2, 0} {
		resp, err := d.CallProtocol(req, 10*time.Second, protocol)
		if err != nil {
			t.Fatalf("protocol %d: %v", protocol, err)
		}
		if socket.Accepts(n, protocol) {
			if resp.Error != nil {
				t.Errorf("protocol %d is inside the window of %d yet answered %+v", protocol, n, resp.Error)
			}
			continue
		}
		if resp.Error == nil || resp.Error.Code != "version_mismatch" {
			t.Errorf("protocol %d is outside the window of %d yet answered %+v", protocol, n, resp)
		}
	}
	// N itself is always inside the window, and N+2 never is.
	if !socket.Accepts(n, n) || socket.Accepts(n, n+2) {
		t.Errorf("window of %d: accepts itself %v, accepts +2 %v", n, socket.Accepts(n, n), socket.Accepts(n, n+2))
	}
}

// fakeDaemonAt answers every request at the given socket protocol with an
// ok-looking list answer, as a daemon one version off would.
func fakeDaemonAt(t *testing.T, sock string, protocol int) {
	t.Helper()
	l, err := listenUnix(sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	const answer = `{"protocolVersion":1,"schemaVersion":1,"result":{"entities":[],"present_ids":[],"cursor":null,"truncated":false}}`
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
				if _, err := socket.ReadFrame(bufio.NewReader(conn), socket.MaxFrameBytes); err != nil {
					return
				}
				_ = socket.WriteFrame(conn, socket.ReplyFrame{SocketProtocol: protocol, DaemonBuild: "fake", Response: json.RawMessage(answer)})
			}()
		}
	}()
}

// A client accepts a reply from a daemon one protocol off (a new daemon may
// answer an old client and the reverse) and answers version_mismatch for one
// further away.
func TestConformanceClientAcceptsAReplyOneProtocolOffAndRefusesTwo(t *testing.T) {
	n := socket.ProtocolVersion
	for _, tc := range []struct {
		protocol int
		accepted bool
	}{{n - 1, n-1 >= 1}, {n, true}, {n + 1, true}, {n + 2, false}} {
		d := servetest.NewDaemon(t, servetest.Options{ClientBudget: "3s"})
		fakeDaemonAt(t, d.Socket, tc.protocol)
		res := d.Client(t, `{"op":"list","args":{"query":"mine"}}`)
		switch {
		case tc.accepted && (res.Resp.Error != nil || res.Exit != 0):
			t.Errorf("a daemon at protocol %d: exit %d %s", tc.protocol, res.Exit, res.Stdout)
		case !tc.accepted && (res.Resp.Error == nil || res.Resp.Error.Code != "version_mismatch"):
			t.Errorf("a daemon at protocol %d: exit %d %s, want version_mismatch", tc.protocol, res.Exit, res.Stdout)
		}
	}
}

// TestConformanceDaemonDownIsUnavailableNeverFromACache: a read with the daemon
// absent answers unavailable (exit 4) naming the socket path and the
// supervisor's restart hint, and never "served_from: cache".
func TestConformanceDaemonDownIsUnavailableNeverFromACache(t *testing.T) {
	d := confDaemon(t)
	b := confBackend(d)

	// Control: while the daemon runs, the same list answers from its store.
	invoke(t, b, string(conformance.ListRequest(servetest.QueryMine, false, nil))).wantResult(t, "control", schema.IssueSchemaVersion)
	if code := servetest.StopDaemon(t, d); code != 0 {
		t.Fatalf("daemon exit = %d; output: %s", code, d.Stderr())
	}

	for _, req := range []string{
		string(conformance.ListRequest(servetest.QueryMine, false, nil)),
		`{"op":"show","args":{"id":"EXA-1"}}`,
		`{"op":"explain","args":{"id":"EXA-1"}}`,
		`{"op":"auth_status"}`,
		`{"op":"changes","args":{"consumer":"c","query":"mine"}}`,
	} {
		r := invoke(t, b, req)
		r.wantError(t, req, "unavailable")
		if r.exit != 4 {
			t.Errorf("%s: exit %d, want 4", req, r.exit)
		}
		if strings.Contains(string(r.stdout), "served_from") || strings.Contains(string(r.stdout), "cache") {
			t.Errorf("%s: a down daemon never answers from a cache: %s", req, r.stdout)
		}
		if r.resp.Error != nil && (!strings.Contains(r.resp.Error.Message, d.Socket) || !strings.Contains(r.resp.Error.Message, servetest.SupervisorLabel)) {
			t.Errorf("%s: the message %q must name the socket path and the supervisor", req, r.resp.Error.Message)
		}
	}
	// capabilities answers without the daemon.
	if out, exit, err := b.Invoke(context.Background(), []byte(`{"op":"capabilities"}`)); err != nil || exit != 0 {
		t.Errorf("capabilities with the daemon down: exit %d err %v %s", exit, err, out)
	}
	// The status op is the one read answered locally: the op catalog says it
	// reports even when the daemon is down, so it is a result (exit 0) holding
	// the down report with source local, never unavailable; no daemon_unreachable
	// row is appended for it.
	rawBefore, _ := os.ReadFile(filepath.Join(d.StateDir, "client.log.jsonl"))
	sr := invoke(t, b, `{"op":"status"}`)
	sr.wantResult(t, "the status op with the daemon down", schema.IssueSchemaVersion)
	if sr.exit != 0 {
		t.Errorf("the status op with the daemon down: exit %d, want 0", sr.exit)
	}
	var down struct {
		Health  string   `json:"health"`
		Source  string   `json:"source"`
		Socket  string   `json:"socket"`
		Reasons []string `json:"reasons"`
	}
	sr.decode(t, "status", &down)
	if down.Health != "down" || down.Source != "local" || down.Socket != d.Socket || len(down.Reasons) == 0 {
		t.Errorf("the status op with the daemon down = %+v, want a local down report for %s", down, d.Socket)
	}
	if rawAfter, _ := os.ReadFile(filepath.Join(d.StateDir, "client.log.jsonl")); string(rawAfter) != string(rawBefore) {
		t.Errorf("the status op answered locally must append no client-log row; before %q after %q", rawBefore, rawAfter)
	}
	if raw, err := os.ReadFile(filepath.Join(d.StateDir, "client.log.jsonl")); err != nil || !strings.Contains(string(raw), `"outcome":"daemon_unreachable"`) {
		t.Errorf("client log %q (%v): want daemon_unreachable rows", raw, err)
	}
}

// corruptDaemon is a fake daemon whose every reply carries a response with BOTH
// result and error: a protocol violation CheckResponse must reject.
func corruptDaemon(t *testing.T, sock string) {
	t.Helper()
	l, err := listenUnix(sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	const bad = `{"protocolVersion":1,"result":{"items":[]},"error":{"code":"unavailable","message":"both branches"}}`
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
				if _, err := socket.ReadFrame(bufio.NewReader(conn), socket.MaxFrameBytes); err != nil {
					return
				}
				_ = socket.WriteFrame(conn, socket.ReplyFrame{SocketProtocol: socket.ProtocolVersion, DaemonBuild: "corrupt-fake", Response: json.RawMessage(bad)})
			}()
		}
	}()
}

// The negative control: the suite's checks are not vacuous. A response carrying
// both result and error is rejected by CheckResponseBytes, and the round trip
// through the real client of a daemon that sends one is rejected too.
func TestConformanceNegativeControlRejectsAResponseWithResultAndError(t *testing.T) {
	both := []byte(`{"protocolVersion":1,"result":{},"error":{"code":"unavailable","message":"x"}}`)
	if err := conformance.CheckResponseBytes(both); err == nil || !conformance.IsSchemaError(err) {
		t.Fatalf("a response with result and error must be rejected, got %v", err)
	}
	for _, neither := range []string{`{"protocolVersion":1}`, `not json`, `[]`} {
		if err := conformance.CheckResponseBytes([]byte(neither)); err == nil {
			t.Errorf("%q must be rejected", neither)
		}
	}
	if err := conformance.CheckResponseBytes([]byte(`{"protocolVersion":1,"error":{"code":"made_up","message":"x"}}`)); err == nil {
		t.Error("an error code outside the taxonomy must be rejected")
	}

	d := servetest.NewDaemon(t, servetest.Options{ClientBudget: "3s"})
	corruptDaemon(t, d.Socket)
	b := confBackend(d)
	out, _, err := b.Invoke(context.Background(), []byte(`{"op":"list","args":{"query":"mine"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := conformance.CheckResponseBytes(out); err == nil {
		t.Errorf("the corrupted answer must not pass the check: %s", out)
	}
	failed := 0
	for _, r := range conformance.Run(context.Background(), b) {
		if r.Err != nil {
			failed++
		}
	}
	if failed == 0 {
		t.Error("conformance.Run passed against a daemon that corrupts every answer")
	}
}

// listenUnix listens on a unix socket at path (its directory created), mode 0600
// as the daemon's own.
func listenUnix(path string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = l.Close()
		return nil, err
	}
	return l, nil
}
