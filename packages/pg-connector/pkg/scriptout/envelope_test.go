package scriptout

import (
	"encoding/json"
	"testing"
)

func TestRequestRoundTrip(t *testing.T) {
	req := Request{Op: "get_pr", Args: json.RawMessage(`{"repo":"o/r","number":7}`)}
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got Request
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Op != "get_pr" {
		t.Fatalf("op = %q", got.Op)
	}
	var args struct {
		Repo   string `json:"repo"`
		Number int    `json:"number"`
	}
	if err := json.Unmarshal(got.Args, &args); err != nil {
		t.Fatalf("decode args: %v", err)
	}
	if args.Repo != "o/r" || args.Number != 7 {
		t.Fatalf("args = %+v", args)
	}
}

func TestResponseRoundTrip_Success(t *testing.T) {
	resp := Response{
		ProtocolVersion: ProtocolVersion,
		SchemaVersion:   3,
		Result:          json.RawMessage(`{"state":"OK"}`),
	}
	b, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got Response
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ProtocolVersion != ProtocolVersion || got.SchemaVersion != 3 {
		t.Fatalf("versions: %+v", got)
	}
	if got.Error != nil {
		t.Fatalf("expected no error, got %+v", got.Error)
	}
	var status AuthStatus
	if err := Decode(got.Result, &status); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if status.State != AuthOK {
		t.Fatalf("state = %q", status.State)
	}
}

func TestResponseRoundTrip_Error(t *testing.T) {
	resp := Response{
		ProtocolVersion: ProtocolVersion,
		SchemaVersion:   3,
		Error:           &Error{Code: "not_found", Message: "no such pr"},
	}
	b, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got Response
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Result != nil {
		t.Fatalf("expected no result, got %q", got.Result)
	}
	if got.Error == nil || got.Error.Code != "not_found" || got.Error.Message != "no such pr" {
		t.Fatalf("error = %+v", got.Error)
	}
}

func TestCapabilitiesResponse_BespokeShape(t *testing.T) {
	resp := CapabilitiesResponse{
		ProtocolVersion: ProtocolVersion,
		SchemaVersions:  map[string]int{"pr": 1},
		Ops:             []string{"get_pr", OpAuthStatus},
		Vocabulary:      map[string]any{"state": []string{"open", "closed"}},
	}
	b, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// The capabilities response is a flat top-level object — no "result" or
	// "error" wrapper key at all.
	var generic map[string]any
	if err := json.Unmarshal(b, &generic); err != nil {
		t.Fatalf("unmarshal generic: %v", err)
	}
	if _, ok := generic["result"]; ok {
		t.Fatalf("capabilities response must not carry a result key: %s", b)
	}
	if _, ok := generic["error"]; ok {
		t.Fatalf("capabilities response must not carry an error key: %s", b)
	}
	if _, ok := generic["schemaVersions"]; !ok {
		t.Fatalf("capabilities response must carry schemaVersions: %s", b)
	}

	var got CapabilitiesResponse
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.SchemaVersions["pr"] != 1 {
		t.Fatalf("schemaVersions = %+v", got.SchemaVersions)
	}
	if len(got.Ops) != 2 {
		t.Fatalf("ops = %+v", got.Ops)
	}
}

func TestDecode_NullAndEmpty(t *testing.T) {
	var v struct{ X int }
	if err := Decode(nil, &v); err != nil {
		t.Fatalf("nil: %v", err)
	}
	if err := Decode(json.RawMessage("null"), &v); err != nil {
		t.Fatalf("null: %v", err)
	}
	if err := Decode(json.RawMessage(""), &v); err != nil {
		t.Fatalf("empty: %v", err)
	}
}
