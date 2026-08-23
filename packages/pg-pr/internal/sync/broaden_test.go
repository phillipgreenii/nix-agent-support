package sync

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/beads"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/vcs"
)

// TestBuildTeamQueries: the "to-review" (not-mine) set is a UNION expressed as
// SEPARATE searches (GitHub ANDs distinct qualifier types, so labels /
// review-requested / reviewed-by / assignee cannot be OR-ed into one query):
// the team-authors bucket, a review-requested:<self> bucket, a
// reviewed-by:<self> bucket, an assignee:<self> bucket, and one bucket per
// configured watch label. The broadened buckets exclude my own PRs
// (-author:<self>); the authors bucket is unchanged.
func TestBuildTeamQueries(t *testing.T) {
	rcfg := config.RepoConfig{Remote: "o/r", TeamMembers: []string{"a", "b"}, WatchLabels: []string{"lbl-one", "lbl-two"}}
	got := buildTeamQueries(rcfg, "me")
	want := []string{
		"is:pr is:open repo:o/r author:a author:b",
		"is:pr is:open repo:o/r review-requested:me -author:me",
		"is:pr is:open repo:o/r reviewed-by:me -author:me",
		"is:pr is:open repo:o/r assignee:me -author:me",
		`is:pr is:open repo:o/r label:"lbl-one" -author:me`,
		`is:pr is:open repo:o/r label:"lbl-two" -author:me`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("buildTeamQueries:\n got=%#v\nwant=%#v", got, want)
	}

	// No team members, but labels + requested + reviewed-by + assignee still
	// broaden the set (the review set is independent of whether the repo has
	// configured team authors) — the reviewed-by and assignee buckets in
	// particular MUST NOT be gated on team_members, since a PR I am reviewing
	// or assigned to is mine to review either way.
	noTeam := config.RepoConfig{Remote: "o/r", WatchLabels: []string{"lbl-one"}}
	got2 := buildTeamQueries(noTeam, "me")
	want2 := []string{
		"is:pr is:open repo:o/r review-requested:me -author:me",
		"is:pr is:open repo:o/r reviewed-by:me -author:me",
		"is:pr is:open repo:o/r assignee:me -author:me",
		`is:pr is:open repo:o/r label:"lbl-one" -author:me`,
	}
	if !reflect.DeepEqual(got2, want2) {
		t.Errorf("buildTeamQueries (no team):\n got=%#v\nwant=%#v", got2, want2)
	}

	// No team members AND no watch labels: the review-requested, reviewed-by,
	// and assignee buckets are STILL present (acceptance criterion: "present
	// even with no team members configured").
	onlySelfBuckets := config.RepoConfig{Remote: "o/r"}
	got2b := buildTeamQueries(onlySelfBuckets, "me")
	want2b := []string{
		"is:pr is:open repo:o/r review-requested:me -author:me",
		"is:pr is:open repo:o/r reviewed-by:me -author:me",
		"is:pr is:open repo:o/r assignee:me -author:me",
	}
	if !reflect.DeepEqual(got2b, want2b) {
		t.Errorf("buildTeamQueries (no team, no labels):\n got=%#v\nwant=%#v", got2b, want2b)
	}

	// Empty self => cannot exclude-mine, so NO broadened buckets (including the
	// new assignee bucket); only the authors bucket (if any) survives.
	got3 := buildTeamQueries(config.RepoConfig{Remote: "o/r", TeamMembers: []string{"a"}, WatchLabels: []string{"x"}}, "")
	if !reflect.DeepEqual(got3, []string{"is:pr is:open repo:o/r author:a"}) {
		t.Errorf("buildTeamQueries (no self) = %#v", got3)
	}
	// ...and with no self login there is no reviewed-by bucket either (it cannot
	// exclude-mine, so it would surface my own PRs as someone-else's-to-review).
	for _, q := range got3 {
		if strings.Contains(q, "reviewed-by:") {
			t.Errorf("empty self must omit the reviewed-by bucket; got %q", q)
		}
	}

	// Self-exclusion on the new bucket: reviewed-by is a BROADENED bucket, so like
	// review-requested/label it must carry -author:<self>. Without it, every PR of
	// mine that I self-reviewed would be pulled into the not-mine roster.
	var reviewedBy []string
	for _, q := range got {
		if strings.Contains(q, "reviewed-by:") {
			reviewedBy = append(reviewedBy, q)
		}
	}
	if len(reviewedBy) != 1 {
		t.Fatalf("expected exactly one reviewed-by bucket, got %#v", reviewedBy)
	}
	if !strings.Contains(reviewedBy[0], "-author:me") {
		t.Errorf("reviewed-by bucket must exclude my own PRs: %q", reviewedBy[0])
	}

	// FALLBACK for the still-open interacted-with fork (pg2-4dz88.11.2): the
	// shipped default emits NO commenter:/involves: bucket. A comment is not a
	// review commitment, and those qualifiers pull in every mention thread. This
	// negative is asserted so adding such a bucket is a deliberate, test-breaking
	// decision rather than a silent scope creep.
	for _, qs := range [][]string{got, got2, got3} {
		for _, q := range qs {
			for _, banned := range []string{"commenter:", "involves:"} {
				if strings.Contains(q, banned) {
					t.Errorf("buildTeamQueries must emit no %s bucket; got %q", banned, q)
				}
			}
		}
	}
}

// TestMergeRosters_DedupsAndTracksComplete: merging the per-bucket rosters
// de-dups by repo#number (a PR matching two criteria appears once) and marks the
// merge incomplete if ANY bucket was truncated (so diffRoster's mass-close guard
// stays off on partial data).
func TestMergeRosters_DedupsAndTracksComplete(t *testing.T) {
	r1 := vcs.FingerprintResult{PRs: []vcs.PRFingerprint{fp("o/r", 1, "a"), fp("o/r", 2, "b")}}
	r2 := vcs.FingerprintResult{PRs: []vcs.PRFingerprint{fp("o/r", 2, "b"), fp("o/r", 3, "c")}} // 2 is a dup
	roster, complete := mergeRosters([]vcs.FingerprintResult{r1, r2})
	if len(roster) != 3 {
		t.Fatalf("expected 3 unique PRs, got %d: %+v", len(roster), roster)
	}
	if !complete {
		t.Errorf("no truncation => complete")
	}
	r3 := vcs.FingerprintResult{PRs: []vcs.PRFingerprint{fp("o/r", 4, "d")}, Truncated: true}
	if _, c := mergeRosters([]vcs.FingerprintResult{r1, r3}); c {
		t.Errorf("a truncated bucket must mark the merge incomplete")
	}

	// THREE buckets (authors + review-requested + reviewed-by is now the minimum
	// shape for a repo with team members), with one PR present in all three: the
	// de-dup is not pairwise, so the same PR arriving from a third bucket still
	// collapses to one roster entry.
	shared := fp("o/r", 5, "e")
	b1 := vcs.FingerprintResult{PRs: []vcs.PRFingerprint{shared, fp("o/r", 6, "f")}}
	b2 := vcs.FingerprintResult{PRs: []vcs.PRFingerprint{shared}}
	b3 := vcs.FingerprintResult{PRs: []vcs.PRFingerprint{shared, fp("o/r", 7, "g")}}
	roster3, complete3 := mergeRosters([]vcs.FingerprintResult{b1, b2, b3})
	if len(roster3) != 3 {
		t.Errorf("3-bucket merge with one thrice-matched PR => 3 unique, got %d: %+v", len(roster3), roster3)
	}
	if !complete3 {
		t.Errorf("no truncation across three buckets => complete")
	}
}

// TestMergeRosters_AllBucketsSamePR_OneEntry proves the multi-bucket de-dup at
// the roster-merge layer: a PR returned by EVERY currently-existing bucket at
// once (team-authors, review-requested, assignee, and two label buckets — the
// post-assignee-bucket set) still merges to exactly ONE roster entry, not five
// — asserted on roster LENGTH, not just presence.
func TestMergeRosters_AllBucketsSamePR_OneEntry(t *testing.T) {
	same := fp("o/r", 42, "same-head")
	results := []vcs.FingerprintResult{
		{PRs: []vcs.PRFingerprint{same}}, // team-authors
		{PRs: []vcs.PRFingerprint{same}}, // review-requested
		{PRs: []vcs.PRFingerprint{same}}, // assignee
		{PRs: []vcs.PRFingerprint{same}}, // label:team/lbl-one
		{PRs: []vcs.PRFingerprint{same}}, // label:team/lbl-two
	}
	roster, complete := mergeRosters(results)
	if len(roster) != 1 {
		t.Fatalf("PR present in every bucket must merge to exactly ONE roster entry, got %d: %+v", len(roster), roster)
	}
	if !complete {
		t.Errorf("no truncation => complete")
	}
}

// queryRosterVCS returns a roster per query substring so a test can prove the
// team loop UNIONs the authors + review-requested + label buckets.
type queryRosterVCS struct {
	fakeVCS
	byQuery map[string][]vcs.PRFingerprint
}

func (q *queryRosterVCS) FingerprintPRs(_ context.Context, query string) (vcs.FingerprintResult, error) {
	for sub, prs := range q.byQuery {
		if strings.Contains(query, sub) {
			return vcs.FingerprintResult{PRs: prs}, nil
		}
	}
	return vcs.FingerprintResult{}, nil
}

func drainQueue(q *refreshQueue) map[prKey]bool {
	out := map[prKey]bool{}
	for {
		k, ok := q.dequeue()
		if !ok {
			return out
		}
		out[k] = true
	}
}

// TestFingerprintTick_TeamLoopUnionsRequestedAndLabels (6b/B3): the daemon
// detector must enqueue not only team-authored PRs but also PRs where I'm a
// requested reviewer, PRs assigned to me, and PRs carrying a configured watch
// label — the broadened review set. Without this, the daemon (which uses
// detector.go, NOT enumerate) never sees them, so pr-pool never reviews them.
// PR 13 (assignee-only case) proves the new assignee:<self> bucket alone is
// enough to enqueue a PR (pg2-4dz88.11.4).
func TestFingerprintTick_TeamLoopUnionsRequestedAndLabels(t *testing.T) {
	vp := &queryRosterVCS{byQuery: map[string][]vcs.PRFingerprint{
		"author:teammate":     {fp("o/r", 10, "a")}, // team-authors bucket
		"review-requested:me": {fp("o/r", 11, "b")}, // requested bucket
		"reviewed-by:me":      {fp("o/r", 13, "d")}, // reviewed-by bucket
		"assignee:me":         {fp("o/r", 14, "e")}, // assignee bucket
		`label:"lbl-one"`:     {fp("o/r", 12, "c")}, // label bucket
	}}
	e, err := New(Deps{
		Cfg:   &config.Config{SelfLogin: "me", Repos: []config.RepoConfig{{Remote: "o/r", TeamMembers: []string{"teammate"}, WatchLabels: []string{"lbl-one"}}}},
		VCS:   map[string]VCSProvider{"github": vp},
		Beads: noopBeads{},
		Now:   func() time.Time { return time.Now().UTC() },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	e.prevMine = map[prKey]string{}
	e.prevTeam = map[prKey]string{}
	mineQ, teamQ := newRefreshQueue(), newRefreshQueue()

	e.fingerprintTick(context.Background(), mineQ, teamQ, discardLogger())

	enq := drainQueue(teamQ)
	for _, n := range []int{10, 11, 12, 13, 14} {
		if !enq[prKey{Repo: "o/r", Number: n}] {
			t.Errorf("PR %d should be enqueued via the broadened team loop; enqueued=%+v", n, enq)
		}
	}
}

// TestFingerprintTick_ReviewedByOnlyIsEnqueued: a PR retrieved ONLY by the
// reviewed-by:<self> bucket — not team-authored, not review-requested, carrying
// no watch label — reaches the team refresh queue. This is the retrieval half of
// the reviewed-by feature; without it the daemon never revisits a PR after the
// review request that first surfaced it is satisfied (GitHub drops a reviewed PR
// out of review-requested:<self>).
func TestFingerprintTick_ReviewedByOnlyIsEnqueued(t *testing.T) {
	vp := &queryRosterVCS{byQuery: map[string][]vcs.PRFingerprint{
		"reviewed-by:me": {fp("o/r", 21, "a")},
	}}
	e, err := New(Deps{
		// No team members and no watch labels: the ONLY bucket that can retrieve
		// anything here is reviewed-by.
		Cfg:   &config.Config{SelfLogin: "me", Repos: []config.RepoConfig{{Remote: "o/r"}}},
		VCS:   map[string]VCSProvider{"github": vp},
		Beads: noopBeads{},
		Now:   func() time.Time { return time.Now().UTC() },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	e.prevMine = map[prKey]string{}
	e.prevTeam = map[prKey]string{}
	mineQ, teamQ := newRefreshQueue(), newRefreshQueue()

	e.fingerprintTick(context.Background(), mineQ, teamQ, discardLogger())

	enq := drainQueue(teamQ)
	if !enq[prKey{Repo: "o/r", Number: 21}] {
		t.Errorf("a reviewed-by-only PR must be enqueued; enqueued=%+v", enq)
	}
	if len(enq) != 1 {
		t.Errorf("expected exactly the reviewed-by PR, got %+v", enq)
	}
}

// TestFingerprintTick_ReviewedByAndTeamAuthoredDedups: a PR that BOTH the
// team-authors and reviewed-by buckets return appears EXACTLY ONCE in the merged
// roster, so it is enqueued once and its prevTeam entry is written once. The
// length assertion (not just presence) is the point — de-duplication is what
// keeps the per-tick roster from double-counting a PR matched by several buckets.
func TestFingerprintTick_ReviewedByAndTeamAuthoredDedups(t *testing.T) {
	both := fp("o/r", 30, "a")
	vp := &queryRosterVCS{byQuery: map[string][]vcs.PRFingerprint{
		"author:teammate": {both},
		"reviewed-by:me":  {both}, // same PR via a second bucket
	}}
	e, err := New(Deps{
		Cfg:   &config.Config{SelfLogin: "me", Repos: []config.RepoConfig{{Remote: "o/r", TeamMembers: []string{"teammate"}}}},
		VCS:   map[string]VCSProvider{"github": vp},
		Beads: noopBeads{},
		Now:   func() time.Time { return time.Now().UTC() },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	e.prevMine = map[prKey]string{}
	e.prevTeam = map[prKey]string{}
	mineQ, teamQ := newRefreshQueue(), newRefreshQueue()

	e.fingerprintTick(context.Background(), mineQ, teamQ, discardLogger())

	enq := drainQueue(teamQ)
	if len(enq) != 1 || !enq[prKey{Repo: "o/r", Number: 30}] {
		t.Errorf("a PR in two buckets must be enqueued exactly once; enqueued=%+v", enq)
	}
	if len(e.prevTeam) != 1 {
		t.Errorf("merged roster must hold one entry for a doubly-matched PR, got %+v", e.prevTeam)
	}
}

// TestFingerprintTick_CommentedOnlyPRNotRetrieved is the FALLBACK assertion for
// the still-open interacted-with fork: with no commenter:/involves: bucket, a PR
// I have only COMMENTED on — no submitted review, not team-authored, no watch
// label — is retrieved by NO bucket and so never reaches the roster or the queue.
// The fake answers only a commenter: query, which buildTeamQueries never issues.
func TestFingerprintTick_CommentedOnlyPRNotRetrieved(t *testing.T) {
	vp := &queryRosterVCS{byQuery: map[string][]vcs.PRFingerprint{
		"commenter:me": {fp("o/r", 40, "a")},
	}}
	e, err := New(Deps{
		Cfg:   &config.Config{SelfLogin: "me", Repos: []config.RepoConfig{{Remote: "o/r"}}},
		VCS:   map[string]VCSProvider{"github": vp},
		Beads: noopBeads{},
		Now:   func() time.Time { return time.Now().UTC() },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	e.prevMine = map[prKey]string{}
	e.prevTeam = map[prKey]string{}
	mineQ, teamQ := newRefreshQueue(), newRefreshQueue()

	e.fingerprintTick(context.Background(), mineQ, teamQ, discardLogger())

	if enq := drainQueue(teamQ); len(enq) != 0 {
		t.Errorf("a commented-only PR must NOT be retrieved (no commenter:/involves: bucket); enqueued=%+v", enq)
	}
	if len(e.prevTeam) != 0 {
		t.Errorf("a commented-only PR must be absent from the merged roster, got %+v", e.prevTeam)
	}
}

// TestFingerprintTick_CommentedAndTeamAuthoredStillRetrieved is the other half of
// the fallback proof: the absent commenter: bucket changes NOTHING for a PR that
// an existing bucket already retrieves. A PR I commented on that is ALSO
// team-authored is still enqueued exactly as before.
func TestFingerprintTick_CommentedAndTeamAuthoredStillRetrieved(t *testing.T) {
	vp := &queryRosterVCS{byQuery: map[string][]vcs.PRFingerprint{
		"author:teammate": {fp("o/r", 41, "a")},
	}}
	e, err := New(Deps{
		Cfg:   &config.Config{SelfLogin: "me", Repos: []config.RepoConfig{{Remote: "o/r", TeamMembers: []string{"teammate"}}}},
		VCS:   map[string]VCSProvider{"github": vp},
		Beads: noopBeads{},
		Now:   func() time.Time { return time.Now().UTC() },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	e.prevMine = map[prKey]string{}
	e.prevTeam = map[prKey]string{}
	mineQ, teamQ := newRefreshQueue(), newRefreshQueue()

	e.fingerprintTick(context.Background(), mineQ, teamQ, discardLogger())

	if enq := drainQueue(teamQ); !enq[prKey{Repo: "o/r", Number: 41}] {
		t.Errorf("a commented-on PR that is also team-authored must still be retrieved; enqueued=%+v", enq)
	}
}

// erroringBucketVCS returns an error for exactly the query containing errSub
// (simulating one bucket's poll failing/timing out) and otherwise behaves
// like queryRosterVCS, resolving the other buckets from byQuery.
type erroringBucketVCS struct {
	fakeVCS
	errSub  string
	byQuery map[string][]vcs.PRFingerprint
}

func (e *erroringBucketVCS) FingerprintPRs(_ context.Context, query string) (vcs.FingerprintResult, error) {
	if strings.Contains(query, e.errSub) {
		return vcs.FingerprintResult{}, errors.New("simulated bucket failure")
	}
	for sub, prs := range e.byQuery {
		if strings.Contains(query, sub) {
			return vcs.FingerprintResult{PRs: prs}, nil
		}
	}
	return vcs.FingerprintResult{}, nil
}

// TestFingerprintTick_AssigneeBucketFailure_KeepsMergeIncompleteAndCarriesRoster
// proves the assignee bucket is subject to the SAME partial-data handling as
// every other team bucket (detector.go's bucketErr/complete plumbing): when
// only the assignee:<self> poll fails, the repo's merge is marked incomplete,
// so (a) the mass-close ("disappeared") guard does NOT fire for a
// bd-tracked PR the (partial) roster didn't see this tick, and (b) that PR's
// prior roster entry is carried forward into prevTeam rather than dropped —
// mirroring TestDiffRoster_TruncatedSkipsDisappeared's guarantee, but driven
// by a real per-bucket poll failure rather than a synthetic incomplete flag.
func TestFingerprintTick_AssigneeBucketFailure_KeepsMergeIncompleteAndCarriesRoster(t *testing.T) {
	vp := &erroringBucketVCS{
		errSub: "assignee:me",
		byQuery: map[string][]vcs.PRFingerprint{
			"author:teammate": {fp("o/r", 20, "a")}, // team-authors bucket still succeeds
		},
	}
	// A bd-tracked (open merge-request bead) PR #99 that the successful
	// buckets this tick do NOT return — normally a "disappeared" candidate.
	bdc := &refreshFakeBeads{
		existing: &beads.MergeRequest{
			ID:     "mr-99",
			Fields: beads.MergeRequestFields{Repo: "o/r", PRNumber: 99},
		},
	}
	e, err := New(Deps{
		Cfg:   &config.Config{SelfLogin: "me", Repos: []config.RepoConfig{{Remote: "o/r", TeamMembers: []string{"teammate"}}}},
		VCS:   map[string]VCSProvider{"github": vp},
		Beads: bdc,
		Now:   func() time.Time { return time.Now().UTC() },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	e.prevMine = map[prKey]string{}
	staleKey := prKey{Repo: "o/r", Number: 99}
	e.prevTeam = map[prKey]string{staleKey: "stale-hash"} // last tick's roster hash for PR 99
	mineQ, teamQ := newRefreshQueue(), newRefreshQueue()

	e.fingerprintTick(context.Background(), mineQ, teamQ, discardLogger())

	enq := drainQueue(teamQ)
	if enq[staleKey] {
		t.Errorf("a failed assignee bucket must disable disappeared-detection (partial data, no mass-close): PR 99 wrongly enqueued as disappeared, enqueued=%+v", enq)
	}
	if _, ok := e.prevTeam[staleKey]; !ok {
		t.Errorf("PR 99's prior roster entry must be carried forward across an incomplete merge, got prevTeam=%+v", e.prevTeam)
	}
}

// ----------------------------------------------------------------------
// One-shot sync broadening (pg2-qzatr): tryEnumerateEnriched (the one-shot
// `pg-pr sync` path, NOT the daemon's fingerprintTick above) stays
// author-only by default; Deps.BroadenOneShotSync opts a one-shot sync into
// ALSO fanning out to the same buildTeamQueries buckets the daemon already
// uses unconditionally, merging their PRs into the enriched result.
// ----------------------------------------------------------------------

// perQueryEnrichedVCS answers each EnrichedPRs bulk-fetch call by EXACT query
// match (not substring, unlike perQueryFingerprintVCS/queryRosterVCS above):
// the broadened buckets all end in "-author:<self>", which contains the
// base author-only query's "author:<self>" as a substring, so substring
// matching here would let the base query's answer leak into a broadened
// bucket's lookup (or vice versa) depending on map iteration order.
// errQuery, when non-empty, names the exact query that fails.
type perQueryEnrichedVCS struct {
	fakeVCS
	byQuery  map[string][]vcs.EnrichedPR
	errQuery string
	queries  []string
}

func (v *perQueryEnrichedVCS) EnrichedPRs(_ context.Context, _ string, query string) ([]vcs.EnrichedPR, error) {
	v.queries = append(v.queries, query)
	if v.errQuery != "" && query == v.errQuery {
		return nil, errors.New("simulated bucket failure")
	}
	return v.byQuery[query], nil
}

func enrichedFor(repo string, n int) vcs.EnrichedPR {
	return vcs.EnrichedPR{PR: api.PR{Repo: repo, Number: n}}
}

// TestTryEnumerateEnriched_BroadenOff_UnchangedSingleQuery proves the DEFAULT
// (Deps.BroadenOneShotSync unset) one-shot path is exactly what it was before
// this bead: exactly one author-only EnrichedPRs call, and a PR retrievable
// ONLY via a broadened bucket (review-requested) never reaches the result.
func TestTryEnumerateEnriched_BroadenOff_UnchangedSingleQuery(t *testing.T) {
	rcfg := config.RepoConfig{Remote: "o/r"}
	self := "me"
	baseQuery := buildEnrichedSearchQuery(rcfg.Remote, self, rcfg.TeamMembers)
	broadened := buildTeamQueries(rcfg, self) // [review-requested, reviewed-by, assignee]

	vp := &perQueryEnrichedVCS{byQuery: map[string][]vcs.EnrichedPR{
		baseQuery:    {enrichedFor("o/r", 1)},
		broadened[0]: {enrichedFor("o/r", 2)}, // review-requested-only PR
	}}
	e, err := New(Deps{
		Cfg: &config.Config{SelfLogin: self, Repos: []config.RepoConfig{rcfg}},
		VCS: map[string]VCSProvider{"github": vp},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	got, ok := e.tryEnumerateEnriched(context.Background(), vp, rcfg)
	if !ok {
		t.Fatalf("expected tryEnumerateEnriched to succeed")
	}
	if len(vp.queries) != 1 {
		t.Fatalf("default (flag off) must issue exactly 1 query, got %d: %v", len(vp.queries), vp.queries)
	}
	if _, dup := got.byNumber[2]; dup {
		t.Errorf("broadened-only PR 2 must NOT appear when BroadenOneShotSync is off, got %+v", got.byNumber)
	}
	if _, ok := got.byNumber[1]; !ok {
		t.Errorf("author-only PR 1 must still appear, got %+v", got.byNumber)
	}
}

// TestTryEnumerateEnriched_BroadenOn_IncludesBroadenedBucketPR proves the
// opt-in half: with Deps.BroadenOneShotSync set, a PR retrievable ONLY via a
// broadened bucket (review-requested, not author-matched) IS now included —
// the retrieval-parity behavior this bead adds.
func TestTryEnumerateEnriched_BroadenOn_IncludesBroadenedBucketPR(t *testing.T) {
	rcfg := config.RepoConfig{Remote: "o/r"}
	self := "me"
	baseQuery := buildEnrichedSearchQuery(rcfg.Remote, self, rcfg.TeamMembers)
	broadened := buildTeamQueries(rcfg, self)

	vp := &perQueryEnrichedVCS{byQuery: map[string][]vcs.EnrichedPR{
		baseQuery:    {enrichedFor("o/r", 1)},
		broadened[0]: {enrichedFor("o/r", 2)}, // review-requested-only PR
	}}
	e, err := New(Deps{
		Cfg:                &config.Config{SelfLogin: self, Repos: []config.RepoConfig{rcfg}},
		VCS:                map[string]VCSProvider{"github": vp},
		BroadenOneShotSync: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	got, ok := e.tryEnumerateEnriched(context.Background(), vp, rcfg)
	if !ok {
		t.Fatalf("expected tryEnumerateEnriched to succeed")
	}
	if len(vp.queries) != 1+len(broadened) {
		t.Errorf("flag on must issue the base query plus every broadened bucket, got %d queries: %v", len(vp.queries), vp.queries)
	}
	if _, ok := got.byNumber[2]; !ok {
		t.Errorf("broadened-only PR 2 must appear when BroadenOneShotSync is on, got %+v", got.byNumber)
	}
	if _, ok := got.byNumber[1]; !ok {
		t.Errorf("author-only PR 1 must still appear, got %+v", got.byNumber)
	}
}

// TestTryEnumerateEnriched_BroadenOn_DedupsAcrossBuckets proves a PR returned
// by BOTH the author-only query and a broadened bucket (e.g. I am
// team-authored AND self-assigned) appears exactly ONCE in the merged
// result — first-seen wins, mirroring mergeRosters' dedup idiom.
func TestTryEnumerateEnriched_BroadenOn_DedupsAcrossBuckets(t *testing.T) {
	rcfg := config.RepoConfig{Remote: "o/r"}
	self := "me"
	baseQuery := buildEnrichedSearchQuery(rcfg.Remote, self, rcfg.TeamMembers)
	broadened := buildTeamQueries(rcfg, self)
	shared := enrichedFor("o/r", 5)

	vp := &perQueryEnrichedVCS{byQuery: map[string][]vcs.EnrichedPR{
		baseQuery:    {shared},
		broadened[2]: {shared}, // assignee bucket returns the SAME PR
	}}
	e, err := New(Deps{
		Cfg:                &config.Config{SelfLogin: self, Repos: []config.RepoConfig{rcfg}},
		VCS:                map[string]VCSProvider{"github": vp},
		BroadenOneShotSync: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	got, ok := e.tryEnumerateEnriched(context.Background(), vp, rcfg)
	if !ok {
		t.Fatalf("expected tryEnumerateEnriched to succeed")
	}
	if n := len(got.prs); n != 1 {
		t.Fatalf("a PR returned by two buckets must merge to exactly ONE entry, got %d: %+v", n, got.prs)
	}
	if _, ok := got.byNumber[5]; !ok {
		t.Errorf("expected PR 5 present, got %+v", got.byNumber)
	}
}

// TestTryEnumerateEnriched_BroadenOn_PartialBucketFailure_KeepsOthers proves
// the partial-failure posture this bead chose (mirroring
// fingerprintTick/mergeRosters' "partial success is still useful"
// precedent): when ONE broadened bucket's query errors, the call still
// succeeds (ok=true) and returns every OTHER bucket's PRs — including the
// base author-only result — rather than discarding everything.
func TestTryEnumerateEnriched_BroadenOn_PartialBucketFailure_KeepsOthers(t *testing.T) {
	rcfg := config.RepoConfig{Remote: "o/r"}
	self := "me"
	baseQuery := buildEnrichedSearchQuery(rcfg.Remote, self, rcfg.TeamMembers)
	broadened := buildTeamQueries(rcfg, self) // [review-requested, reviewed-by, assignee]

	vp := &perQueryEnrichedVCS{
		errQuery: broadened[2], // assignee bucket fails
		byQuery: map[string][]vcs.EnrichedPR{
			baseQuery:    {enrichedFor("o/r", 1)},
			broadened[0]: {enrichedFor("o/r", 2)}, // review-requested still succeeds
		},
	}
	e, err := New(Deps{
		Cfg:                &config.Config{SelfLogin: self, Repos: []config.RepoConfig{rcfg}},
		VCS:                map[string]VCSProvider{"github": vp},
		BroadenOneShotSync: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	got, ok := e.tryEnumerateEnriched(context.Background(), vp, rcfg)
	if !ok {
		t.Fatalf("one failed broadened bucket must NOT fail the whole call (partial success posture)")
	}
	if _, ok := got.byNumber[1]; !ok {
		t.Errorf("author-only PR 1 must survive a failed broadened bucket, got %+v", got.byNumber)
	}
	if _, ok := got.byNumber[2]; !ok {
		t.Errorf("the SUCCEEDING review-requested bucket's PR 2 must still be merged in, got %+v", got.byNumber)
	}
}
