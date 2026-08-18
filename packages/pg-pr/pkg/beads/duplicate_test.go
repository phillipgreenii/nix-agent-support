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
	allTasks      string // for `list --type=task --all …` (every status)
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
	case strings.Contains(joined, "--type=task") && strings.Contains(joined, "--all"):
		// `bd list --all` takes EVERY status, so this fixture must hold the
		// closed rows too — a status-agnostic caller that got only the open ones
		// back would silently look correct.
		return orEmptyList(r.allTasks), nil
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
// the defect: process-feedback beads sharing one (repo, pr_number) key.
func TestFindDuplicateProcessingCycles(t *testing.T) {
	r := &listRunner{allTasks: `{"data":[
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

// closedPairFixture is the real shape of the pair that exposed the open-only
// defect: PR #104236's process-feedback beads zr-4jpnl / zr-agwaj, same key,
// created_at identical to the second (the duplication signature), and now BOTH
// CLOSED. Neither bead was removed, so the pair is still a duplicate.
// statuses lets one fixture be rendered open or closed.
func closedPairFixture(statusA, statusB string) string {
	return `{"data":[
      {"id":"zr-agwaj","title":"process-feedback: o/r#104236","status":"` + statusB + `","issue_type":"task","created_at":"2026-07-28T15:23:22Z"},
      {"id":"zr-4jpnl","title":"process-feedback: o/r#104236","status":"` + statusA + `","issue_type":"task","created_at":"2026-07-28T15:23:22Z"}
    ]}`
}

// excessTotal sums the excess beads across an audit result — the "total excess"
// number the operator's reconcile baseline and the "MUST NOT increase"
// regression check are both built on.
func excessTotal(dups []DuplicateProcessingCycles) int {
	n := 0
	for _, d := range dups {
		n += len(d.Excess)
	}
	return n
}

// TestFindDuplicateProcessingCyclesCountsAClosedPair is the regression test for
// pg2-0z8fw: a duplicate pair whose BOTH members are closed must still be
// reported. Closing a duplicate does not resolve it — both beads still exist —
// and the open-only scan let such a pair silently leave the report, so the
// reported total decayed (63 → 59 → 58) with nothing collapsed.
func TestFindDuplicateProcessingCyclesCountsAClosedPair(t *testing.T) {
	r := &listRunner{
		allTasks: closedPairFixture("closed", "closed"),
		// The open-only view of the same db is EMPTY — pre-fix this pair was
		// invisible, which is exactly the vanishing the fix undoes.
		openTasks: `{"data":[]}`,
	}
	c := NewClientWithRunner(r)

	dups, err := c.FindDuplicateProcessingCycles(context.Background())
	if err != nil {
		t.Fatalf("FindDuplicateProcessingCycles: %v", err)
	}
	if len(dups) != 1 {
		t.Fatalf("a CLOSED duplicate pair must still be reported, got %d groups: %+v", len(dups), dups)
	}
	if dups[0].Key != "o/r#104236" {
		t.Errorf("key = %q, want o/r#104236", dups[0].Key)
	}
	if dups[0].Canonical.ID != "zr-4jpnl" {
		t.Errorf("canonical = %q, want zr-4jpnl (smallest id; both closed)", dups[0].Canonical.ID)
	}
	if len(dups[0].Excess) != 1 || dups[0].Excess[0].ID != "zr-agwaj" {
		t.Errorf("excess = %+v, want [zr-agwaj]", dups[0].Excess)
	}
	for _, verb := range []string{"create", "update", "close"} {
		if r.wrote(verb) {
			t.Errorf("the audit must be read-only, but issued `bd %s`: %v", verb, r.calls)
		}
	}
}

// TestFindDuplicateProcessingCyclesTotalIsStableUnderClose pins the property the
// two consumers need: closing both members of a duplicate pair MUST NOT change
// the total. A total that moves on an unrelated close is unusable as a baseline
// and can mask a real new duplicate in a "MUST NOT increase" check.
func TestFindDuplicateProcessingCyclesTotalIsStableUnderClose(t *testing.T) {
	totals := map[string]int{}
	for _, tc := range []struct {
		name             string
		statusA, statusB string
	}{
		{name: "both open", statusA: "open", statusB: "open"},
		{name: "one closed", statusA: "closed", statusB: "open"},
		{name: "both closed", statusA: "closed", statusB: "closed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := NewClientWithRunner(&listRunner{allTasks: closedPairFixture(tc.statusA, tc.statusB)})
			dups, err := c.FindDuplicateProcessingCycles(context.Background())
			if err != nil {
				t.Fatalf("FindDuplicateProcessingCycles: %v", err)
			}
			totals[tc.name] = excessTotal(dups)
		})
	}
	if totals["both open"] != 1 {
		t.Fatalf("baseline total = %d, want 1", totals["both open"])
	}
	for _, name := range []string{"one closed", "both closed"} {
		if totals[name] != totals["both open"] {
			t.Errorf("total with %s = %d, want %d — the report must not decay as beads close",
				name, totals[name], totals["both open"])
		}
	}
}

// TestFindDuplicateProcessingCyclesScanIsStatusAgnostic pins the SELECTOR, not
// just the result: `bd list` excludes closed beads by DEFAULT, so dropping the
// flag would silently restore the open-only bug (and pass an empty argv element,
// corrupting the error message). The scan must ask for `--all` explicitly.
func TestFindDuplicateProcessingCyclesScanIsStatusAgnostic(t *testing.T) {
	r := &listRunner{allTasks: closedPairFixture("closed", "closed")}
	if _, err := NewClientWithRunner(r).FindDuplicateProcessingCycles(context.Background()); err != nil {
		t.Fatalf("FindDuplicateProcessingCycles: %v", err)
	}
	var scan []string
	for _, call := range r.calls {
		if len(call) > 0 && call[0] == "list" && containsStr(call, "--type=task") {
			scan = call
		}
	}
	if scan == nil {
		t.Fatalf("no `bd list --type=task` call recorded: %v", r.calls)
	}
	if !containsStr(scan, "--all") {
		t.Errorf("scan argv = %v, want --all (status-agnostic)", scan)
	}
	for _, arg := range scan {
		if arg == "" {
			t.Errorf("scan argv contains an EMPTY element, which corrupts the error message: %v", scan)
		}
		if strings.HasPrefix(arg, "--status") {
			t.Errorf("scan argv = %v, must not narrow by status", scan)
		}
	}
}

// TestFindDuplicateProcessingCyclesCanonicalPrefersOpen guards a hazard the
// status-agnostic scan introduces: the report prints `bd close <excess-id>` next
// to its "keep" pick, so a mixed open/closed group must name the LIVE cycle as
// canonical. Here the closed bead has the smaller id, so a pure smallest-id pick
// would advise closing the open one.
func TestFindDuplicateProcessingCyclesCanonicalPrefersOpen(t *testing.T) {
	r := &listRunner{allTasks: `{"data":[
      {"id":"cyc-aaa","title":"process-feedback: o/r#7","status":"closed","issue_type":"task"},
      {"id":"cyc-zzz","title":"process-feedback: o/r#7","status":"open","issue_type":"task"}
    ]}`}
	dups, err := NewClientWithRunner(r).FindDuplicateProcessingCycles(context.Background())
	if err != nil {
		t.Fatalf("FindDuplicateProcessingCycles: %v", err)
	}
	if len(dups) != 1 {
		t.Fatalf("expected one duplicated key, got %d: %+v", len(dups), dups)
	}
	if dups[0].Canonical.ID != "cyc-zzz" {
		t.Errorf("canonical = %q, want cyc-zzz (the OPEN cycle, despite the larger id)", dups[0].Canonical.ID)
	}
	if len(dups[0].Excess) != 1 || dups[0].Excess[0].ID != "cyc-aaa" {
		t.Errorf("excess = %+v, want [cyc-aaa]", dups[0].Excess)
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

// adjudicatedPairFixture is closedPairFixture's shape with the ADJUDICATION
// recorded: zr-agwaj (the excess) carries a `supersedes` edge naming zr-4jpnl
// (the canonical the pg2-xqwy6 reconcile picked). edgeFrom/edgeTo/edgeType are
// parameters so one fixture can also spell the edge the other way round, or with
// a type that is NOT an adjudication.
func adjudicatedPairFixture(edgeFrom, edgeTo, edgeType string) string {
	return `{"data":[
      {"id":"zr-agwaj","title":"process-feedback: o/r#104236","status":"closed","issue_type":"task","created_at":"2026-07-28T15:23:22Z",
       "dependencies":[{"issue_id":"` + edgeFrom + `","depends_on_id":"` + edgeTo + `","type":"` + edgeType + `"}]},
      {"id":"zr-4jpnl","title":"process-feedback: o/r#104236","status":"closed","issue_type":"task","created_at":"2026-07-28T15:23:22Z"}
    ]}`
}

// TestFindDuplicateProcessingCyclesExcludesAnAdjudicatedDuplicate is the
// regression test for pg2-peyf0: the pg2-xqwy6 reconcile closed all 201 excess
// beads against named canonicals and the audit reported the SAME counts, because
// the population is every status. A duplicate that has been ADJUDICATED — a
// `supersedes` edge to another bead with the same key — must leave the count, so
// a completed reconcile can move the number and the total can serve as
// pg2-0waxt's regression baseline.
func TestFindDuplicateProcessingCyclesExcludesAnAdjudicatedDuplicate(t *testing.T) {
	r := &listRunner{allTasks: adjudicatedPairFixture("zr-agwaj", "zr-4jpnl", "supersedes")}
	dups, err := NewClientWithRunner(r).FindDuplicateProcessingCycles(context.Background())
	if err != nil {
		t.Fatalf("FindDuplicateProcessingCycles: %v", err)
	}
	if len(dups) != 0 {
		t.Fatalf("an adjudicated duplicate must not be counted, got %d group(s): %+v", len(dups), dups)
	}
	assertOnlyBDList(t, r)
}

// assertOnlyBDList pins that an audit call issued NOTHING but `bd list`. It is
// stronger than "no writes": the adjudication marker is read out of the rows
// `bd list` already returns, so the exclusion must not have added a `bd dep list`
// per group either.
func assertOnlyBDList(t *testing.T, r *listRunner) {
	t.Helper()
	for _, call := range r.calls {
		if len(call) == 0 || call[0] != "list" {
			t.Errorf("the audit must issue only `bd list`, but ran `bd %v` (all calls: %v)", call, r.calls)
		}
	}
}

// TestFindDuplicateProcessingCyclesExclusionNeedsTheEdgeNotJustAClose is the
// other half of the same guarantee and the one that protects pg2-0z8fw: the
// discriminator is the EDGE. A closed pair with no edge, or with an edge of any
// other type, is still a duplicate nobody has resolved and is still reported.
func TestFindDuplicateProcessingCyclesExclusionNeedsTheEdgeNotJustAClose(t *testing.T) {
	for _, tc := range []struct {
		name    string
		fixture string
	}{
		{name: "closed with no edge at all", fixture: closedPairFixture("closed", "closed")},
		{name: "closed with a related edge", fixture: adjudicatedPairFixture("zr-agwaj", "zr-4jpnl", "related")},
		{name: "closed with a parent-child edge", fixture: adjudicatedPairFixture("zr-agwaj", "zr-4jpnl", "parent-child")},
		// An adjudication naming a bead OUTSIDE this identity proves nothing about
		// this duplicate, so it must not silently remove it from the report.
		{name: "supersedes edge leaving the group", fixture: adjudicatedPairFixture("zr-agwaj", "zr-unrelated", "supersedes")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dups, err := NewClientWithRunner(&listRunner{allTasks: tc.fixture}).FindDuplicateProcessingCycles(context.Background())
			if err != nil {
				t.Fatalf("FindDuplicateProcessingCycles: %v", err)
			}
			if excessTotal(dups) != 1 {
				t.Fatalf("total = %d, want 1 — only an adjudication may remove a duplicate from the count: %+v",
					excessTotal(dups), dups)
			}
		})
	}
}

// TestFindDuplicateProcessingCyclesAdjudicationIsPerBeadNotPerGroup pins the
// MIXED case: a group of three where only one excess bead has been adjudicated
// must still report the OTHER one. Dropping the whole group on sight of an edge
// would hide a duplicate nobody has resolved — the same class of silent
// under-count pg2-0z8fw removed.
func TestFindDuplicateProcessingCyclesAdjudicationIsPerBeadNotPerGroup(t *testing.T) {
	r := &listRunner{allTasks: `{"data":[
      {"id":"cyc-aaa","title":"process-feedback: o/r#7","status":"open","issue_type":"task"},
      {"id":"cyc-bbb","title":"process-feedback: o/r#7","status":"closed","issue_type":"task",
       "dependencies":[{"issue_id":"cyc-bbb","depends_on_id":"cyc-aaa","type":"supersedes"}]},
      {"id":"cyc-ccc","title":"process-feedback: o/r#7","status":"closed","issue_type":"task"}
    ]}`}
	dups, err := NewClientWithRunner(r).FindDuplicateProcessingCycles(context.Background())
	if err != nil {
		t.Fatalf("FindDuplicateProcessingCycles: %v", err)
	}
	if len(dups) != 1 {
		t.Fatalf("expected the unresolved duplicate to still be reported, got %d group(s): %+v", len(dups), dups)
	}
	if dups[0].Canonical.ID != "cyc-aaa" {
		t.Errorf("canonical = %q, want cyc-aaa", dups[0].Canonical.ID)
	}
	if len(dups[0].Excess) != 1 || dups[0].Excess[0].ID != "cyc-ccc" {
		t.Errorf("excess = %+v, want [cyc-ccc] (cyc-bbb is adjudicated)", dups[0].Excess)
	}
}

// successionPairFixture is the pg2-0waxt SUCCESSION shape, distinct from
// adjudicatedPairFixture's same-instant reconcile shape: the predecessor closed
// FAR EARLIER than the successor was created (a real re-sync days later, not a
// bulk reconcile touching two beads in the same tick), linked by the
// `supersedes` edge CreateProcessingCycle now writes when
// ensureProcessFeedbackBead (internal/beadsbridge) opens a successor for a
// closed predecessor.
func successionPairFixture() string {
	return `{"data":[
      {"id":"cyc-successor","title":"process-feedback: o/r#7","status":"open","issue_type":"task","created_at":"2026-08-14T09:00:00Z",
       "dependencies":[{"issue_id":"cyc-successor","depends_on_id":"cyc-predecessor","type":"supersedes"}]},
      {"id":"cyc-predecessor","title":"process-feedback: o/r#7","status":"closed","issue_type":"task","created_at":"2026-07-01T00:00:00Z"}
    ]}`
}

// TestFindDuplicateProcessingCyclesExcludesAGenuineSuccession is pg2-0waxt's
// remaining half proven end to end: a predecessor/successor pair created FAR
// APART in time (the actual succession path's shape) and linked by the
// supersedes edge the succession path now writes must be excluded from the
// audit — the same mechanism as an adjudication, per adjudication.go's own doc
// comment that detection keys only on the edge, never on timing or the
// description prose. Contrast with
// TestFindDuplicateProcessingCyclesExclusionNeedsTheEdgeNotJustAClose's "closed
// with no edge at all" case (using closedPairFixture, both created at the SAME
// instant, no edge): that pair is still counted, unmodified by this bead.
func TestFindDuplicateProcessingCyclesExcludesAGenuineSuccession(t *testing.T) {
	r := &listRunner{allTasks: successionPairFixture()}
	dups, err := NewClientWithRunner(r).FindDuplicateProcessingCycles(context.Background())
	if err != nil {
		t.Fatalf("FindDuplicateProcessingCycles: %v", err)
	}
	if len(dups) != 0 {
		t.Fatalf("a genuine succession recorded via supersedes must not be counted, got %d group(s): %+v", len(dups), dups)
	}
	assertOnlyBDList(t, r)
}

// TestFindDuplicateProcessingCyclesExclusionIsDirectionAgnostic pins that the
// audit reads an adjudication whichever way the edge was written. bd's own type
// names do not read consistently in one direction (`blocks` names the blocker as
// the TARGET; `discovered-from` names the origin as the target), and the
// `supersedes` edges already in these workspaces disagree about it, so keying on
// one orientation would let a real adjudication go unread. The survivor is still
// the arm's canonical pick, not the edge's source.
func TestFindDuplicateProcessingCyclesExclusionIsDirectionAgnostic(t *testing.T) {
	r := &listRunner{allTasks: adjudicatedPairFixture("zr-4jpnl", "zr-agwaj", "supersedes")}
	dups, err := NewClientWithRunner(r).FindDuplicateProcessingCycles(context.Background())
	if err != nil {
		t.Fatalf("FindDuplicateProcessingCycles: %v", err)
	}
	if len(dups) != 0 {
		t.Fatalf("a reversed adjudication edge must still be read, got %d group(s): %+v", len(dups), dups)
	}
}

// TestFindDuplicateMergeRequestsExcludesAnAdjudicatedDuplicate is pg2-peyf0 for
// the OTHER arm: the merge-request half of the 201 (54 beads) was reconciled the
// same way and must drop out of the count the same way. mr-a is the excess bead
// the audit itself picked (mr-b is more recently synced), so the fixture is the
// real reconcile shape.
func TestFindDuplicateMergeRequestsExcludesAnAdjudicatedDuplicate(t *testing.T) {
	r := &listRunner{mergeRequests: `{"data":[
      {"id":"mr-a","status":"closed","issue_type":"merge-request","metadata":{"repo":"o/r","pr_number":7,"last_synced_at":"2026-07-01T00:00:00Z"},
       "dependencies":[{"issue_id":"mr-a","depends_on_id":"mr-b","type":"supersedes"}]},
      {"id":"mr-b","status":"open","issue_type":"merge-request","metadata":{"repo":"o/r","pr_number":7,"last_synced_at":"2026-07-29T00:00:00Z"}}
    ]}`}
	dups, err := NewClientWithRunner(r).FindDuplicateMergeRequests(context.Background())
	if err != nil {
		t.Fatalf("FindDuplicateMergeRequests: %v", err)
	}
	if len(dups) != 0 {
		t.Fatalf("an adjudicated merge-request duplicate must not be counted, got %+v", dups)
	}
	assertOnlyBDList(t, r)
}

// TestFindDuplicateMergeRequestsExclusionNeedsTheEdge is the merge-request
// counterpart of the close-is-not-adjudication guard: a CLOSED duplicate pair
// with no adjudication is still reported, so this arm's long-standing
// status-agnostic count is not narrowed by pg2-peyf0.
func TestFindDuplicateMergeRequestsExclusionNeedsTheEdge(t *testing.T) {
	r := &listRunner{mergeRequests: `{"data":[
      {"id":"mr-a","status":"closed","issue_type":"merge-request","metadata":{"repo":"o/r","pr_number":7,"last_synced_at":"2026-07-01T00:00:00Z"}},
      {"id":"mr-b","status":"closed","issue_type":"merge-request","metadata":{"repo":"o/r","pr_number":7,"last_synced_at":"2026-07-29T00:00:00Z"},
       "dependencies":[{"issue_id":"mr-b","depends_on_id":"mr-a","type":"related"}]}
    ]}`}
	dups, err := NewClientWithRunner(r).FindDuplicateMergeRequests(context.Background())
	if err != nil {
		t.Fatalf("FindDuplicateMergeRequests: %v", err)
	}
	if len(dups) != 1 || len(dups[0].Excess) != 1 {
		t.Fatalf("a closed, unadjudicated pair must still be reported, got %+v", dups)
	}
	if dups[0].Canonical.ID != "mr-b" || dups[0].Excess[0].ID != "mr-a" {
		t.Errorf("keep/excess = %s/%s, want mr-b/mr-a", dups[0].Canonical.ID, dups[0].Excess[0].ID)
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
