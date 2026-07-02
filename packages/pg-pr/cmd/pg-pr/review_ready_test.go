package main

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/beads"
)

// fakeReadyLister is a test double for the review-ready lister seam.
type fakeReadyLister struct {
	refs []beads.DraftReviewRef
	err  error
}

func (f *fakeReadyLister) ListReadyDraftReviews(_ context.Context) ([]beads.DraftReviewRef, error) {
	return f.refs, f.err
}

func TestReviewReady_JSON(t *testing.T) {
	fake := &fakeReadyLister{refs: []beads.DraftReviewRef{
		{ID: "pg2-a.1", Repo: "owner/repo", Number: 123, Mine: true},
		{ID: "pg2-a.2", Repo: "owner/other", Number: 7, Mine: false},
	}}
	prev := reviewReadyBeadsClient
	t.Cleanup(func() { reviewReadyBeadsClient = prev })
	reviewReadyBeadsClient = func(_ string) reviewReadyLister { return fake }

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"review", "ready", "--json"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("review ready: %v (stderr=%s)", err, stderr.String())
	}

	var got []struct {
		ID     string `json:"id"`
		Title  string `json:"title"`
		Repo   string `json:"repo"`
		Number int    `json:"number"`
		Mine   bool   `json:"mine"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal output %q: %v", stdout.String(), err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 entries, got %d: %+v", len(got), got)
	}
	if got[0].ID != "pg2-a.1" || got[0].Repo != "owner/repo" || got[0].Number != 123 || !got[0].Mine {
		t.Fatalf("entry 0 wrong: %+v", got[0])
	}
	if got[1].Mine {
		t.Fatalf("entry 1 should not be mine: %+v", got[1])
	}
}

func TestReviewReady_JSON_Empty(t *testing.T) {
	fake := &fakeReadyLister{refs: nil}
	prev := reviewReadyBeadsClient
	t.Cleanup(func() { reviewReadyBeadsClient = prev })
	reviewReadyBeadsClient = func(_ string) reviewReadyLister { return fake }

	var stdout bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetArgs([]string{"review", "ready", "--json"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("review ready: %v", err)
	}
	// Empty must be a JSON array, not null.
	var got []any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal %q: %v", stdout.String(), err)
	}
	if len(got) != 0 {
		t.Fatalf("want empty array, got %+v", got)
	}
}
