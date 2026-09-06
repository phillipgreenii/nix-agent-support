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
