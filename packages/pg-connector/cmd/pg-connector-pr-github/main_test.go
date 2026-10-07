package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-pr-github/internal"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-pr-github/internal/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-pr-github/internal/eventlog"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-pr-github/internal/github"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout/conformance"
)

// fakeGH is a minimal double for internal.Backend's ghProvider seam, so
// this file's wiring tests never spawn a real `gh` subprocess.
type fakeGH struct{}

func (fakeGH) GetPR(ctx context.Context, repo string, number int) (*api.PR, error) {
	return &api.PR{Repo: repo, Number: number, Title: "hello", State: "open"}, nil
}

func (fakeGH) ListComments(ctx context.Context, repo string, number int) ([]api.Comment, error) {
	return nil, nil
}

func (fakeGH) ListReviews(ctx context.Context, repo string, number int) ([]api.Review, error) {
	return nil, nil
}

func (fakeGH) ListCommentsReport(ctx context.Context, repo string, number int) (*api.CommentsResult, error) {
	return &api.CommentsResult{}, nil
}

func (fakeGH) ListReviewsReport(ctx context.Context, repo string, number int) (*api.ReviewsResult, error) {
	return &api.ReviewsResult{}, nil
}

func (fakeGH) CheckAuth(ctx context.Context) error { return nil }

func (fakeGH) SearchPRs(ctx context.Context, query string) ([]api.PR, error) {
	return nil, nil
}

func (fakeGH) SearchPRsEnrichedGated(ctx context.Context, query string, gate github.RateGate) ([]api.PR, github.RateLimit, error) {
	rl := github.RateLimit{Remaining: 5000, ResetAt: "2026-10-03T14:00:00Z"}
	if gate != nil {
		if err := gate(rl); err != nil {
			return nil, rl, err
		}
	}
	return nil, rl, nil
}

func (fakeGH) SearchPRsActivity(ctx context.Context, query string, limit int) ([]api.PR, error) {
	return nil, nil
}

func (fakeGH) ReadRateLimit(ctx context.Context) (github.RateLimit, error) {
	return github.RateLimit{Remaining: 5000, ResetAt: "2026-10-03T14:00:00Z"}, nil
}

func (fakeGH) GetFiles(ctx context.Context, repo string, number int) ([]api.File, error) {
	return nil, nil
}

func (fakeGH) GetCommits(ctx context.Context, repo string, number int) ([]api.Commit, error) {
	return nil, nil
}

func (fakeGH) ViewerLogin(ctx context.Context) (string, error) {
	return "me", nil
}

func (fakeGH) ListReviewsSubmitted(ctx context.Context, repo string, number int) ([]api.Review, error) {
	return nil, nil
}

func (fakeGH) CreateBodyOnlyPendingReview(ctx context.Context, repo string, number int, commitID, body string) (*github.CreatedReview, error) {
	return &github.CreatedReview{NodeID: "RV", State: "pending"}, nil
}

func (fakeGH) WriteReviewItems(ctx context.Context, reviewID string, items []github.ReviewWriteItem) ([]github.ReviewWriteResult, error) {
	return make([]github.ReviewWriteResult, len(items)), nil
}

func (fakeGH) UpdateReviewBody(ctx context.Context, reviewID, body string) error { return nil }

func (fakeGH) GetPRHistory(ctx context.Context, repo string, number int) (*github.PRHistory, error) {
	return &github.PRHistory{}, nil
}

func (fakeGH) GetComparedFiles(ctx context.Context, repo, base, head string, wanted []string) ([]github.ComparedFile, error) {
	return nil, nil
}

func (fakeGH) GetPendingReview(ctx context.Context, repo string, number int) (*github.PendingReviewData, error) {
	return &github.PendingReviewData{HeadSHA: "deadbeef"}, nil
}

func newTestBackend(t *testing.T) *internal.Backend {
	t.Helper()
	return internal.New(fakeGH{})
}

// TestNewDispatchTable_CapabilitiesDeclaresVersion is bead pg2-a8uf2's
// per-backend regression proof: this binary's own build-time-stamped
// Version var (ldflags-set, "dev" unstamped) must actually reach the
// capabilities response, not just sit unread the way it did before this
// bead — there was previously no way to ask a running backend what
// version it was.
func TestNewDispatchTable_CapabilitiesDeclaresVersion(t *testing.T) {
	table := newDispatchTable(newTestBackend(t))
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
// exactly the dispatch table's own registered keys. If a future change
// here ever reintroduced a hand-typed Ops slice, or added/removed an op
// from pr.NewDispatchTable's own table without this binary's Ops
// following automatically, this test would catch the divergence.
func TestNewDispatchTable_CapabilitiesOpsMatchesTableKeys(t *testing.T) {
	table := newDispatchTable(newTestBackend(t))
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
// (pkg/scriptout.ServeLoop): an end-to-end stdin-JSON-in, stdout-JSON-out
// exercise, mirroring packages/pg-pr/pkg/plugin/scriptout/scriptout_test.go's
// own style. That file's own runServe test helper is unexported in a
// different package (pkg/scriptout, a different module's package this one
// cannot reach into), so this backend's own main() is instead exercised
// through the real os.Stdin/os.Stdout ServeLoop always reads — swapped for
// pipes here — which is the only external seam scriptout.ServeLoop exposes.
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

	if _, err := inW.WriteString(`{"op":"show","args":{"id":"owner/repo#1"}}`); err != nil {
		t.Fatalf("write request: %v", err)
	}
	if err := inW.Close(); err != nil {
		t.Fatalf("close stdin writer: %v", err)
	}

	code := scriptout.ServeLoop(newDispatchTable(newTestBackend(t)))

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
	var pr struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	if err := json.Unmarshal(resp.Result, &pr); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if pr.ID != "owner/repo#1" || pr.Title != "hello" {
		t.Fatalf("result = %+v", pr)
	}
}

// TestNewDispatchTable_ListsReviewSubmit: the GitHub backend implements the
// optional ReviewSubmitter capability, so review_submit is registered and
// therefore listed in capabilities.ops.
func TestNewDispatchTable_ListsReviewSubmit(t *testing.T) {
	table := newDispatchTable(newTestBackend(t))
	if _, ok := table["review_submit"]; !ok {
		t.Fatalf("review_submit missing from dispatch table; ops = %v", table.Ops())
	}
}

// TestRun_InstrumentWritesOneEventPerCallToTheBackendsOwnLog (bead pg2-ph0o4):
// the production wiring (instrument over newDispatchTable) leaves exactly one
// JSONL event per call in the log file the backend owns -- resolved from ITS
// OWN environment, not from pg-connector's config -- and a call that read the
// rate limit carries remaining/reset.
func TestRun_InstrumentWritesOneEventPerCallToTheBackendsOwnLog(t *testing.T) {
	stateHome := t.TempDir()
	getenv := func(k string) string {
		if k == "XDG_STATE_HOME" {
			return stateHome
		}
		return ""
	}
	table := instrument(newDispatchTable(newTestBackend(t)), getenv)

	call := func(op, args string) {
		t.Helper()
		var out bytes.Buffer
		scriptout.ServeOne(table, strings.NewReader(`{"op":"`+op+`","args":`+args+`}`), &out)
	}
	call("show", `{"id":"owner/repo#1"}`)
	call("search", `{"query":"is:open"}`)
	call("show", `{"id":"not-a-valid-id"}`)

	path := filepath.Join(stateHome, "pg-connector-pr-github", "events.jsonl")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("event log not written at %s: %v", path, err)
	}
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	// Each call also writes a start row first (bead pg2-5dyz2), so a call
	// SIGKILLed before its final row is still attributable.
	if len(lines) != 6 {
		t.Fatalf("lines = %d, want 3 start rows + 3 final rows:\n%s", len(lines), raw)
	}
	var evs []map[string]any
	for i, l := range lines {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("bad line %q: %v", l, err)
		}
		if m["phase"] == "start" {
			if i%2 != 0 || m["pid"] == nil || m["args"] == nil {
				t.Errorf("start row %d malformed or misplaced: %v", i, m)
			}
			continue
		}
		evs = append(evs, m)
	}
	if len(evs) != 3 {
		t.Fatalf("final rows = %d, want 3:\n%s", len(evs), raw)
	}
	if evs[0]["op"] != "show" || evs[0]["level"] != "info" || evs[0]["service"] != "pg-connector-pr-github" {
		t.Errorf("show event = %v", evs[0])
	}
	// show is rate-reserve guarded (bead pg2-8wg9a), so its event carries the reading too.
	if evs[0]["graphql_remaining"] != float64(5000) {
		t.Errorf("show event missing rate-limit reading: %v", evs[0])
	}
	if evs[1]["op"] != "search" || evs[1]["graphql_remaining"] != float64(5000) || evs[1]["graphql_reset_at"] != "2026-10-03T14:00:00Z" {
		t.Errorf("search event missing rate-limit reading: %v", evs[1])
	}
	if evs[2]["level"] == "info" || evs[2]["error_code"] == nil {
		t.Errorf("failing call not logged as an error: %v", evs[2])
	}
}

// TestInstrument_DisabledLeavesTableAlone: EnvPath=off must not write
// anything and must not alter the table.
func TestInstrument_DisabledLeavesTableAlone(t *testing.T) {
	dir := t.TempDir()
	getenv := func(k string) string {
		switch k {
		case eventlog.EnvPath:
			return "off"
		case "XDG_STATE_HOME":
			return dir
		}
		return ""
	}
	table := instrument(newDispatchTable(newTestBackend(t)), getenv)
	var out bytes.Buffer
	scriptout.ServeOne(table, strings.NewReader(`{"op":"show","args":{"id":"owner/repo#1"}}`), &out)
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("disabled event log wrote files: %v", entries)
	}
}

// TestNewDispatchTable_ListsReviewPending: the GitHub backend implements the
// optional PendingReviewReader capability, so review_pending is registered and
// therefore listed in capabilities.ops.
func TestNewDispatchTable_ListsReviewPending(t *testing.T) {
	table := newDispatchTable(newTestBackend(t))
	if _, ok := table["review_pending"]; !ok {
		t.Fatalf("review_pending missing from dispatch table; ops = %v", table.Ops())
	}
}

// activityGH extends fakeGH with a small activity fixture: the viewer's own PRs
// plus another person's, one per search kind.
type activityGH struct{ fakeGH }

func (activityGH) SearchPRsActivity(_ context.Context, query string, _ int) ([]api.PR, error) {
	pr := func(n int, author, created, closed string) api.PR {
		return api.PR{Repo: "o/r", Number: n, Title: "t", Author: author, URL: "https://example.invalid/o/r/pull/1", CreatedAt: created, ClosedAt: closed}
	}
	switch {
	case strings.Contains(query, "reviewed-by:"), strings.Contains(query, "commenter:"):
		return nil, nil
	case strings.Contains(query, "created:"):
		return []api.PR{pr(1, "me", "2026-09-02T00:00:00Z", ""), pr(2, "other", "2026-09-02T00:00:00Z", "")}, nil
	case strings.Contains(query, "merged:"):
		return []api.PR{pr(1, "me", "2026-09-02T00:00:00Z", "2026-09-03T00:00:00Z")}, nil
	case strings.Contains(query, "closed:"):
		return []api.PR{pr(3, "me", "2026-09-01T00:00:00Z", "2026-09-04T00:00:00Z")}, nil
	}
	return nil, nil
}

func (activityGH) GetPR(_ context.Context, repo string, number int) (*api.PR, error) {
	return &api.PR{Repo: repo, Number: number, Merged: true, MergedAt: "2026-09-03T00:00:00Z"}, nil
}

func capabilitiesOf(t *testing.T, table scriptout.DispatchTable) scriptout.CapabilitiesResponse {
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

// TestNewDispatchTable_DeclaresActivityCapability proves list_activity is
// wired into this binary's own table and declared: ops derived from the table,
// the activity schema version, and exactly the kinds this backend emits.
func TestNewDispatchTable_DeclaresActivityCapability(t *testing.T) {
	table := newDispatchTable(newTestBackend(t))
	resp := capabilitiesOf(t, table)

	hasOp := false
	for _, op := range resp.Ops {
		if op == "list_activity" {
			hasOp = true
		}
	}
	if !hasOp {
		t.Fatalf("capabilities.ops = %v, want list_activity", resp.Ops)
	}
	if got := resp.SchemaVersions["activity"]; got != schema.ActivitySchemaVersion {
		t.Fatalf("schemaVersions[activity] = %d, want %d", got, schema.ActivitySchemaVersion)
	}
	want := []string{"pr.opened", "pr.merged", "pr.closed", "pr.reviewed", "pr.commented"}
	kinds, ok := resp.Vocabulary["activity_kinds"]
	if !ok {
		t.Fatalf("vocabulary = %v, want activity_kinds", resp.Vocabulary)
	}
	raw, _ := json.Marshal(kinds)
	wantRaw, _ := json.Marshal(want)
	if string(raw) != string(wantRaw) {
		t.Fatalf("vocabulary.activity_kinds = %s, want %s", raw, wantRaw)
	}
}

// TestNewDispatchTable_ListActivityConformance drives the production table
// through the generic list_activity conformance case, then asserts the
// content the generic case deliberately leaves to the backend.
func TestNewDispatchTable_ListActivityConformance(t *testing.T) {
	table := newDispatchTable(internal.New(activityGH{}))
	backend := conformance.TableBackend{Table: table}
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
	ids := map[string]bool{}
	for _, it := range got.Items {
		ids[it.ID] = true
	}
	for _, want := range []string{"o/r#1#pr.opened", "o/r#1#pr.merged", "o/r#3#pr.closed"} {
		if !ids[want] {
			t.Errorf("missing %q in %v", want, ids)
		}
	}
	if len(ids) != 3 {
		t.Errorf("ids = %v, want only the viewer's three items (other people's PR excluded)", ids)
	}
}
