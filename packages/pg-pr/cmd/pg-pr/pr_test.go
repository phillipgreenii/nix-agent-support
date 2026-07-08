package main

import (
	"bytes"
	"context"
	"encoding/json"
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

func (f *fakeVCS) ListMyPRs(context.Context, string) ([]api.PR, error) { return nil, nil }

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
func (f *fakeVCS) PostReview(context.Context, string, int, string, string, []api.Comment) (*api.Review, error) {
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

// TestPRInfo_AliasOfShow verifies that `pr info` renders the base PR metadata
// exactly like `pr show` (the historical alias behavior), even when no store
// exists. XDG_STATE_HOME points at an empty temp dir so DefaultPath() does not
// resolve, exercising the stat-guard skip path in appendEnrichment.
func TestPRInfo_AliasOfShow(t *testing.T) {
	resetPRFlags()
	t.Setenv("XDG_STATE_HOME", t.TempDir()) // no pg-pr/store.db inside → stat-guard skips

	prev := vcsProviderFor
	t.Cleanup(func() { vcsProviderFor = prev })
	vcsProviderFor = func(string) vcs.Provider {
		return &fakeVCS{pr: &api.PR{
			Repo: "foo/bar", State: "open", Branch: "feat/x", Base: "main",
			Author: "phillipg", URL: "https://github.com/foo/bar/pull/7",
		}}
	}

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"pr", "info", "7", "--repo", "foo/bar"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v (stderr=%s)", err, stderr.String())
	}
	got := stdout.String()
	if !strings.Contains(got, "number: 7") {
		t.Fatalf("expected number: 7 in output: %q", got)
	}
	if !strings.Contains(got, "author: phillipg") {
		t.Fatalf("expected author: phillipg in output: %q", got)
	}
}

// TestPRInfo_ShowPlusEnrichment seeds the store with an enriched row AND wires a
// fake VCS provider so the show render succeeds. The output must contain BOTH
// the base PR metadata and the enrichment section.
func TestPRInfo_ShowPlusEnrichment(t *testing.T) {
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

	prev := vcsProviderFor
	t.Cleanup(func() { vcsProviderFor = prev })
	vcsProviderFor = func(string) vcs.Provider {
		return &fakeVCS{pr: &api.PR{
			Repo: "foo/bar", State: "open", Branch: "feat/x", Base: "main",
			Author: "phillipg", URL: "https://github.com/foo/bar/pull/7",
		}}
	}

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"pr", "info", "7", "--repo", "foo/bar"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v (stderr=%s)", err, stderr.String())
	}
	got := stdout.String()
	// Base PR metadata (from the show render) AND enrichment fields.
	for _, want := range []string{"number: 7", "author: phillipg", "bugfix", "M", "high", "Go", "label:p0"} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in output: %q", want, got)
		}
	}
}

// TestPRInfo_NoStore verifies that `pr info` still works (renders the PR, no
// enrichment) when no store file exists at all.
func TestPRInfo_NoStore(t *testing.T) {
	resetPRFlags()
	// Empty temp dir with no pg-pr/store.db → stat-guard in appendEnrichment
	// skips, and nothing is created.
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	prev := vcsProviderFor
	t.Cleanup(func() { vcsProviderFor = prev })
	vcsProviderFor = func(string) vcs.Provider {
		return &fakeVCS{pr: &api.PR{
			Repo: "foo/bar", State: "open", Branch: "feat/x", Base: "main",
			Author: "phillipg", URL: "https://github.com/foo/bar/pull/7",
		}}
	}

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"pr", "info", "7", "--repo", "foo/bar"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v (stderr=%s)", err, stderr.String())
	}
	got := stdout.String()
	if !strings.Contains(got, "number: 7") {
		t.Fatalf("expected number: 7 in output: %q", got)
	}
	// No enrichment section appended (store absent); "Kind:" is the enrichment
	// renderer's first line.
	if strings.Contains(got, "Kind:") {
		t.Errorf("did not expect enrichment section without a store: %q", got)
	}
	// The stat-guard must not have created the store file.
	if _, statErr := os.Stat(store.DefaultPath()); statErr == nil {
		t.Errorf("appendEnrichment created a store file at %s; it must not", store.DefaultPath())
	}
}

// TestPRInfo_JSONIsValid verifies that `pr info --json` produces valid JSON
// (the show render only), with NO trailing plain-text enrichment lines, even
// when a seeded store row exists. The JSON-flag guard in appendEnrichment must
// skip the plain-text append so the output stays parseable.
func TestPRInfo_JSONIsValid(t *testing.T) {
	resetPRFlags()
	// Seed an enriched store row so, without the JSON guard, appendEnrichment
	// would otherwise emit plain-text lines after the JSON object.
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

	prev := vcsProviderFor
	t.Cleanup(func() { vcsProviderFor = prev })
	vcsProviderFor = func(string) vcs.Provider {
		return &fakeVCS{pr: &api.PR{
			Repo: "foo/bar", State: "open", Branch: "feat/x", Base: "main",
			Author: "phillipg", URL: "https://github.com/foo/bar/pull/7",
		}}
	}

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"pr", "info", "7", "--repo", "foo/bar", "--json"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v (stderr=%s)", err, stderr.String())
	}
	got := stdout.String()

	// Whole output must be valid JSON — no trailing plain-text enrichment.
	var obj map[string]any
	if err := json.Unmarshal([]byte(got), &obj); err != nil {
		t.Fatalf("pr info --json output is not valid JSON: %v\noutput:\n%s", err, got)
	}
	if got, want := obj["number"], float64(7); got != want {
		t.Errorf("number = %v, want %v", got, want)
	}
	// The plain-text enrichment renderer's first line must NOT appear.
	if strings.Contains(got, "Kind:") {
		t.Errorf("--json output must not contain plain-text enrichment lines: %q", got)
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
