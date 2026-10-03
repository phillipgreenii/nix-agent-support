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

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-pr-github/internal"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-pr-github/internal/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-pr-github/internal/eventlog"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-pr-github/internal/github"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
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

func (fakeGH) CheckAuth(ctx context.Context) error { return nil }

func (fakeGH) SearchPRs(ctx context.Context, query string) ([]api.PR, error) {
	return nil, nil
}

func (fakeGH) SearchPRsEnriched(ctx context.Context, query string) ([]api.PR, error) {
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

func (fakeGH) ReviewsWithCommit(ctx context.Context, repo string, number int) ([]api.Review, error) {
	return nil, nil
}

func (fakeGH) FindPendingReview(ctx context.Context, repo string, number int) (int64, bool, error) {
	return 0, false, nil
}

func (fakeGH) DeleteReview(ctx context.Context, repo string, number int, reviewID int64) error {
	return nil
}

func (fakeGH) PostPendingReview(ctx context.Context, repo string, number int, commitID, body string, comments []github.ReviewSubmitComment) (*api.Review, error) {
	return &api.Review{ID: "RV", State: "pending"}, nil
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
	if len(lines) != 3 {
		t.Fatalf("lines = %d, want 3:\n%s", len(lines), raw)
	}
	var evs []map[string]any
	for _, l := range lines {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("bad line %q: %v", l, err)
		}
		evs = append(evs, m)
	}
	if evs[0]["op"] != "show" || evs[0]["level"] != "info" || evs[0]["service"] != "pg-connector-pr-github" {
		t.Errorf("show event = %v", evs[0])
	}
	if _, has := evs[0]["graphql_remaining"]; has {
		t.Errorf("show does not read the rate limit but event has graphql_remaining: %v", evs[0])
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
