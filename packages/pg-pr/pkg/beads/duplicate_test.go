package beads

import (
	"context"
	"strings"
	"testing"
)

// listRunner serves canned `bd list` JSON discriminated by the flags of the
// call, and records every argv so a test can assert which writes were issued.
type listRunner struct {
	mergeRequests string // for `list --type=merge-request …`
	openTasks     string // for `list --type=task --status=open …`
	closedTasks   string // for `list --type=task --status=closed …`
	byID          string // for `list --all --id=<id> …`
	depList       string // for `dep list …`
	calls         [][]string
}

func (r *listRunner) Run(_ context.Context, args ...string) (string, error) {
	r.calls = append(r.calls, args)
	joined := strings.Join(args, " ")
	switch {
	case len(args) >= 2 && args[0] == "dep" && args[1] == "list":
		return orEmptyList(r.depList), nil
	case args[0] == "create":
		return "new-bead", nil
	case args[0] != "list":
		return "", nil
	case strings.Contains(joined, "--id="):
		return orEmptyList(r.byID), nil
	case strings.Contains(joined, "--type=merge-request"):
		return orEmptyList(r.mergeRequests), nil
	case strings.Contains(joined, "--status=open"):
		return orEmptyList(r.openTasks), nil
	case strings.Contains(joined, "--status=closed"):
		return orEmptyList(r.closedTasks), nil
	}
	return orEmptyList(""), nil
}

func orEmptyList(s string) string {
	if s == "" {
		return `{"data":[]}`
	}
	return s
}

// wrote reports whether any recorded call started with the given bd verb.
func (r *listRunner) wrote(verb string) bool {
	for _, c := range r.calls {
		if len(c) > 0 && c[0] == verb {
			return true
		}
	}
	return false
}

// TestEnsureMergeRequestUpdatesCanonicalNeverCreatesASecond reproduces the
// duplicate merge-request defect: (repo, pr_number) already resolves to TWO
// beads. A re-sync must UPDATE one of them — deterministically the canonical one
// — and must never add a third.
func TestEnsureMergeRequestUpdatesCanonicalNeverCreatesASecond(t *testing.T) {
	// mr-b is the actively-synced bead (newer last_synced_at); mr-a is the stale
	// duplicate that "never re-synced". Row order deliberately puts the stale one
	// first, so a first-match-wins pick would choose wrongly.
	r := &listRunner{mergeRequests: `{"data":[
      {"id":"mr-a","title":"o/r#7","status":"open","issue_type":"merge-request",
       "metadata":{"repo":"o/r","pr_number":7,"last_synced_at":"2026-07-01T00:00:00Z","state":"open"}},
      {"id":"mr-b","title":"o/r#7","status":"open","issue_type":"merge-request",
       "metadata":{"repo":"o/r","pr_number":7,"last_synced_at":"2026-07-29T00:00:00Z","state":"open"}}
    ]}`}
	c := NewClientWithRunner(r)

	id, alreadyClosed, err := c.EnsureMergeRequest(context.Background(), "t", MergeRequestFields{
		Repo: "o/r", PRNumber: 7, State: "open", Branch: "feat",
	})
	if err != nil {
		t.Fatalf("EnsureMergeRequest: %v", err)
	}
	if alreadyClosed {
		t.Fatal("open duplicates must not report alreadyClosed")
	}
	if id != "mr-b" {
		t.Fatalf("canonical bead = %q, want mr-b (the most recently synced one)", id)
	}
	if r.wrote("create") {
		t.Fatalf("re-sync created ANOTHER merge-request bead for an existing pair: %v", r.calls)
	}
	if !r.wrote("update") {
		t.Fatalf("expected the canonical bead to be updated, got calls %v", r.calls)
	}
}

// TestCanonicalMergeRequestPickIsDeterministic pins the precedence rules so the
// choice cannot drift with bd's row order (which would move where children are
// parented from tick to tick).
func TestCanonicalMergeRequestPickIsDeterministic(t *testing.T) {
	closedNewer := MergeRequest{ID: "mr-closed", Status: "closed", Fields: MergeRequestFields{LastSyncedAt: "2026-07-30T00:00:00Z"}}
	openOlder := MergeRequest{ID: "mr-open", Status: "open", Fields: MergeRequestFields{LastSyncedAt: "2026-07-01T00:00:00Z"}}
	openSameA := MergeRequest{ID: "mr-aaa", Status: "open"}
	openSameB := MergeRequest{ID: "mr-bbb", Status: "open"}

	cases := []struct {
		name    string
		in      []MergeRequest
		wantID  string
		wantNil bool
	}{
		{name: "none", in: nil, wantNil: true},
		{name: "open beats a newer closed", in: []MergeRequest{closedNewer, openOlder}, wantID: "mr-open"},
		{name: "open beats a newer closed (reversed order)", in: []MergeRequest{openOlder, closedNewer}, wantID: "mr-open"},
		{name: "ties break on the smallest id", in: []MergeRequest{openSameB, openSameA}, wantID: "mr-aaa"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := pickCanonicalMergeRequest(tc.in)
			if tc.wantNil {
				if got != nil {
					t.Fatalf("got %+v want nil", got)
				}
				return
			}
			if got == nil || got.ID != tc.wantID {
				t.Fatalf("got %+v want id %q", got, tc.wantID)
			}
		})
	}
}

// TestFindDuplicateMergeRequests verifies the read-only audit: it reports the
// pairs that resolve to more than one bead, names the canonical one, and lists
// the excess. It must issue NO write.
func TestFindDuplicateMergeRequests(t *testing.T) {
	r := &listRunner{mergeRequests: `{"data":[
      {"id":"mr-a","status":"open","issue_type":"merge-request","metadata":{"repo":"o/r","pr_number":7,"last_synced_at":"2026-07-01T00:00:00Z"}},
      {"id":"mr-b","status":"open","issue_type":"merge-request","metadata":{"repo":"o/r","pr_number":7,"last_synced_at":"2026-07-29T00:00:00Z"}},
      {"id":"mr-c","status":"open","issue_type":"merge-request","metadata":{"repo":"o/r","pr_number":8}},
      {"id":"mr-d","status":"open","issue_type":"merge-request","metadata":{}}
    ]}`}
	c := NewClientWithRunner(r)

	dups, err := c.FindDuplicateMergeRequests(context.Background())
	if err != nil {
		t.Fatalf("FindDuplicateMergeRequests: %v", err)
	}
	if len(dups) != 1 {
		t.Fatalf("expected exactly one duplicated pair, got %d: %+v", len(dups), dups)
	}
	if dups[0].Repo != "o/r" || dups[0].PRNumber != 7 {
		t.Errorf("pair = %s#%d, want o/r#7", dups[0].Repo, dups[0].PRNumber)
	}
	if dups[0].Canonical.ID != "mr-b" {
		t.Errorf("canonical = %q, want mr-b", dups[0].Canonical.ID)
	}
	if len(dups[0].Excess) != 1 || dups[0].Excess[0].ID != "mr-a" {
		t.Errorf("excess = %+v, want [mr-a]", dups[0].Excess)
	}
	for _, verb := range []string{"create", "update", "close"} {
		if r.wrote(verb) {
			t.Errorf("the audit must be read-only, but issued `bd %s`: %v", verb, r.calls)
		}
	}
}

// TestResolveProcessingCycleFindsOpenCycleAcrossParents is the root-cause test
// for the duplicate process-feedback beads: the open cycle for this PR hangs off
// a DIFFERENT merge-request bead (the PR had two). Keyed on (repo, pr_number) it
// is still found, so the caller updates it instead of creating a second one.
// Pre-fix the parent-scoped lookup returned "none open" here.
func TestResolveProcessingCycleFindsOpenCycleAcrossParents(t *testing.T) {
	r := &listRunner{
		openTasks: `{"data":[{"id":"cyc-1","title":"process-feedback: o/r#7","status":"open",
                              "issue_type":"task","labels":["mine","fbsum:abc"]}]}`,
		// The resolved parent has NO children — the cycle is parented under the
		// duplicate merge-request bead.
		depList: `{"data":[]}`,
	}
	c := NewClientWithRunner(r)

	st, err := c.ResolveProcessingCycle(context.Background(), ProcessingCycleKey("o/r", 7), "mr-canonical")
	if err != nil {
		t.Fatalf("ResolveProcessingCycle: %v", err)
	}
	if st.Open == nil {
		t.Fatal("open cycle for o/r#7 not found by key — a second cycle would be created")
	}
	if st.Open.ID != "cyc-1" {
		t.Errorf("open cycle = %q, want cyc-1", st.Open.ID)
	}
	if !containsStr(st.Open.Labels, "fbsum:abc") {
		t.Errorf("labels must round-trip so the caller can compare the set marker, got %v", st.Open.Labels)
	}
	if st.Closed != nil {
		t.Errorf("a live cycle must not also report a predecessor, got %+v", st.Closed)
	}
}

// TestResolveProcessingCycleTitleMatchIsExact guards the key comparison:
// `o/r#7` must not match `o/r#70`, or one PR's cycle would suppress another's.
func TestResolveProcessingCycleTitleMatchIsExact(t *testing.T) {
	r := &listRunner{
		openTasks: `{"data":[{"id":"cyc-70","title":"process-feedback: o/r#70","status":"open","issue_type":"task"}]}`,
	}
	c := NewClientWithRunner(r)
	st, err := c.ResolveProcessingCycle(context.Background(), ProcessingCycleKey("o/r", 7), "")
	if err != nil {
		t.Fatalf("ResolveProcessingCycle: %v", err)
	}
	if st.Open != nil {
		t.Fatalf("o/r#7 must not match o/r#70, got %+v", st.Open)
	}
}

// TestResolveProcessingCycleReportsNewestClosedPredecessor verifies the
// closed-predecessor lookup: only consulted when nothing is open, and it returns
// the most recent one so the successor references the right bead.
func TestResolveProcessingCycleReportsNewestClosedPredecessor(t *testing.T) {
	r := &listRunner{
		closedTasks: `{"data":[
          {"id":"cyc-old","title":"process-feedback: o/r#7","status":"closed","issue_type":"task","created_at":"2026-07-01T00:00:00Z","labels":["fbsum:one"]},
          {"id":"cyc-new","title":"process-feedback: o/r#7","status":"closed","issue_type":"task","created_at":"2026-07-28T00:00:00Z","labels":["fbsum:two"]}
        ]}`,
	}
	c := NewClientWithRunner(r)
	st, err := c.ResolveProcessingCycle(context.Background(), ProcessingCycleKey("o/r", 7), "")
	if err != nil {
		t.Fatalf("ResolveProcessingCycle: %v", err)
	}
	if st.Open != nil {
		t.Fatalf("nothing is open, got %+v", st.Open)
	}
	if st.Closed == nil || st.Closed.ID != "cyc-new" {
		t.Fatalf("closed predecessor = %+v, want cyc-new (the newest)", st.Closed)
	}
	if !containsStr(st.Closed.Labels, "fbsum:two") {
		t.Errorf("predecessor labels must round-trip, got %v", st.Closed.Labels)
	}
}

// TestResolveProcessingCyclePrefersOpenOverClosed pins that a closed
// predecessor is NOT reported while a live cycle exists (the caller's branches
// are mutually exclusive).
func TestResolveProcessingCyclePrefersOpenOverClosed(t *testing.T) {
	r := &listRunner{
		openTasks:   `{"data":[{"id":"cyc-live","title":"process-feedback: o/r#7","status":"open","issue_type":"task"}]}`,
		closedTasks: `{"data":[{"id":"cyc-dead","title":"process-feedback: o/r#7","status":"closed","issue_type":"task"}]}`,
	}
	c := NewClientWithRunner(r)
	st, err := c.ResolveProcessingCycle(context.Background(), ProcessingCycleKey("o/r", 7), "")
	if err != nil {
		t.Fatalf("ResolveProcessingCycle: %v", err)
	}
	if st.Open == nil || st.Open.ID != "cyc-live" || st.Closed != nil {
		t.Fatalf("want only the live cycle, got open=%+v closed=%+v", st.Open, st.Closed)
	}
}

// TestFindDuplicateProcessingCycles verifies the read-only audit that measures
// the defect: open process-feedback beads sharing one (repo, pr_number) key.
func TestFindDuplicateProcessingCycles(t *testing.T) {
	r := &listRunner{openTasks: `{"data":[
      {"id":"cyc-b","title":"process-feedback: o/r#7","status":"open","issue_type":"task"},
      {"id":"cyc-a","title":"process-feedback: o/r#7","status":"open","issue_type":"task"},
      {"id":"cyc-c","title":"process-feedback: o/r#8","status":"open","issue_type":"task"},
      {"id":"other","title":"attention: o/r#8","status":"open","issue_type":"task"}
    ]}`}
	c := NewClientWithRunner(r)

	dups, err := c.FindDuplicateProcessingCycles(context.Background())
	if err != nil {
		t.Fatalf("FindDuplicateProcessingCycles: %v", err)
	}
	if len(dups) != 1 {
		t.Fatalf("expected one duplicated key, got %d: %+v", len(dups), dups)
	}
	if dups[0].Key != "o/r#7" {
		t.Errorf("key = %q, want o/r#7", dups[0].Key)
	}
	if dups[0].Canonical.ID != "cyc-a" {
		t.Errorf("canonical = %q, want cyc-a (smallest id)", dups[0].Canonical.ID)
	}
	if len(dups[0].Excess) != 1 || dups[0].Excess[0].ID != "cyc-b" {
		t.Errorf("excess = %+v, want [cyc-b]", dups[0].Excess)
	}
	for _, verb := range []string{"create", "update", "close"} {
		if r.wrote(verb) {
			t.Errorf("the audit must be read-only, but issued `bd %s`: %v", verb, r.calls)
		}
	}
}

// TestCreateProcessingCycleWritesDescriptionAndLabels pins criterion 5 at the bd
// boundary: the description and the set-marker label reach `bd create`.
func TestCreateProcessingCycleWritesDescriptionAndLabels(t *testing.T) {
	r := &listRunner{}
	c := NewClientWithRunner(r)
	if _, err := c.CreateProcessingCycle(context.Background(), CreateProcessingCycleInput{
		PRBeadID: "mr-1", Key: "o/r#7", Description: "2 unaddressed item(s): ci-failure x2.",
		Mine: true, Labels: []string{"fbsum:abc"},
	}); err != nil {
		t.Fatalf("CreateProcessingCycle: %v", err)
	}
	var create []string
	for _, call := range r.calls {
		if len(call) > 0 && call[0] == "create" {
			create = call
		}
	}
	joined := strings.Join(create, " ")
	for _, want := range []string{
		"--title process-feedback: o/r#7",
		"-d 2 unaddressed item(s): ci-failure x2.",
		"-l mine",
		"-l fbsum:abc",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("`bd create` args missing %q; got %q", want, joined)
		}
	}
}

// TestAppendProcessingCycleNoteIsOneUpdate pins that the note and the marker
// swap ride in a SINGLE `bd update` — split across two calls, an interruption
// leaves the bead's marker disagreeing with its notes and the next tick appends
// a duplicate.
func TestAppendProcessingCycleNoteIsOneUpdate(t *testing.T) {
	r := &listRunner{}
	c := NewClientWithRunner(r)
	if err := c.AppendProcessingCycleNote(context.Background(), "cyc-1", "1 unaddressed item(s).", "fbsum:new", []string{"fbsum:old"}); err != nil {
		t.Fatalf("AppendProcessingCycleNote: %v", err)
	}
	updates := 0
	var got []string
	for _, call := range r.calls {
		if len(call) > 0 && call[0] == "update" {
			updates++
			got = call
		}
	}
	if updates != 1 {
		t.Fatalf("expected exactly one `bd update`, got %d (%v)", updates, r.calls)
	}
	joined := strings.Join(got, " ")
	for _, want := range []string{"--append-notes 1 unaddressed item(s).", "--add-label fbsum:new", "--remove-label fbsum:old"} {
		if !strings.Contains(joined, want) {
			t.Errorf("update args missing %q; got %q", want, joined)
		}
	}
}

func containsStr(hay []string, want string) bool {
	for _, s := range hay {
		if s == want {
			return true
		}
	}
	return false
}
