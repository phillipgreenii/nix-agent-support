package changes

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
)

// stubRunner implements beads.Runner with a canned response.
type stubRunner struct {
	stdout   string
	err      error
	gotArgs  []string
	callsLog []string
}

func (s *stubRunner) Run(_ context.Context, args ...string) (string, error) {
	s.gotArgs = args
	s.callsLog = append(s.callsLog, strings.Join(args, " "))
	return s.stdout, s.err
}

func ts(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestSince_CategorizesCreatedUpdatedClosed(t *testing.T) {
	since := ts("2026-05-20T00:00:00Z")
	rows := []map[string]any{
		// created exactly at since boundary -> created
		{
			"id":         "t-1",
			"issue_type": "merge-request",
			"title":      "PR one",
			"status":     "open",
			"created_at": "2026-05-20T00:00:00Z",
			"updated_at": "2026-05-20T01:00:00Z",
		},
		// updated only (created before since) -> updated
		{
			"id":         "t-2",
			"issue_type": "feedback",
			"title":      "fb two",
			"status":     "open",
			"created_at": "2026-05-19T00:00:00Z",
			"updated_at": "2026-05-20T02:00:00Z",
		},
		// closed after since -> closed
		{
			"id":         "t-3",
			"issue_type": "merge-request",
			"title":      "PR three",
			"status":     "closed",
			"created_at": "2026-05-18T00:00:00Z",
			"updated_at": "2026-05-20T03:00:00Z",
			"closed_at":  "2026-05-20T03:00:00Z",
		},
		// excluded type (not pg-pr-managed) -> skipped
		{
			"id":         "t-4",
			"issue_type": "epic",
			"title":      "epic",
			"status":     "open",
			"created_at": "2026-05-20T00:30:00Z",
			"updated_at": "2026-05-20T00:30:00Z",
		},
	}
	stdout, _ := json.Marshal(rows)
	runner := &stubRunner{stdout: string(stdout)}

	cs, err := Since(context.Background(), since, runner)
	if err != nil {
		t.Fatalf("Since: %v", err)
	}
	if len(cs.Created) != 1 || cs.Created[0].ID != "t-1" {
		t.Fatalf("Created: got %+v", cs.Created)
	}
	if len(cs.Updated) != 1 || cs.Updated[0].ID != "t-2" {
		t.Fatalf("Updated: got %+v", cs.Updated)
	}
	if len(cs.Closed) != 1 || cs.Closed[0].ID != "t-3" {
		t.Fatalf("Closed: got %+v", cs.Closed)
	}
	if !cs.Since.Equal(since.UTC()) {
		t.Fatalf("Since field: got %v", cs.Since)
	}
}

func TestSince_PassesUpdatedAfterFlag(t *testing.T) {
	runner := &stubRunner{stdout: "[]"}
	since := ts("2026-05-20T10:00:00Z")
	_, err := Since(context.Background(), since, runner)
	if err != nil {
		t.Fatalf("Since: %v", err)
	}
	joined := strings.Join(runner.gotArgs, " ")
	if !strings.Contains(joined, "list") || !strings.Contains(joined, "--json") {
		t.Fatalf("expected bd list --json invocation, got %v", runner.gotArgs)
	}
	if !strings.Contains(joined, "--all") {
		t.Fatalf("expected --all to include closed beads, got %v", runner.gotArgs)
	}
	if !strings.Contains(joined, "--updated-after") {
		t.Fatalf("expected --updated-after flag, got %v", runner.gotArgs)
	}
	if !strings.Contains(joined, since.UTC().Format(time.RFC3339)) {
		t.Fatalf("expected since timestamp %q in args; got %v", since.UTC().Format(time.RFC3339), runner.gotArgs)
	}
}

func TestSince_EmptyResultIsNotAnError(t *testing.T) {
	runner := &stubRunner{stdout: ""}
	cs, err := Since(context.Background(), ts("2026-01-01T00:00:00Z"), runner)
	if err != nil {
		t.Fatalf("Since on empty: %v", err)
	}
	if len(cs.Created)+len(cs.Updated)+len(cs.Closed) != 0 {
		t.Fatalf("expected empty changeset, got %+v", cs)
	}
}

func TestSince_RunnerErrorPropagates(t *testing.T) {
	runner := &stubRunner{err: errors.New("bd not found")}
	_, err := Since(context.Background(), time.Now(), runner)
	if err == nil {
		t.Fatal("expected runner error to propagate")
	}
}

func TestSince_RequiresRunner(t *testing.T) {
	_, err := Since(context.Background(), time.Now(), nil)
	if err == nil {
		t.Fatal("expected error when runner is nil")
	}
}

// openTestStore opens a real SQLite store at a fresh temp path, returning
// both the handle and its path (unlike store.OpenForTest, which hides the
// path — LoadRepoErrors needs it to re-open the store independently, exactly
// as the CLI wiring does). Callers close db themselves before calling
// LoadRepoErrors against the same path.
func openTestStore(t *testing.T) (*store.DB, string) {
	t.Helper()
	store.SetSynchronousForTests("OFF")
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	return db, path
}

func TestLoadRepoErrors_ReadsFromStore(t *testing.T) {
	db, path := openTestStore(t)
	ctx := context.Background()
	if err := db.UpsertRepoSyncState(ctx, store.RepoSyncState{
		Repo:             "foo/bar",
		LastSyncedAt:     "2026-01-01T00:00:00Z",
		LastErrorCode:    "enum_failed",
		LastErrorMessage: "gh auth required",
	}); err != nil {
		t.Fatalf("UpsertRepoSyncState foo/bar: %v", err)
	}
	if err := db.UpsertRepoSyncState(ctx, store.RepoSyncState{
		Repo:         "baz/qux",
		LastSyncedAt: "2026-01-01T00:00:00Z",
		// no error -> excluded
	}); err != nil {
		t.Fatalf("UpsertRepoSyncState baz/qux: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	errs, err := LoadRepoErrors(ctx, path)
	if err != nil {
		t.Fatalf("LoadRepoErrors: %v", err)
	}
	if len(errs) != 1 {
		t.Fatalf("Errors: got %+v", errs)
	}
	if errs[0].Repo != "foo/bar" || errs[0].Code != "enum_failed" {
		t.Fatalf("Error[0]: got %+v", errs[0])
	}
	if !strings.Contains(errs[0].Message, "gh auth") {
		t.Fatalf("Error[0].Message: %q", errs[0].Message)
	}
}

func TestLoadRepoErrors_MissingStoreIsNotFatal(t *testing.T) {
	errs, err := LoadRepoErrors(context.Background(), "/no/such/path.db")
	if err != nil {
		t.Fatalf("LoadRepoErrors: %v", err)
	}
	if len(errs) != 0 {
		t.Fatalf("unexpected Errors: %+v", errs)
	}
}

func TestLoadRepoErrors_EmptyPathIsNotFatal(t *testing.T) {
	errs, err := LoadRepoErrors(context.Background(), "")
	if err != nil {
		t.Fatalf("LoadRepoErrors: %v", err)
	}
	if len(errs) != 0 {
		t.Fatalf("unexpected Errors: %+v", errs)
	}
}

func TestSince_FiltersToManagedTypes(t *testing.T) {
	rows := []map[string]any{
		{
			"id": "t-1", "issue_type": "merge-request", "status": "open",
			"created_at": "2026-05-20T00:00:00Z", "updated_at": "2026-05-20T00:00:00Z",
		},
		{
			"id": "t-2", "issue_type": "feedback", "status": "open",
			"created_at": "2026-05-20T00:00:00Z", "updated_at": "2026-05-20T00:00:00Z",
		},
		{
			"id": "t-3", "issue_type": "task", "status": "open",
			"created_at": "2026-05-20T00:00:00Z", "updated_at": "2026-05-20T00:00:00Z",
		},
		{
			"id": "t-4", "issue_type": "bug", "status": "open",
			"created_at": "2026-05-20T00:00:00Z", "updated_at": "2026-05-20T00:00:00Z",
		},
		{
			"id": "t-5", "issue_type": "epic", "status": "open",
			"created_at": "2026-05-20T00:00:00Z", "updated_at": "2026-05-20T00:00:00Z",
		},
		{
			"id": "t-6", "issue_type": "convoy", "status": "open",
			"created_at": "2026-05-20T00:00:00Z", "updated_at": "2026-05-20T00:00:00Z",
		},
	}
	data, _ := json.Marshal(rows)
	runner := &stubRunner{stdout: string(data)}
	cs, err := Since(context.Background(), ts("2026-05-19T00:00:00Z"), runner)
	if err != nil {
		t.Fatalf("Since: %v", err)
	}
	// 4 managed types (mr, feedback, task, bug) should appear in Created.
	if len(cs.Created) != 4 {
		t.Fatalf("Created count: got %d want 4 (rows=%+v)", len(cs.Created), cs.Created)
	}
}
