package scriptout

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// --------------------------------------------------------------------
// Test-helper-process pattern (mirrors pg-pr's pkg/plugin/scriptout).
// --------------------------------------------------------------------

func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	defer os.Exit(0)
	helperMain()
}

func helperMain() {
	behavior := os.Getenv("GO_HELPER_BEHAVIOR")
	stdin, _ := io.ReadAll(os.Stdin)
	_ = stdin

	switch behavior {
	case "ok_auth":
		_ = json.NewEncoder(os.Stdout).Encode(Response{
			ProtocolVersion: ProtocolVersion,
			SchemaVersion:   1,
			Result:          json.RawMessage(`{"state":"OK"}`),
		})
		os.Exit(0)
	case "not_found":
		_ = json.NewEncoder(os.Stdout).Encode(Response{
			ProtocolVersion: ProtocolVersion,
			SchemaVersion:   1,
			Error:           &Error{Code: "not_found", Message: "no such pr"},
		})
		os.Exit(1)
	case "unknown_op":
		_ = json.NewEncoder(os.Stdout).Encode(Response{
			ProtocolVersion: ProtocolVersion,
			Error:           &Error{Code: "unknown_op", Message: `unknown op "auth_status"`},
		})
		os.Exit(1)
	case "non_json":
		_, _ = fmt.Fprintln(os.Stdout, "this is not json")
		os.Exit(0)
	case "capabilities":
		_ = json.NewEncoder(os.Stdout).Encode(CapabilitiesResponse{
			ProtocolVersion: ProtocolVersion,
			SchemaVersions:  map[string]int{"pr": 1},
			Ops:             []string{"get_pr", OpAuthStatus, OpCapabilities},
		})
		os.Exit(0)
	case "protocol_mismatch":
		// A well-formed, otherwise-successful envelope from a binary built
		// against a different (here: newer) wire-envelope protocolVersion
		// than this process expects — the ordinary-op half of the
		// umbrella/backend skew this docket exists to catch.
		_ = json.NewEncoder(os.Stdout).Encode(Response{
			ProtocolVersion: ProtocolVersion + 1,
			SchemaVersion:   1,
			Result:          json.RawMessage(`{"state":"OK"}`),
		})
		os.Exit(0)
	case "capabilities_protocol_mismatch":
		// Same skew, surfaced through the bespoke capabilities shape
		// instead of the ordinary Response envelope.
		_ = json.NewEncoder(os.Stdout).Encode(CapabilitiesResponse{
			ProtocolVersion: ProtocolVersion + 1,
			SchemaVersions:  map[string]int{"pr": 1},
			Ops:             []string{"get_pr", OpAuthStatus, OpCapabilities},
		})
		os.Exit(0)
	case "echo_op":
		var req Request
		if err := json.Unmarshal(stdin, &req); err != nil {
			_ = json.NewEncoder(os.Stdout).Encode(Response{Error: &Error{Code: "unavailable", Message: err.Error()}})
			os.Exit(1)
		}
		_ = json.NewEncoder(os.Stdout).Encode(Response{
			ProtocolVersion: ProtocolVersion,
			Result:          json.RawMessage(`"` + req.Op + `"`),
		})
		os.Exit(0)
	case "stderr_only":
		fmt.Fprintln(os.Stderr, "boom")
		os.Exit(2)
	case "hang":
		// Blocks for far longer than any test-scoped deadline, writing
		// nothing to stdout/stderr — simulates a hung gh/bd/backend the
		// DefaultExecTimeout+WaitDelay pairing exists to bound
		// [bead pg2-332z8 #13]. Deliberately a bounded sleep (not a true
		// `select{}` infinite block): if the parent's kill mechanism ever
		// regresses, this process still self-terminates instead of
		// leaking an unkillable orphan.
		time.Sleep(30 * time.Second)
		os.Exit(0)
	case "big_stderr":
		// Writes far more than MaxFoldedOutputBytes to stderr and nothing
		// to stdout, exercising runInvoke's stderr-fold path
		// [bead pg2-332z8 #26].
		fmt.Fprint(os.Stderr, strings.Repeat("e", MaxFoldedOutputBytes*3))
		os.Exit(1)
	case "big_nonjson_stdout":
		// Writes far more than MaxFoldedOutputBytes of non-JSON text to
		// stdout, exercising Invoke's invalid-JSON stdout-fold path
		// [bead pg2-332z8 #26].
		fmt.Fprint(os.Stdout, strings.Repeat("o", MaxFoldedOutputBytes*3))
		os.Exit(0)
	case "empty_envelope":
		// Neither result nor error is set — a protocol violation, not a
		// success [bug A7].
		_ = json.NewEncoder(os.Stdout).Encode(Response{
			ProtocolVersion: ProtocolVersion,
			SchemaVersion:   1,
		})
		os.Exit(0)
	case "explicit_null_result":
		// A deliberate no-payload success: "result" is present but null,
		// which decodes to a non-empty RawMessage ("null") — distinct
		// from the omitted-field case above and MUST still be treated as
		// success [bug A7].
		_ = json.NewEncoder(os.Stdout).Encode(Response{
			ProtocolVersion: ProtocolVersion,
			SchemaVersion:   1,
			Result:          json.RawMessage("null"),
		})
		os.Exit(0)
	default:
		fmt.Fprintln(os.Stderr, "unknown GO_HELPER_BEHAVIOR")
		os.Exit(99)
	}
}

func helperCmdFactory(behavior string) func(ctx context.Context, name string, args ...string) *exec.Cmd {
	return func(ctx context.Context, name string, args ...string) *exec.Cmd {
		cs := append([]string{"-test.run=^TestHelperProcess$", "--", name}, args...)
		cmd := exec.CommandContext(ctx, os.Args[0], cs...)
		cmd.Env = append(
			os.Environ(),
			"GO_WANT_HELPER_PROCESS=1",
			"GO_HELPER_BEHAVIOR="+behavior,
		)
		return cmd
	}
}

func withFactory(t *testing.T, behavior string) {
	t.Helper()
	orig := execCmdFactory
	execCmdFactory = helperCmdFactory(behavior)
	t.Cleanup(func() { execCmdFactory = orig })
}

// --------------------------------------------------------------------
// Invoke tests
// --------------------------------------------------------------------

func TestInvoke_Success(t *testing.T) {
	withFactory(t, "ok_auth")
	resp, err := Invoke(context.Background(), "fake-binary", OpAuthStatus, nil)
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
	_, err := Invoke(context.Background(), "fake-binary", "get_pr", map[string]any{"number": 1})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected errors.Is(err, ErrNotFound), got %v", err)
	}
}

func TestInvoke_UnknownOpWrapsSentinel(t *testing.T) {
	withFactory(t, "unknown_op")
	_, err := Invoke(context.Background(), "fake-binary", OpAuthStatus, nil)
	if err == nil || !errors.Is(err, ErrUnknownOp) {
		t.Fatalf("expected errors.Is(err, ErrUnknownOp), got %v", err)
	}
}

func TestInvoke_NonJSONOutput(t *testing.T) {
	withFactory(t, "non_json")
	_, err := Invoke(context.Background(), "fake-binary", "x", nil)
	if err == nil || !strings.Contains(err.Error(), "invalid JSON") {
		t.Fatalf("expected invalid-JSON error, got %v", err)
	}
}

func TestInvoke_EmptyBinary(t *testing.T) {
	_, err := Invoke(context.Background(), "", "x", nil)
	if err == nil || !strings.Contains(err.Error(), "empty backend binary") {
		t.Fatalf("expected empty-binary error, got %v", err)
	}
}

func TestInvoke_BinaryNotFound(t *testing.T) {
	orig := execCmdFactory
	execCmdFactory = exec.CommandContext
	t.Cleanup(func() { execCmdFactory = orig })

	_, err := Invoke(context.Background(), "definitely-does-not-exist-xyz-987", "x", nil)
	if err == nil {
		t.Fatal("expected error for missing binary")
	}
	if !strings.Contains(err.Error(), "definitely-does-not-exist-xyz-987") {
		t.Fatalf("error should mention binary: %v", err)
	}
}

func TestInvoke_ArgsRoundTrip(t *testing.T) {
	withFactory(t, "echo_op")
	resp, err := Invoke(context.Background(), "fake-binary", "get_pr", map[string]any{"number": 7})
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

func TestInvoke_ProtocolVersionMismatch_IsVersionMismatchNotSilentSuccess(t *testing.T) {
	// Before this fix, Invoke decoded a well-formed, otherwise-successful
	// response whose protocolVersion disagreed with this process's own
	// ProtocolVersion constant as a plain success — a genuine
	// umbrella/backend version skew (the ordinary case, since the
	// umbrella and each backend are separate, independently-deployed nix
	// derivations (INV-VER-1)) passed silently instead of surfacing
	// version_mismatch.
	withFactory(t, "protocol_mismatch")
	resp, err := Invoke(context.Background(), "fake-binary", "get_pr", nil)
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
	resp, err := Invoke(context.Background(), "fake-binary", "get_pr", nil)
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
	resp, err := Invoke(context.Background(), "fake-binary", "rerun_failed", nil)
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
// execCmdFactory swap pattern) so the test stays fast rather than waiting
// out the real 30s DefaultExecTimeout.
func TestInvoke_HungChild_KilledAtDeadlineNotHungForever(t *testing.T) {
	withFactory(t, "hang")
	origTimeout := execTimeout
	execTimeout = 150 * time.Millisecond
	t.Cleanup(func() { execTimeout = origTimeout })

	start := time.Now()
	_, err := Invoke(context.Background(), "fake-binary", "x", nil)
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

// TestInvoke_StderrFoldIsCapped is the umbrella-side regression proof for
// bead #26: before TruncateForFold existed, runInvoke's stderr-fold branch
// (used when the backend binary produced no stdout at all) interpolated
// the ENTIRE captured stderr into the returned error with no bound.
func TestInvoke_StderrFoldIsCapped(t *testing.T) {
	withFactory(t, "big_stderr")
	_, err := Invoke(context.Background(), "fake-binary", "x", nil)
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
	_, err := Invoke(context.Background(), "fake-binary", "x", nil)
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
