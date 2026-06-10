package sync

import (
	"context"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/beads"
)

// fakeDepBeads embeds noopBeads (which already satisfies the BeadClient
// interface) and adds the depTreeReader methods so buildPRInput's dep path runs
// without a *beads.Client / real bd workspace.
type fakeDepBeads struct {
	noopBeads
	mrID  string
	deps  []beads.DepNode
	human map[string]bool
}

func (f *fakeDepBeads) FindByRepoAndNumber(_ context.Context, _ string, _ int) (*beads.MergeRequest, error) {
	if f.mrID == "" {
		return nil, nil
	}
	return &beads.MergeRequest{ID: f.mrID}, nil
}
func (f *fakeDepBeads) DepTreeUp(_ context.Context, _ string) ([]beads.DepNode, error) {
	return f.deps, nil
}
func (f *fakeDepBeads) HumanLabeledBeads(_ context.Context) (map[string]bool, error) {
	return f.human, nil
}

func TestBuildPRInput_AppliesHumanLabelWithoutCache(t *testing.T) {
	bdc := &fakeDepBeads{
		mrID:  "mr-1",
		deps:  []beads.DepNode{{ID: "fb-1", Status: "open"}},
		human: map[string]bool{"fb-1": true},
	}
	e, err := New(Deps{
		Cfg:   &config.Config{SelfLogin: "me", Repos: []config.RepoConfig{{Remote: "o/r"}}},
		VCS:   map[string]VCSProvider{"github": &fakeVCS{}},
		Beads: bdc,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	pr := api.PR{Repo: "o/r", Number: 1, Author: "me", State: "open"}

	in := e.buildPRInput(context.Background(), pr, nil, bdc, nil, config.RepoConfig{Remote: "o/r"})

	if len(in.BeadsDeps) != 1 {
		t.Fatalf("want 1 dep, got %d", len(in.BeadsDeps))
	}
	found := false
	for _, l := range in.BeadsDeps[0].Labels {
		if l == "human" {
			found = true
		}
	}
	if !found {
		t.Fatal("human label not applied on cache-less path; WaitingOnMe will regress")
	}
}

// ----------------------------------------------------------------------
// refreshPR tests
// ----------------------------------------------------------------------

// refreshFakeBeads embeds noopBeads (full BeadClient) and records the
// signals the three refreshPR outcomes are distinguished by:
//   - lastState: the State passed to the most recent EnsureMergeRequest.
//   - closed: whether CloseMergeRequest was called.
//   - feedbackRan: whether the active pipeline reached
//     ListFeedbackPendingReply (only the active path does; the dormant
//     team-draft and closed/merged paths do not).
//
// existing, when non-nil, is returned by ListMergeRequests so findBeadByPR
// locates a pre-existing open bead to close on the merged path.
type refreshFakeBeads struct {
	noopBeads
	existing    *beads.MergeRequest
	lastState   string
	closed      bool
	feedbackRan bool
}

func (f *refreshFakeBeads) EnsureMergeRequest(_ context.Context, _ string, fields beads.MergeRequestFields) (string, bool, error) {
	f.lastState = fields.State
	return "mr-1", false, nil
}

func (f *refreshFakeBeads) ListMergeRequests(_ context.Context, _ bool) ([]beads.MergeRequest, error) {
	if f.existing == nil {
		return nil, nil
	}
	return []beads.MergeRequest{*f.existing}, nil
}

func (f *refreshFakeBeads) CloseMergeRequest(_ context.Context, _, _ string) error {
	f.closed = true
	return nil
}

func (f *refreshFakeBeads) ListFeedbackPendingReply(_ context.Context) ([]beads.Feedback, error) {
	f.feedbackRan = true
	return nil, nil
}

// newRefreshEngine builds an Engine over a single repo "o/r" with the given
// self login, the supplied fake bead client, and a fakeVCS whose GetPR
// returns pr. The fakeVCS doubles as a ThreadReplier so the active path's
// processReplyDrafts reaches ListFeedbackPendingReply.
func newRefreshEngine(t *testing.T, self string, bdc BeadClient, pr api.PR) *Engine {
	t.Helper()
	vcs := newFakeVCS()
	vcs.views[keyOf("o/r", pr.Number)] = pr
	e, err := New(Deps{
		Cfg: &config.Config{
			SelfLogin: self,
			Repos: []config.RepoConfig{
				{Remote: "o/r", VCS: "github", TeamMembers: []string{"teammate"}},
			},
		},
		VCS:      map[string]VCSProvider{"github": vcs},
		Beads:    bdc,
		StateDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return e
}

func TestRefreshPR_ClosedMerged_ClosesAndRemoves(t *testing.T) {
	bdc := &refreshFakeBeads{
		existing: &beads.MergeRequest{
			ID:     "mr-1",
			Fields: beads.MergeRequestFields{Repo: "o/r", PRNumber: 1},
		},
	}
	pr := api.PR{
		Repo: "o/r", Number: 1, State: "merged", Merged: true,
		Author: "me", URL: "https://github.com/o/r/pull/1",
	}
	e := newRefreshEngine(t, "me", bdc, pr)

	in, err := e.refreshPR(context.Background(), "o/r", 1)
	if err != nil {
		t.Fatalf("refreshPR: %v", err)
	}
	if in != nil {
		t.Fatalf("merged PR must be removed (nil input); got %+v", in)
	}
	if !bdc.closed {
		t.Fatal("expected the existing open bead to be closed")
	}
	if bdc.feedbackRan {
		t.Fatal("closed path must not run the active pipeline")
	}
}

func TestRefreshPR_TeamDraft_MarksDraftKeepsBeadHidden(t *testing.T) {
	bdc := &refreshFakeBeads{}
	pr := api.PR{
		Repo: "o/r", Number: 2, State: "open", Draft: true,
		Author: "teammate", URL: "https://github.com/o/r/pull/2",
	}
	e := newRefreshEngine(t, "me", bdc, pr)

	in, err := e.refreshPR(context.Background(), "o/r", 2)
	if err != nil {
		t.Fatalf("refreshPR: %v", err)
	}
	if in != nil {
		t.Fatalf("hidden team draft must be removed (nil input); got %+v", in)
	}
	if bdc.lastState != "draft" {
		t.Fatalf("EnsureMergeRequest State: got %q want \"draft\"", bdc.lastState)
	}
	if bdc.closed {
		t.Fatal("team draft bead must not be closed")
	}
	if bdc.feedbackRan {
		t.Fatal("dormant team-draft path must not run the active pipeline")
	}
}

func TestRefreshPR_ActiveMine_UpsertsSnapshot(t *testing.T) {
	bdc := &refreshFakeBeads{}
	pr := api.PR{
		Repo: "o/r", Number: 3, State: "open",
		Author: "me", URL: "https://github.com/o/r/pull/3",
	}
	e := newRefreshEngine(t, "me", bdc, pr)

	in, err := e.refreshPR(context.Background(), "o/r", 3)
	if err != nil {
		t.Fatalf("refreshPR: %v", err)
	}
	if in == nil {
		t.Fatal("active self PR must yield a non-nil snapshot input")
	}
	if in.PR.Number != 3 {
		t.Fatalf("input PR.Number: got %d want 3", in.PR.Number)
	}
	if bdc.closed {
		t.Fatal("active PR bead must not be closed")
	}
	if !bdc.feedbackRan {
		t.Fatal("active path must run the full pipeline (ListFeedbackPendingReply)")
	}
}

func TestEngineCfg_AtomicSwap(t *testing.T) {
	e, err := New(Deps{
		Cfg:   &config.Config{SelfLogin: "old"},
		VCS:   map[string]VCSProvider{"github": &fakeVCS{}},
		Beads: &refreshFakeBeads{},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if e.cfg().SelfLogin != "old" {
		t.Fatalf("cfg() initial = %q", e.cfg().SelfLogin)
	}
	e.ReplaceCfg(&config.Config{SelfLogin: "new"})
	if e.cfg().SelfLogin != "new" {
		t.Fatalf("cfg() after swap = %q", e.cfg().SelfLogin)
	}
}
