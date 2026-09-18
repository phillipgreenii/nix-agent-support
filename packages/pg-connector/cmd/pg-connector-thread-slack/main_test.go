package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"reflect"
	"testing"

	internal "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-thread-slack/internal"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// fakeRunner is a minimal double for internal.Runner, so this file's
// wiring tests never spawn a real claude subprocess — mirrors
// cmd/pg-connector-issue-jira/main_test.go's identical fakeRunner
// convention.
type fakeRunner struct {
	handle func(prompt string) (string, error)
}

func (f *fakeRunner) Run(_ context.Context, prompt string) (string, error) {
	return f.handle(prompt)
}

func (f *fakeRunner) Binary() string { return "claude" }

func newTestBackend() *internal.Backend {
	return internal.New(&fakeRunner{handle: func(prompt string) (string, error) {
		return `{"result":"{\"found\":true,\"id\":\"1726000000.000100\",\"channel\":\"C1\",\"permalink\":\"https://example.slack.invalid/p1\",\"text\":\"hello\"}","is_error":false}`, nil
	}})
}

// TestNewDispatchTable_CapabilitiesDeclaresThreadSchemaVersionAndNoAuthStatus
// is this bead's own required test asserting this binary's own
// capabilities response — schemaVersions.thread populated, and
// auth_status deliberately absent from Ops since this backend implements
// no AuthChecker (it resolves no credential of its own — "no Slack token
// is provisioned" is this bead's own binding decision).
func TestNewDispatchTable_CapabilitiesDeclaresThreadSchemaVersionAndNoAuthStatus(t *testing.T) {
	table := newDispatchTable(newTestBackend())
	entry, ok := table[scriptout.OpCapabilities]
	if !ok {
		t.Fatal("capabilities entry missing from this binary's own dispatch table")
	}
	result, err := entry.Handle(context.Background(), nil)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	resp, ok := result.(scriptout.CapabilitiesResponse)
	if !ok {
		t.Fatalf("result type = %T, want scriptout.CapabilitiesResponse", result)
	}
	if resp.SchemaVersions["thread"] == 0 {
		t.Fatalf("SchemaVersions = %#v, want a populated \"thread\" entry", resp.SchemaVersions)
	}
	for _, op := range resp.Ops {
		if op == scriptout.OpAuthStatus {
			t.Fatalf("Ops = %v, must not include auth_status: this backend implements no AuthChecker", resp.Ops)
		}
	}
}

// TestNewDispatchTable_CapabilitiesDeclaresVersion mirrors bead
// pg2-a8uf2's per-backend regression proof: this binary's own build-time-
// stamped Version var (ldflags-set, "dev" unstamped) must actually reach
// the capabilities response.
func TestNewDispatchTable_CapabilitiesDeclaresVersion(t *testing.T) {
	table := newDispatchTable(newTestBackend())
	entry := table[scriptout.OpCapabilities]
	result, err := entry.Handle(context.Background(), nil)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	resp := result.(scriptout.CapabilitiesResponse)
	if resp.Version != Version {
		t.Fatalf("capabilities.version = %q, want this binary's own Version var %q", resp.Version, Version)
	}
	if resp.Version == "" {
		t.Fatal("capabilities.version is empty — Version defaults to \"dev\" even unstamped, never empty")
	}
}

// TestNewDispatchTable_CapabilitiesOpsMatchesTableKeys mirrors bead
// pg2-fh2vh's per-backend regression proof: this binary never hand-types
// a capabilities.ops literal.
func TestNewDispatchTable_CapabilitiesOpsMatchesTableKeys(t *testing.T) {
	table := newDispatchTable(newTestBackend())
	entry, ok := table[scriptout.OpCapabilities]
	if !ok {
		t.Fatal("capabilities entry missing from this binary's own dispatch table")
	}
	result, err := entry.Handle(context.Background(), nil)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	resp, ok := result.(scriptout.CapabilitiesResponse)
	if !ok {
		t.Fatalf("result type = %T, want scriptout.CapabilitiesResponse", result)
	}
	want := table.Ops()
	if !reflect.DeepEqual(resp.Ops, want) {
		t.Fatalf("Ops = %v, want exactly the dispatch table's own registered keys %v", resp.Ops, want)
	}
}

// TestServeLoop_ShowRoundTripsThroughStdinStdout is this bead's own
// required scriptout-level test that this binary's main() correctly
// wires its op table into the Tier-1 core's generic serve loop, mirroring
// cmd/pg-connector-issue-jira/main_test.go's identical
// TestServeLoop_ShowRoundTripsThroughStdinStdout style.
func TestServeLoop_ShowRoundTripsThroughStdinStdout(t *testing.T) {
	origStdin, origStdout := os.Stdin, os.Stdout
	defer func() { os.Stdin, os.Stdout = origStdin, origStdout }()

	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdin, os.Stdout = inR, outW

	if _, err := inW.WriteString(`{"op":"show","args":{"id":"1726000000.000100"}}`); err != nil {
		t.Fatalf("write request: %v", err)
	}
	if err := inW.Close(); err != nil {
		t.Fatalf("close stdin writer: %v", err)
	}

	code := scriptout.ServeLoop(newDispatchTable(newTestBackend()))

	if err := outW.Close(); err != nil {
		t.Fatalf("close stdout writer: %v", err)
	}
	raw, err := io.ReadAll(outR)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}

	if code != 0 {
		t.Fatalf("exit code = %d, stdout=%s", code, raw)
	}
	var resp scriptout.Response
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("decode response: %v (stdout=%s)", err, raw)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected wire error: %+v", resp.Error)
	}
	var th struct {
		ID   string `json:"id"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(resp.Result, &th); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if th.ID != "1726000000.000100" || th.Text != "hello" {
		t.Fatalf("result = %+v", th)
	}
}
