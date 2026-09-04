package scriptout

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
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
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
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
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	errObj, ok := resp["error"].(map[string]any)
	if !ok || errObj["code"] != "unknown_op" {
		t.Fatalf("expected unknown_op error, got %v", resp)
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

func TestServeLoop_MalformedRequest(t *testing.T) {
	table := DispatchTable{}
	code, resp := runServeLoop(t, table, `not json`)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if _, ok := resp["error"]; !ok {
		t.Fatalf("expected error object, got %v", resp)
	}
}
