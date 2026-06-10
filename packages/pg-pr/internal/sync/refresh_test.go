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
