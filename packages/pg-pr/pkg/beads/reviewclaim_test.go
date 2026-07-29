package beads

import (
	"context"
	"strings"
	"testing"
)

// claimRunner records every bd invocation and returns canned show output.
type claimRunner struct {
	calls   [][]string
	showOut string
	showErr error
	err     error
}

func (r *claimRunner) Run(_ context.Context, args ...string) (string, error) {
	r.calls = append(r.calls, args)
	if len(args) >= 1 && args[0] == "show" {
		return r.showOut, r.showErr
	}
	return "", r.err
}

func (r *claimRunner) sawArgs(want ...string) bool {
	for _, call := range r.calls {
		if len(call) < len(want) {
			continue
		}
		match := true
		for i := range want {
			if call[i] != want[i] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// sawOneCallWith reports whether a SINGLE bd invocation carried every given
// token. Release paths must clear status and assignee in one atomic `bd update`
// — two separate calls would leave a window in which the bead is `open` but
// still claimed, so a per-call check is required, not a global one.
func (r *claimRunner) sawOneCallWith(want ...string) bool {
	for _, call := range r.calls {
		have := make(map[string]struct{}, len(call))
		for _, a := range call {
			have[a] = struct{}{}
		}
		ok := true
		for _, w := range want {
			if _, found := have[w]; !found {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func TestClaimDraftReview_CallsUpdateClaim(t *testing.T) {
	r := &claimRunner{}
	c := NewClientWithRunner(r)
	if err := c.ClaimDraftReview(context.Background(), "pg2-a.1"); err != nil {
		t.Fatalf("ClaimDraftReview: %v", err)
	}
	if !r.sawArgs("update", "pg2-a.1", "--claim") {
		t.Fatalf("expected `update pg2-a.1 --claim`, calls=%v", r.calls)
	}
}

func TestUnclaimDraftReview_ReopensToReady(t *testing.T) {
	r := &claimRunner{}
	c := NewClientWithRunner(r)
	if err := c.UnclaimDraftReview(context.Background(), "pg2-a.1"); err != nil {
		t.Fatalf("UnclaimDraftReview: %v", err)
	}
	// It must set status back to open so the bead re-appears in `bd ready`.
	if !r.sawArgs("update", "pg2-a.1", "--status", "open") {
		t.Fatalf("expected status=open, calls=%v", r.calls)
	}
}

// TestUnclaimDraftReview_ClearsAssigneeAtomically is the pg2-jcljm regression
// guard. Setting status=open WITHOUT clearing the assignee strands the bead:
// `bd ready --claim` correctly skips an already-claimed bead, and the next
// ClaimDraftReview's `bd update --claim` fails "issue already claimed by <name>"
// — while a stale-`in_progress` sweep never sees it, because it is `open`.
func TestUnclaimDraftReview_ClearsAssigneeAtomically(t *testing.T) {
	r := &claimRunner{}
	c := NewClientWithRunner(r)
	if err := c.UnclaimDraftReview(context.Background(), "pg2-a.1"); err != nil {
		t.Fatalf("UnclaimDraftReview: %v", err)
	}
	if !r.sawOneCallWith("update", "pg2-a.1", "--status", "open", "--assignee=") {
		t.Fatalf("release must clear status AND assignee in ONE bd update, calls=%v", r.calls)
	}
}

func TestCloseDraftReview_ClosesWithReason(t *testing.T) {
	r := &claimRunner{}
	c := NewClientWithRunner(r)
	if err := c.CloseDraftReview(context.Background(), "pg2-a.1", "reviewed"); err != nil {
		t.Fatalf("CloseDraftReview: %v", err)
	}
	if !r.sawArgs("close", "pg2-a.1") {
		t.Fatalf("expected close, calls=%v", r.calls)
	}
	// reason present
	found := false
	for _, call := range r.calls {
		if len(call) >= 1 && call[0] == "close" && strings.Contains(strings.Join(call, " "), "reviewed") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected close reason 'reviewed', calls=%v", r.calls)
	}
}

func TestReopenDraftReview_SetsStatusOpen(t *testing.T) {
	r := &claimRunner{}
	c := NewClientWithRunner(r)
	if err := c.ReopenDraftReview(context.Background(), "pg2-a.1"); err != nil {
		t.Fatalf("ReopenDraftReview: %v", err)
	}
	if !r.sawArgs("update", "pg2-a.1", "--status", "open") {
		t.Fatalf("expected status=open, calls=%v", r.calls)
	}
}

// TestReopenDraftReview_ClearsAssignee pins the JR4 invariant stated in
// docs/pr-review-flow.md ("on reopen ... the assignee cleared"). bd's close does
// not drop the assignee, so reopening without clearing it yields the stranded
// `open`+assignee state and the next claim fails (pg2-jcljm).
func TestReopenDraftReview_ClearsAssignee(t *testing.T) {
	r := &claimRunner{}
	c := NewClientWithRunner(r)
	if err := c.ReopenDraftReview(context.Background(), "pg2-a.1"); err != nil {
		t.Fatalf("ReopenDraftReview: %v", err)
	}
	if !r.sawOneCallWith("update", "pg2-a.1", "--status", "open", "--assignee=") {
		t.Fatalf("reopen must clear status AND assignee in ONE bd update, calls=%v", r.calls)
	}
}

func TestDeadLetterDraftReview_BlocksAndLabels(t *testing.T) {
	r := &claimRunner{}
	c := NewClientWithRunner(r)
	if err := c.DeadLetterDraftReview(context.Background(), "pg2-a.1"); err != nil {
		t.Fatalf("DeadLetterDraftReview: %v", err)
	}
	joined := ""
	for _, call := range r.calls {
		joined += strings.Join(call, " ") + "\n"
	}
	if !strings.Contains(joined, "blocked") {
		t.Fatalf("expected status blocked, calls=%v", r.calls)
	}
	if !strings.Contains(joined, "needs-human") {
		t.Fatalf("expected needs-human label, calls=%v", r.calls)
	}
}

// TestDeadLetterDraftReview_ClearsAssignee — dead-lettering is a release, so it
// must not leave the daemon's claim behind: a human who later un-blocks the bead
// to `open` would otherwise land directly in the stranded state (pg2-jcljm).
func TestDeadLetterDraftReview_ClearsAssignee(t *testing.T) {
	r := &claimRunner{}
	c := NewClientWithRunner(r)
	if err := c.DeadLetterDraftReview(context.Background(), "pg2-a.1"); err != nil {
		t.Fatalf("DeadLetterDraftReview: %v", err)
	}
	if !r.sawOneCallWith("update", "pg2-a.1", "--status", "blocked", needsHumanLabel, "--assignee=") {
		t.Fatalf("dead-letter must park AND clear assignee in ONE bd update, calls=%v", r.calls)
	}
}

func TestReviewFailCount_ReadsFromLabels(t *testing.T) {
	r := &claimRunner{showOut: `{"data":[{"id":"pg2-a.1","title":"draft-review: o/r#1","status":"open","issue_type":"task","labels":["mine","review-fail-count-2"]}]}`}
	c := NewClientWithRunner(r)
	n, err := c.ReviewFailCount(context.Background(), "pg2-a.1")
	if err != nil {
		t.Fatalf("ReviewFailCount: %v", err)
	}
	if n != 2 {
		t.Fatalf("fail count = %d, want 2", n)
	}
}

func TestReviewFailCount_ZeroWhenNoLabel(t *testing.T) {
	r := &claimRunner{showOut: `{"data":[{"id":"pg2-a.1","title":"draft-review: o/r#1","status":"open","issue_type":"task","labels":["mine"]}]}`}
	c := NewClientWithRunner(r)
	n, err := c.ReviewFailCount(context.Background(), "pg2-a.1")
	if err != nil {
		t.Fatalf("ReviewFailCount: %v", err)
	}
	if n != 0 {
		t.Fatalf("fail count = %d, want 0", n)
	}
}

func TestBumpReviewFailCount_ReplacesLabel(t *testing.T) {
	// Current count is 1; bumping must remove the old label and add count-2.
	r := &claimRunner{showOut: `{"data":[{"id":"pg2-a.1","title":"draft-review: o/r#1","status":"open","issue_type":"task","labels":["mine","review-fail-count-1"]}]}`}
	c := NewClientWithRunner(r)
	n, err := c.BumpReviewFailCount(context.Background(), "pg2-a.1")
	if err != nil {
		t.Fatalf("BumpReviewFailCount: %v", err)
	}
	if n != 2 {
		t.Fatalf("new count = %d, want 2", n)
	}
	joined := ""
	for _, call := range r.calls {
		joined += strings.Join(call, " ") + "\n"
	}
	if !strings.Contains(joined, "review-fail-count-2") {
		t.Fatalf("expected new label review-fail-count-2, calls=%v", r.calls)
	}
	if !strings.Contains(joined, "review-fail-count-1") {
		t.Fatalf("expected old label review-fail-count-1 removed, calls=%v", r.calls)
	}
}

func TestResetReviewFailCount_RemovesLabel(t *testing.T) {
	r := &claimRunner{showOut: `{"data":[{"id":"pg2-a.1","title":"draft-review: o/r#1","status":"open","issue_type":"task","labels":["mine","review-fail-count-2"]}]}`}
	c := NewClientWithRunner(r)
	if err := c.ResetReviewFailCount(context.Background(), "pg2-a.1"); err != nil {
		t.Fatalf("ResetReviewFailCount: %v", err)
	}
	joined := ""
	for _, call := range r.calls {
		joined += strings.Join(call, " ") + "\n"
	}
	if !strings.Contains(joined, "--remove-label") || !strings.Contains(joined, "review-fail-count-2") {
		t.Fatalf("expected remove of review-fail-count-2, calls=%v", r.calls)
	}
}
