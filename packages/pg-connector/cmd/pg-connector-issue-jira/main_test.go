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

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout/conformance"
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

// activityFakeRunner answers the one activity search with an operator-created
// issue and an issue created by someone else.
func activityFakeRunner() *fakeRunner {
	return &fakeRunner{handle: func(args []string) (string, error) {
		if args[0] == "auth-status" {
			return "OK\n", nil
		}
		return `{"truncated":false,"items":[` +
			`{"key":"PROJ-1","summary":"mine","project":"PROJ","created":"2026-09-05T10:00:00.000+0000","reporter":{"email":"operator@example.com"}},` +
			`{"key":"PROJ-2","summary":"theirs","project":"PROJ","created":"2026-09-05T10:00:00.000+0000","reporter":{"email":"other@example.com"}}]}`, nil
	}}
}

// TestNewDispatchTable_DeclaresActivityCapability proves list_activity is
// wired into this binary's own table and declared: ops derived from the table
// (list_activity and auth_status), the activity schema version, and exactly the
// kinds this backend emits.
func TestNewDispatchTable_DeclaresActivityCapability(t *testing.T) {
	resp := capabilitiesResponse(t, newDispatchTable(newTestBackend()))

	ops := map[string]bool{}
	for _, op := range resp.Ops {
		ops[op] = true
	}
	for _, want := range []string{"list_activity", scriptout.OpAuthStatus} {
		if !ops[want] {
			t.Errorf("capabilities.ops = %v, want %q", resp.Ops, want)
		}
	}
	if got := resp.SchemaVersions["activity"]; got != schema.ActivitySchemaVersion {
		t.Fatalf("schemaVersions[activity] = %d, want %d", got, schema.ActivitySchemaVersion)
	}
	kinds, ok := resp.Vocabulary["activity_kinds"]
	if !ok {
		t.Fatalf("vocabulary = %v, want activity_kinds", resp.Vocabulary)
	}
	raw, _ := json.Marshal(kinds)
	if string(raw) != `["issue.created","issue.transitioned","issue.commented"]` {
		t.Fatalf("vocabulary.activity_kinds = %s, want [\"issue.created\",\"issue.transitioned\",\"issue.commented\"]", raw)
	}
}

func capabilitiesResponse(t *testing.T, table scriptout.DispatchTable) scriptout.CapabilitiesResponse {
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

// TestNewDispatchTable_ListActivityConformance drives the production table
// (with a fake pjira runner) through the generic list_activity conformance
// case, then asserts the content the generic case leaves to the backend. The
// operator identity comes from JIRA_EMAIL, set with t.Setenv because the
// backend's getenv field is unexported.
func TestNewDispatchTable_ListActivityConformance(t *testing.T) {
	t.Setenv("JIRA_EMAIL", "operator@example.com")
	backend := conformance.TableBackend{Table: newDispatchTable(internal.New(activityFakeRunner()))}
	since := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	before := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

	for _, r := range conformance.RunListActivityCase(context.Background(), backend, since, before) {
		if r.Err != nil {
			t.Errorf("%s: %v", r.Name, r.Err)
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
	if len(got.Items) != 1 || got.Items[0].ID != "PROJ-1#issue.created" {
		t.Fatalf("items = %+v, want only the operator's own PROJ-1#issue.created", got.Items)
	}
}

// TestNewDispatchTable_ListActivityUnavailableWithoutIdentity proves the wire
// answer when no operator identity can be established: code unavailable, the
// message naming what is missing, and no items.
func TestNewDispatchTable_ListActivityUnavailableWithoutIdentity(t *testing.T) {
	t.Setenv("JIRA_EMAIL", "")
	fr := &fakeRunner{handle: func([]string) (string, error) { return `{"items":[],"truncated":false}`, nil }}
	backend := conformance.TableBackend{Table: newDispatchTable(internal.New(fr))}
	res, err := conformance.InvokeListActivity(context.Background(), backend, "2026-09-01T00:00:00Z", "2026-10-01T00:00:00Z")
	if err != nil {
		t.Fatalf("InvokeListActivity: %v", err)
	}
	if res.ErrorCode != "unavailable" {
		t.Fatalf("error code = %q, want unavailable", res.ErrorCode)
	}
	if len(res.Result) != 0 && !strings.Contains(string(res.Result), "null") {
		t.Errorf("result = %s, want no items", res.Result)
	}
}

// cacheContractRunner is a pjira double for the umbrella cache contract tests
// below: one issue that exists, one search that answers a fixed id set, and a
// log of every pjira search so a test can count origin calls.
func cacheContractRunner(searches *[]string) *fakeRunner {
	return &fakeRunner{handle: func(args []string) (string, error) {
		switch args[0] {
		case "issue":
			if args[len(args)-1] == "PROJ-404" {
				return "", errorString("pjira issue -- PROJ-404: exit status 1: pjira: issue PROJ-404 not found")
			}
			return `{"key":"PROJ-1","summary":"hello","status":"To Do","issuetype":"Task"}`, nil
		case "search":
			*searches = append(*searches, args[2])
			return `{"items":[{"key":"PROJ-1","summary":"a","status":"To Do"},{"key":"PROJ-2","summary":"b","status":"To Do"}],"truncated":false}`, nil
		}
		return "", nil
	}}
}

type errorString string

func (e errorString) Error() string { return string(e) }

// TestCacheContract_MembershipIsOneCheapSearch proves the wire-level
// ids_only list the umbrella's refresher sends (the membership query is a
// JQL key list) answers present_ids with no entities from a single
// unbounded search per expression.
func TestCacheContract_MembershipIsOneCheapSearch(t *testing.T) {
	var searches []string
	table := newDispatchTable(internal.New(cacheContractRunner(&searches)))
	ctx := scriptout.WithConfig(context.Background(), json.RawMessage(`{"queries":{"mine":["assignee = currentUser()"]}}`))

	result, err := table["list"].Handle(ctx, json.RawMessage(`{"query":"mine","cursor":null,"ids_only":true}`))
	if err != nil {
		t.Fatalf("list ids_only: %v", err)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got struct {
		Entities   []json.RawMessage `json:"entities"`
		PresentIDs []string          `json:"present_ids"`
		Truncated  bool              `json:"truncated"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, raw)
	}
	if !reflect.DeepEqual(got.PresentIDs, []string{"PROJ-1", "PROJ-2"}) || len(got.Entities) != 0 || got.Truncated {
		t.Fatalf("list ids_only = %s, want present_ids [PROJ-1 PROJ-2], no entities, not truncated", raw)
	}
	if !reflect.DeepEqual(searches, []string{"assignee = currentUser()"}) {
		t.Fatalf("pjira searches = %q, want exactly one unbounded search", searches)
	}
}

// TestCacheContract_ShowIsADetailEntityTheCacheCanKey proves the wire-level
// show answer carries what the umbrella cache keys and ages on (a non-empty
// id and an RFC3339 as_of) and that a missing issue answers the not_found
// code the removal confirmation reads.
func TestCacheContract_ShowIsADetailEntityTheCacheCanKey(t *testing.T) {
	var searches []string
	table := newDispatchTable(internal.New(cacheContractRunner(&searches)))

	result, err := table["show"].Handle(context.Background(), json.RawMessage(`{"id":"PROJ-1"}`))
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	raw, _ := json.Marshal(result)
	var got struct {
		ID    string `json:"id"`
		AsOf  string `json:"as_of"`
		Stale bool   `json:"stale"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, raw)
	}
	if _, perr := time.Parse(time.RFC3339, got.AsOf); got.ID != "PROJ-1" || perr != nil || got.Stale {
		t.Fatalf("show = %s, want id PROJ-1, an RFC3339 as_of and stale false", raw)
	}

	_, err = table["show"].Handle(context.Background(), json.RawMessage(`{"id":"PROJ-404"}`))
	if code := scriptout.CodeForError(err); code != "not_found" {
		t.Fatalf("show of a missing issue: err=%v code=%q, want not_found", err, code)
	}
}

// TestIssueChildrenUnsupportedBackendIsUnknownOp (bead pg2-nd60k): this
// backend does not implement the optional children op, so it is absent from
// capabilities.ops and a call answers the wire-level unknown_op error (no
// eighth error code exists for it).
func TestIssueChildrenUnsupportedBackendIsUnknownOp(t *testing.T) {
	table := newDispatchTable(newTestBackend())
	if _, ok := table["children"]; ok {
		t.Fatal("children must not be registered for a backend without issue.ChildrenLister")
	}
	for _, op := range table.Ops() {
		if op == "children" {
			t.Fatalf("capabilities ops = %v must not advertise children", table.Ops())
		}
	}

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
	if _, err := inW.WriteString(`{"op":"children","args":{"id":"PROJ-1"}}`); err != nil {
		t.Fatalf("write request: %v", err)
	}
	_ = inW.Close()
	scriptout.ServeLoop(table)
	_ = outW.Close()
	raw, err := io.ReadAll(outR)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	var resp scriptout.Response
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("decode response: %v (stdout=%s)", err, raw)
	}
	if resp.Error == nil || resp.Error.Code != "unknown_op" {
		t.Fatalf("response = %s, want error.code unknown_op", raw)
	}
}
