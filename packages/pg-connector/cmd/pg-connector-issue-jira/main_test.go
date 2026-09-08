package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"reflect"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// fakeRunner is a minimal double for internal.Runner, so this file's
// wiring tests never spawn a real pjira subprocess.
type fakeRunner struct {
	handle func(args []string) (string, error)
}

func (f *fakeRunner) Run(_ context.Context, args ...string) (string, error) {
	return f.handle(args)
}

func (f *fakeRunner) Binary() string { return "pjira" }

func newTestBackend() *internal.Backend {
	return internal.New(&fakeRunner{handle: func(args []string) (string, error) {
		if args[0] == "issue" {
			return `{"key":"PROJ-1","summary":"hello","status":"To Do","issuetype":"Task"}`, nil
		}
		if args[0] == "auth-status" {
			return "OK\n", nil
		}
		return "", nil
	}})
}

// TestNewDispatchTable_CapabilitiesVocabularyNonEmpty is the packet's
// required test asserting the capabilities op's vocabulary.state list is
// non-empty and reflects this backend's own real Jira vocabulary, and that
// ops includes auth_status (internal.Backend implements AuthChecker,
// unlike pg-connector-issue-beads).
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
	foundAuthStatus := false
	for _, op := range resp.Ops {
		if op == scriptout.OpAuthStatus {
			foundAuthStatus = true
		}
	}
	if !foundAuthStatus {
		t.Fatalf("ops = %v, want it to include %q: Backend implements provider.AuthChecker", resp.Ops, scriptout.OpAuthStatus)
	}
}

// TestNewDispatchTable_CapabilitiesVocabularyPriority proves vocabulary.
// priority is declared, non-empty, and matches internal.PriorityVocabulary
// exactly.
func TestNewDispatchTable_CapabilitiesVocabularyPriority(t *testing.T) {
	table := newDispatchTable(newTestBackend())
	entry := table[scriptout.OpCapabilities]
	result, err := entry.Handle(context.Background(), nil)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	resp := result.(scriptout.CapabilitiesResponse)
	priorities, ok := resp.Vocabulary["priority"].([]string)
	if !ok || len(priorities) == 0 {
		t.Fatalf("vocabulary.priority = %#v, want a non-empty []string", resp.Vocabulary["priority"])
	}
	if !reflect.DeepEqual(priorities, internal.PriorityVocabulary) {
		t.Fatalf("vocabulary.priority = %v, want exactly internal.PriorityVocabulary %v", priorities, internal.PriorityVocabulary)
	}
}

// TestNewDispatchTable_CapabilitiesDeclaresVersion mirrors
// pg-connector-issue-beads' identical regression proof (bead pg2-a8uf2):
// this binary's own build-time-stamped Version var must actually reach the
// capabilities response.
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

// TestNewDispatchTable_CapabilitiesOpsMatchesTableKeys mirrors
// pg-connector-issue-beads' identical regression proof (bead pg2-fh2vh):
// this binary must never hand-type a capabilities.ops literal.
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

// TestServeLoop_ShowRoundTripsThroughStdinStdout is the packet's required
// scriptout-level test that this binary's main() correctly wires its op
// table into the Tier-1 core's generic serve loop.
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

	if _, err := inW.WriteString(`{"op":"show","args":{"id":"PROJ-1"}}`); err != nil {
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
	if iss.ID != "PROJ-1" || iss.Title != "hello" {
		t.Fatalf("result = %+v", iss)
	}
}
