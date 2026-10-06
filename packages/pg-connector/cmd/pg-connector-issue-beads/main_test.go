package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-beads/internal"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout/conformance"
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
// is actually declared and not just committed to in prose (interfaces.md's vocabulary note).
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

// TestNewDispatchTable_CapabilitiesVocabularyPriority locks in finding 33's
// fix: capabilities.vocabulary must declare a non-empty "priority" entry
// matching internal.PriorityVocabulary (bd's real accepted priority
// values), since it previously omitted priority entirely [review finding A-33].
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

// TestNewDispatchTable_CapabilitiesDeclaresVersion is bead pg2-a8uf2's
// per-backend regression proof: this binary's own build-time-stamped
// Version var (ldflags-set, "dev" unstamped) must actually reach the
// capabilities response, not just sit unread the way it did before this
// bead — there was previously no way to ask a running backend what
// version it was.
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

// TestNewDispatchTable_DeclaresActivityCapability proves list_activity is
// wired into this binary's own table and declared: ops derived from the table
// (without auth_status, since Backend is no AuthChecker), the activity schema
// version, and exactly the kinds this backend emits.
func TestNewDispatchTable_DeclaresActivityCapability(t *testing.T) {
	table := newDispatchTable(newTestBackend())
	result, err := table[scriptout.OpCapabilities].Handle(context.Background(), nil)
	if err != nil {
		t.Fatalf("capabilities Handle: %v", err)
	}
	resp, ok := result.(scriptout.CapabilitiesResponse)
	if !ok {
		t.Fatalf("result type = %T, want scriptout.CapabilitiesResponse", result)
	}
	hasOp := false
	for _, op := range resp.Ops {
		if op == "list_activity" {
			hasOp = true
		}
		if op == scriptout.OpAuthStatus {
			t.Errorf("ops must not claim %q: Backend is not a provider.AuthChecker", op)
		}
	}
	if !hasOp {
		t.Fatalf("capabilities.ops = %v, want list_activity", resp.Ops)
	}
	if got := resp.SchemaVersions["activity"]; got != schema.ActivitySchemaVersion {
		t.Fatalf("schemaVersions[activity] = %d, want %d", got, schema.ActivitySchemaVersion)
	}
	raw, _ := json.Marshal(resp.Vocabulary["activity_kinds"])
	if want := `["issue.created","issue.started","issue.closed"]`; string(raw) != want {
		t.Fatalf("vocabulary.activity_kinds = %s, want %s", raw, want)
	}
}

// configInjectingBackend wraps a conformance.Backend and adds the host's
// per-backend config block to every request, as the umbrella does for the
// static backends.<name> block: conformance.ListActivityRequest sends none.
type configInjectingBackend struct {
	inner  conformance.Backend
	config string
}

func (c configInjectingBackend) Invoke(ctx context.Context, request []byte) ([]byte, int, error) {
	var req map[string]json.RawMessage
	if err := json.Unmarshal(request, &req); err != nil {
		return nil, 0, err
	}
	req["config"] = json.RawMessage(c.config)
	out, err := json.Marshal(req)
	if err != nil {
		return nil, 0, err
	}
	return c.inner.Invoke(ctx, out)
}

// TestNewDispatchTable_ListActivityConformance drives the production table
// through the generic list_activity conformance case, then asserts the
// content the generic case deliberately leaves to the backend.
func TestNewDispatchTable_ListActivityConformance(t *testing.T) {
	runner := &fakeRunner{handle: func(args []string) (string, error) {
		return `{"data":[
			{"id":"tp-1","title":"mine","status":"open","priority":2,"issue_type":"task","created_by":"me","created_at":"2026-09-05T00:00:00Z"},
			{"id":"tp-2","title":"theirs","status":"open","priority":2,"issue_type":"task","created_by":"someone-else","created_at":"2026-09-06T00:00:00Z"},
			{"id":"tp-3","title":"mine too","status":"closed","priority":1,"issue_type":"bug","created_by":"my-agent","created_at":"2026-09-07T00:00:00Z"}
		],"schema_version":1}`, nil
	}}
	table := newDispatchTable(internal.New(runner))
	backend := configInjectingBackend{
		inner:  conformance.TableBackend{Table: table},
		config: `{"activity_actors":["me","my-agent"]}`,
	}
	since := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	before := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

	for _, s := range []time.Time{since, {}} {
		results := conformance.RunListActivityCase(context.Background(), backend, s, before)
		if len(results) != 5 {
			t.Fatalf("sub-cases = %d, want 5", len(results))
		}
		for _, r := range results {
			if r.Err != nil {
				t.Errorf("%s (since=%v): %v", r.Name, s, r.Err)
			}
			if r.Skipped {
				t.Errorf("%s (since=%v) skipped: %s", r.Name, s, r.SkipReason)
			}
		}
	}

	res, err := conformance.InvokeListActivity(context.Background(), backend, since.Format(time.RFC3339), before.Format(time.RFC3339))
	if err != nil || res.ErrorCode != "" {
		t.Fatalf("InvokeListActivity: res=%+v err=%v", res, err)
	}
	var got schema.ActivityListResult
	if err := json.Unmarshal(res.Result, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var ids []string
	for _, it := range got.Items {
		ids = append(ids, it.ID)
	}
	if want := "tp-1#issue.created,tp-3#issue.created"; strings.Join(ids, ",") != want {
		t.Errorf("ids = %v, want %s (other people's bead excluded)", ids, want)
	}

	// Without activity_actors the wire answer is unavailable naming the key.
	bare := conformance.TableBackend{Table: table}
	res, err = conformance.InvokeListActivity(context.Background(), bare, since.Format(time.RFC3339), before.Format(time.RFC3339))
	if err != nil {
		t.Fatalf("InvokeListActivity (no config): %v", err)
	}
	if res.ErrorCode != "unavailable" {
		t.Errorf("no-config error code = %q, want unavailable", res.ErrorCode)
	}
}
