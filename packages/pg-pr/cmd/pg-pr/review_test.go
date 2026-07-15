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
	postedBody       string
	postedCommitID   string
	postedComments   []api.Comment
	postCalls        int
	hasPending       bool
	hasPendingErr    error
	addBody          string
	resolveErr       error
	replyThreadID    string
	replyBody        string
	replyRepo        string
	replyErr         error
	replyReturnedNil bool
}

func (f *reviewFakeVCS) GetPR(context.Context, string, int) (*api.PR, error) { return &api.PR{}, nil }
func (f *reviewFakeVCS) ListMyPRs(context.Context, string) ([]api.PR, error) { return nil, nil }
func (f *reviewFakeVCS) ListTeamPRs(context.Context, string, []string) ([]api.PR, error) {
	return nil, nil
}

func (f *reviewFakeVCS) CreatePR(context.Context, string, bool, string, string, string, string, []string, []string) (*api.PR, error) {
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

func (f *reviewFakeVCS) ReplyToThread(_ context.Context, repo, threadID, body string) (*api.Comment, error) {
	f.replyRepo = repo
	f.replyThreadID = threadID
	f.replyBody = body
	if f.replyErr != nil {
		return nil, f.replyErr
	}
	if f.replyReturnedNil {
		return nil, nil
	}
	return &api.Comment{ID: "IC_reply", Body: body, ThreadID: threadID}, nil
}
func (f *reviewFakeVCS) ResolveThread(context.Context, string, string) error { return f.resolveErr }
func (f *reviewFakeVCS) PostReview(_ context.Context, _ string, _ int, commitID, body string, comments []api.Comment) (*api.Review, error) {
	f.postCalls++
	f.postedCommitID = commitID
	f.postedBody = body
	f.postedComments = comments
	return &api.Review{ID: "RV_x", State: "pending", Body: body}, nil
}

// HasPendingReviewByViewer satisfies the optional pendingReviewChecker
// capability probed by the review post/submit skip-if-pending guard (pg2-3fo3c).
func (f *reviewFakeVCS) HasPendingReviewByViewer(context.Context, string, int) (bool, error) {
	return f.hasPending, f.hasPendingErr
}

func (f *reviewFakeVCS) ListReviews(context.Context, string, int) ([]api.Review, error) {
	return nil, nil
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

	if !marker.IsOurs(fake.postedBody) {
		t.Fatalf("body should have marker: %q", fake.postedBody)
	}
	if len(fake.postedComments) != 1 {
		t.Fatalf("expected 1 comment, got %d", len(fake.postedComments))
	}
	if !marker.IsOurs(fake.postedComments[0].Body) {
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

	if !marker.IsOurs(fake.postedBody) {
		t.Fatalf("body should have marker: %q", fake.postedBody)
	}
	// No staging file produced.
	files, _ := filepath.Glob(filepath.Join(dir, "reviews", "*.json"))
	if len(files) != 0 {
		t.Fatalf("submit should not persist staging, got %d files", len(files))
	}
}

// TestReviewSubmit_SkipsWhenPendingReviewExists: re-running the submit path (the
// path the pr-pool review role drives) against a PR that already has this
// reviewer's PENDING review must NOT stack a second PENDING review (pg2-3fo3c).
func TestReviewSubmit_SkipsWhenPendingReviewExists(t *testing.T) {
	resetReviewFlags()
	t.Setenv("PG_PR_STATE_HOME", t.TempDir())
	prev := vcsProviderFor
	t.Cleanup(func() { vcsProviderFor = prev })
	fake := &reviewFakeVCS{hasPending: true}
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

	if fake.postCalls != 0 {
		t.Fatalf("PostReview must NOT be called when a pending review already exists, got %d call(s)", fake.postCalls)
	}
	if !strings.Contains(stdout.String(), "Skipped") {
		t.Fatalf("expected a skip message on stdout, got %q", stdout.String())
	}
}

// TestReviewSubmit_SkipsWhenPendingReviewExists_JSON: the --json skip emits the
// documented machine shape (status=skipped, reason=pending_review_exists) so a
// programmatic caller (the pr-pool review role) can distinguish skip from post.
func TestReviewSubmit_SkipsWhenPendingReviewExists_JSON(t *testing.T) {
	resetReviewFlags()
	t.Setenv("PG_PR_STATE_HOME", t.TempDir())
	prev := vcsProviderFor
	t.Cleanup(func() { vcsProviderFor = prev })
	fake := &reviewFakeVCS{hasPending: true}
	vcsProviderFor = func(string) vcs.Provider { return fake }

	rootCmd.SetIn(strings.NewReader(`{"body":"top"}`))
	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"review", "submit", "42", "--repo", "foo/bar", "--json"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("submit: %v (stderr=%s)", err, stderr.String())
	}
	if fake.postCalls != 0 {
		t.Fatalf("PostReview must NOT be called, got %d call(s)", fake.postCalls)
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not JSON: %v (%q)", err, stdout.String())
	}
	if got["status"] != "skipped" || got["reason"] != "pending_review_exists" {
		t.Fatalf("unexpected skip JSON: %v", got)
	}
}

// TestReviewSubmit_PendingCheckError_Propagates: a failure detecting an existing
// pending review must fail closed (abort, do not post) so we never post over a
// review we merely could not see (mirrors the daemon team sink) (pg2-3fo3c).
func TestReviewSubmit_PendingCheckError_Propagates(t *testing.T) {
	resetReviewFlags()
	t.Setenv("PG_PR_STATE_HOME", t.TempDir())
	prev := vcsProviderFor
	t.Cleanup(func() { vcsProviderFor = prev })
	fake := &reviewFakeVCS{hasPendingErr: errors.New("gh graphql boom")}
	vcsProviderFor = func(string) vcs.Provider { return fake }

	in := `{"body":"top"}`
	rootCmd.SetIn(strings.NewReader(in))
	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"review", "submit", "42", "--repo", "foo/bar"})
	if err := rootCmd.Execute(); err == nil {
		t.Fatalf("expected error from pending-check failure, got nil (stdout=%s)", stdout.String())
	}
	if fake.postCalls != 0 {
		t.Fatalf("PostReview must NOT be called when pending detection errors, got %d call(s)", fake.postCalls)
	}
}

// TestReviewSubmit_ForwardsHeadSHAAsCommitID: a submit JSON carrying head_sha
// must anchor the review to that commit (commit_id), so a PR head that advanced
// between review and post does not 422 (pg2-pipw). This is the path the pr-pool
// review role uses.
func TestReviewSubmit_ForwardsHeadSHAAsCommitID(t *testing.T) {
	resetReviewFlags()
	t.Setenv("PG_PR_STATE_HOME", t.TempDir())
	prev := vcsProviderFor
	t.Cleanup(func() { vcsProviderFor = prev })
	fake := &reviewFakeVCS{}
	vcsProviderFor = func(string) vcs.Provider { return fake }

	in := `{"head_sha":"abc123","comments":[{"path":"a.go","line":1,"body":"x"}]}`
	rootCmd.SetIn(strings.NewReader(in))
	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"review", "submit", "42", "--repo", "foo/bar"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("submit: %v (stderr=%s)", err, stderr.String())
	}
	if fake.postedCommitID != "abc123" {
		t.Errorf("submit must forward head_sha as commit_id, got %q", fake.postedCommitID)
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
	if !marker.IsOurs(fake.addBody) {
		t.Fatalf("AddComment body missing marker: %q", fake.addBody)
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
