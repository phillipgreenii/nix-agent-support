package beads

import (
	"context"
	"testing"
)

// readyRunner returns canned `bd ready --json` output and records calls.
type readyRunner struct {
	calls    [][]string
	ready    string
	readyErr error
}

func (r *readyRunner) Run(_ context.Context, args ...string) (string, error) {
	r.calls = append(r.calls, args)
	if len(args) >= 1 && args[0] == "ready" {
		if r.readyErr != nil {
			return "", r.readyErr
		}
		return r.ready, nil
	}
	return "", nil
}

func TestListReadyDraftReviews_FiltersAndParses(t *testing.T) {
	// bd ready --json envelope: a draft-review task (mine), a draft-review task
	// (team, no mine label), and a non-draft-review task that MUST be excluded.
	runner := &readyRunner{ready: `{"data":[
		{"id":"pg2-a.1","title":"draft-review: owner/repo#123","status":"open","issue_type":"task","labels":["mine","pg-pr"]},
		{"id":"pg2-a.2","title":"draft-review: owner/other#7","status":"open","issue_type":"task","labels":["pg-pr"]},
		{"id":"pg2-a.3","title":"process-feedback: owner/repo#123","status":"open","issue_type":"task","labels":[]},
		{"id":"pg2-a.4","title":"some unrelated feature","status":"open","issue_type":"feature","labels":[]}
	]}`}
	c := NewClientWithRunner(runner)

	refs, err := c.ListReadyDraftReviews(context.Background())
	if err != nil {
		t.Fatalf("ListReadyDraftReviews: %v", err)
	}
	if len(refs) != 2 {
		t.Fatalf("want 2 draft-review refs, got %d: %+v", len(refs), refs)
	}

	byID := map[string]DraftReviewRef{}
	for _, r := range refs {
		byID[r.ID] = r
	}

	mine := byID["pg2-a.1"]
	if mine.Repo != "owner/repo" || mine.Number != 123 || !mine.Mine {
		t.Fatalf("mine ref parsed wrong: %+v", mine)
	}
	team := byID["pg2-a.2"]
	if team.Repo != "owner/other" || team.Number != 7 || team.Mine {
		t.Fatalf("team ref parsed wrong: %+v", team)
	}

	// It must call `bd ready --json`.
	sawReady := false
	for _, call := range runner.calls {
		if len(call) >= 1 && call[0] == "ready" {
			sawReady = true
		}
	}
	if !sawReady {
		t.Fatalf("expected a `bd ready` call, calls=%v", runner.calls)
	}
}

func TestListReadyDraftReviews_Empty(t *testing.T) {
	runner := &readyRunner{ready: `{"data":[]}`}
	c := NewClientWithRunner(runner)
	refs, err := c.ListReadyDraftReviews(context.Background())
	if err != nil {
		t.Fatalf("ListReadyDraftReviews: %v", err)
	}
	if len(refs) != 0 {
		t.Fatalf("want 0 refs, got %d", len(refs))
	}
}

// A malformed title (no owner/repo#number) is skipped, not fatal.
func TestListReadyDraftReviews_SkipsUnparseableTitle(t *testing.T) {
	runner := &readyRunner{ready: `{"data":[
		{"id":"pg2-a.9","title":"draft-review: garbage-no-number","status":"open","issue_type":"task","labels":[]}
	]}`}
	c := NewClientWithRunner(runner)
	refs, err := c.ListReadyDraftReviews(context.Background())
	if err != nil {
		t.Fatalf("ListReadyDraftReviews: %v", err)
	}
	if len(refs) != 0 {
		t.Fatalf("unparseable title must be skipped, got %+v", refs)
	}
}
