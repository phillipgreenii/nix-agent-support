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

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/servetest"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/socket"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout/conformance"
)

// Wire conformance of the pg-connector-github backend: the module's generic
// suite (pkg/scriptout/conformance) is driven through the REAL client binary,
// in the registered {name, command} argv form `pg-connector-github --config
// <rendered file> pr|ci`, against a REAL daemon child (the assembled serve,
// with the counting fake gh) on a short temp socket path. Cases the generic
// suite cannot know (status, explain, and each failure the daemon produces in
// this phase) are added here; a later phase extends phase2Ops as it delivers
// ops.

// confInstances are the two registered instances and the schema version each
// stamps on its answers.
var confInstances = []struct {
	name          string
	schemaVersion int
}{
	{"pr", schema.PRSchemaVersion},
	{"ci", schema.CISchemaVersion},
}

// Fixture pull requests of the daemon's mine query: generic names only.
var (
	confPRA = servetest.PR{NodeID: "PR_conf_a", Repo: "example-org/widget", Number: 3}
	confPRB = servetest.PR{NodeID: "PR_conf_b", Repo: "example-org/gadget", Number: 7}
)

// confDaemon starts a real daemon with fixture pull requests behind the mine
// query and warms the query, so a conformance call is answered from the store
// rather than racing a cold fetch.
func confDaemon(t *testing.T) *servetest.Daemon {
	t.Helper()
	d := servetest.StartDaemon(t, servetest.Options{Before: func(d *servetest.Daemon) {
		d.Shim.SetPRs(servetest.MineExpr, confPRA, confPRB)
		d.Shim.SetPRs(servetest.TeamExpr, confPRA)
	}})
	listReq := scriptout.Request{Op: scriptout.OpList, Args: json.RawMessage(`{"query":"` + servetest.QueryMine + `"}`)}
	if resp := d.MustCall(t, "pr", listReq, 20*time.Second); resp.Error != nil {
		t.Fatalf("warm the mine query: %+v", resp.Error)
	}
	return d
}

// confBackend is the registered argv form of one instance: the binary and the
// words after command[0].
func confBackend(bin, cfgPath, instance string) conformance.ExecBackend {
	return conformance.ExecBackend{Binary: bin, Args: []string{"--config", cfgPath, instance}}
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

// wantResult asserts a success answer stamped with the instance's schemaVersion.
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

func TestConformanceGenericSuiteThroughTheRealClient(t *testing.T) {
	d := confDaemon(t)
	bin := servetest.Binary(t, "", "")
	for _, inst := range confInstances {
		results := conformance.Run(context.Background(), confBackend(bin, d.ConfigPath, inst.name))
		invoking := 0
		for _, r := range results {
			if r.Err != nil {
				t.Errorf("%s: %s: %v", inst.name, r.Name, r.Err)
			}
			if r.Skipped {
				t.Errorf("%s: %s was skipped (%s): every invoking case applies to this backend", inst.name, r.Name, r.SkipReason)
			}
			if strings.HasPrefix(r.Name, "invoking/") {
				invoking++
			}
		}
		// unknown-op, malformed-stdin and capabilities all ran for real.
		if invoking != 3 {
			t.Errorf("%s: %d invoking cases ran, want 3", inst.name, invoking)
		}
	}
}

func TestConformancePhase2OpsAnswerResponseEnvelopes(t *testing.T) {
	d := confDaemon(t)
	bin := servetest.Binary(t, "", "")

	for _, inst := range confInstances {
		b := confBackend(bin, d.ConfigPath, inst.name)

		// capabilities: answered by the client, bespoke (non-enveloped) shape.
		out, exit, err := b.Invoke(context.Background(), []byte(`{"op":"capabilities"}`))
		if err != nil || exit != 0 {
			t.Fatalf("%s capabilities: exit %d err %v", inst.name, exit, err)
		}
		if err := conformance.CheckBytes("capabilities-response", out); err != nil {
			t.Errorf("%s capabilities: %v (stdout %q)", inst.name, err, out)
		}
		var caps scriptout.CapabilitiesResponse
		if err := json.Unmarshal(out, &caps); err != nil {
			t.Fatal(err)
		}
		for _, op := range phase2Ops(inst.name) {
			if !slices.Contains(caps.Ops, op) {
				t.Errorf("%s capabilities: ops %v miss %q", inst.name, caps.Ops, op)
			}
		}

		invoke(t, b, `{"op":"auth_status"}`).wantResult(t, inst.name+" auth_status", inst.schemaVersion)
		invoke(t, b, `{"op":"status"}`).wantResult(t, inst.name+" status", inst.schemaVersion)

		// explain: a pull request the daemon has listed is explained; one it
		// has never seen is not_found; a missing id is invalid_argument.
		known := `{"op":"explain","args":{"id":"` + confPRA.Ref() + `"}}`
		invoke(t, b, known).wantResult(t, inst.name+" explain", inst.schemaVersion)
		invoke(t, b, `{"op":"explain","args":{"id":"example-org/widget#9999"}}`).wantError(t, inst.name+" explain unseen", "not_found")
		invoke(t, b, `{"op":"explain","args":{}}`).wantError(t, inst.name+" explain without id", "invalid_argument")

		// An op no phase delivered yet is unknown_op, from the daemon: the
		// change feed belongs to a later phase.
		for _, op := range []string{"changes", "changes_ack"} {
			invoke(t, b, `{"op":"`+op+`","args":{}}`).wantError(t, inst.name+" "+op, "unknown_op")
		}
		// An op a phase delivered is registered: with no arguments it is the
		// handler's invalid_argument, never unknown_op.
		for _, op := range delivered3aOps(inst.name) {
			invoke(t, b, `{"op":"`+op+`","args":{}}`).wantError(t, inst.name+" "+op+" with no arguments", "invalid_argument")
		}
	}

	// list is delivered by the pr instance only, through InvokeList.
	pr := confBackend(bin, d.ConfigPath, "pr")
	lr, err := conformance.InvokeList(context.Background(), pr, servetest.QueryMine, false, nil)
	if err != nil || lr.ErrorCode != "" || lr.ExitCode != 0 {
		t.Fatalf("pr list: %+v err %v", lr, err)
	}
	var list schema.PRListResult
	if err := json.Unmarshal(lr.Result, &list); err != nil || len(list.PresentIDs) != 2 {
		t.Errorf("pr list result: %s (%v)", lr.Result, err)
	}
	lr, err = conformance.InvokeList(context.Background(), pr, servetest.QueryMine, true, nil)
	if err != nil || lr.ErrorCode != "" || lr.ExitCode != 0 {
		t.Errorf("pr list ids_only: %+v err %v", lr, err)
	}
	invoke(t, pr, string(conformance.ListRequest(servetest.QueryMine, false, nil))).wantResult(t, "pr list", schema.PRSchemaVersion)

	ci := confBackend(bin, d.ConfigPath, "ci")
	lr, err = conformance.InvokeList(context.Background(), ci, servetest.QueryMine, false, nil)
	if err != nil || lr.ErrorCode != "unknown_op" || lr.ExitCode != scriptout.ExitCodeForCode("unknown_op") {
		t.Errorf("ci list is not delivered in this phase: %+v err %v", lr, err)
	}
}

// phase2Ops are the ops the instance's capabilities list in this phase; a
// later phase extends it as it delivers ops.
func phase2Ops(instance string) []string {
	ops := []string{scriptout.OpCapabilities, scriptout.OpAuthStatus, "status", "explain"}
	if instance == "pr" {
		ops = append(ops, scriptout.OpList)
	}
	ops = append(ops, delivered3aOps(instance)...)
	if instance == "pr" {
		return append(ops, "review_submit", "search", "list_activity")
	}
	return append(ops, "get_logs", "rerun_failed")
}

// delivered3aOps are the ops Phase 3a added whose empty-argument answer is
// invalid_argument (review_submit, search and list_activity validate their own
// arguments later and are covered by the daemon's own tests).
func delivered3aOps(instance string) []string {
	if instance == "pr" {
		return []string{"show", "files", "commits", "review_pending", "refresh"}
	}
	return []string{"list_runs", "refresh"}
}

func TestConformanceFailureCodesFollowTheClosedTaxonomy(t *testing.T) {
	d := confDaemon(t)
	bin := servetest.Binary(t, "", "")
	pr := confBackend(bin, d.ConfigPath, "pr")

	// query_not_recognized: the daemon's own config decides, whatever the
	// request's config block says.
	lr, err := conformance.InvokeList(context.Background(), pr, "no-such-query", false, json.RawMessage(`{"queries":{"no-such-query":"is:open"}}`))
	if err != nil || lr.ErrorCode != "query_not_recognized" || lr.ExitCode != scriptout.ExitCodeForCode("query_not_recognized") {
		t.Errorf("an unknown query: %+v err %v", lr, err)
	}
	// invalid_argument: list args that are not an object.
	invoke(t, pr, `{"op":"list","args":"not-an-object"}`).wantError(t, "list with malformed args", "invalid_argument")
	// invalid_argument: a config block rendered from another config.
	invoke(t, pr, `{"op":"list","args":{"query":"mine"},"config":{"config_hash":"some-other-hash","config_path":"/etc/example/other.json"}}`).
		wantError(t, "list with a stale config block", "invalid_argument")
	// A block with no hash, and none at all, passes.
	invoke(t, pr, `{"op":"list","args":{"query":"mine"},"config":{"queries":{}}}`).wantResult(t, "list with a hashless block", schema.PRSchemaVersion)

	// unknown_op for an op nobody has.
	invoke(t, pr, `{"op":"no_such_op"}`).wantError(t, "an unregistered op", "unknown_op")
}

func TestConformanceVersionMismatchIsAnsweredForAnOutOfRangeDaemon(t *testing.T) {
	bin := fastClient(t)
	// The binary speaks socket protocol N; a daemon at N+2 refuses it, which the
	// client reports as version_mismatch (a real daemon child always runs the
	// same N, so this one is the in-process daemon of the client tests).
	cfg, state, sock := clientSetup(t)
	startDaemonAt(t, state, sock, daemonOpts{protocol: socket.ProtocolVersion + 2})
	b := conformance.ExecBackend{Binary: bin, Args: []string{"--config", cfg, "pr"}}
	invoke(t, b, `{"op":"list","args":{"query":"mine"}}`).wantError(t, "a daemon two protocols ahead", "version_mismatch")
	// capabilities needs no daemon, so it still answers.
	if out, exit, err := b.Invoke(context.Background(), []byte(`{"op":"capabilities"}`)); err != nil || exit != 0 {
		t.Errorf("capabilities beside a version mismatch: exit %d err %v %s", exit, err, out)
	}
}

func TestConformanceDaemonDownIsUnavailableNeverFromACache(t *testing.T) {
	d := confDaemon(t)
	bin := servetest.Binary(t, "", fastBudget) // retries for half of 3 s, not 15 s
	pr := confBackend(bin, d.ConfigPath, "pr")

	// Control: while the daemon runs, the same list answers from its store.
	if lr, err := conformance.InvokeList(context.Background(), pr, servetest.QueryMine, false, nil); err != nil || lr.ErrorCode != "" {
		t.Fatalf("control: list with the daemon up: %+v err %v", lr, err)
	}
	if code := servetest.StopDaemon(t, d); code != 0 {
		t.Fatalf("daemon exit = %d; output: %s", code, d.Stderr())
	}

	for _, inst := range confInstances {
		b := confBackend(bin, d.ConfigPath, inst.name)
		for _, req := range []string{
			string(conformance.ListRequest(servetest.QueryMine, false, nil)),
			`{"op":"show","args":{"id":"` + confPRA.Ref() + `"}}`,
			`{"op":"explain","args":{"id":"` + confPRA.Ref() + `"}}`,
			`{"op":"auth_status"}`,
		} {
			r := invoke(t, b, req)
			r.wantError(t, inst.name+" "+req, "unavailable")
			if r.exit != 4 {
				t.Errorf("%s %s: exit %d, want 4", inst.name, req, r.exit)
			}
			if strings.Contains(string(r.stdout), "served_from") || strings.Contains(string(r.stdout), "cache") {
				t.Errorf("%s %s: a down daemon never answers from a cache: %s", inst.name, req, r.stdout)
			}
		}
		// capabilities answers without the daemon; status answers locally.
		if out, exit, err := b.Invoke(context.Background(), []byte(`{"op":"capabilities"}`)); err != nil || exit != 0 {
			t.Errorf("%s capabilities with the daemon down: exit %d err %v %s", inst.name, exit, err, out)
		}
		invoke(t, b, `{"op":"status"}`).wantResult(t, inst.name+" degraded local status", inst.schemaVersion)
	}
}

// corruptDaemon is a fake daemon whose every reply carries a response with BOTH
// result and error: a protocol violation CheckResponse must reject.
func corruptDaemon(t *testing.T, sock string) {
	t.Helper()
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sock, 0o600); err != nil {
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
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
				if _, err := socket.ReadFrame(bufio.NewReader(conn), socket.MaxFrameBytes); err != nil {
					return
				}
				_ = socket.WriteFrame(conn, socket.ReplyFrame{SocketProtocol: socket.ProtocolVersion, DaemonBuild: "corrupt-fake", Response: json.RawMessage(bad)})
			}()
		}
	}()
}

// The negative control: the suite's checks are not vacuous. A response
// carrying both result and error is rejected by CheckResponseBytes, and the
// round trip through the real client of a daemon that sends one is rejected
// too (the client relays the daemon's answer verbatim).
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
	// An error code outside the closed taxonomy is rejected too.
	if err := conformance.CheckResponseBytes([]byte(`{"protocolVersion":1,"error":{"code":"made_up","message":"x"}}`)); err == nil {
		t.Error("an error code outside the taxonomy must be rejected")
	}

	bin := fastClient(t)
	stateDir := shortTempDir(t)
	sock := filepath.Join(stateDir, "s")
	cfg := writeServeConfig(t, stateDir, sock)
	corruptDaemon(t, sock)
	b := conformance.ExecBackend{Binary: bin, Args: []string{"--config", cfg, "pr"}}

	out, _, err := b.Invoke(context.Background(), []byte(`{"op":"list","args":{"query":"mine"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := conformance.CheckResponseBytes(out); err == nil {
		t.Errorf("the corrupted answer must not pass the check: %s", out)
	}
	if _, err := conformance.InvokeList(context.Background(), b, "mine", false, nil); err == nil {
		t.Error("InvokeList must fail the round trip with the corrupted answer")
	}
	// The generic suite fails against it as well: its unknown-op case is
	// answered with the corrupted reply.
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
