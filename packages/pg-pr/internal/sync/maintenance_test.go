package sync

import (
	"context"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/config"
)

func TestRefreshHumanLabels_PopulatesAtomicSet(t *testing.T) {
	bdc := &fakeDepBeads{human: map[string]bool{"fb-1": true}}
	e, err := New(Deps{
		Cfg:   &config.Config{Repos: []config.RepoConfig{{Remote: "o/r"}}},
		VCS:   map[string]VCSProvider{"github": &fakeVCS{}},
		Beads: bdc,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	e.refreshHumanLabels(context.Background())

	got := e.humanLabelsFor("o/r")
	if !got["fb-1"] {
		t.Fatalf("humanLabelsFor(o/r) missing fb-1; got %v", got)
	}
	if e.humanLabelsFor("absent") != nil {
		t.Fatal("humanLabelsFor for an unknown repo must be nil")
	}
}

func TestHumanLabelsFor_NilBeforeFirstPull(t *testing.T) {
	e, err := New(Deps{
		Cfg:   &config.Config{Repos: []config.RepoConfig{{Remote: "o/r"}}},
		VCS:   map[string]VCSProvider{"github": &fakeVCS{}},
		Beads: &fakeDepBeads{},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if e.humanLabelsFor("o/r") != nil {
		t.Fatal("humanLabelsFor must be nil before the first pull")
	}
}

func TestMaintenanceCycle_RefreshesLabelsAndDrainsReplies(t *testing.T) {
	// refreshFakeBeads embeds noopBeads, which lacks HumanLabeledBeads, so the
	// label pull is skipped for it; this test asserts the reply drain ran.
	// TestMaintenanceCycle_PullsLabels covers the label pull via fakeDepBeads.
	bdc := &refreshFakeBeads{}
	e, err := New(Deps{
		Cfg:   &config.Config{SelfLogin: "me", Repos: []config.RepoConfig{{Remote: "o/r", VCS: "github"}}},
		VCS:   map[string]VCSProvider{"github": newFakeVCS()},
		Beads: bdc,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	e.maintenanceCycle(context.Background(), NewTextLogger())

	if !bdc.replyDrainRan {
		t.Fatal("maintenanceCycle must drain replies")
	}
}

func TestMaintenanceCycle_PullsLabels(t *testing.T) {
	bdc := &fakeDepBeads{human: map[string]bool{"fb-9": true}}
	e, err := New(Deps{
		Cfg:   &config.Config{Repos: []config.RepoConfig{{Remote: "o/r"}}},
		VCS:   map[string]VCSProvider{"github": newFakeVCS()},
		Beads: bdc,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	e.maintenanceCycle(context.Background(), NewTextLogger())

	if !e.humanLabelsFor("o/r")["fb-9"] {
		t.Fatal("maintenanceCycle must refresh the human-label set")
	}
}
