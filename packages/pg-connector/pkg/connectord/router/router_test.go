package router

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

func echo(sv int) scriptout.OpHandler {
	return scriptout.OpHandler{SchemaVersion: sv, Handle: func(_ context.Context, args json.RawMessage) (any, error) {
		return map[string]any{"ok": true, "args": json.RawMessage(args)}, nil
	}}
}

func call(instance, op string) Call {
	return Call{Instance: instance, Request: scriptout.Request{Op: op}}
}

func wantError(t *testing.T, resp scriptout.Response, code string) *scriptout.Error {
	t.Helper()
	if resp.Error == nil || resp.Error.Code != code {
		t.Fatalf("want error %q, got %+v", code, resp)
	}
	return resp.Error
}

func TestRoutesByInstanceAndOp(t *testing.T) {
	r := New(Options{})
	r.Register("pr", "list", echo(3))
	r.Register("ci", "list", scriptout.OpHandler{SchemaVersion: 9, Handle: func(context.Context, json.RawMessage) (any, error) {
		return "ci-list", nil
	}})

	resp := r.Dispatch(context.Background(), Call{Instance: "pr", Request: scriptout.Request{Op: "list", Args: json.RawMessage(`{"q":1}`)}})
	if resp.Error != nil || resp.SchemaVersion != 3 || resp.ProtocolVersion != scriptout.ProtocolVersion {
		t.Fatalf("pr list: %+v", resp)
	}
	if string(resp.Result) != `{"args":{"q":1},"ok":true}` {
		t.Errorf("result %s", resp.Result)
	}
	resp = r.Dispatch(context.Background(), call("ci", "list"))
	if string(resp.Result) != `"ci-list"` || resp.SchemaVersion != 9 {
		t.Errorf("ci list: %+v", resp)
	}
	if got := r.Ops("pr"); len(got) != 1 || got[0] != "list" {
		t.Errorf("Ops(pr) = %v", got)
	}
}

func TestUnknownOpAndUnregisteredInstance(t *testing.T) {
	r := New(Options{})
	r.Register("pr", "list", echo(1))

	e := wantError(t, r.Dispatch(context.Background(), call("pr", "show")), "unknown_op")
	if !strings.Contains(e.Message, `"show"`) {
		t.Errorf("unknown_op message %q must name the op", e.Message)
	}
	// An op registered under another instance is still unknown here.
	r.Register("ci", "list_runs", echo(1))
	wantError(t, r.Dispatch(context.Background(), call("pr", "list_runs")), "unknown_op")

	// A declared instance with nothing registered yet answers unknown_op too.
	d := New(Options{Instances: []string{"pr", "ci"}})
	wantError(t, d.Dispatch(context.Background(), call("ci", "list_runs")), "unknown_op")
	wantError(t, d.Dispatch(context.Background(), call("gh", "list")), "invalid_argument")

	for _, inst := range []string{"", "gh", "PR", "pr "} {
		e := wantError(t, r.Dispatch(context.Background(), call(inst, "list")), "invalid_argument")
		if !strings.Contains(e.Message, "instance") {
			t.Errorf("instance %q: message %q must say it is the instance", inst, e.Message)
		}
	}
}

func TestMigratingGateAnswersEveryOpUnavailable(t *testing.T) {
	r := New(Options{})
	r.Register("pr", "list", echo(1))
	r.SetMigrating(true, "store schema 2 to 3, 40% done")
	for _, c := range []Call{call("pr", "list"), call("pr", "no_such_op"), call("nope", "list")} {
		e := wantError(t, r.Dispatch(context.Background(), c), "unavailable")
		if !strings.HasPrefix(e.Message, "migrating:") || !strings.Contains(e.Message, "40% done") {
			t.Errorf("%v: message %q must start with migrating: and carry the detail", c, e.Message)
		}
	}
	if !r.Migrating() {
		t.Error("Migrating must report the open gate")
	}
	r.SetMigrating(true, "")
	e := wantError(t, r.Dispatch(context.Background(), call("pr", "list")), "unavailable")
	if !strings.HasPrefix(e.Message, "migrating:") {
		t.Errorf("default message %q", e.Message)
	}
	r.SetMigrating(false, "")
	if resp := r.Dispatch(context.Background(), call("pr", "list")); resp.Error != nil || r.Migrating() {
		t.Errorf("a closed gate serves: %+v", resp)
	}
}

func TestConfigCheckAnswersInvalidArgumentNamingBothPaths(t *testing.T) {
	check := func(block json.RawMessage) error {
		var b struct {
			Hash string `json:"config_hash"`
			Path string `json:"config_path"`
		}
		if len(block) == 0 {
			return nil
		}
		if err := json.Unmarshal(block, &b); err != nil {
			return err
		}
		if b.Hash == "" || b.Hash == "h-daemon" {
			return nil
		}
		return errors.New("block rendered from " + b.Path + " but the daemon runs /etc/daemon.json")
	}
	r := New(Options{CheckConfig: check})
	r.Register("pr", "list", echo(1))

	mk := func(cfg string) Call {
		c := call("pr", "list")
		if cfg != "" {
			c.Request.Config = json.RawMessage(cfg)
		}
		return c
	}
	// No block, a block with no hash and a matching hash all pass.
	for _, cfg := range []string{"", `{}`, `{"config_path":"/x"}`, `{"config_hash":"h-daemon"}`} {
		if resp := r.Dispatch(context.Background(), mk(cfg)); resp.Error != nil {
			t.Errorf("config %q must be accepted: %+v", cfg, resp.Error)
		}
	}
	e := wantError(t, r.Dispatch(context.Background(), mk(`{"config_hash":"h-old","config_path":"/etc/umbrella.json"}`)), "invalid_argument")
	if !strings.Contains(e.Message, "/etc/umbrella.json") || !strings.Contains(e.Message, "/etc/daemon.json") {
		t.Errorf("message %q must name both config paths", e.Message)
	}
}

func TestHandlerContextCarriesRequestConfig(t *testing.T) {
	r := New(Options{})
	var gotConfig json.RawMessage
	var gotRange scriptout.TimeRange
	var rangeErr error
	r.Register("pr", "list", scriptout.OpHandler{SchemaVersion: 1, Handle: func(ctx context.Context, _ json.RawMessage) (any, error) {
		gotConfig = scriptout.ConfigFromContext(ctx)
		gotRange, rangeErr = scriptout.ListRangeFromContext(ctx)
		return nil, nil
	}})
	cfg := `{"list_since":"2026-01-02T03:04:05Z","list_before":"2026-02-03T00:00:00Z","other":1}`
	c := call("pr", "list")
	c.Request.Config = json.RawMessage(cfg)
	if resp := r.Dispatch(context.Background(), c); resp.Error != nil {
		t.Fatal(resp.Error)
	}
	if string(gotConfig) != cfg {
		t.Errorf("config %s, want %s", gotConfig, cfg)
	}
	want := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if rangeErr != nil || !gotRange.Since.Equal(want) || gotRange.Before.IsZero() {
		t.Errorf("range %+v err %v", gotRange, rangeErr)
	}

	// No config attached: the context still answers (nil), as ServeLoop does.
	gotConfig = json.RawMessage("sentinel")
	r.Dispatch(context.Background(), call("pr", "list"))
	if len(gotConfig) != 0 {
		t.Errorf("no config: got %q", gotConfig)
	}
}

func TestHandlerErrorsKeepTheirTaxonomyAndSchemaVersion(t *testing.T) {
	r := New(Options{})
	r.Register("pr", "list", scriptout.OpHandler{SchemaVersion: 4, Handle: func(context.Context, json.RawMessage) (any, error) {
		return nil, scriptout.WrapError(scriptout.ErrQueryNotRecognized, "no query mine")
	}})
	r.Register("pr", "plain", scriptout.OpHandler{SchemaVersion: 4, Handle: func(context.Context, json.RawMessage) (any, error) {
		return nil, errors.New("boom")
	}})
	resp := r.Dispatch(context.Background(), call("pr", "list"))
	wantError(t, resp, "query_not_recognized")
	if resp.SchemaVersion != 4 {
		t.Errorf("schemaVersion %d", resp.SchemaVersion)
	}
	wantError(t, r.Dispatch(context.Background(), call("pr", "plain")), "unavailable")
}

func TestDeadlineIsOnTheContextButNeverEnforced(t *testing.T) {
	r := New(Options{})
	var sawDeadline bool
	r.Register("pr", "list", scriptout.OpHandler{SchemaVersion: 1, Handle: func(ctx context.Context, _ json.RawMessage) (any, error) {
		_, sawDeadline = ctx.Deadline()
		<-ctx.Done() // outlive the deadline, then answer anyway
		return "late but answered", nil
	}})
	c := call("pr", "list")
	c.Deadline = 30 * time.Millisecond
	resp := r.Dispatch(context.Background(), c)
	if !sawDeadline || resp.Error != nil || string(resp.Result) != `"late but answered"` {
		t.Fatalf("sawDeadline=%v resp=%+v: the handler's answer after the deadline is the answer", sawDeadline, resp)
	}

	// A handler that fails because the deadline passed says what it was doing.
	r.Register("pr", "slow", scriptout.OpHandler{SchemaVersion: 1, Handle: func(ctx context.Context, _ json.RawMessage) (any, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}})
	c.Request.Op = "slow"
	e := wantError(t, r.Dispatch(context.Background(), c), "unavailable")
	if !strings.Contains(e.Message, "deadline exceeded") || !strings.Contains(e.Message, "op=slow") {
		t.Errorf("message %q", e.Message)
	}

	// No stated deadline: none assumed.
	r.Register("pr", "free", scriptout.OpHandler{SchemaVersion: 1, Handle: func(ctx context.Context, _ json.RawMessage) (any, error) {
		if _, ok := ctx.Deadline(); ok {
			t.Error("a call with no deadline must not get one")
		}
		return nil, nil
	}})
	r.Dispatch(context.Background(), call("pr", "free"))
}

func TestRegisterRejectsDuplicatesAndEmpties(t *testing.T) {
	r := New(Options{})
	r.Register("pr", "list", echo(1))
	for name, f := range map[string]func(){
		"duplicate":   func() { r.Register("pr", "list", echo(1)) },
		"no instance": func() { r.Register("", "x", echo(1)) },
		"no op":       func() { r.Register("pr", "", echo(1)) },
		"no handler":  func() { r.Register("pr", "y", scriptout.OpHandler{}) },
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: want a panic", name)
				}
			}()
			f()
		}()
	}
}

func TestAnnotationsAndMiddleware(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	tick := now
	r := New(Options{Now: func() time.Time { t := tick; tick = tick.Add(40 * time.Millisecond); return t }})
	r.Register("pr", "list", scriptout.OpHandler{SchemaVersion: 1, Handle: func(ctx context.Context, _ json.RawMessage) (any, error) {
		Annotate(ctx, "store", true, 90, 0)
		return nil, nil
	}})
	r.Register("pr", "fill", scriptout.OpHandler{SchemaVersion: 1, Handle: func(ctx context.Context, _ json.RawMessage) (any, error) {
		Annotate(ctx, "origin", false, 0, 5*time.Second) // the handler's own wait wins
		return nil, nil
	}})

	var order []string
	var seen []Annotations
	var seenOps []string
	r.Use(func(next Func) Func {
		return func(ctx context.Context, c Call) scriptout.Response {
			order = append(order, "outer-in")
			resp := next(ctx, c)
			order = append(order, "outer-out")
			seen = append(seen, AnnotationsFrom(ctx))
			seenOps = append(seenOps, c.Request.Op)
			return resp
		}
	})
	r.Use(func(next Func) Func {
		return func(ctx context.Context, c Call) scriptout.Response {
			order = append(order, "inner-in")
			defer func() { order = append(order, "inner-out") }()
			return next(ctx, c)
		}
	})

	c := call("pr", "list")
	c.Received = now.Add(-time.Second) // queued for one second before the router saw it
	r.Dispatch(context.Background(), c)
	if strings.Join(order, ",") != "outer-in,inner-in,inner-out,outer-out" {
		t.Errorf("middleware order %v (the first Use is the outermost)", order)
	}
	a := seen[0]
	if a.ServedFrom != "store" || !a.Stale || a.AgeSeconds != 90 {
		t.Errorf("annotations %+v", a)
	}
	if a.Wait < time.Second || a.Wait > 2*time.Second {
		t.Errorf("the router measures the wait from Received to the handler's start: %v", a.Wait)
	}

	c = call("pr", "fill")
	r.Dispatch(context.Background(), c)
	if got := seen[1]; got.ServedFrom != "origin" || got.Wait != 5*time.Second {
		t.Errorf("a handler-set wait replaces the measured one: %+v", got)
	}

	// An error answered before any handler ran still reaches the middleware,
	// with empty annotations.
	r.Dispatch(context.Background(), call("pr", "nope"))
	if len(seen) != 3 || seen[2] != (Annotations{}) || seenOps[2] != "nope" {
		t.Errorf("seen %+v ops %v", seen, seenOps)
	}

	// Annotate on a context that never went through Dispatch is harmless.
	Annotate(context.Background(), "x", false, 0, 0)
	if got := AnnotationsFrom(context.Background()); got != (Annotations{}) {
		t.Errorf("a bare context holds nothing: %+v", got)
	}
}
