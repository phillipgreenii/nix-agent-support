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
