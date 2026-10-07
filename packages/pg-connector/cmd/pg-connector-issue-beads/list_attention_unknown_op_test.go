package main

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// Entity attention is evaluated by pg-desk, so this backend no longer answers
// list_attention. A stale registration of it in attention.sources must degrade
// to "not applicable" (the wire-level unknown_op) rather than fail the feed,
// and capabilities must not advertise the op or the attention schema.
func TestListAttention_AnswersUnknownOp(t *testing.T) {
	table := newDispatchTable(newTestBackend())

	var out bytes.Buffer
	code := scriptout.ServeOne(table, strings.NewReader(`{"op":"list_attention","args":{}}`), &out)
	if want := scriptout.ExitCodeForError(scriptout.ErrUnknownOp); code != want {
		t.Fatalf("exit code = %d, want %d (unknown_op)", code, want)
	}
	var resp scriptout.Response
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("decode %q: %v", out.String(), err)
	}
	if resp.Error == nil || resp.Error.Code != "unknown_op" {
		t.Fatalf("response = %+v, want error code unknown_op", resp)
	}
}

func TestCapabilities_DoNotAdvertiseAttention(t *testing.T) {
	table := newDispatchTable(newTestBackend())
	if _, ok := table["list_attention"]; ok {
		t.Fatal("dispatch table registers list_attention; want it absent")
	}
	resp := retiredAttentionCapabilities(t, table)
	if slices.Contains(resp.Ops, "list_attention") {
		t.Errorf("capabilities.ops = %v, want no list_attention", resp.Ops)
	}
	if _, ok := resp.SchemaVersions["attention"]; ok {
		t.Errorf("capabilities.schemaVersions = %v, want no attention entry", resp.SchemaVersions)
	}
}

func retiredAttentionCapabilities(t *testing.T, table scriptout.DispatchTable) scriptout.CapabilitiesResponse {
	t.Helper()
	result, err := table[scriptout.OpCapabilities].Handle(context.Background(), nil)
	if err != nil {
		t.Fatalf("capabilities Handle: %v", err)
	}
	resp, ok := result.(scriptout.CapabilitiesResponse)
	if !ok {
		t.Fatalf("result type = %T, want scriptout.CapabilitiesResponse", result)
	}
	return resp
}
