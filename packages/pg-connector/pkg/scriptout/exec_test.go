package scriptout

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// --------------------------------------------------------------------
// In-process backend double (a Runner, see runner.go).
//
// It used to re-execute the race-built test binary once per call
// (GO_WANT_HELPER_PROCESS / TestHelperProcess), and a race-built child
// sleeps 1 s at exit (atexit_sleep_ms), so every fake call cost about 1 s
// under -race. The behavior names (GO_HELPER_BEHAVIOR values) are kept so
// call sites did not change. The real exec path (ExecRunner, runInvoke's
// WaitDelay) is covered by runner_test.go with sh -c.
// --------------------------------------------------------------------

// encodeLine renders v as json.NewEncoder(os.Stdout).Encode did: compact
// JSON plus a newline.
func encodeLine(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b) + "\n"
}

// helperRun answers one Call as a backend binary would for behavior: the
// request arrives on c.Stdin, the answer is the Result.
func helperRun(ctx context.Context, behavior string, c Call) Result {
	stdin := []byte(c.Stdin)
	okOut := func(v any) Result { return Result{Stdout: encodeLine(v)} }

	switch behavior {
	case "ok_auth":
		return okOut(Response{
			ProtocolVersion: ProtocolVersion,
			SchemaVersion:   1,
			Result:          json.RawMessage(`{"state":"OK"}`),
		})
	case "not_found":
		return Result{Code: 1, Stdout: encodeLine(Response{
			ProtocolVersion: ProtocolVersion,
			SchemaVersion:   1,
			Error:           &Error{Code: "not_found", Message: "no such pr"},
		})}
	case "unknown_op":
		return Result{Code: 1, Stdout: encodeLine(Response{
			ProtocolVersion: ProtocolVersion,
			Error:           &Error{Code: "unknown_op", Message: `unknown op "auth_status"`},
		})}
	case "non_json":
		return Result{Stdout: "this is not json\n"}
	case "capabilities":
		return okOut(CapabilitiesResponse{
			ProtocolVersion: ProtocolVersion,
			SchemaVersions:  map[string]int{"pr": 1},
			Ops:             []string{"get_pr", OpAuthStatus, OpCapabilities},
		})
	case "protocol_mismatch":
		// A well-formed, otherwise-successful envelope from a binary built
		// against a different (here: newer) wire-envelope protocolVersion
		// than this process expects — the ordinary-op half of the
		// umbrella/backend skew this docket exists to catch.
		return okOut(Response{
			ProtocolVersion: ProtocolVersion + 1,
			SchemaVersion:   1,
			Result:          json.RawMessage(`{"state":"OK"}`),
		})
	case "capabilities_protocol_mismatch":
		// Same skew, surfaced through the bespoke capabilities shape
		// instead of the ordinary Response envelope.
		return okOut(CapabilitiesResponse{
			ProtocolVersion: ProtocolVersion + 1,
			SchemaVersions:  map[string]int{"pr": 1},
			Ops:             []string{"get_pr", OpAuthStatus, OpCapabilities},
		})
	case "echo_op":
		var req Request
		if err := json.Unmarshal(stdin, &req); err != nil {
			return Result{Code: 1, Stdout: encodeLine(Response{Error: &Error{Code: "unavailable", Message: err.Error()}})}
		}
		return okOut(Response{
			ProtocolVersion: ProtocolVersion,
			Result:          json.RawMessage(`"` + req.Op + `"`),
		})
	case "echo_config":
		// Echoes back the request's own Config member (bead pg2-2j5ac.28.1)
		// as the result — used to prove Invoke's config parameter actually
		// reaches the wire request, distinct from echo_op's op-only echo.
		var req Request
		if err := json.Unmarshal(stdin, &req); err != nil {
			return Result{Code: 1, Stdout: encodeLine(Response{Error: &Error{Code: "unavailable", Message: err.Error()}})}
		}
		result := req.Config
		if len(result) == 0 {
			result = json.RawMessage("null")
		}
		return okOut(Response{
			ProtocolVersion: ProtocolVersion,
			Result:          result,
		})
	case "echo_request":
		// Echoes the raw stdin request back as the result, so a test can
		// assert the wire request is byte-for-byte what the umbrella
		// sends (bead pg2-91y12: argv changes must not touch stdin).
		return okOut(Response{
			ProtocolVersion: ProtocolVersion,
			Result:          json.RawMessage(stdin),
		})
	case "stderr_only":
		return Result{Code: 2, Stderr: "boom\n"}
	case "hang":
		// Blocks for far longer than any test-scoped deadline, writing
		// nothing to stdout/stderr — simulates a hung gh/bd/backend the
		// DefaultExecTimeout+WaitDelay pairing exists to bound
		// [bead pg2-332z8 #13]. The context ending ends it, as the real
		// runner's kill does (Code -1, Signaled); the bounded fallback means
		// a call that never carries a deadline still returns.
		select {
		case <-ctx.Done():
			return Result{Code: -1, Signaled: true}
		case <-time.After(30 * time.Second):
			return Result{}
		}
	case "big_stderr":
		// Writes far more than MaxFoldedOutputBytes to stderr and nothing
		// to stdout, exercising runInvoke's stderr-fold path
		// [bead pg2-332z8 #26].
		return Result{Code: 1, Stderr: strings.Repeat("e", MaxFoldedOutputBytes*3) + "\n"}
	case "big_nonjson_stdout":
		// Writes far more than MaxFoldedOutputBytes of non-JSON text to
		// stdout, exercising Invoke's invalid-JSON stdout-fold path
		// [bead pg2-332z8 #26].
		return Result{Stdout: strings.Repeat("o", MaxFoldedOutputBytes*3) + "\n"}
	case "empty_envelope":
		// Neither result nor error is set — a protocol violation, not a
		// success [bug A7].
		return okOut(Response{
			ProtocolVersion: ProtocolVersion,
			SchemaVersion:   1,
		})
	case "explicit_null_result":
		// A deliberate no-payload success: "result" is present but null,
		// which decodes to a non-empty RawMessage ("null") — distinct
		// from the omitted-field case above and MUST still be treated as
		// success [bug A7].
		return okOut(Response{
			ProtocolVersion: ProtocolVersion,
			SchemaVersion:   1,
			Result:          json.RawMessage("null"),
		})
	default:
		return Result{Code: 99, Stderr: "unknown GO_HELPER_BEHAVIOR\n"}
	}
}

// helperRunner returns a Runner that answers every Call with behavior.
func helperRunner(behavior string) Runner {
	return func(ctx context.Context, c Call) Result {
		return helperRun(ctx, behavior, c)
	}
}

func withFactory(t *testing.T, behavior string) {
	t.Helper()
	orig := execRunner
	execRunner = helperRunner(behavior)
	t.Cleanup(func() { execRunner = orig })
}

// --------------------------------------------------------------------
// Invoke tests
// --------------------------------------------------------------------

func TestInvoke_Success(t *testing.T) {
	withFactory(t, "ok_auth")
	resp, err := Invoke(context.Background(), "fake-binary", OpAuthStatus, nil, nil)
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	var status AuthStatus
	if err := Decode(resp.Result, &status); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if status.State != AuthOK {
		t.Fatalf("state = %q", status.State)
	}
}

func TestInvoke_ErrorWrapsSentinel(t *testing.T) {
	withFactory(t, "not_found")
	_, err := Invoke(context.Background(), "fake-binary", "get_pr", map[string]any{"number": 1}, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected errors.Is(err, ErrNotFound), got %v", err)
	}
}

func TestInvoke_UnknownOpWrapsSentinel(t *testing.T) {
	withFactory(t, "unknown_op")
	_, err := Invoke(context.Background(), "fake-binary", OpAuthStatus, nil, nil)
	if err == nil || !errors.Is(err, ErrUnknownOp) {
		t.Fatalf("expected errors.Is(err, ErrUnknownOp), got %v", err)
	}
}

func TestInvoke_NonJSONOutput(t *testing.T) {
	withFactory(t, "non_json")
	_, err := Invoke(context.Background(), "fake-binary", "x", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "invalid JSON") {
		t.Fatalf("expected invalid-JSON error, got %v", err)
	}
}

func TestInvoke_EmptyBinary(t *testing.T) {
	_, err := Invoke(context.Background(), "", "x", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "empty backend binary") {
		t.Fatalf("expected empty-binary error, got %v", err)
	}
}

func TestInvoke_BinaryNotFound(t *testing.T) {
	orig := execRunner
	execRunner = ExecRunner
	t.Cleanup(func() { execRunner = orig })

	_, err := Invoke(context.Background(), "definitely-does-not-exist-xyz-987", "x", nil, nil)
	if err == nil {
		t.Fatal("expected error for missing binary")
	}
	if !strings.Contains(err.Error(), "definitely-does-not-exist-xyz-987") {
		t.Fatalf("error should mention binary: %v", err)
	}
}

func TestInvoke_ArgsRoundTrip(t *testing.T) {
	withFactory(t, "echo_op")
	resp, err := Invoke(context.Background(), "fake-binary", "get_pr", map[string]any{"number": 7}, nil)
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	var op string
	if err := Decode(resp.Result, &op); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if op != "get_pr" {
		t.Fatalf("op = %q", op)
	}
}

// TestInvoke_ConfigRoundTrip proves the config parameter (bead
// pg2-2j5ac.28.1) actually reaches the wire request's own Config member,
// verbatim.
func TestInvoke_ConfigRoundTrip(t *testing.T) {
	withFactory(t, "echo_config")
	config := json.RawMessage(`{"queries":{"team":"is:open"}}`)
	resp, err := Invoke(context.Background(), "fake-binary", "list", nil, config)
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	var got struct {
		Queries map[string]string `json:"queries"`
	}
	if err := Decode(resp.Result, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Queries["team"] != "is:open" {
		t.Fatalf("echoed config = %+v, want the config passed to Invoke", got)
	}
}

// TestInvoke_NilConfig_OmittedFromWire proves a nil config produces no
// config member on the wire at all (envelope.go's own omitempty
// guarantee), rather than a present-but-null one.
func TestInvoke_NilConfig_OmittedFromWire(t *testing.T) {
	withFactory(t, "echo_config")
	resp, err := Invoke(context.Background(), "fake-binary", "show", nil, nil)
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if string(resp.Result) != "null" {
		t.Fatalf("resp.Result = %s, want the echo_config helper's own null fallback (no config member on the wire)", resp.Result)
	}
}

func TestInvoke_ProtocolVersionMismatch_IsVersionMismatchNotSilentSuccess(t *testing.T) {
	// Before this fix, Invoke decoded a well-formed, otherwise-successful
	// response whose protocolVersion disagreed with this process's own
	// ProtocolVersion constant as a plain success — a genuine
	// umbrella/backend version skew (the ordinary case, since the
	// umbrella and each backend are separate, independently-deployed nix
	// derivations (INV-VER-1)) passed silently instead of surfacing
	// version_mismatch.
	withFactory(t, "protocol_mismatch")
	resp, err := Invoke(context.Background(), "fake-binary", "get_pr", nil, nil)
	if err == nil {
		t.Fatalf("expected version_mismatch error, got success resp=%+v", resp)
	}
	if resp != nil {
		t.Fatalf("expected nil resp on version mismatch, got %+v", resp)
	}
	if !errors.Is(err, ErrVersionMismatch) {
		t.Fatalf("expected errors.Is(err, ErrVersionMismatch), got %v", err)
	}
}

func TestInvoke_EmptyEnvelope_IsProtocolViolationNotSuccess(t *testing.T) {
	// A response with neither result nor error currently returned success
	// — this must be a protocol-violation error instead [bug A7].
	withFactory(t, "empty_envelope")
	resp, err := Invoke(context.Background(), "fake-binary", "get_pr", nil, nil)
	if err == nil {
		t.Fatalf("expected error, got resp=%+v", resp)
	}
	if resp != nil {
		t.Fatalf("expected nil resp on protocol violation, got %+v", resp)
	}
	if !strings.Contains(err.Error(), "protocol violation") {
		t.Fatalf("expected protocol violation error, got %v", err)
	}
}

func TestInvoke_ExplicitNullResult_IsSuccessNotViolation(t *testing.T) {
	// A deliberate no-payload success ("result":null, present but null)
	// MUST remain success, distinct from the omitted-field violation above
	// [bug A7].
	withFactory(t, "explicit_null_result")
	resp, err := Invoke(context.Background(), "fake-binary", "rerun_failed", nil, nil)
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil resp")
	}
}

// --------------------------------------------------------------------
// InvokeCapabilities tests
// --------------------------------------------------------------------

func TestInvokeCapabilities_Success(t *testing.T) {
	withFactory(t, "capabilities")
	resp, err := InvokeCapabilities(context.Background(), "fake-binary")
	if err != nil {
		t.Fatalf("InvokeCapabilities: %v", err)
	}
	if resp.SchemaVersions["pr"] != 1 {
		t.Fatalf("schemaVersions = %+v", resp.SchemaVersions)
	}
	if len(resp.Ops) != 3 {
		t.Fatalf("ops = %+v", resp.Ops)
	}
}

func TestInvokeCapabilities_NonJSON(t *testing.T) {
	withFactory(t, "non_json")
	_, err := InvokeCapabilities(context.Background(), "fake-binary")
	if err == nil || !strings.Contains(err.Error(), "invalid capabilities response") {
		t.Fatalf("expected invalid capabilities response error, got %v", err)
	}
}

func TestInvokeCapabilities_ErrorEnvelope_NotSilentSuccess(t *testing.T) {
	// Before the fix, an error envelope decoded straight into
	// CapabilitiesResponse (which has no error field) as a zero-value
	// success, silently passing the one designed health gate [bug A6].
	withFactory(t, "not_found")
	resp, err := InvokeCapabilities(context.Background(), "fake-binary")
	if err == nil {
		t.Fatalf("expected error, got resp=%+v", resp)
	}
	if resp != nil {
		t.Fatalf("expected nil resp on error envelope, got %+v", resp)
	}
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected errors.Is(err, ErrNotFound), got %v", err)
	}
}

func TestInvokeCapabilities_ProtocolVersionMismatch_IsVersionMismatchNotSilentSuccess(t *testing.T) {
	// Same skew as TestInvoke_ProtocolVersionMismatch_..., surfaced
	// through the bespoke CapabilitiesResponse shape — capabilities is
	// not exempt from the check just because its envelope is bespoke.
	withFactory(t, "capabilities_protocol_mismatch")
	resp, err := InvokeCapabilities(context.Background(), "fake-binary")
	if err == nil {
		t.Fatalf("expected version_mismatch error, got success resp=%+v", resp)
	}
	if resp != nil {
		t.Fatalf("expected nil resp on version mismatch, got %+v", resp)
	}
	if !errors.Is(err, ErrVersionMismatch) {
		t.Fatalf("expected errors.Is(err, ErrVersionMismatch), got %v", err)
	}
}

func TestInvokeCapabilities_UnknownOp_WrapsSentinel(t *testing.T) {
	// config_validate.go excuses ErrUnknownOp from a capabilities call (a
	// backend that doesn't implement the op at all isn't itself a
	// failure) — that excusal only works once InvokeCapabilities actually
	// surfaces the wire error instead of swallowing it [bug A6].
	withFactory(t, "unknown_op")
	_, err := InvokeCapabilities(context.Background(), "fake-binary")
	if err == nil || !errors.Is(err, ErrUnknownOp) {
		t.Fatalf("expected errors.Is(err, ErrUnknownOp), got %v", err)
	}
}

// --------------------------------------------------------------------
// bead pg2-332z8: timeout/WaitDelay and output-cap regressions
// --------------------------------------------------------------------

// TestInvoke_HungChild_KilledAtDeadlineNotHungForever is the core
// regression proof for bead #13's umbrella-side half: before runInvoke
// applied any context deadline of its own, a backend binary that never
// exits (a hung gh, a bd blocked on a wedged dolt server) would hang this
// call for as long as the CALLER's own ctx stayed alive — and in
// production, cmd/pg-connector's root command never supplies one at all
// (cobra's root.Execute() with no ExecuteContext leaves cmd.Context() at a
// bare context.Background(), so nothing upstream of runInvoke ever
// imposed a deadline). This test therefore deliberately calls Invoke with
// context.Background() too — mirroring that real caller exactly — and
// relies SOLELY on runInvoke's own internal execTimeout wrapping to bound
// it; a caller-supplied deadline is not involved, so this fails to
// discriminate the fix only if the internal wrapping itself is missing.
// execTimeout is overridden to a short value (mirroring exec.go's own
// execRunner swap pattern) so the test stays fast rather than waiting
// out the real 30s DefaultExecTimeout.
func TestInvoke_HungChild_KilledAtDeadlineNotHungForever(t *testing.T) {
	withFactory(t, "hang")
	origTimeout := execTimeout
	execTimeout = 150 * time.Millisecond
	t.Cleanup(func() { execTimeout = origTimeout })

	start := time.Now()
	_, err := Invoke(context.Background(), "fake-binary", "x", nil, nil)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected an error from a killed hung child, got nil")
	}
	// Generous relative to the 150ms execTimeout override, but far below
	// the helper's own 30s self-terminating sleep — proves the child was
	// actually killed by runInvoke's own deadline rather than merely
	// outliving it.
	if elapsed > 5*time.Second {
		t.Fatalf("Invoke took %v to return with execTimeout=150ms; hung child was not killed promptly", elapsed)
	}
}

// TestInvoke_HungChild_ErrorNamesOpArgsAndElapsed is the bead pg2-5dyz2
// regression proof for the umbrella-side half: a backend killed by the
// umbrella's deadline used to surface as a bare "scriptout: <bin>: signal:
// killed". The error must now name the op, the elapsed time, the limit and
// the request's args, and keep "signal: killed" as the wrapped cause.
func TestInvoke_HungChild_ErrorNamesOpArgsAndElapsed(t *testing.T) {
	withFactory(t, "hang")
	origTimeout := listExecTimeout
	listExecTimeout = 150 * time.Millisecond
	t.Cleanup(func() { listExecTimeout = origTimeout })

	_, err := Invoke(context.Background(), "fake-binary", "list", map[string]any{"query": "is:open"}, nil)
	if err == nil {
		t.Fatal("expected an error from a killed hung child, got nil")
	}
	msg := err.Error()
	for _, want := range []string{
		"fake-binary",
		"op=list",
		"elapsed=",
		"umbrella deadline 150ms exceeded",
		`args={"query":"is:open"}`,
		"signal: killed",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not contain %q", msg, want)
		}
	}
}

// TestInvoke_ChildFailure_NamesOpWithoutArgs proves a failure that is not a
// deadline still names the op and elapsed time, but does not echo the args
// (they are only folded in when the deadline kill needs diagnosing).
func TestInvoke_ChildFailure_NamesOpWithoutArgs(t *testing.T) {
	withFactory(t, "big_stderr")
	_, err := Invoke(context.Background(), "fake-binary", "comment", map[string]any{"body": "SECRET-BODY"}, nil)
	if err == nil {
		t.Fatal("expected error from the big_stderr helper")
	}
	msg := err.Error()
	if !strings.Contains(msg, "op=comment elapsed=") {
		t.Errorf("error does not name op and elapsed: %.200q", msg)
	}
	if strings.Contains(msg, "SECRET-BODY") || strings.Contains(msg, "args=") {
		t.Errorf("non-deadline failure echoed the request args: %.200q", msg)
	}
}

// TestInvoke_StderrFoldIsCapped is the umbrella-side regression proof for
// bead #26: before TruncateForFold existed, runInvoke's stderr-fold branch
// (used when the backend binary produced no stdout at all) interpolated
// the ENTIRE captured stderr into the returned error with no bound.
func TestInvoke_StderrFoldIsCapped(t *testing.T) {
	withFactory(t, "big_stderr")
	_, err := Invoke(context.Background(), "fake-binary", "x", nil, nil)
	if err == nil {
		t.Fatal("expected error from the big_stderr helper")
	}
	if got := len(err.Error()); got > MaxFoldedOutputBytes+256 {
		t.Fatalf("error message is %d bytes; stderr fold was not capped (helper wrote %d bytes of stderr)",
			got, MaxFoldedOutputBytes*3)
	}
	if !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("expected a truncation marker in the error, got a %d-byte message", len(err.Error()))
	}
}

// TestInvoke_NonJSONStdoutFoldIsCapped is the umbrella-side regression
// proof for bead #26's other exec.go fold site: Invoke's invalid-JSON
// branch, which used to interpolate the full captured stdout via
// "stdout=%q" with no bound.
func TestInvoke_NonJSONStdoutFoldIsCapped(t *testing.T) {
	withFactory(t, "big_nonjson_stdout")
	_, err := Invoke(context.Background(), "fake-binary", "x", nil, nil)
	if err == nil {
		t.Fatal("expected invalid-JSON error from the big_nonjson_stdout helper")
	}
	if got := len(err.Error()); got > MaxFoldedOutputBytes+256 {
		t.Fatalf("error message is %d bytes; stdout fold was not capped", got)
	}
	if !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("expected a truncation marker in the error, got a %d-byte message", len(err.Error()))
	}
}

// TestInvoke_ListHasItsOwnExecBudget pins bead pg2-4ae4q on the umbrella side:
// a hung "list" backend is killed at listExecTimeout, any other op at
// execTimeout, and each error names the limit that actually applied.
func TestInvoke_ListHasItsOwnExecBudget(t *testing.T) {
	withFactory(t, "hang")
	origExec, origList := execTimeout, listExecTimeout
	execTimeout = 50 * time.Millisecond
	listExecTimeout = 300 * time.Millisecond
	t.Cleanup(func() { execTimeout, listExecTimeout = origExec, origList })

	start := time.Now()
	_, err := Invoke(context.Background(), "fake-binary", "list", nil, nil)
	listElapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected a hung list backend to be killed")
	}
	if listElapsed < 250*time.Millisecond {
		t.Errorf("list was killed after %v, before its %v budget", listElapsed, listExecTimeout)
	}
	if !strings.Contains(err.Error(), "umbrella deadline 300ms exceeded") {
		t.Errorf("list error %q does not name the 300ms list limit", err)
	}

	// No wall-clock UPPER bound here: under host load (and -race) a kill can
	// land hundreds of ms late, which made an elapsed ceiling flaky. The error
	// text names the budget that fired, which is the proof of which one applied.
	_, err = Invoke(context.Background(), "fake-binary", "commit", nil, nil)
	if err == nil {
		t.Fatal("expected a hung non-list backend to be killed")
	}
	if !strings.Contains(err.Error(), "umbrella deadline 50ms exceeded") {
		t.Errorf("non-list error %q does not name the 50ms limit", err)
	}
}

// TestInvoke_ListActivityHasItsOwnExecBudget pins bead pg2-m4z5k on the
// umbrella side: a hung "list_activity" backend is killed at
// listActivityExecTimeout, a hung "list" at listExecTimeout, any other op at
// execTimeout, and each error names the limit that actually applied.
func TestInvoke_ListActivityHasItsOwnExecBudget(t *testing.T) {
	withFactory(t, "hang")
	origExec, origList, origActivity := execTimeout, listExecTimeout, listActivityExecTimeout
	execTimeout = 50 * time.Millisecond
	listExecTimeout = 50 * time.Millisecond
	listActivityExecTimeout = 300 * time.Millisecond
	t.Cleanup(func() {
		execTimeout, listExecTimeout, listActivityExecTimeout = origExec, origList, origActivity
	})

	start := time.Now()
	_, err := Invoke(context.Background(), "fake-binary", "list_activity", nil, nil)
	activityElapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected a hung list_activity backend to be killed")
	}
	if activityElapsed < 250*time.Millisecond {
		t.Errorf("list_activity was killed after %v, before its %v budget", activityElapsed, listActivityExecTimeout)
	}
	if !strings.Contains(err.Error(), "umbrella deadline 300ms exceeded") {
		t.Errorf("list_activity error %q does not name the 300ms list_activity limit", err)
	}

	for _, op := range []string{"list", "commit"} {
		// No wall-clock upper bound (flaky under load); the error names the budget.
		_, err = Invoke(context.Background(), "fake-binary", op, nil, nil)
		if err == nil {
			t.Fatalf("expected a hung %q backend to be killed", op)
		}
		if !strings.Contains(err.Error(), "umbrella deadline 50ms exceeded") {
			t.Errorf("op %q error %q does not name the 50ms limit", op, err)
		}
	}
}
