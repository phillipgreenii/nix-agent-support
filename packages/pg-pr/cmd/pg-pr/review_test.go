package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/marker"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/beads"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/vcs"
)

// reviewFakeVCS records the inputs to write methods.
type reviewFakeVCS struct {
	postedBody       string
	postedComments   []api.Comment
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
func (f *reviewFakeVCS) PostReview(_ context.Context, _ string, _ int, body string, comments []api.Comment) (*api.Review, error) {
	f.postedBody = body
	f.postedComments = comments
	return &api.Review{ID: "RV_x", State: "pending", Body: body}, nil
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

// fakeFeedbackBeads is an in-memory beadsFeedbackClient used by the
// comment-respond tests.
type fakeFeedbackBeads struct {
	feedback  map[string]*beads.Feedback
	mr        map[string]*beads.MergeRequest // keyed by feedback id
	getErr    error
	walkErr   error
	mrMissing bool
}

func (f *fakeFeedbackBeads) GetFeedback(_ context.Context, id string) (*beads.Feedback, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	if f.feedback == nil {
		return nil, nil
	}
	return f.feedback[id], nil
}

func (f *fakeFeedbackBeads) FindMergeRequestForFeedback(_ context.Context, feedbackID string) (*beads.MergeRequest, error) {
	if f.walkErr != nil {
		return nil, f.walkErr
	}
	if f.mrMissing {
		return nil, nil
	}
	if f.mr == nil {
		return nil, nil
	}
	return f.mr[feedbackID], nil
}

func swapBeadsForComment(t *testing.T, fb *fakeFeedbackBeads) {
	t.Helper()
	prev := beadsClientForComment
	beadsClientForComment = func(string) beadsFeedbackClient { return fb }
	t.Cleanup(func() { beadsClientForComment = prev })
}

func TestCommentRespond_CommentThread_Replies(t *testing.T) {
	resetReviewFlags()
	prev := vcsProviderFor
	t.Cleanup(func() { vcsProviderFor = prev })
	fake := &reviewFakeVCS{}
	vcsProviderFor = func(string) vcs.Provider { return fake }

	fb := &fakeFeedbackBeads{
		feedback: map[string]*beads.Feedback{
			"fb-1": {
				ID:     "fb-1",
				Status: "hooked",
				Fields: beads.FeedbackFields{
					Kind:       string(beads.FeedbackKindCommentThread),
					ExternalID: "PRRT_abc",
				},
			},
		},
		mr: map[string]*beads.MergeRequest{
			"fb-1": {
				ID:     "mr-1",
				Status: "open",
				Type:   beads.TypeMergeRequest,
				Fields: beads.MergeRequestFields{
					Repo:     "foo/bar",
					PRNumber: 42,
				},
			},
		},
	}
	swapBeadsForComment(t, fb)

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"comment", "respond", "fb-1", "--repo", "foo/bar", "--body", "ack"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v (stderr=%s)", err, stderr.String())
	}
	if fake.replyThreadID != "PRRT_abc" {
		t.Fatalf("threadID: got %q want PRRT_abc", fake.replyThreadID)
	}
	if fake.replyRepo != "foo/bar" {
		t.Fatalf("repo: got %q want foo/bar", fake.replyRepo)
	}
	if !strings.Contains(fake.replyBody, marker.Glyph) {
		t.Fatalf("reply body missing marker: %q", fake.replyBody)
	}
	if !strings.Contains(fake.replyBody, "ack") {
		t.Fatalf("reply body should contain 'ack': %q", fake.replyBody)
	}
	if !strings.Contains(stdout.String(), "PRRT_abc") {
		t.Errorf("stdout should reference thread: %q", stdout.String())
	}
}

func TestCommentRespond_ReviewThread_Replies(t *testing.T) {
	resetReviewFlags()
	prev := vcsProviderFor
	t.Cleanup(func() { vcsProviderFor = prev })
	fake := &reviewFakeVCS{}
	vcsProviderFor = func(string) vcs.Provider { return fake }

	fb := &fakeFeedbackBeads{
		feedback: map[string]*beads.Feedback{
			"fb-2": {
				ID: "fb-2",
				Fields: beads.FeedbackFields{
					Kind:       string(beads.FeedbackKindReviewThread),
					ExternalID: "PRT_xyz",
				},
			},
		},
		mr: map[string]*beads.MergeRequest{
			"fb-2": {ID: "mr-2", Type: beads.TypeMergeRequest, Fields: beads.MergeRequestFields{Repo: "x/y", PRNumber: 9}},
		},
	}
	swapBeadsForComment(t, fb)

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"comment", "respond", "fb-2", "--repo", "x/y", "--body", "ok"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v (stderr=%s)", err, stderr.String())
	}
	if fake.replyThreadID != "PRT_xyz" {
		t.Fatalf("threadID: got %q want PRT_xyz", fake.replyThreadID)
	}
}

func TestCommentRespond_RejectsNonRespondableKinds(t *testing.T) {
	for _, kind := range []beads.FeedbackKind{
		beads.FeedbackKindCIFailure,
		beads.FeedbackKindReviewRequest,
		beads.FeedbackKindJiraLink,
	} {
		t.Run(string(kind), func(t *testing.T) {
			resetReviewFlags()
			prev := vcsProviderFor
			t.Cleanup(func() { vcsProviderFor = prev })
			vcsProviderFor = func(string) vcs.Provider { return &reviewFakeVCS{} }
			fb := &fakeFeedbackBeads{
				feedback: map[string]*beads.Feedback{
					"fb-x": {ID: "fb-x", Fields: beads.FeedbackFields{Kind: string(kind), ExternalID: "ext"}},
				},
			}
			swapBeadsForComment(t, fb)

			var stdout, stderr bytes.Buffer
			rootCmd.SetOut(&stdout)
			rootCmd.SetErr(&stderr)
			rootCmd.SetArgs([]string{"comment", "respond", "fb-x", "--repo", "a/b", "--body", "x"})
			err := rootCmd.Execute()
			if err == nil {
				t.Fatalf("expected error for kind=%s", kind)
			}
			if !strings.Contains(err.Error(), "cannot respond to") {
				t.Fatalf("expected 'cannot respond to' message, got %v", err)
			}
		})
	}
}

func TestCommentRespond_FeedbackNotFound(t *testing.T) {
	resetReviewFlags()
	prev := vcsProviderFor
	t.Cleanup(func() { vcsProviderFor = prev })
	vcsProviderFor = func(string) vcs.Provider { return &reviewFakeVCS{} }
	swapBeadsForComment(t, &fakeFeedbackBeads{})

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"comment", "respond", "nope", "--repo", "a/b", "--body", "x"})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error when feedback not found")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected 'not found' message, got %v", err)
	}
}

func TestCommentRespond_MissingMergeRequest(t *testing.T) {
	resetReviewFlags()
	prev := vcsProviderFor
	t.Cleanup(func() { vcsProviderFor = prev })
	vcsProviderFor = func(string) vcs.Provider { return &reviewFakeVCS{} }
	fb := &fakeFeedbackBeads{
		feedback: map[string]*beads.Feedback{
			"fb-orphan": {ID: "fb-orphan", Fields: beads.FeedbackFields{
				Kind:       string(beads.FeedbackKindCommentThread),
				ExternalID: "ext",
			}},
		},
		mrMissing: true,
	}
	swapBeadsForComment(t, fb)

	rootCmd.SetOut(io_discard)
	rootCmd.SetErr(io_discard)
	rootCmd.SetArgs([]string{"comment", "respond", "fb-orphan", "--repo", "a/b", "--body", "x"})
	if err := rootCmd.Execute(); err == nil {
		t.Fatal("expected error when merge-request bead missing")
	}
}

func TestCommentRespond_MissingExternalID(t *testing.T) {
	resetReviewFlags()
	prev := vcsProviderFor
	t.Cleanup(func() { vcsProviderFor = prev })
	vcsProviderFor = func(string) vcs.Provider { return &reviewFakeVCS{} }
	fb := &fakeFeedbackBeads{
		feedback: map[string]*beads.Feedback{
			"fb-z": {ID: "fb-z", Fields: beads.FeedbackFields{Kind: string(beads.FeedbackKindCommentThread)}},
		},
		mr: map[string]*beads.MergeRequest{
			"fb-z": {ID: "mr-z", Type: beads.TypeMergeRequest, Fields: beads.MergeRequestFields{Repo: "a/b", PRNumber: 1}},
		},
	}
	swapBeadsForComment(t, fb)

	rootCmd.SetOut(io_discard)
	rootCmd.SetErr(io_discard)
	rootCmd.SetArgs([]string{"comment", "respond", "fb-z", "--repo", "a/b", "--body", "x"})
	err := rootCmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "external_id") {
		t.Fatalf("expected error about missing external_id, got %v", err)
	}
}

func TestCommentRespond_JSONOutput(t *testing.T) {
	resetReviewFlags()
	prev := vcsProviderFor
	t.Cleanup(func() { vcsProviderFor = prev })
	vcsProviderFor = func(string) vcs.Provider { return &reviewFakeVCS{} }
	fb := &fakeFeedbackBeads{
		feedback: map[string]*beads.Feedback{
			"fb-j": {ID: "fb-j", Fields: beads.FeedbackFields{
				Kind: string(beads.FeedbackKindCommentThread), ExternalID: "ext",
			}},
		},
		mr: map[string]*beads.MergeRequest{
			"fb-j": {ID: "mr-j", Type: beads.TypeMergeRequest, Fields: beads.MergeRequestFields{Repo: "a/b", PRNumber: 2}},
		},
	}
	swapBeadsForComment(t, fb)

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"comment", "respond", "fb-j", "--repo", "a/b", "--body", "ack", "--json"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v (stderr=%s)", err, stderr.String())
	}
	var parsed map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &parsed); err != nil {
		t.Fatalf("not JSON: %v -- %q", err, stdout.String())
	}
	if got, _ := parsed["id"].(string); got != "IC_reply" {
		t.Fatalf("expected id=IC_reply, got %v", parsed["id"])
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
