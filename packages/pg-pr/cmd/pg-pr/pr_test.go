package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/vcs"
)

// fakeVCS satisfies vcs.Provider with controllable GetPR output.
type fakeVCS struct {
	pr  *api.PR
	err error
}

func (f *fakeVCS) GetPR(_ context.Context, repo string, n int) (*api.PR, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := *f.pr
	out.Repo = repo
	out.Number = n
	return &out, nil
}
func (f *fakeVCS) ListMyPRs(context.Context, string) ([]api.PR, error)             { return nil, nil }
func (f *fakeVCS) ListTeamPRs(context.Context, string, []string) ([]api.PR, error) { return nil, nil }
func (f *fakeVCS) CreatePR(context.Context, string, bool, string, string, string, string, []string, []string) (*api.PR, error) {
	return nil, nil
}
func (f *fakeVCS) UpdatePR(context.Context, string, int, string) error   { return nil }
func (f *fakeVCS) SetDraft(context.Context, string, int, bool) error     { return nil }
func (f *fakeVCS) SetAutomerge(context.Context, string, int, bool) error { return nil }
func (f *fakeVCS) Merge(context.Context, string, int) error              { return nil }
func (f *fakeVCS) Close(context.Context, string, int) error              { return nil }
func (f *fakeVCS) ListComments(context.Context, string, int) ([]api.Comment, error) {
	return nil, nil
}
func (f *fakeVCS) AddComment(context.Context, string, int, string) (*api.Comment, error) {
	return nil, nil
}
func (f *fakeVCS) ReplyToThread(context.Context, string, string, string) (*api.Comment, error) {
	return nil, nil
}
func (f *fakeVCS) ResolveThread(context.Context, string, string) error { return nil }
func (f *fakeVCS) PostReview(context.Context, string, int, string, []api.Comment) (*api.Review, error) {
	return nil, nil
}
func (f *fakeVCS) ListReviews(context.Context, string, int) ([]api.Review, error) {
	return nil, nil
}

// Compile check.
var _ vcs.Provider = (*fakeVCS)(nil)

// resetPRFlags clears mutable state between cobra tests since flag values
// persist across rootCmd.Execute() calls.
func resetPRFlags() {
	prF = prFlags{}
}

func TestPRShow_HumanOutput(t *testing.T) {
	resetPRFlags()
	prev := vcsProviderFor
	t.Cleanup(func() { vcsProviderFor = prev })
	vcsProviderFor = func(string) vcs.Provider {
		return &fakeVCS{pr: &api.PR{
			Repo: "foo/bar", State: "open", Branch: "feat/x", Base: "main",
			Author: "phillipg", URL: "https://github.com/foo/bar/pull/42",
		}}
	}

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"pr", "show", "42", "--repo", "foo/bar"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v (stderr=%s)", err, stderr.String())
	}
	got := stdout.String()
	if !strings.Contains(got, "number: 42") {
		t.Fatalf("expected number: 42 in output: %q", got)
	}
	if !strings.Contains(got, "author: phillipg") {
		t.Fatalf("expected author: phillipg in output: %q", got)
	}
}

func TestPRShow_JSONOutput(t *testing.T) {
	resetPRFlags()
	prev := vcsProviderFor
	t.Cleanup(func() { vcsProviderFor = prev })
	vcsProviderFor = func(string) vcs.Provider {
		return &fakeVCS{pr: &api.PR{
			Repo: "foo/bar", State: "open", Branch: "feat/x", Base: "main",
			Author: "phillipg", URL: "https://github.com/foo/bar/pull/42",
		}}
	}

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"pr", "show", "42", "--repo", "foo/bar", "--json"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v (stderr=%s)", err, stderr.String())
	}
	got := stdout.String()
	if !strings.Contains(got, `"number": 42`) {
		t.Fatalf("expected JSON number: 42, got: %q", got)
	}
}

func TestPRInfo_NoStoreEntry(t *testing.T) {
	resetPRFlags()
	// Point XDG_STATE_HOME at an empty temp dir so the store is always empty.
	// DefaultPath() appends "pg-pr/store.db", so create that subdirectory.
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, "pg-pr"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Setenv("XDG_STATE_HOME", tmp)

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"pr", "info", "7", "--repo", "foo/bar"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v (stderr=%s)", err, stderr.String())
	}
	got := stdout.String()
	if !strings.Contains(got, "not yet synced") {
		t.Fatalf("expected 'not yet synced' in output: %q", got)
	}
}

func TestPRInfo_WithStoreEntry(t *testing.T) {
	resetPRFlags()
	// Seed the store with an enriched PR and point XDG_STATE_HOME at it.
	// DefaultPath() appends "pg-pr/store.db", so create that subdirectory.
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, "pg-pr"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Setenv("XDG_STATE_HOME", tmp)

	db, err := store.Open(store.DefaultPath())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	ctx := context.Background()
	if _, err := db.UpsertPR(ctx, store.PullRequest{
		Repo: "foo/bar", Number: 7, Ownership: "mine", State: "open", Author: "phillipg",
	}); err != nil {
		t.Fatalf("upsert pr: %v", err)
	}
	if err := db.SetEnrichment(ctx, "foo/bar", 7, store.Enrichment{
		Kind: "bugfix", Size: "M", Urgency: "high",
		Languages:      []string{"Go"},
		UrgencyReasons: []string{"label:p0"},
	}); err != nil {
		t.Fatalf("set enrichment: %v", err)
	}
	_ = db.Close()

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"pr", "info", "7", "--repo", "foo/bar"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v (stderr=%s)", err, stderr.String())
	}
	got := stdout.String()
	for _, want := range []string{"bugfix", "M", "high", "Go", "label:p0"} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in output: %q", want, got)
		}
	}
}

func TestPRShow_InvalidNumber(t *testing.T) {
	resetPRFlags()
	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"pr", "show", "abc"})

	if err := rootCmd.Execute(); err == nil {
		t.Fatalf("expected error for non-numeric PR id")
	}
}

func TestRenderEnrichment(t *testing.T) {
	pr := &store.PullRequest{
		Repo: "o/r", Number: 7, Kind: "bugfix", Size: "M", Urgency: "high",
		Languages: []string{"Go", "Nix"}, UrgencyReasons: []string{"label:p0"},
	}
	var b strings.Builder
	if err := renderEnrichment(&b, pr); err != nil {
		t.Fatalf("renderEnrichment: %v", err)
	}
	out := b.String()
	for _, want := range []string{"bugfix", "M", "high", "Go", "Nix", "label:p0"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered output missing %q:\n%s", want, out)
		}
	}
}
