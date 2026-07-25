package beads

import (
	"context"
	"strings"
	"testing"
)

// writePathRunner records every bd invocation and returns canned JSON per
// subcommand, so tests can assert whether a lookup shelled out to `bd list`
// (a full scan) or was answered from the attached TickCache with no bd call.
type writePathRunner struct {
	calls [][]string
	// listMR is returned for `list --type=merge-request ...` (findByRepoPR /
	// ListMergeRequests). listByID is returned for `list --all --id=... --json`
	// (getMergeRequestUncached).
	listMR   string
	listByID string
}

func (r *writePathRunner) Run(_ context.Context, args ...string) (string, error) {
	r.calls = append(r.calls, args)
	joined := strings.Join(args, " ")
	switch {
	case strings.Contains(joined, "--type=merge-request"):
		return r.listMR, nil
	case strings.Contains(joined, "--id="):
		return r.listByID, nil
	default:
		return "", nil
	}
}

// listCalls returns the recorded invocations whose first arg is "list".
func (r *writePathRunner) listCalls() [][]string {
	var out [][]string
	for _, c := range r.calls {
		if len(c) > 0 && c[0] == "list" {
			out = append(out, c)
		}
	}
	return out
}

// sawArg reports whether any recorded call contained the given substring in
// its joined args.
func (r *writePathRunner) sawArg(sub string) bool {
	for _, c := range r.calls {
		if strings.Contains(strings.Join(c, " "), sub) {
			return true
		}
	}
	return false
}

// sawUpdate reports whether any `update` call was recorded (a write).
func (r *writePathRunner) sawUpdate() bool {
	for _, c := range r.calls {
		if len(c) > 0 && c[0] == "update" {
			return true
		}
	}
	return false
}

// TestFindByRepoAndNumber_CacheHit_NoBDList proves a repo+PR lookup that HITS
// the attached per-tick cache is answered from memory with ZERO bd calls.
func TestFindByRepoAndNumber_CacheHit_NoBDList(t *testing.T) {
	ctx := context.Background()
	r := &writePathRunner{}
	c := NewClientWithRunner(r).UseTickCache(&TickCache{
		MergeRequestsByID: map[string]MergeRequest{
			"mr-31": {ID: "mr-31", Status: "open", Fields: MergeRequestFields{Repo: "foo/bar", PRNumber: 31}},
		},
	})

	got, err := c.FindByRepoAndNumber(ctx, "foo/bar", 31)
	if err != nil {
		t.Fatalf("FindByRepoAndNumber: %v", err)
	}
	if got == nil || got.ID != "mr-31" {
		t.Fatalf("expected cached mr-31, got %+v", got)
	}
	if n := len(r.listCalls()); n != 0 {
		t.Fatalf("cache hit must issue no bd list; got %d list call(s): %v", n, r.listCalls())
	}
}

// TestFindByRepoAndNumber_CacheMiss_FallsBackToScan proves a repo+PR lookup
// that MISSES the cache falls back to the full scan and still resolves the
// bead — never a false "not found" from a stale snapshot.
func TestFindByRepoAndNumber_CacheMiss_FallsBackToScan(t *testing.T) {
	ctx := context.Background()
	r := &writePathRunner{
		// Fresh scan holds the bead the cache (deliberately) does not.
		listMR: `[{"id":"mr-99","status":"open","issue_type":"merge-request","metadata":{"repo":"foo/bar","pr_number":99}}]`,
	}
	// Cache is present but holds a DIFFERENT bead — this PR is a miss.
	c := NewClientWithRunner(r).UseTickCache(&TickCache{
		MergeRequestsByID: map[string]MergeRequest{
			"mr-1": {ID: "mr-1", Status: "open", Fields: MergeRequestFields{Repo: "other/repo", PRNumber: 1}},
		},
	})

	got, err := c.FindByRepoAndNumber(ctx, "foo/bar", 99)
	if err != nil {
		t.Fatalf("FindByRepoAndNumber: %v", err)
	}
	if got == nil || got.ID != "mr-99" {
		t.Fatalf("cache miss must fall back to scan and resolve mr-99, got %+v", got)
	}
	if !r.sawArg("--type=merge-request") {
		t.Fatalf("cache miss must fall back to a `bd list --type=merge-request` scan; calls: %v", r.calls)
	}
}

// TestGetMergeRequest_CacheHit_NoBDList proves a by-id lookup that HITS the
// cache is answered from memory with ZERO bd calls.
func TestGetMergeRequest_CacheHit_NoBDList(t *testing.T) {
	ctx := context.Background()
	r := &writePathRunner{}
	c := NewClientWithRunner(r).UseTickCache(&TickCache{
		MergeRequestsByID: map[string]MergeRequest{
			"mr-1": {ID: "mr-1", Status: "open", Fields: MergeRequestFields{Repo: "foo/bar", PRNumber: 7}},
		},
	})

	got, err := c.GetMergeRequest(ctx, "mr-1")
	if err != nil {
		t.Fatalf("GetMergeRequest: %v", err)
	}
	if got == nil || got.ID != "mr-1" {
		t.Fatalf("expected cached mr-1, got %+v", got)
	}
	if n := len(r.listCalls()); n != 0 {
		t.Fatalf("cache hit must issue no bd list; got %d list call(s): %v", n, r.listCalls())
	}
}

// TestGetMergeRequest_CacheMiss_FallsBackToScan proves a by-id lookup that
// MISSES the cache falls back to the `bd list --id` scan and resolves.
func TestGetMergeRequest_CacheMiss_FallsBackToScan(t *testing.T) {
	ctx := context.Background()
	r := &writePathRunner{
		listByID: `[{"id":"mr-2","status":"open","issue_type":"merge-request","metadata":{"repo":"foo/bar","pr_number":8}}]`,
	}
	c := NewClientWithRunner(r).UseTickCache(&TickCache{
		MergeRequestsByID: map[string]MergeRequest{
			"mr-1": {ID: "mr-1", Status: "open", Fields: MergeRequestFields{Repo: "foo/bar", PRNumber: 7}},
		},
	})

	got, err := c.GetMergeRequest(ctx, "mr-2")
	if err != nil {
		t.Fatalf("GetMergeRequest: %v", err)
	}
	if got == nil || got.ID != "mr-2" {
		t.Fatalf("cache miss must fall back to scan and resolve mr-2, got %+v", got)
	}
	if !r.sawArg("--id=mr-2") {
		t.Fatalf("cache miss must fall back to a `bd list --id` scan; calls: %v", r.calls)
	}
}

// TestEnsureMergeRequest_DiffReadsFreshNotStaleCache is the staleness-safety
// proof for FB-5: EnsureMergeRequest's FB-2 diff-before-write MUST read the
// bead's CURRENT stored fields (a fresh scan), never the tick-start cache.
//
// Setup: the attached cache holds a STALE view (branch "old") of the bead,
// while the fresh scan reports the current branch "new" == desired. If the diff
// consulted the cache it would see a delta ("old" != "new") and issue a
// needless `bd update` (re-introducing the no-op commit churn FB-1/FB-2
// eliminated). Reading fresh, the diff is a correct no-op.
func TestEnsureMergeRequest_DiffReadsFreshNotStaleCache(t *testing.T) {
	ctx := context.Background()
	r := &writePathRunner{
		// Fresh stored state: branch already "new" (== desired below).
		listMR: `[{"id":"mr-7","status":"open","issue_type":"merge-request","metadata":{"repo":"foo/bar","pr_number":7,"branch":"new","state":"open"}}]`,
	}
	// STALE cache: same bead, but branch "old". If the diff used this, it would
	// wrongly decide a write is needed.
	c := NewClientWithRunner(r).UseTickCache(&TickCache{
		MergeRequestsByID: map[string]MergeRequest{
			"mr-7": {ID: "mr-7", Status: "open", Fields: MergeRequestFields{Repo: "foo/bar", PRNumber: 7, Branch: "old", State: "open"}},
		},
	})

	id, alreadyClosed, err := c.EnsureMergeRequest(ctx, "foo/bar#7", MergeRequestFields{
		Repo: "foo/bar", PRNumber: 7, Branch: "new", State: "open",
	})
	if err != nil {
		t.Fatalf("EnsureMergeRequest: %v", err)
	}
	if alreadyClosed || id != "mr-7" {
		t.Fatalf("expected (mr-7, false), got (%q, %v)", id, alreadyClosed)
	}
	if !r.sawArg("--type=merge-request") {
		t.Fatalf("diff-before-write must read fresh state via a `bd list --type=merge-request` scan; calls: %v", r.calls)
	}
	if r.sawUpdate() {
		t.Fatalf("desired == fresh stored state: no `bd update` must be issued (a write here means the stale cache corrupted the diff); calls: %v", r.calls)
	}
}
