package beads

import (
	"context"
	"testing"
)

// findPRRunner scripts bd responses for FindDraftReviewForPR:
//   - `list --type=merge-request` → the MR list (with metadata repo/pr_number)
//   - `dep list <mr> --direction=up` → the MR's children ids
//   - `list --type=task --all` → the task beads (incl. closed) w/ titles+status
type findPRRunner struct {
	mrs      string
	children string
	tasks    string
}

func (r *findPRRunner) Run(_ context.Context, args ...string) (string, error) {
	switch {
	case len(args) >= 2 && args[0] == "dep" && args[1] == "list":
		return r.children, nil
	case len(args) >= 2 && args[0] == "list" && contains(args, "--type=merge-request"):
		return r.mrs, nil
	case len(args) >= 2 && args[0] == "list" && contains(args, "--type=task"):
		return r.tasks, nil
	}
	return "", nil
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func TestFindDraftReviewForPR_ReturnsClosedChild(t *testing.T) {
	r := &findPRRunner{
		mrs:      `{"data":[{"id":"mr-1","title":"MR","status":"open","issue_type":"merge-request","metadata":{"repo":"o/r","pr_number":5}}]}`,
		children: `{"data":[{"id":"dr-1"}]}`,
		tasks:    `{"data":[{"id":"dr-1","title":"draft-review: o/r#5","status":"closed","issue_type":"task","labels":["mine"]}]}`,
	}
	c := NewClientWithRunner(r)

	id, closed, found, err := c.FindDraftReviewForPR(context.Background(), "o/r", 5)
	if err != nil {
		t.Fatalf("FindDraftReviewForPR: %v", err)
	}
	if !found || id != "dr-1" || !closed {
		t.Fatalf("want (dr-1, closed=true, found=true), got (%q, %v, %v)", id, closed, found)
	}
}

func TestFindDraftReviewForPR_NoMatchingMR(t *testing.T) {
	r := &findPRRunner{
		mrs: `{"data":[{"id":"mr-1","title":"MR","status":"open","issue_type":"merge-request","metadata":{"repo":"o/r","pr_number":99}}]}`,
	}
	c := NewClientWithRunner(r)
	_, _, found, err := c.FindDraftReviewForPR(context.Background(), "o/r", 5)
	if err != nil {
		t.Fatalf("FindDraftReviewForPR: %v", err)
	}
	if found {
		t.Fatalf("expected not found for a PR with no MR bead")
	}
}
