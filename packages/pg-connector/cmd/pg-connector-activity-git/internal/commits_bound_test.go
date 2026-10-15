package internal

// pg2-p5asw: two bounds on the cost and the blast radius of one pull, both
// pinned on REAL git against a throwaway fixture repository.
//
//  1. The branch-label region is bounded by the window (the same committer-date
//     floor the log walk used), not by the candidates' common ancestor: one
//     in-window commit on a branch forked long ago used to widen the region to
//     everything since that fork point, on every branch.
//  2. Enrichment runs under its own budget, the op deadline minus
//     enrichReserve: when only enrichment runs out of time, the commits already
//     read are returned (truncated, without branch labels or line counts)
//     instead of the whole source being killed at the op deadline.

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// recordingRunner delegates to the real git Runner and keeps every call's
// args and, for a successful call, the number of output lines.
type recordingRunner struct {
	inner Runner
	mu    sync.Mutex
	calls []recordedCall
}

type recordedCall struct {
	args  []string
	lines int
}

func (r *recordingRunner) Run(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := r.inner.Run(ctx, dir, args...)
	n := 0
	if err == nil && out != "" {
		n = strings.Count(out, "\n") + 1
	}
	r.mu.Lock()
	r.calls = append(r.calls, recordedCall{args: append([]string(nil), args...), lines: n})
	r.mu.Unlock()
	return out, err
}

// withVerb returns the recorded calls whose first git argument is verb.
func (r *recordingRunner) withVerb(verb string) []recordedCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []recordedCall
	for _, c := range r.calls {
		if len(c.args) > 0 && c.args[0] == verb {
			out = append(out, c)
		}
	}
	return out
}

// ancientForkRepo builds: `ancient` commits far older than any window on main
// (the first one is the fork point), then one in-window commit on main
// ("main-in") and one on branch feat, forked from the FIRST ancient commit
// ("feat-in"). The common ancestor of the two in-window commits is therefore
// the first ancient commit, and the old region reached back through every
// ancient commit.
func ancientForkRepo(f *gitFixture, ancient int) (repo string) {
	repo = f.newRepo()
	var first string
	for i := 0; i < ancient; i++ {
		d := rawGitDate(rangeSince.Add(-200*24*time.Hour + time.Duration(i)*time.Hour))
		sha := f.commit(repo, meEmail, d, d, "ancient")
		if i == 0 {
			first = sha
		}
	}
	in := func(h int) string { return rawGitDate(rangeSince.Add(time.Duration(h) * time.Hour)) }
	f.commit(repo, meEmail, in(1), in(1), "main-in")
	f.git(repo, nil, "checkout", "-b", "feat", first)
	f.commit(repo, meEmail, in(2), in(2), "feat-in")
	f.git(repo, nil, "checkout", "main")
	return repo
}

func TestListActivity_RealRepo_BranchRegionBoundedByWindow(t *testing.T) {
	const ancient = 40
	f := newGitFixture(t)
	repo := ancientForkRepo(f, ancient)
	cfg := Config{AuthorEmails: []string{meEmail}, RepoPaths: []string{repo}}

	rec := &recordingRunner{inner: NewExecRunner()}
	b := New(Options{Stderr: &bytes.Buffer{}, Runner: rec})
	b.now = func() time.Time { return pinnedNow }
	res := listWith(t, b, cfg, rangeSince, rangeBefore)

	if res.Truncated {
		t.Error("truncated = true, want false")
	}
	got := map[string][]string{}
	for _, it := range res.Items {
		got[it.Summary] = it.Labels
	}
	want := map[string][]string{
		"main-in": {"repo:" + repoIdent(context.Background(), NewExecRunner(), repo), "branch:main"},
		"feat-in": {"repo:" + repoIdent(context.Background(), NewExecRunner(), repo), "branch:feat"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("items = %v, want %v (branch labels must survive the bounded region)", got, want)
	}

	if mb := rec.withVerb("merge-base"); len(mb) != 0 {
		t.Errorf("merge-base ran %d times, want 0: a floor-bounded region needs no common ancestor", len(mb))
	}
	regions := rec.withVerb("rev-list")
	if len(regions) != 1 {
		t.Fatalf("rev-list ran %d times, want exactly one region read", len(regions))
	}
	joined := strings.Join(regions[0].args, " ")
	if !strings.Contains(joined, "--since=2026-08-18T00:00:00+00:00") || strings.Contains(joined, "--not") {
		t.Errorf("region args %q, want the window floor and no --not <base>", joined)
	}
	// The region holds the in-window commits only (main-in, feat-in), never
	// the %d ancient ones the fork point would have dragged in.
	if regions[0].lines >= ancient {
		t.Errorf("region read %d commits, want a window-sized region (< %d ancient commits)", regions[0].lines, ancient)
	}
}

// With no floor (an open-ended since) the common-ancestor region stays, and
// the labels are the same.
func TestListActivity_RealRepo_BranchRegionWithoutFloorUsesCommonAncestor(t *testing.T) {
	f := newGitFixture(t)
	repo := ancientForkRepo(f, 10)
	cfg := Config{AuthorEmails: []string{meEmail}, RepoPaths: []string{repo}}

	rec := &recordingRunner{inner: NewExecRunner()}
	b := New(Options{Stderr: &bytes.Buffer{}, Runner: rec})
	b.now = func() time.Time { return pinnedNow }
	res := listWith(t, b, cfg, time.Time{}, rangeBefore)

	branches := map[string]string{}
	for _, it := range res.Items {
		for _, l := range it.Labels {
			if strings.HasPrefix(l, "branch:") {
				branches[it.Summary] = l
			}
		}
	}
	if branches["main-in"] != "branch:main" || branches["feat-in"] != "branch:feat" {
		t.Errorf("branch labels = %v, want main-in on main and feat-in on feat", branches)
	}
	if len(rec.withVerb("merge-base")) == 0 {
		t.Error("no merge-base call: the open-ended read must keep the common-ancestor region")
	}
}

// twoInWindowCommits builds one repo with two in-window commits by me.
func twoInWindowCommits(f *gitFixture) string {
	repo := f.newRepo()
	in := func(h int) string { return rawGitDate(rangeSince.Add(time.Duration(h) * time.Hour)) }
	f.commit(repo, meEmail, in(1), in(1), "first")
	f.commit(repo, meEmail, in(2), in(2), "second")
	return repo
}

func listWithDeadline(t *testing.T, b *Backend, cfg Config, deadline time.Time) (resultItems []map[string]any, truncated bool) {
	t.Helper()
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithDeadline(scriptout.WithConfig(context.Background(), raw), deadline)
	defer cancel()
	res, err := b.ListActivity(ctx, rangeSince, rangeBefore)
	if err != nil {
		t.Fatalf("ListActivity: %v", err)
	}
	for _, it := range res.Items {
		var fields map[string]any
		if err := json.Unmarshal(it.Fields, &fields); err != nil {
			t.Fatal(err)
		}
		fields["_labels"] = it.Labels
		resultItems = append(resultItems, fields)
	}
	return resultItems, res.Truncated
}

func TestListActivity_RealRepo_EnrichmentBudgetSpentKeepsTheCommits(t *testing.T) {
	f := newGitFixture(t)
	repo := twoInWindowCommits(f)
	cfg := Config{AuthorEmails: []string{meEmail}, RepoPaths: []string{repo}}

	// A reserve longer than the whole op leaves enrichment no budget at all,
	// while the commit read (under the op's own deadline) runs for real.
	old := enrichReserve
	enrichReserve = 2 * time.Hour
	defer func() { enrichReserve = old }()

	var stderr bytes.Buffer
	b := New(Options{Stderr: &stderr, Runner: NewExecRunner()})
	b.now = func() time.Time { return pinnedNow }
	items, truncated := listWithDeadline(t, b, cfg, time.Now().Add(time.Hour))

	if !truncated {
		t.Error("truncated = false, want true: enrichment was skipped")
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want both commits kept", len(items))
	}
	for _, it := range items {
		if _, has := it["insertions"]; has {
			t.Errorf("item %v carries line counts, want none (never a made-up zero)", it)
		}
		if it["enriched"] != false {
			t.Errorf("item %v: enriched = %v, want false", it, it["enriched"])
		}
		for _, l := range it["_labels"].([]string) {
			if strings.HasPrefix(l, "branch:") {
				t.Errorf("item %v carries %s without branch enrichment", it, l)
			}
		}
	}
	if !strings.Contains(stderr.String(), repo) || strings.Count(stderr.String(), "\n") != 1 {
		t.Errorf("stderr = %q, want one line naming the repo", stderr.String())
	}
}

func TestListActivity_RealRepo_EnrichmentWithinBudgetIsComplete(t *testing.T) {
	f := newGitFixture(t)
	repo := twoInWindowCommits(f)
	cfg := Config{AuthorEmails: []string{meEmail}, RepoPaths: []string{repo}}

	b := New(Options{Stderr: &bytes.Buffer{}, Runner: NewExecRunner()})
	b.now = func() time.Time { return pinnedNow }
	items, truncated := listWithDeadline(t, b, cfg, time.Now().Add(time.Hour))

	if truncated {
		t.Error("truncated = true, want false: enrichment had its whole budget")
	}
	for _, it := range items {
		if _, has := it["insertions"]; !has {
			t.Errorf("item %v lacks line counts", it)
		}
		if !slices.Contains(it["_labels"].([]string), "branch:main") {
			t.Errorf("item %v lacks its branch label", it)
		}
	}
}

// The op deadline itself firing during enrichment is not a spent enrichment
// budget: with no repo finished yet it stays the error it always was.
func TestCollect_OpDeadlineDuringEnrichmentWithBudgetIsStillAnError(t *testing.T) {
	_, repoB, shaB := twoRepos(t)
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(time.Hour))
	defer cancel()
	b := New(Options{Stderr: &bytes.Buffer{}, Runner: cancelOnBatch(cancel, shaB)})
	b.now = func() time.Time { return pinnedNow }

	items, truncated, err := b.collectCommits(ctx, Config{RepoPaths: []string{repoB}},
		[]string{meEmail}, time.Time{}, rangeBefore)
	if err == nil || truncated || items != nil {
		t.Errorf("items=%v truncated=%v err=%v, want the error and nothing else", items, truncated, err)
	}
}
