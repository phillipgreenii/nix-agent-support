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

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/marker"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/vcs"
)

// reviewFakeVCS records the inputs to write methods.
type reviewFakeVCS struct {
	postedBody     string
	postedComments []api.Comment
	addBody        string
	resolveErr     error
}

func (f *reviewFakeVCS) GetPR(context.Context, string, int) (*api.PR, error) { return &api.PR{}, nil }
func (f *reviewFakeVCS) ListMyPRs(context.Context, string) ([]api.PR, error) { return nil, nil }
func (f *reviewFakeVCS) ListTeamPRs(context.Context, string, []string) ([]api.PR, error) {
	return nil, nil
}
func (f *reviewFakeVCS) CreatePR(context.Context, string, bool, string, string, string, string) (*api.PR, error) {
	return nil, nil
}
func (f *reviewFakeVCS) UpdatePR(context.Context, string, int, string) error   { return nil }
func (f *reviewFakeVCS) SetDraft(context.Context, string, int, bool) error     { return nil }
func (f *reviewFakeVCS) SetAutomerge(context.Context, string, int, bool) error { return nil }
func (f *reviewFakeVCS) Merge(context.Context, string, int) error              { return nil }
func (f *reviewFakeVCS) Close(context.Context, string, int) error              { return nil }
func (f *reviewFakeVCS) ListComments(context.Context, string, int) ([]api.Comment, error) {
	return nil, nil
}
func (f *reviewFakeVCS) AddComment(_ context.Context, _ string, _ int, body string) (*api.Comment, error) {
	f.addBody = body
	return &api.Comment{ID: "IC_x", Body: body}, nil
}
func (f *reviewFakeVCS) ReplyToThread(context.Context, string, string, string) (*api.Comment, error) {
	return nil, errors.New("not implemented")
}
func (f *reviewFakeVCS) ResolveThread(context.Context, string, string) error { return f.resolveErr }
func (f *reviewFakeVCS) PostReview(_ context.Context, _ string, _ int, body string, comments []api.Comment) (*api.Review, error) {
	f.postedBody = body
	f.postedComments = comments
	return &api.Review{ID: "RV_x", State: "pending", Body: body}, nil
}

var _ vcs.Provider = (*reviewFakeVCS)(nil)

func resetReviewFlags() {
	rvF = reviewFlags{}
}

func TestReviewDraft_PersistsToStateDir(t *testing.T) {
	resetReviewFlags()
	dir := t.TempDir()
	t.Setenv("PG_PR_STATE_HOME", dir)

	in := `{"body":"top","comments":[{"path":"a.go","line":1,"body":"x"}]}`
	rootCmd.SetIn(strings.NewReader(in))
	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"review", "draft", "42", "--repo", "foo/bar"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v (stderr=%s)", err, stderr.String())
	}
	files, _ := filepath.Glob(filepath.Join(dir, "reviews", "*.json"))
	if len(files) != 1 {
		t.Fatalf("expected 1 staged file, got %d: %v", len(files), files)
	}
}

func TestReviewPost_AppliesMarkerAndPosts(t *testing.T) {
	resetReviewFlags()
	dir := t.TempDir()
	t.Setenv("PG_PR_STATE_HOME", dir)

	// First stage a draft.
	in := `{"body":"top","comments":[{"path":"a.go","line":1,"body":"x"}]}`
	rootCmd.SetIn(strings.NewReader(in))
	rootCmd.SetOut(io_discard)
	rootCmd.SetErr(io_discard)
	rootCmd.SetArgs([]string{"review", "draft", "42", "--repo", "foo/bar"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("draft: %v", err)
	}

	// Now post with a fake provider.
	resetReviewFlags()
	prev := vcsProviderFor
	t.Cleanup(func() { vcsProviderFor = prev })
	fake := &reviewFakeVCS{}
	vcsProviderFor = func(string) vcs.Provider { return fake }

	rootCmd.SetIn(strings.NewReader(""))
	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"review", "post", "42", "--repo", "foo/bar"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("post: %v (stderr=%s)", err, stderr.String())
	}

	if !strings.Contains(fake.postedBody, marker.Glyph) {
		t.Fatalf("body should have marker: %q", fake.postedBody)
	}
	if len(fake.postedComments) != 1 {
		t.Fatalf("expected 1 comment, got %d", len(fake.postedComments))
	}
	if !strings.Contains(fake.postedComments[0].Body, marker.Glyph) {
		t.Fatalf("comment should have marker: %q", fake.postedComments[0].Body)
	}
	// Staged draft should be cleared.
	files, _ := filepath.Glob(filepath.Join(dir, "reviews", "*.json"))
	if len(files) != 0 {
		t.Fatalf("expected no staged files after post, got %d", len(files))
	}
}

func TestReviewSubmit_NoStaging(t *testing.T) {
	resetReviewFlags()
	dir := t.TempDir()
	t.Setenv("PG_PR_STATE_HOME", dir)
	prev := vcsProviderFor
	t.Cleanup(func() { vcsProviderFor = prev })
	fake := &reviewFakeVCS{}
	vcsProviderFor = func(string) vcs.Provider { return fake }

	in := `{"body":"top","comments":[{"path":"a.go","line":1,"body":"x"}]}`
	rootCmd.SetIn(strings.NewReader(in))
	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"review", "submit", "42", "--repo", "foo/bar"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("submit: %v (stderr=%s)", err, stderr.String())
	}

	if !strings.Contains(fake.postedBody, marker.Glyph) {
		t.Fatalf("body should have marker: %q", fake.postedBody)
	}
	// No staging file produced.
	files, _ := filepath.Glob(filepath.Join(dir, "reviews", "*.json"))
	if len(files) != 0 {
		t.Fatalf("submit should not persist staging, got %d files", len(files))
	}
}

func TestCommentAdd_AppliesMarker(t *testing.T) {
	resetReviewFlags()
	prev := vcsProviderFor
	t.Cleanup(func() { vcsProviderFor = prev })
	fake := &reviewFakeVCS{}
	vcsProviderFor = func(string) vcs.Provider { return fake }

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"comment", "add", "42", "--repo", "foo/bar", "--body", "hello"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("comment add: %v (stderr=%s)", err, stderr.String())
	}
	if !strings.Contains(fake.addBody, marker.Glyph) {
		t.Fatalf("AddComment body missing marker: %q", fake.addBody)
	}
}

func TestCommentRespond_NotImplemented(t *testing.T) {
	resetReviewFlags()
	rootCmd.SetArgs([]string{"comment", "respond", "fb-abc", "--repo", "foo/bar"})
	if err := rootCmd.Execute(); err == nil {
		t.Fatalf("expected not-implemented error")
	} else if !strings.Contains(err.Error(), "Phase 3") {
		t.Fatalf("expected mention of Phase 3, got: %v", err)
	}
}

func TestReviewPost_JSONOutput(t *testing.T) {
	resetReviewFlags()
	dir := t.TempDir()
	t.Setenv("PG_PR_STATE_HOME", dir)

	in := `{"body":"top","comments":[]}`
	rootCmd.SetIn(strings.NewReader(in))
	rootCmd.SetOut(io_discard)
	rootCmd.SetErr(io_discard)
	rootCmd.SetArgs([]string{"review", "draft", "42", "--repo", "foo/bar"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("draft: %v", err)
	}

	resetReviewFlags()
	prev := vcsProviderFor
	t.Cleanup(func() { vcsProviderFor = prev })
	vcsProviderFor = func(string) vcs.Provider { return &reviewFakeVCS{} }

	rootCmd.SetIn(strings.NewReader(""))
	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"review", "post", "42", "--repo", "foo/bar", "--json"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("post --json: %v (stderr=%s)", err, stderr.String())
	}

	var parsed map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &parsed); err != nil {
		t.Fatalf("not JSON: %v -- %q", err, stdout.String())
	}
	if parsed["status"] != "posted" {
		t.Fatalf("status: %v", parsed["status"])
	}
}

// io_discard is a minimal io.Writer that drops all output.
var io_discard = nopWriter{}

type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }

// Quiet unused-import linter; this helper file references os.
var _ = os.Stdout
