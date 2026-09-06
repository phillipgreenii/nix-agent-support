package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"reflect"
	"testing"

	internal "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-ci-github-actions/internal"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// fakeGH is a minimal double for internal.Backend's ghRunner seam, so this
// file's wiring tests never spawn a real `gh` subprocess.
type fakeGH struct{}

func (fakeGH) Run(_ context.Context, args ...string) ([]byte, error) {
	return []byte(`[{"databaseId":1,"name":"ci","status":"completed","conclusion":"success","headBranch":"feat/x","headSha":"deadbeef"}]`), nil
}

// fakePR is a minimal double for internal.Backend's PRResolver seam.
type fakePR struct{}

func (fakePR) Resolve(_ context.Context, _ string) (string, string, error) {
	return "owner/repo", "feat/x", nil
}

func newTestBackend() *internal.Backend {
	return internal.NewWithDeps(fakeGH{}, fakePR{})
}

// TestNewDispatchTable_CapabilitiesDeclaresCISchemaVersion is the packet's
// required test asserting the capabilities op declares this backend's own
// schemaVersions.ci entry and its ops list, proving the wiring in main.go
// is actually exercised rather than only asserted in prose
// [design: §4.3].
func TestNewDispatchTable_CapabilitiesDeclaresCISchemaVersion(t *testing.T) {
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
	if got, want := resp.SchemaVersions["ci"], schema.CISchemaVersion; got != want {
		t.Fatalf("schemaVersions[ci] = %d, want %d", got, want)
	}
	wantOps := map[string]bool{"list_runs": false, "get_logs": false, "rerun_failed": false}
	for _, op := range resp.Ops {
		if _, ok := wantOps[op]; ok {
			wantOps[op] = true
		}
	}
	for op, seen := range wantOps {
		if !seen {
			t.Errorf("capabilities ops missing %q: %v", op, resp.Ops)
		}
	}
}

// TestNewDispatchTable_CapabilitiesOpsMatchesTableKeys is bead pg2-fh2vh's
// per-backend regression proof: this binary no longer hand-types a
// capabilities.ops literal (see newDispatchTable), so Ops MUST always be
// exactly the dispatch table's own registered keys. If a future change
// here ever reintroduced a hand-typed Ops slice, or added/removed an op
// from ci.NewDispatchTable's own table without this binary's Ops following
// automatically, this test would catch the divergence.
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

// TestServeLoop_ListRunsRoundTripsThroughStdinStdout is the packet's
// required scriptout-level end-to-end test: this binary's main() correctly
// wires its op table into the Tier-1 core's generic serve loop
// (pkg/scriptout.ServeLoop), exercised through the real os.Stdin/os.Stdout
// ServeLoop always reads — swapped for pipes here — mirroring the sibling
// pg-connector-pr-github backend's own main_test.go.
func TestServeLoop_ListRunsRoundTripsThroughStdinStdout(t *testing.T) {
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

	if _, err := inW.WriteString(`{"op":"list_runs","args":{"pr_id":"owner/repo#1"}}`); err != nil {
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
	var runs []schema.CIRun
	if err := json.Unmarshal(resp.Result, &runs); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("expected 1 run, got %d: %+v", len(runs), runs)
	}
	if runs[0].ID != "1" || runs[0].PRID != "owner/repo#1" {
		t.Fatalf("result = %+v", runs[0])
	}
}
