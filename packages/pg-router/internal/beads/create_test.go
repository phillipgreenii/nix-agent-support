package beads

import (
	"context"
	"encoding/json"
	"testing"
)

// argValue returns the value following flag in a recorded call, or "".
func argValue(call []string, flag string) string {
	for i, a := range call {
		if a == flag && i+1 < len(call) {
			return call[i+1]
		}
	}
	return ""
}

func TestCreate(t *testing.T) {
	fr := &scriptRunner{responses: map[string]string{"create": "zr-99\n"}}
	meta := map[string]any{"repo": "o/r", "pr_number": 7, "branch": "feat/x", "head_sha": "abc123"}
	id, err := Create(context.Background(), fr, "task", "review-pr: o/r#7", meta)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if id != "zr-99" {
		t.Errorf("id = %q, want zr-99 (trimmed)", id)
	}
	call := findCall(fr.calls, "create", "--type=task", "--silent")
	if call == nil {
		t.Fatalf("expected create --type=task --silent; calls=%v", fr.calls)
	}
	if argValue(call, "--title") != "review-pr: o/r#7" {
		t.Errorf("--title = %q", argValue(call, "--title"))
	}
	// --metadata must carry the PR coords, with pr_number as a JSON number.
	var gotMeta map[string]any
	if err := json.Unmarshal([]byte(argValue(call, "--metadata")), &gotMeta); err != nil {
		t.Fatalf("metadata not valid JSON: %v (arg=%q)", err, argValue(call, "--metadata"))
	}
	if gotMeta["repo"] != "o/r" || gotMeta["branch"] != "feat/x" || gotMeta["head_sha"] != "abc123" {
		t.Errorf("metadata missing coords: %+v", gotMeta)
	}
	if n, ok := gotMeta["pr_number"].(float64); !ok || n != 7 {
		t.Errorf("pr_number must serialize as a JSON number 7, got %v (%T)", gotMeta["pr_number"], gotMeta["pr_number"])
	}
}

func TestLinkChild(t *testing.T) {
	fr := &scriptRunner{}
	if err := LinkChild(context.Background(), fr, "zr-99", "zr-mr"); err != nil {
		t.Fatalf("LinkChild: %v", err)
	}
	if findCall(fr.calls, "dep", "add", "zr-99", "zr-mr", "--type=parent-child", "--no-cycle-check") == nil {
		t.Errorf("expected dep add child parent --type=parent-child --no-cycle-check; calls=%v", fr.calls)
	}
}

func TestMatchMergeRequest(t *testing.T) {
	issues := []Issue{
		{ID: "zr-mr7", Type: "merge-request", Metadata: map[string]any{"repo": "o/r", "pr_number": float64(7)}},
		{ID: "zr-mr9", Type: "merge-request", Metadata: map[string]any{"repo": "o/r", "pr_number": float64(9)}},
	}
	if mr := MatchMergeRequest(issues, "o/r", 7); mr == nil || mr.ID != "zr-mr7" {
		t.Fatalf("expected zr-mr7, got %+v", mr)
	}
	// A missing PR yields nil (NH2: the ACL must NOT create an MR bead).
	if miss := MatchMergeRequest(issues, "o/r", 404); miss != nil {
		t.Errorf("expected nil for a missing MR, got %+v", miss)
	}
}

func TestMatchReviewPR(t *testing.T) {
	issues := []Issue{
		{ID: "zr-rv7", Type: "task", Title: "review-pr: o/r#7", Metadata: map[string]any{"repo": "o/r", "pr_number": float64(7)}},
		// pg-pr's own draft-review task bead for the same PR MUST NOT match.
		{ID: "zr-df7", Type: "task", Title: "draft-review: o/r#7", Metadata: map[string]any{"repo": "o/r", "pr_number": float64(7)}},
		{ID: "zr-rv9", Type: "task", Title: "review-pr: o/r#9", Metadata: map[string]any{"repo": "o/r", "pr_number": float64(9)}},
	}
	if rv := MatchReviewPR(issues, "o/r", 7); rv == nil || rv.ID != "zr-rv7" {
		t.Fatalf("expected zr-rv7 (review-pr prefix + repo#7), got %+v", rv)
	}
	if miss := MatchReviewPR(issues, "o/r", 404); miss != nil {
		t.Errorf("expected nil for a missing review-pr, got %+v", miss)
	}
	// A CLOSED review-pr must still match (so the ACL skips it — no resurrection).
	closed := []Issue{
		{ID: "zr-rv7", Type: "task", Status: "closed", Title: "review-pr: o/r#7", Metadata: map[string]any{"repo": "o/r", "pr_number": float64(7)}},
	}
	if rv := MatchReviewPR(closed, "o/r", 7); rv == nil || rv.Status != "closed" {
		t.Fatalf("a closed review-pr must be found (no-resurrection), got %+v", rv)
	}
}
