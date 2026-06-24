package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
)

// resetFeedbackFlags clears mutable flag state between cobra tests since flag
// values persist across rootCmd.Execute() calls in the same process.
func resetFeedbackFlags() {
	fbF = feedbackFlags{}
}

// seedFeedbackStore opens a fresh store, inserts a PR and two feedback items,
// and returns (storePath, prID, feedbackID1, feedbackID2).
func seedFeedbackStore(t *testing.T) (storePath string, prID, fbID1, fbID2 int64) {
	t.Helper()
	dir := t.TempDir()
	storePath = filepath.Join(dir, "test.db")
	db, err := store.Open(storePath)
	if err != nil {
		t.Fatalf("seedFeedbackStore: Open: %v", err)
	}
	defer func() { _ = db.Close() }()

	ctx := context.Background()
	prID, err = db.UpsertPR(ctx, store.PullRequest{
		Repo: "o/r", Number: 1, Ownership: "mine", State: "open",
	})
	if err != nil {
		t.Fatalf("seedFeedbackStore: UpsertPR: %v", err)
	}

	fbID1, err = db.UpsertFeedback(ctx, store.Feedback{
		PRID:        prID,
		Kind:        "pr-comments",
		Fingerprint: "fp-1",
		Title:       "Add tests",
		AuthorLogin: "alice",
		AuthorKind:  "human",
		AuthorRole:  "team_member",
	})
	if err != nil {
		t.Fatalf("seedFeedbackStore: UpsertFeedback 1: %v", err)
	}

	fbID2, err = db.UpsertFeedback(ctx, store.Feedback{
		PRID:        prID,
		Kind:        "ci-failure",
		Fingerprint: "fp-2",
		Title:       "lint failed",
		AuthorKind:  "agent",
		AgentName:   "ci-bot",
	})
	if err != nil {
		t.Fatalf("seedFeedbackStore: UpsertFeedback 2: %v", err)
	}

	return storePath, prID, fbID1, fbID2
}

// execFeedbackCmd runs pg-pr feedback [args] with --store pointing at storePath.
// Returns (stdout, stderr, error).
func execFeedbackCmd(t *testing.T, storePath string, args ...string) (string, string, error) {
	t.Helper()
	resetFeedbackFlags()

	// persistent --store must come immediately after the "feedback" sub-command.
	full := append([]string{"feedback", "--store=" + storePath}, args...)

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs(full)
	err := rootCmd.Execute()
	return stdout.String(), stderr.String(), err
}

// countOutboxRows returns how many pending/complete outbox rows of eventType
// exist in the store at storePath.
func countOutboxRows(t *testing.T, storePath, eventType string) int {
	t.Helper()
	db, err := store.Open(storePath)
	if err != nil {
		t.Fatalf("countOutboxRows: open: %v", err)
	}
	defer func() { _ = db.Close() }()
	var n int
	err = db.InTx(context.Background(), func(tx *store.Tx) error {
		row := tx.QueryRow("SELECT COUNT(*) FROM outbox WHERE type=?", eventType)
		return row.Scan(&n)
	})
	if err != nil {
		t.Fatalf("countOutboxRows: query: %v", err)
	}
	return n
}

func TestFeedbackList_JSON(t *testing.T) {
	storePath, _, fbID1, fbID2 := seedFeedbackStore(t)
	stdout, _, err := execFeedbackCmd(t, storePath, "list", "o/r", "1", "--json")
	if err != nil {
		t.Fatalf("feedback list --json: %v", err)
	}

	var items []store.Feedback
	if err := json.Unmarshal([]byte(stdout), &items); err != nil {
		t.Fatalf("unmarshal output: %v\nraw: %s", err, stdout)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}

	// Verify IDs are present.
	ids := map[int64]bool{items[0].ID: true, items[1].ID: true}
	if !ids[fbID1] || !ids[fbID2] {
		t.Errorf("expected ids %d and %d in output; got %v", fbID1, fbID2, ids)
	}

	// Verify author_kind and agent_name are present.
	for _, it := range items {
		switch it.Kind {
		case "pr-comments":
			if it.AuthorKind != "human" {
				t.Errorf("pr-comments author_kind = %q, want human", it.AuthorKind)
			}
		case "ci-failure":
			if it.AuthorKind != "agent" {
				t.Errorf("ci-failure author_kind = %q, want agent", it.AuthorKind)
			}
			if it.AgentName != "ci-bot" {
				t.Errorf("ci-failure agent_name = %q, want ci-bot", it.AgentName)
			}
		}
	}
}

func TestFeedbackList_NoSuchPR(t *testing.T) {
	storePath, _, _, _ := seedFeedbackStore(t)
	stdout, _, err := execFeedbackCmd(t, storePath, "list", "o/r", "99")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(stdout, "no such PR") {
		t.Errorf("expected friendly 'no such PR' message, got: %q", stdout)
	}
}

func TestFeedbackShow_JSON(t *testing.T) {
	storePath, _, fbID1, _ := seedFeedbackStore(t)
	idStr := fmt.Sprintf("%d", fbID1)
	stdout, _, err := execFeedbackCmd(t, storePath, "show", idStr, "--json")
	if err != nil {
		t.Fatalf("feedback show --json: %v", err)
	}

	var out struct {
		Feedback store.Feedback  `json:"feedback"`
		Messages []store.Message `json:"messages"`
	}
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, stdout)
	}
	if out.Feedback.ID != fbID1 {
		t.Errorf("feedback id = %d, want %d", out.Feedback.ID, fbID1)
	}
	// messages gap: code_comment_message is not yet populated by ingestion.
	// The slice should be empty (nil or empty array) — both are fine.
}

func TestFeedbackDisposition_WillFix(t *testing.T) {
	storePath, _, fbID1, _ := seedFeedbackStore(t)
	idStr := strconv.FormatInt(fbID1, 10)
	stdout, _, err := execFeedbackCmd(t, storePath, "disposition", idStr,
		"--action=will-fix", "--note=looks good")
	if err != nil {
		t.Fatalf("feedback disposition: %v", err)
	}
	if !strings.Contains(stdout, "dispositioned") {
		t.Errorf("expected confirmation, got: %q", stdout)
	}

	// Verify the store was actually updated.
	db, err := store.Open(storePath)
	if err != nil {
		t.Fatalf("re-open store: %v", err)
	}
	defer func() { _ = db.Close() }()
	f, err := db.GetFeedback(context.Background(), fbID1)
	if err != nil || f == nil {
		t.Fatalf("GetFeedback after disposition: %v / nil=%v", err, f == nil)
	}
	if f.DispositionAction != "will-fix" {
		t.Errorf("DispositionAction = %q, want will-fix", f.DispositionAction)
	}
	if f.Status != "dispositioned" {
		t.Errorf("Status = %q, want dispositioned", f.Status)
	}
}

// TestFeedbackDisposition_EnqueuesEvent verifies that the disposition command
// records the disposition in the store and does NOT enqueue a feedback.disposed
// outbox row (the dead-event removal from M3: the disposed event had no
// consumer and accumulated never-dispatched pending rows).
func TestFeedbackDisposition_EnqueuesEvent(t *testing.T) {
	storePath, _, fbID1, _ := seedFeedbackStore(t)
	idStr := strconv.FormatInt(fbID1, 10)
	_, _, err := execFeedbackCmd(t, storePath, "disposition", idStr,
		"--action=no-action", "--note=intentional")
	if err != nil {
		t.Fatalf("feedback disposition: %v", err)
	}

	// Disposition must be recorded in the store.
	db, err := store.Open(storePath)
	if err != nil {
		t.Fatalf("re-open store: %v", err)
	}
	defer func() { _ = db.Close() }()
	f, err := db.GetFeedback(context.Background(), fbID1)
	if err != nil || f == nil {
		t.Fatalf("GetFeedback after disposition: %v / nil=%v", err, f == nil)
	}
	if f.DispositionAction != "no-action" {
		t.Errorf("DispositionAction = %q, want no-action", f.DispositionAction)
	}
	if f.Status != "dispositioned" {
		t.Errorf("Status = %q, want dispositioned", f.Status)
	}

	// NO feedback.disposed outbox row must be enqueued (dead event removed in M3).
	n := countOutboxRows(t, storePath, store.EventFeedbackDisposed)
	if n != 0 {
		t.Fatalf("expected 0 feedback.disposed outbox rows (dead event removed), got %d", n)
	}
}

func TestFeedbackDisposition_InvalidAction(t *testing.T) {
	storePath, _, fbID1, _ := seedFeedbackStore(t)
	idStr := strconv.FormatInt(fbID1, 10)
	_, _, err := execFeedbackCmd(t, storePath, "disposition", idStr, "--action=bogus")
	if err == nil {
		t.Fatal("expected error for invalid --action, got nil")
	}
	if !strings.Contains(err.Error(), "bogus") {
		t.Errorf("expected error to mention 'bogus', got: %v", err)
	}
}
