package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-beads/internal"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// fakeRunner is a minimal double for internal.Runner, so this file's
// wiring tests never spawn a real `bd` subprocess.
type fakeRunner struct {
	handle func(args []string) (string, error)
}

func (f *fakeRunner) Run(_ context.Context, args ...string) (string, error) {
	return f.handle(args)
}

func newTestBackend() *internal.Backend {
	return internal.New(&fakeRunner{handle: func(args []string) (string, error) {
		if args[0] == "show" {
			return `{"data":[{"id":"tp-1","title":"hello","status":"open","priority":2,"issue_type":"task"}],"schema_version":1}`, nil
		}
		return `{"data":{"error":"unsupported op in this fake"},"schema_version":1}`, nil
	}})
}

// TestNewDispatchTable_CapabilitiesVocabularyNonEmpty is the packet's
// required test asserting the capabilities op's vocabulary.state list is
// non-empty and reflects bd's real status values, proving the vocabulary
// is actually declared and not just committed to in prose [design: §4.3,
// §4.3 AC].
func TestNewDispatchTable_CapabilitiesVocabularyNonEmpty(t *testing.T) {
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
	states, ok := resp.Vocabulary["state"].([]string)
	if !ok || len(states) == 0 {
		t.Fatalf("vocabulary.state = %#v, want a non-empty []string", resp.Vocabulary["state"])
	}
	if resp.SchemaVersions["issue"] == 0 {
		t.Fatalf("schemaVersions.issue missing/zero: %#v", resp.SchemaVersions)
	}
	for _, op := range resp.Ops {
		if op == scriptout.OpAuthStatus {
			t.Fatalf("ops must not claim %q: Backend does not implement provider.AuthChecker", scriptout.OpAuthStatus)
		}
	}
}

// TestServeLoop_ShowRoundTripsThroughStdinStdout is the packet's required
// scriptout-level test that this binary's main() correctly wires its op
// table into the Tier-1 core's generic serve loop
// (pkg/scriptout.ServeLoop), mirroring
// pg-connector-pr-github/main_test.go's identical style.
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

	if _, err := inW.WriteString(`{"op":"show","args":{"id":"tp-1"}}`); err != nil {
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
	var iss struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	if err := json.Unmarshal(resp.Result, &iss); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if iss.ID != "tp-1" || iss.Title != "hello" {
		t.Fatalf("result = %+v", iss)
	}
}
