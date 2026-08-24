package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/agentregistry"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/vcs"
)

// fakeListVCS controls the live roster/labels round-trip for `pr list` tests.
// It embeds fakeVCS for the no-op remainder of the vcs.Provider surface and
// overrides GetPR (labels) and ListReviews (roster). It counts calls so a test
// can assert the default (no --reviewers) makes no provider calls.
type fakeListVCS struct {
	fakeVCS
	labels   map[int][]string
	reviews  map[int][]api.Review
	getErr   error
	revErr   error
	getCalls int
	revCalls int
}

func (f *fakeListVCS) GetPR(_ context.Context, repo string, n int) (*api.PR, error) {
	f.getCalls++
	if f.getErr != nil {
		return nil, f.getErr
	}
	return &api.PR{Repo: repo, Number: n, Labels: f.labels[n]}, nil
}

func (f *fakeListVCS) ListReviews(_ context.Context, _ string, n int) ([]api.Review, error) {
	f.revCalls++
	if f.revErr != nil {
		return nil, f.revErr
	}
	return f.reviews[n], nil
}

var _ vcs.Provider = (*fakeListVCS)(nil)

// wireListFakes installs a fake provider and a synthetic config (for the agent
// registry) for the duration of the test, restoring both afterwards.
func wireListFakes(t *testing.T, prov vcs.Provider, agents []agentregistry.Entry) {
	t.Helper()
	prevProv := vcsProviderFor
	prevCfg := loadConfigForRepoPath
	t.Cleanup(func() {
		vcsProviderFor = prevProv
		loadConfigForRepoPath = prevCfg
	})
	vcsProviderFor = func(string) vcs.Provider { return prov }
	loadConfigForRepoPath = func(context.Context) (*config.Config, error) {
		return &config.Config{Agents: agents}, nil
	}
}

// seedListStore creates a temp store at the XDG state path and upserts the given
// PRs. The caller has already pointed XDG_STATE_HOME at tmp.
func seedListStore(t *testing.T, prs ...store.PullRequest) {
	t.Helper()
	db, err := store.Open(store.DefaultPath())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	ctx := context.Background()
	for _, pr := range prs {
		if _, err := db.UpsertPR(ctx, pr); err != nil {
			t.Fatalf("upsert pr %s#%d: %v", pr.Repo, pr.Number, err)
		}
	}
	_ = db.Close()
}

// setListStateHome points XDG_STATE_HOME at a fresh temp dir with the pg-pr
// subdir created, so store.DefaultPath() resolves. Returns the temp dir.
func setListStateHome(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, "pg-pr"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Setenv("XDG_STATE_HOME", tmp)
	return tmp
}

// runPRListRaw runs `pr list <args>` and returns stdout, failing on error.
func runPRListRaw(t *testing.T, args ...string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs(append([]string{"pr", "list"}, args...))
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v (stderr=%s)", err, stderr.String())
	}
	return stdout.String()
}

// runPRList runs `pr list --json <args>` and decodes the JSON array.
func runPRList(t *testing.T, args ...string) []prListItem {
	t.Helper()
	out := runPRListRaw(t, append(args, "--json")...)
	var got []prListItem
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output is not a JSON array of items: %v\noutput:\n%s", err, out)
	}
	return got
}

// TestPRList_JSONBaseFields verifies `pr list --json` (the default, no
// --reviewers) emits the base data seam fields for the open/draft PRs, makes NO
// provider calls, excludes closed PRs and other repos, and derives draft.
func TestPRList_JSONBaseFields(t *testing.T) {
	resetPRFlags()
	setListStateHome(t)
	prov := &fakeListVCS{}
	wireListFakes(t, prov, nil)
	seedListStore(
		t,
		store.PullRequest{
			Repo: "foo/bar", Number: 10, Ownership: "mine", State: "open",
			Author: "phillipg", Branch: "feat/a", Base: "main", HeadSHA: "aaa111",
		},
		store.PullRequest{
			Repo: "foo/bar", Number: 11, Ownership: "team", State: "draft",
			Author: "octocat", Branch: "feat/b", Base: "main", HeadSHA: "bbb222",
		},
		store.PullRequest{
			Repo: "foo/bar", Number: 12, Ownership: "mine", State: "closed",
			Author: "phillipg", Branch: "feat/c", Base: "main", HeadSHA: "ccc333",
		},
		store.PullRequest{
			Repo: "other/repo", Number: 99, Ownership: "team", State: "open",
			Author: "octocat", Branch: "feat/z", Base: "main", HeadSHA: "zzz999",
		},
	)

	got := runPRList(t, "--repo", "foo/bar")
	if len(got) != 2 {
		t.Fatalf("expected 2 open PRs for foo/bar, got %d: %+v", len(got), got)
	}
	if prov.getCalls != 0 || prov.revCalls != 0 {
		t.Errorf("default listing must make NO provider calls, got get=%d rev=%d", prov.getCalls, prov.revCalls)
	}
	byNum := map[int]prListItem{}
	for _, it := range got {
		byNum[it.Number] = it
	}

	pr10 := byNum[10]
	if pr10.Repo != "foo/bar" || pr10.Ownership != "mine" || pr10.Draft ||
		pr10.State != "open" || pr10.Branch != "feat/a" || pr10.HeadSHA != "aaa111" {
		t.Errorf("PR 10 base fields wrong: %+v", pr10)
	}
	pr11 := byNum[11]
	if pr11.Ownership != "team" || !pr11.Draft || pr11.Branch != "feat/b" || pr11.HeadSHA != "bbb222" {
		t.Errorf("PR 11 base fields wrong (expected team/draft): %+v", pr11)
	}
	if _, present := byNum[12]; present {
		t.Errorf("closed PR 12 must be excluded from the listing")
	}
	if _, present := byNum[99]; present {
		t.Errorf("PR 99 from other/repo must not appear in foo/bar listing")
	}
}

// TestPRList_MergedPRExcluded_SeamProtection is the pg2-ew4kf seam-protection
// regression guard: `pg-pr pr list` is the machine-readable read seam the
// pr-pool ACL consumes, and it MUST stay open/draft-only regardless of the
// dashboard/snapshot layer's separate 24h merged-PR retention (implemented in
// internal/snapshot's Build, not here). A merged PR — including one authored
// by ME, the exact case the dashboard now retains — must NEVER appear in this
// command's output.
func TestPRList_MergedPRExcluded_SeamProtection(t *testing.T) {
	resetPRFlags()
	setListStateHome(t)
	wireListFakes(t, &fakeListVCS{}, nil)
	seedListStore(
		t,
		store.PullRequest{
			Repo: "foo/bar", Number: 10, Ownership: "mine", State: "open",
			Author: "phillipg", Branch: "feat/a", Base: "main", HeadSHA: "aaa111",
		},
		store.PullRequest{
			// Merged just now, authored by me — the retained-in-the-dashboard
			// case. The seam must exclude it exactly like any other merged PR.
			Repo: "foo/bar", Number: 12, Ownership: "mine", State: "merged",
			Author: "phillipg", Branch: "feat/c", Base: "main", HeadSHA: "ccc333",
		},
	)

	got := runPRList(t, "--repo", "foo/bar")
	if len(got) != 1 {
		t.Fatalf("expected only the open PR, got %d: %+v", len(got), got)
	}
	for _, it := range got {
		if it.State == "merged" {
			t.Fatalf("a merged PR must never appear in `pr list` output (pr-pool ACL seam): %+v", it)
		}
	}
	if got[0].Number != 10 {
		t.Fatalf("expected PR 10 (open) to be the sole result, got %+v", got)
	}
}

// TestPRList_HiddenPRAlwaysIncludedWithFlagAndReason is the pg2-4dz88.4.3
// machine-seam acceptance test: `pr list --json` (the read seam pr-pool's ACL
// consumes, per ADR 0034 / the fork #1 operator ruling) NEVER filters on
// USER_HIDDEN -- a hidden PR appears in the default output exactly like an
// unhidden one, carrying "hidden": true and its "reason".
func TestPRList_HiddenPRAlwaysIncludedWithFlagAndReason(t *testing.T) {
	resetPRFlags()
	setListStateHome(t)
	wireListFakes(t, &fakeListVCS{}, nil)
	seedListStore(
		t,
		store.PullRequest{
			Repo: "foo/bar", Number: 10, Ownership: "mine", State: "open",
			Author: "phillipg", Branch: "feat/a", Base: "main", HeadSHA: "aaa111",
		},
		store.PullRequest{
			Repo: "foo/bar", Number: 11, Ownership: "mine", State: "open",
			Author: "phillipg", Branch: "feat/b", Base: "main", HeadSHA: "bbb222",
		},
	)
	setStoreHidden(t, "foo/bar", 11, true, "duplicate of #10")

	got := runPRList(t, "--repo", "foo/bar")
	if len(got) != 2 {
		t.Fatalf("hiding PR #11 must not remove it from the seam's default output: got %d, want 2: %+v", len(got), got)
	}
	byNum := map[int]prListItem{}
	for _, it := range got {
		byNum[it.Number] = it
	}
	if byNum[10].Hidden {
		t.Errorf("unhidden PR 10 must not carry hidden=true: %+v", byNum[10])
	}
	if !byNum[11].Hidden || byNum[11].Reason != "duplicate of #10" {
		t.Errorf("hidden PR 11 must carry hidden=true and its reason: %+v", byNum[11])
	}

	// The fields must actually be on the wire under exactly these JSON keys.
	raw := runPRListRaw(t, "--repo", "foo/bar", "--json")
	if !strings.Contains(raw, `"hidden": true`) {
		t.Errorf("hidden flag missing from the emitted JSON:\n%s", raw)
	}
	if !strings.Contains(raw, `"reason": "duplicate of #10"`) {
		t.Errorf("reason missing from the emitted JSON:\n%s", raw)
	}
}

// TestPRList_RosterAndLabels verifies the --reviewers augmentation: labels come
// from GetPR, and the roster from ListReviews with each reviewer classified
// agent vs person via the agent registry (config.Agents).
func TestPRList_RosterAndLabels(t *testing.T) {
	resetPRFlags()
	setListStateHome(t)
	prov := &fakeListVCS{
		labels: map[int][]string{10: {"p0", "backend"}},
		reviews: map[int][]api.Review{10: {
			{Author: "reviewbot", State: "APPROVED"},
			{Author: "alice", State: "CHANGES_REQUESTED"},
		}},
	}
	wireListFakes(t, prov, []agentregistry.Entry{{Login: "reviewbot"}})
	seedListStore(t, store.PullRequest{
		Repo: "foo/bar", Number: 10, Ownership: "mine", State: "open",
		Author: "phillipg", Branch: "feat/a", Base: "main", HeadSHA: "aaa111",
	})

	got := runPRList(t, "--repo", "foo/bar", "--reviewers")
	if len(got) != 1 {
		t.Fatalf("expected 1 PR, got %d: %+v", len(got), got)
	}
	it := got[0]
	if len(it.Labels) != 2 || it.Labels[0] != "p0" || it.Labels[1] != "backend" {
		t.Errorf("labels wrong: %+v", it.Labels)
	}
	kindByLogin := map[string]string{}
	stateByLogin := map[string]string{}
	for _, r := range it.Reviewers {
		kindByLogin[r.Login] = r.Kind
		stateByLogin[r.Login] = r.State
	}
	if kindByLogin["reviewbot"] != "agent" {
		t.Errorf("reviewbot should classify as agent, got %q (roster=%+v)", kindByLogin["reviewbot"], it.Reviewers)
	}
	if kindByLogin["alice"] != "person" {
		t.Errorf("alice should classify as person, got %q (roster=%+v)", kindByLogin["alice"], it.Reviewers)
	}
	if stateByLogin["reviewbot"] != "APPROVED" || stateByLogin["alice"] != "CHANGES_REQUESTED" {
		t.Errorf("review states wrong: %+v", it.Reviewers)
	}
}

// TestPRList_RosterDedupAndOrder verifies that a reviewer who reviewed multiple
// times collapses to one roster entry with the LATEST state, and that roster
// order follows first appearance.
func TestPRList_RosterDedupAndOrder(t *testing.T) {
	resetPRFlags()
	setListStateHome(t)
	prov := &fakeListVCS{
		reviews: map[int][]api.Review{10: {
			{Author: "bob", State: "COMMENTED"},
			{Author: "carol", State: "CHANGES_REQUESTED"},
			{Author: "bob", State: "APPROVED"}, // later review by bob wins
		}},
	}
	wireListFakes(t, prov, nil)
	seedListStore(t, store.PullRequest{
		Repo: "foo/bar", Number: 10, Ownership: "mine", State: "open",
		Author: "phillipg", Branch: "feat/a", Base: "main", HeadSHA: "aaa111",
	})

	got := runPRList(t, "--repo", "foo/bar", "--reviewers")
	if len(got) != 1 {
		t.Fatalf("expected 1 PR, got %d", len(got))
	}
	roster := got[0].Reviewers
	if len(roster) != 2 {
		t.Fatalf("expected 2 deduped reviewers, got %d: %+v", len(roster), roster)
	}
	if roster[0].Login != "bob" || roster[0].State != "APPROVED" {
		t.Errorf("first reviewer should be bob/APPROVED (latest state, first appearance): %+v", roster[0])
	}
	if roster[1].Login != "carol" || roster[1].State != "CHANGES_REQUESTED" {
		t.Errorf("second reviewer should be carol/CHANGES_REQUESTED: %+v", roster[1])
	}
}

// TestPRList_AugmentationBestEffort verifies that when the live round-trip
// fails, the base fields are still emitted (exit 0) with empty labels/roster
// rather than failing the command.
func TestPRList_AugmentationBestEffort(t *testing.T) {
	resetPRFlags()
	setListStateHome(t)
	prov := &fakeListVCS{
		getErr: errors.New("boom get"),
		revErr: errors.New("boom reviews"),
	}
	wireListFakes(t, prov, nil)
	seedListStore(t, store.PullRequest{
		Repo: "foo/bar", Number: 10, Ownership: "mine", State: "open",
		Author: "phillipg", Branch: "feat/a", Base: "main", HeadSHA: "aaa111",
	})

	got := runPRList(t, "--repo", "foo/bar", "--reviewers")
	if len(got) != 1 {
		t.Fatalf("expected 1 PR even when augmentation fails, got %d: %+v", len(got), got)
	}
	it := got[0]
	if it.HeadSHA != "aaa111" || it.Branch != "feat/a" {
		t.Errorf("base fields must survive augmentation failure: %+v", it)
	}
	if len(it.Labels) != 0 {
		t.Errorf("labels must be empty on provider failure, got %+v", it.Labels)
	}
	if len(it.Reviewers) != 0 {
		t.Errorf("roster must be empty on provider failure, got %+v", it.Reviewers)
	}
	raw, _ := json.Marshal(it)
	if !bytes.Contains(raw, []byte(`"labels":[]`)) || !bytes.Contains(raw, []byte(`"reviewers":[]`)) {
		t.Errorf("labels/reviewers must marshal as [] not null: %s", raw)
	}
}

// TestPRList_EmptyStore verifies that with no store file present, `pr list
// --json` returns an empty JSON array AND does not create a store file as a
// side effect.
func TestPRList_EmptyStore(t *testing.T) {
	resetPRFlags()
	setListStateHome(t) // creates the pg-pr dir but NOT the store.db
	wireListFakes(t, &fakeListVCS{}, nil)

	got := runPRList(t, "--repo", "foo/bar")
	if len(got) != 0 {
		t.Fatalf("expected empty listing with no store, got %d: %+v", len(got), got)
	}
	if _, err := os.Stat(store.DefaultPath()); err == nil {
		t.Errorf("pr list must not create a store file at %s", store.DefaultPath())
	}
}

// fakeBulkListVCS adds the vcs.EnrichedPRsProvider bulk capability on top of
// fakeListVCS so `pr list --reviewers` can collapse the 2N per-PR fan-out into
// one round-trip. It embeds fakeListVCS so the per-PR GetPR/ListReviews counters
// remain available — the bulk-path test asserts they stay at zero.
type fakeBulkListVCS struct {
	fakeListVCS
	enriched  []vcs.EnrichedPR
	enrErr    error
	bulkCalls int
	lastQuery string
}

func (f *fakeBulkListVCS) EnrichedPRs(_ context.Context, _ string, query string) ([]vcs.EnrichedPR, error) {
	f.bulkCalls++
	f.lastQuery = query
	if f.enrErr != nil {
		return nil, f.enrErr
	}
	return f.enriched, nil
}

var (
	_ vcs.Provider            = (*fakeBulkListVCS)(nil)
	_ vcs.EnrichedPRsProvider = (*fakeBulkListVCS)(nil)
)

// TestPRList_ReviewersBulkOneRoundTrip verifies that when the provider
// implements vcs.EnrichedPRsProvider, the --reviewers augmentation collapses to
// ONE bulk round-trip (not 2N per-PR GetPR+ListReviews calls) and that the
// augmented labels + classified roster match the per-PR path (output parity),
// preserving base-list ordering.
func TestPRList_ReviewersBulkOneRoundTrip(t *testing.T) {
	resetPRFlags()
	setListStateHome(t)
	prov := &fakeBulkListVCS{
		enriched: []vcs.EnrichedPR{
			// Deliberately returned out of base-list order to prove the merge
			// keys by PR number and preserves the store's ordering.
			{
				PR: api.PR{Repo: "foo/bar", Number: 11, Labels: []string{"docs"}},
				Reviews: []api.Review{
					{Author: "bob", State: "COMMENTED"},
				},
			},
			{
				PR: api.PR{Repo: "foo/bar", Number: 10, Labels: []string{"p0", "backend"}},
				Reviews: []api.Review{
					{Author: "reviewbot", State: "APPROVED"},
					{Author: "alice", State: "CHANGES_REQUESTED"},
				},
			},
		},
	}
	wireListFakes(t, prov, []agentregistry.Entry{{Login: "reviewbot"}})
	seedListStore(
		t,
		store.PullRequest{
			Repo: "foo/bar", Number: 10, Ownership: "mine", State: "open",
			Author: "phillipg", Branch: "feat/a", Base: "main", HeadSHA: "aaa111",
		},
		store.PullRequest{
			Repo: "foo/bar", Number: 11, Ownership: "team", State: "draft",
			Author: "octocat", Branch: "feat/b", Base: "main", HeadSHA: "bbb222",
		},
	)

	got := runPRList(t, "--repo", "foo/bar", "--reviewers")

	// ONE bulk round-trip, and NONE of the 2N per-PR calls.
	if prov.bulkCalls != 1 {
		t.Errorf("expected exactly 1 bulk EnrichedPRs call, got %d", prov.bulkCalls)
	}
	if prov.getCalls != 0 || prov.revCalls != 0 {
		t.Errorf("bulk path must make NO per-PR calls, got get=%d rev=%d", prov.getCalls, prov.revCalls)
	}
	if !strings.Contains(prov.lastQuery, "repo:foo/bar") {
		t.Errorf("bulk search query should scope to the repo, got %q", prov.lastQuery)
	}

	if len(got) != 2 {
		t.Fatalf("expected 2 PRs, got %d: %+v", len(got), got)
	}
	// Base-list ordering preserved: 10 then 11.
	if got[0].Number != 10 || got[1].Number != 11 {
		t.Errorf("base-list ordering not preserved: %+v", got)
	}

	byNum := map[int]prListItem{}
	for _, it := range got {
		byNum[it.Number] = it
	}
	pr10 := byNum[10]
	if len(pr10.Labels) != 2 || pr10.Labels[0] != "p0" || pr10.Labels[1] != "backend" {
		t.Errorf("PR 10 labels wrong (parity with per-PR path): %+v", pr10.Labels)
	}
	kind := map[string]string{}
	state := map[string]string{}
	for _, r := range pr10.Reviewers {
		kind[r.Login] = r.Kind
		state[r.Login] = r.State
	}
	if kind["reviewbot"] != "agent" || kind["alice"] != "person" {
		t.Errorf("roster classification wrong: %+v", pr10.Reviewers)
	}
	if state["reviewbot"] != "APPROVED" || state["alice"] != "CHANGES_REQUESTED" {
		t.Errorf("roster states wrong: %+v", pr10.Reviewers)
	}
	pr11 := byNum[11]
	if len(pr11.Labels) != 1 || pr11.Labels[0] != "docs" {
		t.Errorf("PR 11 labels wrong: %+v", pr11.Labels)
	}
	if len(pr11.Reviewers) != 1 || pr11.Reviewers[0].Login != "bob" || pr11.Reviewers[0].Kind != "person" {
		t.Errorf("PR 11 roster wrong: %+v", pr11.Reviewers)
	}
}

// TestPRList_ReviewersFallsBackToPerPRWithoutBulk verifies that a provider
// WITHOUT the bulk capability keeps the existing per-PR path: one GetPR + one
// ListReviews per PR (2N calls), with identical augmented output.
func TestPRList_ReviewersFallsBackToPerPRWithoutBulk(t *testing.T) {
	resetPRFlags()
	setListStateHome(t)
	prov := &fakeListVCS{
		labels: map[int][]string{10: {"p0"}, 11: {"docs"}},
		reviews: map[int][]api.Review{
			10: {{Author: "alice", State: "APPROVED"}},
			11: {{Author: "bob", State: "COMMENTED"}},
		},
	}
	// Guard: the plain fake must NOT satisfy the bulk capability, otherwise
	// this test would not exercise the fallback path.
	if _, ok := vcs.Provider(prov).(vcs.EnrichedPRsProvider); ok {
		t.Fatal("fakeListVCS must NOT implement vcs.EnrichedPRsProvider")
	}
	wireListFakes(t, prov, nil)
	seedListStore(
		t,
		store.PullRequest{
			Repo: "foo/bar", Number: 10, Ownership: "mine", State: "open",
			Author: "phillipg", Branch: "feat/a", Base: "main", HeadSHA: "aaa111",
		},
		store.PullRequest{
			Repo: "foo/bar", Number: 11, Ownership: "team", State: "draft",
			Author: "octocat", Branch: "feat/b", Base: "main", HeadSHA: "bbb222",
		},
	)

	got := runPRList(t, "--repo", "foo/bar", "--reviewers")

	// Per-PR fallback: one GetPR + one ListReviews per PR (2N).
	if prov.getCalls != 2 || prov.revCalls != 2 {
		t.Errorf("expected per-PR fallback (2 GetPR + 2 ListReviews), got get=%d rev=%d", prov.getCalls, prov.revCalls)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 PRs, got %d: %+v", len(got), got)
	}
	byNum := map[int]prListItem{}
	for _, it := range got {
		byNum[it.Number] = it
	}
	if len(byNum[10].Labels) != 1 || byNum[10].Labels[0] != "p0" {
		t.Errorf("PR 10 labels wrong on fallback: %+v", byNum[10].Labels)
	}
	if len(byNum[10].Reviewers) != 1 || byNum[10].Reviewers[0].Login != "alice" {
		t.Errorf("PR 10 roster wrong on fallback: %+v", byNum[10].Reviewers)
	}
	if len(byNum[11].Reviewers) != 1 || byNum[11].Reviewers[0].Login != "bob" {
		t.Errorf("PR 11 roster wrong on fallback: %+v", byNum[11].Reviewers)
	}
}

// TestPRList_ReviewersBulkErrorFallsBackToPerPR verifies that when the provider
// HAS the bulk capability but the single EnrichedPRs call fails, augmentPRItems
// falls back to the per-PR path: the one bulk attempt is made (bulkCalls==1) and
// then GetPR+ListReviews run once per PR (2N). Items are still correctly
// augmented via the per-PR path, and the failed bulk attempt leaves no partial
// mutation (final values come entirely from the per-PR fake data).
func TestPRList_ReviewersBulkErrorFallsBackToPerPR(t *testing.T) {
	resetPRFlags()
	setListStateHome(t)
	prov := &fakeBulkListVCS{
		// Bulk call fails; embedded fakeListVCS supplies the per-PR data the
		// fallback path must use. `enriched` is left nil to prove no bulk data
		// can leak: EnrichedPRs returns (nil, enrErr) before the merge loop.
		enrErr: errors.New("boom bulk"),
		fakeListVCS: fakeListVCS{
			labels: map[int][]string{10: {"p0"}, 11: {"docs"}},
			reviews: map[int][]api.Review{
				10: {{Author: "alice", State: "APPROVED"}},
				11: {{Author: "bob", State: "COMMENTED"}},
			},
		},
	}
	wireListFakes(t, prov, nil)
	seedListStore(
		t,
		store.PullRequest{
			Repo: "foo/bar", Number: 10, Ownership: "mine", State: "open",
			Author: "phillipg", Branch: "feat/a", Base: "main", HeadSHA: "aaa111",
		},
		store.PullRequest{
			Repo: "foo/bar", Number: 11, Ownership: "team", State: "draft",
			Author: "octocat", Branch: "feat/b", Base: "main", HeadSHA: "bbb222",
		},
	)

	got := runPRList(t, "--repo", "foo/bar", "--reviewers")

	// One failed bulk attempt, then the full 2N per-PR fallback.
	if prov.bulkCalls != 1 {
		t.Errorf("expected exactly 1 (failed) bulk attempt, got %d", prov.bulkCalls)
	}
	if prov.getCalls != 2 || prov.revCalls != 2 {
		t.Errorf("expected per-PR fallback after bulk error (2 GetPR + 2 ListReviews), got get=%d rev=%d", prov.getCalls, prov.revCalls)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 PRs, got %d: %+v", len(got), got)
	}
	byNum := map[int]prListItem{}
	for _, it := range got {
		byNum[it.Number] = it
	}
	// Augmented via the per-PR path (parity); no partial data from the failed
	// bulk attempt.
	if len(byNum[10].Labels) != 1 || byNum[10].Labels[0] != "p0" {
		t.Errorf("PR 10 labels wrong on bulk-error fallback: %+v", byNum[10].Labels)
	}
	if len(byNum[10].Reviewers) != 1 || byNum[10].Reviewers[0].Login != "alice" || byNum[10].Reviewers[0].State != "APPROVED" {
		t.Errorf("PR 10 roster wrong on bulk-error fallback: %+v", byNum[10].Reviewers)
	}
	if len(byNum[11].Labels) != 1 || byNum[11].Labels[0] != "docs" {
		t.Errorf("PR 11 labels wrong on bulk-error fallback: %+v", byNum[11].Labels)
	}
	if len(byNum[11].Reviewers) != 1 || byNum[11].Reviewers[0].Login != "bob" {
		t.Errorf("PR 11 roster wrong on bulk-error fallback: %+v", byNum[11].Reviewers)
	}
}

// fixedPRListNow pins the freshness clock for the duration of the test so the
// stale verdict is deterministic, restoring the real clock afterwards.
func fixedPRListNow(t *testing.T, now time.Time) {
	t.Helper()
	prev := prListNow
	t.Cleanup(func() { prListNow = prev })
	prListNow = func() time.Time { return now }
}

// TestPRList_JSONCarriesStoreLastSyncedAt is the round-trip proof for the
// freshness as-of half of the seam: the store's pull_request.last_synced_at
// column is emitted VERBATIM as the item's last_synced_at, and a row synced
// inside the bound is NOT flagged stale.
func TestPRList_JSONCarriesStoreLastSyncedAt(t *testing.T) {
	resetPRFlags()
	setListStateHome(t)
	wireListFakes(t, &fakeListVCS{}, nil)

	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	fixedPRListNow(t, now)
	// 30s old: well inside the bound (2 x 60s default cadence).
	synced := now.Add(-30 * time.Second).Format(time.RFC3339)
	seedListStore(t, store.PullRequest{
		Repo: "foo/bar", Number: 10, Ownership: "mine", State: "open",
		Author: "phillipg", Branch: "feat/a", Base: "main", HeadSHA: "aaa111",
		LastSyncedAt: synced,
	})

	got := runPRList(t, "--repo", "foo/bar")
	if len(got) != 1 {
		t.Fatalf("expected 1 PR, got %d: %+v", len(got), got)
	}
	if got[0].LastSyncedAt != synced {
		t.Errorf("last_synced_at must round-trip the store column verbatim: got %q want %q",
			got[0].LastSyncedAt, synced)
	}
	if got[0].Stale {
		t.Errorf("a row synced 30s ago must not be flagged stale: %+v", got[0])
	}
	// The fields must actually be on the wire (not merely on the decoded struct),
	// under exactly these JSON keys — the pr-pool ACL binds to them by name.
	raw := runPRListRaw(t, "--repo", "foo/bar", "--json")
	if !strings.Contains(raw, `"last_synced_at": "`+synced+`"`) {
		t.Errorf("last_synced_at missing from the emitted JSON:\n%s", raw)
	}
	if !strings.Contains(raw, `"stale": false`) {
		t.Errorf("stale flag missing from the emitted JSON:\n%s", raw)
	}
}

// TestPRList_StaleFlagPastBound is the freshness-flag half: an as-of time aged
// past the bound sets stale, one inside it does not, and a row with NO usable
// as-of time is stale (fail closed — a surface that cannot say how old its data
// is must not present it as current).
func TestPRList_StaleFlagPastBound(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name      string
		syncedAt  string
		wantStale bool
	}{
		{"synced this tick", now.Add(-5 * time.Second).Format(time.RFC3339), false},
		{"one tick behind, still inside the bound", now.Add(-90 * time.Second).Format(time.RFC3339), false},
		{"past the bound (daemon behind or stopped)", now.Add(-10 * time.Minute).Format(time.RFC3339), true},
		{"no as-of time recorded at all", "", true},
		{"unparseable as-of time", "not-a-timestamp", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetPRFlags()
			setListStateHome(t)
			wireListFakes(t, &fakeListVCS{}, nil)
			fixedPRListNow(t, now)
			seedListStore(t, store.PullRequest{
				Repo: "foo/bar", Number: 10, Ownership: "mine", State: "open",
				Author: "phillipg", Branch: "feat/a", Base: "main", HeadSHA: "aaa111",
				LastSyncedAt: tc.syncedAt,
			})

			got := runPRList(t, "--repo", "foo/bar")
			if len(got) != 1 {
				t.Fatalf("expected 1 PR, got %d: %+v", len(got), got)
			}
			if got[0].Stale != tc.wantStale {
				t.Errorf("stale = %v, want %v for last_synced_at=%q (age judged at %v)",
					got[0].Stale, tc.wantStale, tc.syncedAt, now)
			}
			// A stale row still reports its base fields — the seam degrades by
			// FLAGGING, never by hiding the PR.
			if got[0].HeadSHA != "aaa111" || got[0].Branch != "feat/a" {
				t.Errorf("base fields must survive the freshness stamp: %+v", got[0])
			}
		})
	}
}

// TestPRList_HumanOutputCarriesFreshness: the operator-facing table also shows
// the as-of age and marks a past-bound row STALE.
func TestPRList_HumanOutputCarriesFreshness(t *testing.T) {
	resetPRFlags()
	setListStateHome(t)
	wireListFakes(t, &fakeListVCS{}, nil)
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	fixedPRListNow(t, now)
	seedListStore(
		t,
		store.PullRequest{
			Repo: "foo/bar", Number: 10, Ownership: "mine", State: "open",
			Author: "phillipg", Branch: "feat/a", Base: "main", HeadSHA: "aaa111",
			LastSyncedAt: now.Add(-30 * time.Second).Format(time.RFC3339),
		},
		store.PullRequest{
			Repo: "foo/bar", Number: 11, Ownership: "team", State: "open",
			Author: "octocat", Branch: "feat/b", Base: "main", HeadSHA: "bbb222",
			LastSyncedAt: now.Add(-1 * time.Hour).Format(time.RFC3339),
		},
	)

	out := runPRListRaw(t, "--repo", "foo/bar")
	if !strings.Contains(out, "SYNCED") {
		t.Errorf("human table must carry a SYNCED (as-of) column:\n%s", out)
	}
	if !strings.Contains(out, "30s ago") {
		t.Errorf("human table must show the fresh row's age:\n%s", out)
	}
	if !strings.Contains(out, "3600s ago STALE") {
		t.Errorf("human table must mark the past-bound row STALE:\n%s", out)
	}
}

// TestPRList_HumanOutput verifies the non-JSON table renderer includes the PR
// number, ownership, and branch.
func TestPRList_HumanOutput(t *testing.T) {
	resetPRFlags()
	setListStateHome(t)
	wireListFakes(t, &fakeListVCS{}, nil)
	seedListStore(t, store.PullRequest{
		Repo: "foo/bar", Number: 10, Ownership: "mine", State: "open",
		Author: "phillipg", Branch: "feat/a", Base: "main", HeadSHA: "aaa111",
	})

	out := runPRListRaw(t, "--repo", "foo/bar")
	for _, want := range []string{"#10", "mine", "feat/a"} {
		if !strings.Contains(out, want) {
			t.Errorf("human output missing %q:\n%s", want, out)
		}
	}
}
