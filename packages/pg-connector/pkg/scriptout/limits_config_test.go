package scriptout

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// Tests of the per-backend deadline override [bead pg2-apllc]: a backend's
// config block may carry timeouts.list and timeouts.show (Go duration strings)
// that replace that one op's call deadline on BOTH sides of the exec. Every
// test here is hermetic: no real backend, no network; the umbrella side is
// observed through the context the exec factory receives, the backend side
// through the context its handler receives.

func rawCfg(s string) json.RawMessage { return json.RawMessage(s) }

// timeoutsCfg builds a config block with the given timeouts members, e.g.
// timeoutsCfg(`"list":"60s"`), plus an unrelated member the override must
// leave alone.
func timeoutsCfg(members string) json.RawMessage {
	return rawCfg(`{"queries":{"q":["x"]},"timeouts":{` + members + `}}`)
}

func TestConfiguredTimeout_Parsing(t *testing.T) {
	cases := []struct {
		name    string
		op      string
		cfg     json.RawMessage
		want    time.Duration
		wantSet bool
		wantErr string // substring; "" means no error
	}{
		{name: "nil config", op: OpList, cfg: nil},
		{name: "empty config", op: OpList, cfg: rawCfg(``)},
		{name: "empty object", op: OpList, cfg: rawCfg(`{}`)},
		{name: "config that is not an object is not this package's to judge", op: OpList, cfg: rawCfg(`["x"]`)},
		{name: "null timeouts", op: OpList, cfg: rawCfg(`{"timeouts":null}`)},
		{name: "timeouts without the op", op: OpList, cfg: timeoutsCfg(`"show":"20s"`)},
		{name: "null entry", op: OpList, cfg: timeoutsCfg(`"list":null`)},
		{name: "list configured", op: OpList, cfg: timeoutsCfg(`"list":"60s"`), want: 60 * time.Second, wantSet: true},
		{name: "show configured", op: OpShow, cfg: timeoutsCfg(`"show":"45s"`), want: 45 * time.Second, wantSet: true},
		{name: "both configured, list read", op: OpList, cfg: timeoutsCfg(`"list":"61s","show":"22s"`), want: 61 * time.Second, wantSet: true},
		{name: "both configured, show read", op: OpShow, cfg: timeoutsCfg(`"list":"61s","show":"22s"`), want: 22 * time.Second, wantSet: true},
		{name: "composite duration", op: OpList, cfg: timeoutsCfg(`"list":"1m30s"`), want: 90 * time.Second, wantSet: true},
		{name: "minimum is allowed", op: OpList, cfg: timeoutsCfg(`"list":"1s"`), want: time.Second, wantSet: true},
		{name: "maximum is allowed", op: OpList, cfg: timeoutsCfg(`"list":"300s"`), want: 300 * time.Second, wantSet: true},
		{name: "maximum in other units", op: OpList, cfg: timeoutsCfg(`"list":"5m"`), want: 300 * time.Second, wantSet: true},

		{name: "below minimum", op: OpList, cfg: timeoutsCfg(`"list":"999ms"`), wantErr: "timeouts.list"},
		{name: "zero", op: OpList, cfg: timeoutsCfg(`"list":"0s"`), wantErr: "timeouts.list"},
		{name: "negative", op: OpList, cfg: timeoutsCfg(`"list":"-5s"`), wantErr: "timeouts.list"},
		{name: "above maximum", op: OpList, cfg: timeoutsCfg(`"list":"301s"`), wantErr: "timeouts.list"},
		{name: "an hour (the typo the cap exists for)", op: OpList, cfg: timeoutsCfg(`"list":"1h"`), wantErr: "timeouts.list"},
		{name: "not a duration", op: OpList, cfg: timeoutsCfg(`"list":"soon"`), wantErr: "timeouts.list"},
		{name: "bare number string has no unit", op: OpList, cfg: timeoutsCfg(`"list":"60"`), wantErr: "timeouts.list"},
		{name: "empty string", op: OpList, cfg: timeoutsCfg(`"list":""`), wantErr: "timeouts.list"},
		{name: "number instead of a string", op: OpList, cfg: timeoutsCfg(`"list":60`), wantErr: "timeouts.list"},
		{name: "bool instead of a string", op: OpList, cfg: timeoutsCfg(`"list":true`), wantErr: "timeouts.list"},
		{name: "show above maximum names show", op: OpShow, cfg: timeoutsCfg(`"show":"400s"`), wantErr: "timeouts.show"},
		{name: "timeouts is not an object", op: OpList, cfg: rawCfg(`{"timeouts":"60s"}`), wantErr: "timeouts"},
		{name: "timeouts is an array", op: OpShow, cfg: rawCfg(`{"timeouts":[1]}`), wantErr: "timeouts"},

		// A bad entry for one op never leaks into another.
		{name: "bad list entry, show asked", op: OpShow, cfg: timeoutsCfg(`"list":"bad","show":"20s"`), want: 20 * time.Second, wantSet: true},
		{name: "bad show entry, list asked", op: OpList, cfg: timeoutsCfg(`"show":"bad","list":"70s"`), want: 70 * time.Second, wantSet: true},

		// Ops that are not configurable ignore the block entirely, even a
		// block that would be invalid for list or show.
		{name: "list_activity ignores timeouts.list_activity", op: OpListActivity, cfg: timeoutsCfg(`"list_activity":"90s"`)},
		{name: "list_activity ignores timeouts.list", op: OpListActivity, cfg: timeoutsCfg(`"list":"90s"`)},
		{name: "commit ignores a valid block", op: "commit", cfg: timeoutsCfg(`"commit":"90s","list":"90s"`)},
		{name: "commit ignores an invalid block", op: "commit", cfg: timeoutsCfg(`"list":"bad"`)},
		{name: "commit ignores a malformed timeouts member", op: "commit", cfg: rawCfg(`{"timeouts":"x"}`)},
		{name: "capabilities ignores an invalid block", op: OpCapabilities, cfg: timeoutsCfg(`"list":"1h"`)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, set, err := configuredTimeout(c.op, c.cfg)
			if c.wantErr != "" {
				if err == nil {
					t.Fatalf("configuredTimeout(%q, %s) = %v, %v, nil; want an error naming %q", c.op, c.cfg, d, set, c.wantErr)
				}
				if !errors.Is(err, ErrInvalidArgument) {
					t.Errorf("error %q does not wrap ErrInvalidArgument", err)
				}
				if !strings.Contains(err.Error(), c.wantErr) {
					t.Errorf("error %q does not name %q", err, c.wantErr)
				}
				if set || d != 0 {
					t.Errorf("on error got (%v, %v), want the zero value", d, set)
				}
				if got := validateTimeoutConfig(c.op, c.cfg); got == nil {
					t.Errorf("validateTimeoutConfig returned nil for an invalid value")
				}
				return
			}
			if err != nil {
				t.Fatalf("configuredTimeout(%q, %s) error: %v", c.op, c.cfg, err)
			}
			if set != c.wantSet || d != c.want {
				t.Errorf("configuredTimeout(%q, %s) = (%v, %v), want (%v, %v)", c.op, c.cfg, d, set, c.want, c.wantSet)
			}
			if got := validateTimeoutConfig(c.op, c.cfg); got != nil {
				t.Errorf("validateTimeoutConfig = %v, want nil", got)
			}
		})
	}
}

// TestConfiguredTimeout_PinnedValues pins the literal budgets for the ZR-shaped
// case (list 60s: backend 60s, umbrella kill 65s) and the two bounds. The
// literals are deliberate: a budget is a behaviour change.
func TestConfiguredTimeout_PinnedValues(t *testing.T) {
	cases := []struct {
		members string
		backend time.Duration
		exec    time.Duration
	}{
		{`"list":"60s"`, 60 * time.Second, 65 * time.Second},
		{`"list":"1s"`, 1 * time.Second, 6 * time.Second},
		{`"list":"300s"`, 300 * time.Second, 305 * time.Second},
	}
	for _, c := range cases {
		cf := timeoutsCfg(c.members)
		if got := backendTimeoutFor(OpList, cf); got != c.backend {
			t.Errorf("%s: backend %v, want %v", c.members, got, c.backend)
		}
		if got := execTimeoutFor(OpList, cf); got != c.exec {
			t.Errorf("%s: umbrella %v, want %v", c.members, got, c.exec)
		}
	}
	if MinConfiguredTimeout != time.Second || MaxConfiguredTimeout != 300*time.Second {
		t.Errorf("bounds are %v..%v, want 1s..300s", MinConfiguredTimeout, MaxConfiguredTimeout)
	}
}

// TestConfiguredTimeout_KillMarginHoldsInEveryCase pins the acceptance rule:
// the umbrella's deadline minus the backend's equals BackendDeadlineMargin for
// the default, a configured, an invalid and an out-of-range value, for every op.
func TestConfiguredTimeout_KillMarginHoldsInEveryCase(t *testing.T) {
	configs := map[string]json.RawMessage{
		"default (nil)":       nil,
		"default (no block)":  rawCfg(`{"queries":{}}`),
		"list 60s":            timeoutsCfg(`"list":"60s"`),
		"list 1s (minimum)":   timeoutsCfg(`"list":"1s"`),
		"list 300s (maximum)": timeoutsCfg(`"list":"300s"`),
		"show 20s":            timeoutsCfg(`"show":"20s"`),
		"invalid":             timeoutsCfg(`"list":"soon","show":"x"`),
		"below minimum":       timeoutsCfg(`"list":"1ms","show":"1ms"`),
		"above maximum":       timeoutsCfg(`"list":"1h","show":"1h"`),
		"timeouts not object": rawCfg(`{"timeouts":3}`),
	}
	ops := []string{OpList, OpShow, OpListActivity, "comment", OpCapabilities, "review_submit"}
	for name, cf := range configs {
		for _, op := range ops {
			backend, umb := backendTimeoutFor(op, cf), execTimeoutFor(op, cf)
			if umb-backend != BackendDeadlineMargin {
				t.Errorf("%s, op %q: umbrella %v - backend %v = %v, want exactly %v",
					name, op, umb, backend, umb-backend, BackendDeadlineMargin)
			}
		}
	}
}

// TestConfiguredTimeout_OnlyTheNamedOpChanges pins that timeouts.list leaves
// show, list_activity and every other op on the budget it had without it, and
// timeouts.show leaves list alone.
func TestConfiguredTimeout_OnlyTheNamedOpChanges(t *testing.T) {
	listOnly := timeoutsCfg(`"list":"90s"`)
	showOnly := timeoutsCfg(`"show":"20s"`)
	for _, op := range []string{OpShow, OpListActivity, "comment", "review_submit", OpCapabilities, OpAuthStatus} {
		if got, want := backendTimeoutFor(op, listOnly), backendTimeoutFor(op, nil); got != want {
			t.Errorf("op %q: backend %v with timeouts.list set, want the unchanged %v", op, got, want)
		}
		if got, want := execTimeoutFor(op, listOnly), execTimeoutFor(op, nil); got != want {
			t.Errorf("op %q: umbrella %v with timeouts.list set, want the unchanged %v", op, got, want)
		}
	}
	for _, op := range []string{OpList, OpListActivity, "comment"} {
		if got, want := backendTimeoutFor(op, showOnly), backendTimeoutFor(op, nil); got != want {
			t.Errorf("op %q: backend %v with timeouts.show set, want the unchanged %v", op, got, want)
		}
		if got, want := execTimeoutFor(op, showOnly), execTimeoutFor(op, nil); got != want {
			t.Errorf("op %q: umbrella %v with timeouts.show set, want the unchanged %v", op, got, want)
		}
	}
	if got := backendTimeoutFor(OpList, listOnly); got != 90*time.Second {
		t.Errorf("list backend %v, want 90s", got)
	}
	if got := backendTimeoutFor(OpShow, showOnly); got != 20*time.Second {
		t.Errorf("show backend %v, want 20s", got)
	}
	// And the defaults themselves are untouched.
	if got := backendTimeoutFor(OpShow, nil); got != DefaultBackendTimeout {
		t.Errorf("show default backend %v, want %v", got, DefaultBackendTimeout)
	}
	if got := backendTimeoutFor(OpList, nil); got != ListBackendTimeout {
		t.Errorf("list default backend %v, want %v", got, ListBackendTimeout)
	}
	if got := backendTimeoutFor(OpListActivity, listOnly); got != ListActivityBackendTimeout {
		t.Errorf("list_activity backend %v, want %v", got, ListActivityBackendTimeout)
	}
}

// TestConfiguredTimeout_Precedence pins config over the swappable test
// variables over the constants: with the variables swapped, a request without
// a (valid) override sees the swap, one with a valid override sees the config.
func TestConfiguredTimeout_Precedence(t *testing.T) {
	origB, origE := listBackendTimeout, listExecTimeout
	origShowB, origShowE := backendTimeout, execTimeout
	listBackendTimeout, listExecTimeout = 7*time.Second, 12*time.Second
	backendTimeout, execTimeout = 3*time.Second, 8*time.Second
	t.Cleanup(func() {
		listBackendTimeout, listExecTimeout = origB, origE
		backendTimeout, execTimeout = origShowB, origShowE
	})

	// Swap beats constant.
	if b, e := backendTimeoutFor(OpList, nil), execTimeoutFor(OpList, nil); b != 7*time.Second || e != 12*time.Second {
		t.Errorf("no config: list (%v, %v), want the swapped (7s, 12s)", b, e)
	}
	if b, e := backendTimeoutFor(OpShow, nil), execTimeoutFor(OpShow, nil); b != 3*time.Second || e != 8*time.Second {
		t.Errorf("no config: show (%v, %v), want the swapped (3s, 8s)", b, e)
	}
	// Config beats swap.
	cf := timeoutsCfg(`"list":"60s","show":"20s"`)
	if b, e := backendTimeoutFor(OpList, cf), execTimeoutFor(OpList, cf); b != 60*time.Second || e != 65*time.Second {
		t.Errorf("configured: list (%v, %v), want (60s, 65s)", b, e)
	}
	if b, e := backendTimeoutFor(OpShow, cf), execTimeoutFor(OpShow, cf); b != 20*time.Second || e != 25*time.Second {
		t.Errorf("configured: show (%v, %v), want (20s, 25s)", b, e)
	}
	// An invalid value falls back to the swap, not to the constant.
	bad := timeoutsCfg(`"list":"1h"`)
	if b, e := backendTimeoutFor(OpList, bad), execTimeoutFor(OpList, bad); b != 7*time.Second || e != 12*time.Second {
		t.Errorf("invalid: list (%v, %v), want the swapped default (7s, 12s)", b, e)
	}
}

// deadlineOf returns how long ctx has left, failing the test if it has no
// deadline at all.
func deadlineOf(t *testing.T, ctx context.Context) time.Duration {
	t.Helper()
	dl, ok := ctx.Deadline()
	if !ok {
		t.Error("context has no deadline")
		return 0
	}
	return time.Until(dl)
}

// umbrellaDeadline runs Invoke for op with config cf against a helper child
// that answers immediately, and returns the deadline the umbrella armed on the
// exec's context (observed in the exec factory).
func umbrellaDeadline(t *testing.T, op string, cf json.RawMessage) time.Duration {
	t.Helper()
	base := helperCmdFactory("ok_auth")
	var got time.Duration
	orig := execCmdFactory
	execCmdFactory = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		got = deadlineOf(t, ctx)
		return base(ctx, name, args...)
	}
	t.Cleanup(func() { execCmdFactory = orig })
	if _, err := Invoke(context.Background(), "fake-binary", op, nil, cf); err != nil {
		t.Fatalf("Invoke %q: %v", op, err)
	}
	return got
}

// backendDeadline runs the serve loop for op with config cf and a handler that
// records the deadline of the context it receives.
func backendDeadline(t *testing.T, op string, cf json.RawMessage) time.Duration {
	t.Helper()
	var (
		mu  sync.Mutex
		got time.Duration
	)
	table := DispatchTable{op: {SchemaVersion: 1, Handle: func(ctx context.Context, _ json.RawMessage) (any, error) {
		mu.Lock()
		defer mu.Unlock()
		got = deadlineOf(t, ctx)
		return map[string]bool{"ok": true}, nil
	}}}
	req := Request{Op: op, Args: json.RawMessage(`{}`), Config: cf}
	payload, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if code, resp := runServeLoop(t, table, string(payload)); code != 0 {
		t.Fatalf("serve %q: exit %d, resp=%v", op, code, resp)
	}
	mu.Lock()
	defer mu.Unlock()
	return got
}

// TestConfiguredTimeout_BothSidesArmTheConfiguredDeadline observes the REAL
// deadlines both processes arm (the umbrella's exec context, the backend's
// handler context) and pins the acceptance rule end to end: the umbrella's
// minus the backend's is the 5s margin in the default, the configured, the
// invalid and the out-of-range cases, and the configured value is what each
// side actually armed, not merely what the helper computes.
func TestConfiguredTimeout_BothSidesArmTheConfiguredDeadline(t *testing.T) {
	cases := []struct {
		name         string
		op           string
		cfg          json.RawMessage
		wantBackend  time.Duration // armed by the backend; 0 means the request is refused
		wantUmbrella time.Duration
	}{
		{"list default", OpList, nil, 40 * time.Second, 45 * time.Second},
		{"list configured 60s", OpList, timeoutsCfg(`"list":"60s"`), 60 * time.Second, 65 * time.Second},
		{"list configured 1s", OpList, timeoutsCfg(`"list":"1s"`), 1 * time.Second, 6 * time.Second},
		{"list configured 300s", OpList, timeoutsCfg(`"list":"300s"`), 300 * time.Second, 305 * time.Second},
		{"show default", OpShow, nil, 30 * time.Second, 35 * time.Second},
		{"show unaffected by timeouts.list", OpShow, timeoutsCfg(`"list":"60s"`), 30 * time.Second, 35 * time.Second},
		{"show configured 45s", OpShow, timeoutsCfg(`"show":"45s"`), 45 * time.Second, 50 * time.Second},
		{"list_activity unaffected by timeouts.list", OpListActivity, timeoutsCfg(`"list":"90s"`), 60 * time.Second, 65 * time.Second},
		{"comment unaffected by timeouts.list", "comment", timeoutsCfg(`"list":"90s"`), 30 * time.Second, 35 * time.Second},
		// Invalid values: the backend refuses (0), the umbrella arms the
		// default so the backend's answer arrives first.
		{"list invalid text", OpList, timeoutsCfg(`"list":"soon"`), 0, 45 * time.Second},
		{"list below minimum", OpList, timeoutsCfg(`"list":"1ms"`), 0, 45 * time.Second},
		{"list above maximum", OpList, timeoutsCfg(`"list":"301s"`), 0, 45 * time.Second},
		{"show invalid text", OpShow, timeoutsCfg(`"show":"soon"`), 0, 35 * time.Second},
	}
	const slack = 3 * time.Second // wall time spent between arming and observing
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			umbrella := umbrellaDeadline(t, c.op, c.cfg)
			if umbrella > c.wantUmbrella || umbrella < c.wantUmbrella-slack {
				t.Errorf("umbrella armed %v, want about %v", umbrella, c.wantUmbrella)
			}
			if c.wantBackend == 0 {
				return // covered by TestServeLoop_InvalidTimeoutConfig_IsInvalidArgument
			}
			backend := backendDeadline(t, c.op, c.cfg)
			if backend > c.wantBackend || backend < c.wantBackend-slack {
				t.Errorf("backend armed %v, want about %v", backend, c.wantBackend)
			}
			if diff := umbrella - backend; diff < BackendDeadlineMargin-slack || diff > BackendDeadlineMargin+slack {
				t.Errorf("umbrella %v - backend %v = %v, want about the %v margin", umbrella, backend, diff, BackendDeadlineMargin)
			}
		})
	}
}

// TestServeLoop_InvalidTimeoutConfig_IsInvalidArgument pins that an invalid or
// out-of-range timeouts.<op> makes the BACKEND answer invalid_argument naming
// the key, without running the handler, while an op the entry does not govern
// is served as before.
func TestServeLoop_InvalidTimeoutConfig_IsInvalidArgument(t *testing.T) {
	var mu sync.Mutex
	calls := map[string]int{}
	handler := func(op string) OpHandler {
		return OpHandler{SchemaVersion: 1, Handle: func(context.Context, json.RawMessage) (any, error) {
			mu.Lock()
			defer mu.Unlock()
			calls[op]++
			return map[string]bool{"ok": true}, nil
		}}
	}
	table := DispatchTable{OpList: handler(OpList), OpShow: handler(OpShow), OpListActivity: handler(OpListActivity)}

	for _, tc := range []struct {
		name, op, members, wantKey string
	}{
		{"list not a duration", OpList, `"list":"soon"`, "timeouts.list"},
		{"list below minimum", OpList, `"list":"500ms"`, "timeouts.list"},
		{"list above maximum", OpList, `"list":"301s"`, "timeouts.list"},
		{"list an hour", OpList, `"list":"1h"`, "timeouts.list"},
		{"list not a string", OpList, `"list":60`, "timeouts.list"},
		{"show above maximum", OpShow, `"show":"1h"`, "timeouts.show"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := `{"op":"` + tc.op + `","args":{},"config":{"timeouts":{` + tc.members + `}}}`
			code, resp := runServeLoop(t, table, payload)
			if want := ExitCodeForError(ErrInvalidArgument); code != want {
				t.Errorf("exit code %d, want invalid_argument's %d (resp=%v)", code, want, resp)
			}
			errObj, _ := resp["error"].(map[string]any)
			if errObj["code"] != "invalid_argument" {
				t.Errorf("error code %v, want invalid_argument (resp=%v)", errObj["code"], resp)
			}
			if msg, _ := errObj["message"].(string); !strings.Contains(msg, tc.wantKey) {
				t.Errorf("message %q does not name %q", msg, tc.wantKey)
			}
			mu.Lock()
			defer mu.Unlock()
			if calls[tc.op] != 0 {
				t.Errorf("handler for %q ran %d time(s) despite the invalid config", tc.op, calls[tc.op])
			}
		})
	}

	// The same bad list entry does not touch show, list_activity, or a show
	// request that carries its own valid entry.
	bad := `"list":"1h"`
	for _, op := range []string{OpShow, OpListActivity} {
		payload := `{"op":"` + op + `","args":{},"config":{"timeouts":{` + bad + `}}}`
		if code, resp := runServeLoop(t, table, payload); code != 0 {
			t.Errorf("op %q refused because of timeouts.list: exit %d resp=%v", op, code, resp)
		}
	}
	payload := `{"op":"show","args":{},"config":{"timeouts":{` + bad + `,"show":"20s"}}}`
	if code, resp := runServeLoop(t, table, payload); code != 0 {
		t.Errorf("show with its own valid entry refused: exit %d resp=%v", code, resp)
	}
}

// TestServeLoop_ConfiguredListDeadlineIsEnforced proves the configured value is
// the deadline the handler really runs under, not just one that is armed: the
// same slow handler fails under a configured 1s and succeeds without it under
// a swapped, longer list default. (The 1s minimum makes this the one real
// wait here.)
func TestServeLoop_ConfiguredListDeadlineIsEnforced(t *testing.T) {
	origList := listBackendTimeout
	listBackendTimeout = 10 * time.Second
	t.Cleanup(func() { listBackendTimeout = origList })

	slow := func(ctx context.Context, _ json.RawMessage) (any, error) {
		select {
		case <-time.After(1500 * time.Millisecond):
			return map[string]any{"ok": true}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	table := DispatchTable{OpList: {SchemaVersion: 1, Handle: slow}}

	if code, resp := runServeLoop(t, table, `{"op":"list","args":{}}`); code != 0 {
		t.Fatalf("list without config under the %v default: exit %d resp=%v, want success", listBackendTimeout, code, resp)
	}
	code, resp := runServeLoop(t, table, `{"op":"list","args":{},"config":{"timeouts":{"list":"1s"}}}`)
	if code == 0 {
		t.Fatalf("list ran past its configured 1s and still succeeded (resp=%v)", resp)
	}
	errObj, _ := resp["error"].(map[string]any)
	if msg, _ := errObj["message"].(string); !strings.Contains(msg, "limit 1s") {
		t.Errorf("deadline error %q does not name the configured limit 1s", msg)
	}
}
