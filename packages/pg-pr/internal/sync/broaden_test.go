package sync

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/vcs"
)

// TestBuildTeamQueries: the "to-review" (not-mine) set is a UNION expressed as
// SEPARATE searches (GitHub ANDs distinct qualifier types, so labels /
// review-requested cannot be OR-ed into one query): the team-authors bucket, a
// review-requested:<self> bucket, and one bucket per configured watch label. The
// new (requested/label) buckets exclude my own PRs (-author:<self>); the authors
// bucket is unchanged.
func TestBuildTeamQueries(t *testing.T) {
	rcfg := config.RepoConfig{Remote: "o/r", TeamMembers: []string{"a", "b"}, WatchLabels: []string{"team/findev", "team/jvm-guild"}}
	got := buildTeamQueries(rcfg, "me")
	want := []string{
		"is:pr is:open repo:o/r author:a author:b",
		"is:pr is:open repo:o/r review-requested:me -author:me",
		`is:pr is:open repo:o/r label:"team/findev" -author:me`,
		`is:pr is:open repo:o/r label:"team/jvm-guild" -author:me`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("buildTeamQueries:\n got=%#v\nwant=%#v", got, want)
	}

	// No team members, but labels + requested still broaden the set (the review
	// set is independent of whether the repo has configured team authors).
	noTeam := config.RepoConfig{Remote: "o/r", WatchLabels: []string{"team/findev"}}
	got2 := buildTeamQueries(noTeam, "me")
	want2 := []string{
		"is:pr is:open repo:o/r review-requested:me -author:me",
		`is:pr is:open repo:o/r label:"team/findev" -author:me`,
	}
	if !reflect.DeepEqual(got2, want2) {
		t.Errorf("buildTeamQueries (no team):\n got=%#v\nwant=%#v", got2, want2)
	}

	// Empty self => cannot exclude-mine, so NO broadened buckets; only the authors
	// bucket (if any) survives.
	got3 := buildTeamQueries(config.RepoConfig{Remote: "o/r", TeamMembers: []string{"a"}, WatchLabels: []string{"x"}}, "")
	if !reflect.DeepEqual(got3, []string{"is:pr is:open repo:o/r author:a"}) {
		t.Errorf("buildTeamQueries (no self) = %#v", got3)
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
// requested reviewer and PRs carrying a configured watch label — the broadened
// review set. Without this, the daemon (which uses detector.go, NOT enumerate)
// never sees them, so pr-pool never reviews them.
func TestFingerprintTick_TeamLoopUnionsRequestedAndLabels(t *testing.T) {
	vp := &queryRosterVCS{byQuery: map[string][]vcs.PRFingerprint{
		"author:teammate":     {fp("o/r", 10, "a")}, // team-authors bucket
		"review-requested:me": {fp("o/r", 11, "b")}, // requested bucket
		`label:"team/findev"`: {fp("o/r", 12, "c")}, // label bucket
	}}
	e, err := New(Deps{
		Cfg:   &config.Config{SelfLogin: "me", Repos: []config.RepoConfig{{Remote: "o/r", TeamMembers: []string{"teammate"}, WatchLabels: []string{"team/findev"}}}},
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
	for _, n := range []int{10, 11, 12} {
		if !enq[prKey{Repo: "o/r", Number: n}] {
			t.Errorf("PR %d should be enqueued via the broadened team loop; enqueued=%+v", n, enq)
		}
	}
}
