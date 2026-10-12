package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/proc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/router"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/socket"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

func shortTempDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("/tmp", "pcc")
	if err != nil {
		d, err = os.MkdirTemp("", "pcc")
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return d
}

// serveRouter serves r on a short socket through the real process shell.
func serveRouter(t *testing.T, r *router.Router) string {
	t.Helper()
	dir := shortTempDir(t)
	cfg := proc.Config{StateDir: dir, SocketPath: filepath.Join(dir, "s"), DrainTimeout: 2 * time.Second}
	srv, err := proc.Start(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = srv.Serve(ctx, socket.Handler(r, socket.ServerConfig{Build: "d"}), nil) }()
	t.Cleanup(func() { cancel(); <-done })
	return cfg.SocketPath
}

type result struct {
	code int
	out  string
}

func run(t *testing.T, o Options, stdin string) result {
	t.Helper()
	var out bytes.Buffer
	code := Run(context.Background(), o, strings.NewReader(stdin), &out)
	return result{code, out.String()}
}

func opts(inst Instance, socketPath string) Options {
	return Options{
		Instance: inst, SocketPath: socketPath, SupervisorLabel: "com.example.daemon", ConfigPath: "/etc/pcg/config.json",
		Build: "build-1", Budget: 800 * time.Millisecond, Margin: 100 * time.Millisecond,
	}
}

func errCode(t *testing.T, out string) *scriptout.Error {
	t.Helper()
	var resp scriptout.Response
	if err := json.Unmarshal([]byte(out), &resp); err != nil || resp.Error == nil {
		t.Fatalf("want an error body, got %q (%v)", out, err)
	}
	return resp.Error
}

func TestCapabilitiesAnsweredStaticallyWithNoDaemon(t *testing.T) {
	missing := filepath.Join(shortTempDir(t), "nope")
	for _, inst := range []Instance{PR(), CI()} {
		start := time.Now()
		res := run(t, opts(inst, missing), `{"op":"capabilities"}`)
		if res.code != 0 {
			t.Fatalf("%s: exit %d, out %q", inst.Name, res.code, res.out)
		}
		if time.Since(start) > 300*time.Millisecond {
			t.Errorf("%s: capabilities waited on a daemon that is down", inst.Name)
		}
		var caps scriptout.CapabilitiesResponse
		if err := json.Unmarshal([]byte(res.out), &caps); err != nil {
			t.Fatal(err)
		}
		if caps.ProtocolVersion != scriptout.ProtocolVersion || caps.Version != "build-1" {
			t.Errorf("%s: %+v", inst.Name, caps)
		}
		if caps.Vocabulary[VocabCacheOptOut] != true || caps.Vocabulary[VocabOwnsChanges] != true {
			t.Errorf("%s: vocabulary %v must declare cache_opt_out and owns_changes", inst.Name, caps.Vocabulary)
		}
		if !slices.Equal(caps.Ops, inst.OpNames()) || !slices.IsSorted(caps.Ops) || !slices.Contains(caps.Ops, "capabilities") {
			t.Errorf("%s: ops %v must equal the compiled-in table %v", inst.Name, caps.Ops, inst.OpNames())
		}
		if got := caps.SchemaVersions[inst.Name]; got == 0 {
			t.Errorf("%s: schemaVersions %v", inst.Name, caps.SchemaVersions)
		}
	}
	if got := PR().Capabilities("v").SchemaVersions["pr"]; got != schema.PRSchemaVersion {
		t.Errorf("pr schema version %d", got)
	}
	if !slices.Contains(PR().OpNames(), "list") || slices.Contains(CI().OpNames(), "list") {
		t.Errorf("pr lists list, ci does not: %v / %v", PR().OpNames(), CI().OpNames())
	}
}

func TestEveryOtherOpIsForwardedAndAnsweredUnavailableWhenTheDaemonIsDown(t *testing.T) {
	missing := filepath.Join(shortTempDir(t), "nope")
	for _, op := range []string{"list", "auth_status", "status", "show", "changes", "something_new"} {
		res := run(t, opts(PR(), missing), `{"op":"`+op+`"}`)
		e := errCode(t, res.out)
		if e.Code != "unavailable" || res.code != scriptout.ExitCodeForCode("unavailable") {
			t.Errorf("%s: code %q exit %d", op, e.Code, res.code)
		}
		hint := proc.RestartHint("com.example.daemon", "/etc/pcg/config.json")
		for _, want := range []string{missing, hint} {
			if !strings.Contains(e.Message, want) {
				t.Errorf("%s: message %q must contain %q (the socket path and the platform's restart hint)", op, e.Message, want)
			}
		}
		if strings.Contains(res.out, "served_from") || strings.Contains(res.out, "cache") {
			t.Errorf("%s: a down daemon never answers from a cache: %s", op, res.out)
		}
	}
}

func TestForwardsUnlistedOpsAndRelaysTheDaemonsAnswer(t *testing.T) {
	r := router.New(router.Options{})
	r.Register("pr", "list", scriptout.OpHandler{SchemaVersion: 7, Handle: func(ctx context.Context, args json.RawMessage) (any, error) {
		dl, ok := ctx.Deadline()
		return map[string]any{"args": json.RawMessage(args), "has_deadline": ok, "left_ms": time.Until(dl).Milliseconds()}, nil
	}})
	path := serveRouter(t, r)

	res := run(t, opts(PR(), path), `{"op":"list","args":{"query":"mine"}}`)
	if res.code != 0 {
		t.Fatalf("exit %d: %s", res.code, res.out)
	}
	var resp struct {
		SchemaVersion int `json:"schemaVersion"`
		Result        struct {
			Args        map[string]string `json:"args"`
			HasDeadline bool              `json:"has_deadline"`
			LeftMS      int64             `json:"left_ms"`
		}
	}
	if err := json.Unmarshal([]byte(res.out), &resp); err != nil {
		t.Fatal(err)
	}
	// The budget is 800ms and the margin 100ms: the daemon was told about 700ms.
	if resp.SchemaVersion != 7 || resp.Result.Args["query"] != "mine" || !resp.Result.HasDeadline || resp.Result.LeftMS > 700 || resp.Result.LeftMS < 300 {
		t.Errorf("%+v", resp)
	}

	// An op the client does not list is still forwarded; the daemon says unknown_op.
	res = run(t, opts(PR(), path), `{"op":"show","args":{"id":1}}`)
	if e := errCode(t, res.out); e.Code != "unknown_op" || res.code != scriptout.ExitCodeForCode("unknown_op") {
		t.Errorf("show: %+v exit %d", e, res.code)
	}
	// Exit codes follow the response's error code, for a relayed daemon error too.
	r.SetMigrating(true, "")
	res = run(t, opts(PR(), path), `{"op":"list"}`)
	if e := errCode(t, res.out); !strings.HasPrefix(e.Message, "migrating:") || res.code != 4 {
		t.Errorf("migrating relay: %+v exit %d", e, res.code)
	}
}

func TestMalformedStdinAnswersAnErrorWithItsExitCode(t *testing.T) {
	missing := filepath.Join(shortTempDir(t), "nope")
	for name, in := range map[string]string{"empty": "", "not json": "{nope", "no op": `{"args":{}}`} {
		res := run(t, opts(PR(), missing), in)
		e := errCode(t, res.out)
		if e.Code != "unavailable" || res.code != scriptout.ExitCodeForCode(e.Code) || res.code == 0 || res.code == 1 {
			t.Errorf("%s: %+v exit %d", name, e, res.code)
		}
	}
}

func TestVersionMismatchIsAnsweredOnlyForAnOutOfRangeDaemon(t *testing.T) {
	r := router.New(router.Options{})
	r.Register("pr", "list", scriptout.OpHandler{SchemaVersion: 1, Handle: func(context.Context, json.RawMessage) (any, error) { return "ok", nil }})
	dir := shortTempDir(t)
	cfg := proc.Config{StateDir: dir, SocketPath: filepath.Join(dir, "s"), DrainTimeout: time.Second}
	srv, err := proc.Start(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(ctx, socket.Handler(r, socket.ServerConfig{Protocol: 4}), nil)
	}()
	t.Cleanup(func() { cancel(); <-done })

	o := opts(PR(), cfg.SocketPath)
	o.Protocol = 2 // the daemon at 4 is two ahead: outside the range
	res := run(t, o, `{"op":"list"}`)
	if e := errCode(t, res.out); e.Code != "version_mismatch" || res.code != scriptout.ExitCodeForCode("version_mismatch") {
		t.Errorf("%+v exit %d", e, res.code)
	}
	o.Protocol = 3 // one behind: served
	if res := run(t, o, `{"op":"list"}`); res.code != 0 {
		t.Errorf("N-1 is served: %s", res.out)
	}
}

func TestFallbackAnswersOnlyWhenTheDaemonIsDown(t *testing.T) {
	missing := filepath.Join(shortTempDir(t), "nope")
	var gotCause error
	inst := PR()
	inst.Ops = map[string]Op{
		"status": {SchemaVersion: 11, Fallback: func(_ context.Context, req scriptout.Request, cause error) (any, error) {
			gotCause = cause
			return map[string]string{"state": "daemon_down", "op": req.Op}, nil
		}},
		"explain": {SchemaVersion: 11, Fallback: func(context.Context, scriptout.Request, error) (any, error) {
			return nil, scriptout.WrapError(scriptout.ErrNotFound, "no such entity")
		}},
		"list": {SchemaVersion: 3},
	}
	res := run(t, opts(inst, missing), `{"op":"status"}`)
	var resp scriptout.Response
	if err := json.Unmarshal([]byte(res.out), &resp); err != nil || res.code != 0 || resp.SchemaVersion != 11 || !strings.Contains(string(resp.Result), "daemon_down") {
		t.Fatalf("fallback result: %q %v exit %d", res.out, err, res.code)
	}
	var down *socket.DaemonDownError
	if !errors.As(gotCause, &down) || down.SocketPath != missing {
		t.Errorf("the fallback gets the daemon-down cause: %v", gotCause)
	}
	if res := run(t, opts(inst, missing), `{"op":"explain"}`); errCode(t, res.out).Code != "not_found" || res.code != scriptout.ExitCodeForCode("not_found") {
		t.Errorf("a fallback error is classified like a handler's: %q exit %d", res.out, res.code)
	}
	// An op without a fallback is plain unavailable, stamped with its schema version.
	res = run(t, opts(inst, missing), `{"op":"list"}`)
	_ = json.Unmarshal([]byte(res.out), &resp)
	if errCode(t, res.out).Code != "unavailable" || resp.SchemaVersion != 3 {
		t.Errorf("%q", res.out)
	}

	// With the daemon UP the fallback never runs: the daemon's answer is relayed.
	r := router.New(router.Options{})
	r.Register("pr", "status", scriptout.OpHandler{SchemaVersion: 11, Handle: func(context.Context, json.RawMessage) (any, error) { return "from-daemon", nil }})
	gotCause = nil
	res = run(t, opts(inst, serveRouter(t, r)), `{"op":"status"}`)
	if !strings.Contains(res.out, "from-daemon") || gotCause != nil {
		t.Errorf("%q fallback ran=%v", res.out, gotCause != nil)
	}
}

func TestBudgetFollowsTheBackendDeadlines(t *testing.T) {
	if BudgetFor("list") != scriptout.ListBackendTimeout || BudgetFor("list_activity") != scriptout.ListActivityBackendTimeout ||
		BudgetFor("show") != scriptout.DefaultBackendTimeout {
		t.Error("the client budget is the backend deadline of pkg/scriptout/limits.go")
	}
}

// A listener that accepts and never answers: the client gives up at its budget
// and says so, it does not hang.
func TestADaemonThatNeverAnswersIsUnavailableAtTheBudget(t *testing.T) {
	dir := shortTempDir(t)
	path := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close()
		}
	}()
	start := time.Now()
	res := run(t, opts(PR(), path), `{"op":"list"}`)
	if e := errCode(t, res.out); e.Code != "unavailable" || !strings.Contains(e.Message, path) {
		t.Errorf("%+v", e)
	}
	if took := time.Since(start); took < 700*time.Millisecond || took > 3*time.Second {
		t.Errorf("answered after %v, want about the 800ms budget", took)
	}
}
