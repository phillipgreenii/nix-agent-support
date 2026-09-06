package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"reflect"
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

// Workspace reports a fixed test value — these wiring tests care about
// dispatch-table plumbing, not workspace resolution (that is covered in
// internal/runner_test.go and internal/backend_test.go).
func (f *fakeRunner) Workspace() (string, error) {
	return "/fake/workspace", nil
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

// TestNewDispatchTable_CapabilitiesAdvertisesWorkspaceDir is the packet's
// AC2 test for the capabilities-side half of "surfaced through
// schema.Issue/capabilities" (bead pg2-1q9c0): capabilities must echo back
// the resolved bd workspace directory so `config validate`'s fan-out can
// show which tracker each issue-beads instance targets.
func TestNewDispatchTable_CapabilitiesAdvertisesWorkspaceDir(t *testing.T) {
	table := newDispatchTable(newTestBackend())
	entry := table[scriptout.OpCapabilities]
	result, err := entry.Handle(context.Background(), nil)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	resp := result.(scriptout.CapabilitiesResponse)
	if got := resp.Vocabulary["workspace_dir"]; got != "/fake/workspace" {
		t.Fatalf("vocabulary.workspace_dir = %#v, want /fake/workspace", got)
	}
}

// TestNewDispatchTable_CapabilitiesOpsMatchesTableKeys is bead pg2-fh2vh's
// per-backend regression proof: this binary no longer hand-types a
// capabilities.ops literal (see newDispatchTable), so Ops MUST always be
// exactly the dispatch table's own registered keys — including the
// deliberate absence of auth_status this backend's own
// TestNewDispatchTable_CapabilitiesVocabularyNonEmpty above already checks
// for. If a future change here ever reintroduced a hand-typed Ops slice,
// or added/removed an op from issue.NewDispatchTable's own table without
// this binary's Ops following automatically, this test would catch the
// divergence.
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
		t.Fatalf("Ops = %v, want exactly the dispatch table's own registered keys %v: capabilities.ops must be mechanically derived from the table, never a separately maintained literal", resp.Ops, want)
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
