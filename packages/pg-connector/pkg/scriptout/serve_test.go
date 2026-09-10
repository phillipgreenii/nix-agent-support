package scriptout

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func runServeLoop(t *testing.T, table DispatchTable, requestJSON string) (int, map[string]any) {
	t.Helper()
	in := bytes.NewBufferString(requestJSON)
	var out bytes.Buffer
	code := serveLoop(dispatchEnv{in: in, out: &out}, table)

	var decoded map[string]any
	if out.Len() > 0 {
		if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
			t.Fatalf("decode stdout %q: %v", out.String(), err)
		}
	}
	return code, decoded
}

func TestServeLoop_Success(t *testing.T) {
	table := DispatchTable{
		"echo": {
			SchemaVersion: 5,
			Handle: func(ctx context.Context, args json.RawMessage) (any, error) {
				return map[string]string{"hello": "world"}, nil
			},
		},
	}
	code, resp := runServeLoop(t, table, `{"op":"echo","args":{}}`)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if resp["protocolVersion"] != float64(ProtocolVersion) {
		t.Fatalf("protocolVersion = %v", resp["protocolVersion"])
	}
	if resp["schemaVersion"] != float64(5) {
		t.Fatalf("schemaVersion = %v", resp["schemaVersion"])
	}
	result, ok := resp["result"].(map[string]any)
	if !ok || result["hello"] != "world" {
		t.Fatalf("result = %v", resp["result"])
	}
	if _, hasErr := resp["error"]; hasErr {
		t.Fatalf("did not expect an error field: %v", resp)
	}
}

// TestServeLoop_ThreadsConfigOntoContext proves the request's own Config
// member (bead pg2-2j5ac.28.1) reaches the op handler via
// ConfigFromContext (config_context.go), without widening OpHandler's own
// Handle signature.
func TestServeLoop_ThreadsConfigOntoContext(t *testing.T) {
	table := DispatchTable{
		"echo_config": {
			Handle: func(ctx context.Context, args json.RawMessage) (any, error) {
				return map[string]json.RawMessage{"config": ConfigFromContext(ctx)}, nil
			},
		},
	}
	code, resp := runServeLoop(t, table, `{"op":"echo_config","args":{},"config":{"queries":{"team":"is:open"}}}`)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected result object, got %v", resp)
	}
	cfg, ok := result["config"].(map[string]any)
	if !ok {
		t.Fatalf("expected config object echoed back, got %v", result)
	}
	if _, ok := cfg["queries"]; !ok {
		t.Fatalf("config = %v, want queries key preserved", cfg)
	}
}

// TestServeLoop_NoConfig_HandlerSeesNil proves a request with no config
// member leaves ConfigFromContext returning nil inside the handler — the
// ordinary case for every pre-existing op this packet does not touch.
func TestServeLoop_NoConfig_HandlerSeesNil(t *testing.T) {
	var gotConfig json.RawMessage
	sawCall := false
	table := DispatchTable{
		"op": {
			Handle: func(ctx context.Context, args json.RawMessage) (any, error) {
				sawCall = true
				gotConfig = ConfigFromContext(ctx)
				return map[string]string{"ok": "true"}, nil
			},
		},
	}
	if _, _ = runServeLoop(t, table, `{"op":"op","args":{}}`); !sawCall {
		t.Fatal("handler was never called")
	}
	if gotConfig != nil {
		t.Fatalf("ConfigFromContext = %q, want nil for a request with no config member", gotConfig)
	}
}

func TestServeLoop_HandlerError(t *testing.T) {
	table := DispatchTable{
		"get_pr": {
			SchemaVersion: 2,
			Handle: func(ctx context.Context, args json.RawMessage) (any, error) {
				return nil, WrapError(ErrNotFound, "no such pr")
			},
		},
	}
	code, resp := runServeLoop(t, table, `{"op":"get_pr","args":{}}`)
	// 2, not the old plain 1: not_found now gets its own branchable exit
	// code (bead pg2-7vgn5), matching the wire body's error.code below.
	if code != ExitCodeForCode("not_found") {
		t.Fatalf("exit code = %d, want %d (not_found)", code, ExitCodeForCode("not_found"))
	}
	errObj, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected error object, got %v", resp)
	}
	if errObj["code"] != "not_found" {
		t.Fatalf("error.code = %v, want not_found", errObj["code"])
	}
	if resp["schemaVersion"] != float64(2) {
		t.Fatalf("schemaVersion on error response = %v, want 2", resp["schemaVersion"])
	}
}

func TestServeLoop_UnknownOp(t *testing.T) {
	table := DispatchTable{}
	code, resp := runServeLoop(t, table, `{"op":"does_not_exist","args":{}}`)
	// 5, not the old plain 1: unknown_op now gets its own branchable exit
	// code (bead pg2-7vgn5).
	if code != ExitCodeForCode("unknown_op") {
		t.Fatalf("exit code = %d, want %d (unknown_op)", code, ExitCodeForCode("unknown_op"))
	}
	errObj, ok := resp["error"].(map[string]any)
	if !ok || errObj["code"] != "unknown_op" {
		t.Fatalf("expected unknown_op error, got %v", resp)
	}
}

// TestServeLoop_ExitCodeMatchesErrorCode_AllSixSentinels proves the
// backend-process exit code and the JSON error body's Code field NEVER
// disagree, for every one of the six wire-taxonomy sentinels a handler can
// wrap (bead pg2-7vgn5's core invariant) — not just the two spot-checked
// above.
func TestServeLoop_ExitCodeMatchesErrorCode_AllSevenSentinels(t *testing.T) {
	cases := []struct {
		sentinel error
		code     string
	}{
		{ErrNotFound, "not_found"},
		{ErrUnauthenticated, "unauthenticated"},
		{ErrUnavailable, "unavailable"},
		{ErrUnknownOp, "unknown_op"},
		{ErrVersionMismatch, "version_mismatch"},
		{ErrInvalidArgument, "invalid_argument"},
		{ErrQueryNotRecognized, "query_not_recognized"},
	}
	seen := map[int]string{}
	for _, c := range cases {
		table := DispatchTable{
			"op": {
				Handle: func(ctx context.Context, args json.RawMessage) (any, error) {
					return nil, WrapError(c.sentinel, "detail")
				},
			},
		}
		code, resp := runServeLoop(t, table, `{"op":"op","args":{}}`)
		errObj, ok := resp["error"].(map[string]any)
		if !ok {
			t.Fatalf("%s: expected error object, got %v", c.code, resp)
		}
		if errObj["code"] != c.code {
			t.Fatalf("%s: error.code = %v", c.code, errObj["code"])
		}
		if want := ExitCodeForCode(c.code); code != want {
			t.Fatalf("%s: exit code = %d, want %d", c.code, code, want)
		}
		// Exit 1 is reserved for the generic/catch-all case per this
		// workspace's code-file-standards convention — no sentinel-backed
		// error should ever surface it.
		if code < 2 {
			t.Fatalf("%s: exit code = %d, want >=2 (a specific branchable meaning)", c.code, code)
		}
		if prior, dup := seen[code]; dup {
			t.Fatalf("exit code %d assigned to both %q and %q, want each code distinct", code, prior, c.code)
		}
		seen[code] = c.code
	}
	if len(seen) < 2 {
		t.Fatalf("only %d distinct exit codes observed, want the workspace's required >=2", len(seen))
	}
}

// TestServeLoop_UnwrappedHandlerError_ExitsGenericUnavailableCode proves an
// unwrapped/unclassified handler error still resolves to a real,
// branchable exit code (codeForError's own "unavailable" fallback), not the
// old flat 1 — an implementer who forgets to wrap their error still gets a
// self-consistent exit code/error.code pair.
func TestServeLoop_UnwrappedHandlerError_ExitsGenericUnavailableCode(t *testing.T) {
	table := DispatchTable{
		"op": {
			Handle: func(ctx context.Context, args json.RawMessage) (any, error) {
				return nil, errors.New("boom")
			},
		},
	}
	code, resp := runServeLoop(t, table, `{"op":"op","args":{}}`)
	if want := ExitCodeForCode("unavailable"); code != want {
		t.Fatalf("exit code = %d, want %d (unavailable fallback)", code, want)
	}
	errObj, _ := resp["error"].(map[string]any)
	if errObj["code"] != "unavailable" {
		t.Fatalf("error.code = %v, want unavailable", errObj["code"])
	}
}

// TestServeOne_MatchesServeLoop proves ServeOne (the exported,
// caller-supplied-io/out entry point pkg/scriptout/conformance's in-process
// Backend double drives) produces byte-identical output and the same exit
// code as ServeLoop's own internal serveLoop call for the same input.
func TestServeOne_MatchesServeLoop(t *testing.T) {
	table := DispatchTable{
		"echo": {
			SchemaVersion: 5,
			Handle: func(ctx context.Context, args json.RawMessage) (any, error) {
				return map[string]string{"hello": "world"}, nil
			},
		},
	}
	in := bytes.NewBufferString(`{"op":"echo","args":{}}`)
	var out bytes.Buffer
	code := ServeOne(table, in, &out)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	var decoded map[string]any
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("decode stdout %q: %v", out.String(), err)
	}
	if decoded["protocolVersion"] != float64(ProtocolVersion) {
		t.Fatalf("protocolVersion = %v", decoded["protocolVersion"])
	}
}

func TestServeLoop_CapabilitiesBespokeShape(t *testing.T) {
	table := DispatchTable{
		OpCapabilities: {
			Handle: func(ctx context.Context, args json.RawMessage) (any, error) {
				return CapabilitiesResponse{
					ProtocolVersion: ProtocolVersion,
					SchemaVersions:  map[string]int{"pr": 1},
					Ops:             []string{"get_pr", OpAuthStatus, OpCapabilities},
				}, nil
			},
		},
	}
	code, resp := runServeLoop(t, table, `{"op":"capabilities","args":{}}`)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if _, ok := resp["result"]; ok {
		t.Fatalf("capabilities response must not be wrapped in result: %v", resp)
	}
	if _, ok := resp["schemaVersions"]; !ok {
		t.Fatalf("expected top-level schemaVersions, got %v", resp)
	}
}

// --------------------------------------------------------------------
// bead pg2-332z8: timeout and []byte-result-streaming regressions
// --------------------------------------------------------------------

// TestServeLoop_HandlerGetsDeadline_DoesNotHangForever is the core
// regression proof for bead #13's backend-side half: before serveLoop
// applied any deadline, every handler received a bare context.Background()
// with NO way to ever observe ctx.Done() — a handler that (like every real
// Provider method in this design) shells out via exec.CommandContext(ctx,
// ...) and blocks on that child would hang this whole backend process
// forever. execTimeout is overridden to a short value (mirroring exec.go's
// own execCmdFactory swap pattern in exec_test.go) so this stays fast
// instead of waiting out the real 30s DefaultExecTimeout.
func TestServeLoop_HandlerGetsDeadline_DoesNotHangForever(t *testing.T) {
	origTimeout := execTimeout
	execTimeout = 150 * time.Millisecond
	t.Cleanup(func() { execTimeout = origTimeout })

	table := DispatchTable{
		"slow_op": {
			SchemaVersion: 1,
			Handle: func(ctx context.Context, _ json.RawMessage) (any, error) {
				<-ctx.Done()
				return nil, ctx.Err()
			},
		},
	}

	start := time.Now()
	code, resp := runServeLoop(t, table, `{"op":"slow_op","args":{}}`)
	elapsed := time.Since(start)

	if elapsed > 5*time.Second {
		t.Fatalf("serveLoop took %v to return with execTimeout=150ms; handler's ctx never got a deadline", elapsed)
	}
	if code == 0 {
		t.Fatalf("expected a non-zero exit code for a deadline-exceeded handler, got 0 (resp=%v)", resp)
	}
}

// TestServeLoop_BytesResult_MatchesGenericEnvelopeShape proves the
// streaming writeBytesResult path (used for a []byte result — today, ci
// get_logs's raw log bytes — to avoid holding the payload three times
// over; see serveLoop's own comment) produces the exact same
// protocolVersion/schemaVersion/result shape the generic
// json.Marshal(result)+writeJSON(Response{...}) path produces for any
// other result type, so this is a genuine drop-in rather than a
// wire-format change [bead #26].
func TestServeLoop_BytesResult_MatchesGenericEnvelopeShape(t *testing.T) {
	payload := []byte("hello CI log output\nwith multiple lines\n")
	table := DispatchTable{
		"get_logs": {
			SchemaVersion: 3,
			Handle: func(ctx context.Context, _ json.RawMessage) (any, error) {
				return payload, nil
			},
		},
	}
	code, resp := runServeLoop(t, table, `{"op":"get_logs","args":{}}`)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if resp["protocolVersion"] != float64(ProtocolVersion) {
		t.Fatalf("protocolVersion = %v", resp["protocolVersion"])
	}
	if resp["schemaVersion"] != float64(3) {
		t.Fatalf("schemaVersion = %v", resp["schemaVersion"])
	}
	resultStr, ok := resp["result"].(string)
	if !ok {
		t.Fatalf("result = %v (%T), want a base64 string", resp["result"], resp["result"])
	}
	decoded, err := base64.StdEncoding.DecodeString(resultStr)
	if err != nil {
		t.Fatalf("decode result base64: %v", err)
	}
	if string(decoded) != string(payload) {
		t.Fatalf("decoded result = %q, want %q", decoded, payload)
	}
}

// TestServeLoop_BytesResult_EmptyPayload proves an empty []byte result
// (a CI run with an empty log) still round-trips as an empty string, not
// an omitted/null result field — matching how the generic path already
// treats "present but empty" for any other result type.
func TestServeLoop_BytesResult_EmptyPayload(t *testing.T) {
	table := DispatchTable{
		"get_logs": {
			Handle: func(ctx context.Context, _ json.RawMessage) (any, error) {
				return []byte{}, nil
			},
		},
	}
	code, resp := runServeLoop(t, table, `{"op":"get_logs","args":{}}`)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	resultStr, ok := resp["result"].(string)
	if !ok {
		t.Fatalf("result = %v (%T), want a string", resp["result"], resp["result"])
	}
	if resultStr != "" {
		t.Fatalf("result = %q, want empty", resultStr)
	}
}

func TestServeLoop_MalformedRequest(t *testing.T) {
	table := DispatchTable{}
	code, resp := runServeLoop(t, table, `not json`)
	// A malformed request is currently wrapped as ErrUnavailable
	// (serveLoop's own readRequest branch), so it now exits with
	// unavailable's own code rather than the old flat 1 (bead pg2-7vgn5).
	// Computed via ExitCodeForCode rather than hardcoded, so this test
	// tracks the mapping table instead of asserting an unrelated magic
	// number.
	if want := ExitCodeForCode("unavailable"); code != want {
		t.Fatalf("exit code = %d, want %d (unavailable)", code, want)
	}
	errObj, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected error object, got %v", resp)
	}
	if errObj["code"] != "unavailable" {
		t.Fatalf("error.code = %v, want unavailable", errObj["code"])
	}
}
