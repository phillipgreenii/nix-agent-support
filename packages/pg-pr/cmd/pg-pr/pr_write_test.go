package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/beads"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/vcs"
)

// writeFakeVCS records every call to the write paths so tests can assert
// the flags propagated correctly.
type writeFakeVCS struct {
	// createCalls captures (repo, draft, title, body, head, base).
	createCalls []writeCreateCall
	updateCalls []writeUpdateCall
	closeCalls  []writeNumCall
	setDraft    []writeSetDraftCall
	automerge   []writeAutomergeCall
	mergeCalls  []writeNumCall

	createPR   *api.PR // canned return for CreatePR
	createErr  error
	genericErr error
}

type writeCreateCall struct {
	repo, title, body, head, base string
	draft                         bool
}
type writeUpdateCall struct {
	repo string
	num  int
	body string
}
type writeNumCall struct {
	repo string
	num  int
}
type writeSetDraftCall struct {
	repo  string
	num   int
	draft bool
}
type writeAutomergeCall struct {
	repo    string
	num     int
	enabled bool
}

func (f *writeFakeVCS) GetPR(_ context.Context, repo string, n int) (*api.PR, error) {
	return &api.PR{Repo: repo, Number: n}, nil
}
func (f *writeFakeVCS) ListMyPRs(context.Context, string) ([]api.PR, error) { return nil, nil }
func (f *writeFakeVCS) ListTeamPRs(context.Context, string, []string) ([]api.PR, error) {
	return nil, nil
}
func (f *writeFakeVCS) CreatePR(_ context.Context, repo string, draft bool, title, body, head, base string) (*api.PR, error) {
	f.createCalls = append(f.createCalls, writeCreateCall{repo, title, body, head, base, draft})
	if f.createErr != nil {
		return nil, f.createErr
	}
	if f.createPR != nil {
		out := *f.createPR
		out.Repo = repo
		return &out, nil
	}
	return &api.PR{Repo: repo, Number: 1, URL: "https://example/pr/1", Branch: head, Base: base, Draft: draft}, nil
}
func (f *writeFakeVCS) UpdatePR(_ context.Context, repo string, n int, body string) error {
	f.updateCalls = append(f.updateCalls, writeUpdateCall{repo, n, body})
	return f.genericErr
}
func (f *writeFakeVCS) SetDraft(_ context.Context, repo string, n int, d bool) error {
	f.setDraft = append(f.setDraft, writeSetDraftCall{repo, n, d})
	return f.genericErr
}
func (f *writeFakeVCS) SetAutomerge(_ context.Context, repo string, n int, e bool) error {
	f.automerge = append(f.automerge, writeAutomergeCall{repo, n, e})
	return f.genericErr
}
func (f *writeFakeVCS) Merge(_ context.Context, repo string, n int) error {
	f.mergeCalls = append(f.mergeCalls, writeNumCall{repo, n})
	return f.genericErr
}
func (f *writeFakeVCS) Close(_ context.Context, repo string, n int) error {
	f.closeCalls = append(f.closeCalls, writeNumCall{repo, n})
	return f.genericErr
}
func (f *writeFakeVCS) ListComments(context.Context, string, int) ([]api.Comment, error) {
	return nil, nil
}
func (f *writeFakeVCS) AddComment(context.Context, string, int, string) (*api.Comment, error) {
	return nil, nil
}
func (f *writeFakeVCS) ReplyToThread(context.Context, string, string, string) (*api.Comment, error) {
	return nil, nil
}
func (f *writeFakeVCS) ResolveThread(context.Context, string, string) error { return nil }
func (f *writeFakeVCS) PostReview(context.Context, string, int, string, []api.Comment) (*api.Review, error) {
	return nil, nil
}

var _ vcs.Provider = (*writeFakeVCS)(nil)

// fakeBeadsClient is the in-memory beads client used by pr_write tests.
type fakeBeadsClient struct {
	ensureCalls []beads.MergeRequestFields
	closeCalls  []string
	ensureErr   error
}

func (f *fakeBeadsClient) EnsureMergeRequest(_ context.Context, _ string, fields beads.MergeRequestFields) (string, bool, error) {
	f.ensureCalls = append(f.ensureCalls, fields)
	if f.ensureErr != nil {
		return "", false, f.ensureErr
	}
	return "test-bd-1", false, nil
}
func (f *fakeBeadsClient) CloseMergeRequest(_ context.Context, id, _ string) error {
	f.closeCalls = append(f.closeCalls, id)
	return nil
}

func swapFakes(t *testing.T) (*writeFakeVCS, *fakeBeadsClient) {
	t.Helper()
	fv := &writeFakeVCS{}
	fb := &fakeBeadsClient{}
	prevV := vcsProviderFor
	prevB := beadsClientForPR
	vcsProviderFor = func(string) vcs.Provider { return fv }
	beadsClientForPR = func() beadsMergeRequestClient { return fb }
	t.Cleanup(func() {
		vcsProviderFor = prevV
		beadsClientForPR = prevB
	})
	return fv, fb
}

func TestPRCreate_DefaultDraft(t *testing.T) {
	resetPRWriteFlags()
	fv, fb := swapFakes(t)

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{
		"pr", "create",
		"--repo", "foo/bar",
		"--title", "Test PR",
		"--head", "feat/x",
		"--body", "hello",
	})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v (stderr=%s)", err, stderr.String())
	}
	if len(fv.createCalls) != 1 {
		t.Fatalf("CreatePR called %d times; want 1", len(fv.createCalls))
	}
	call := fv.createCalls[0]
	if !call.draft {
		t.Errorf("expected draft=true by default; got false")
	}
	if call.title != "Test PR" {
		t.Errorf("title: got %q want %q", call.title, "Test PR")
	}
	if call.body != "hello" {
		t.Errorf("body: got %q want %q", call.body, "hello")
	}
	if call.head != "feat/x" {
		t.Errorf("head: got %q want feat/x", call.head)
	}
	if call.base != "main" {
		// origin/main was passed; we strip origin/ for gh.
		t.Errorf("base: got %q want main", call.base)
	}
	if len(fb.ensureCalls) != 1 {
		t.Errorf("EnsureMergeRequest called %d times; want 1", len(fb.ensureCalls))
	}
	if !fb.ensureCalls[0].Draft {
		t.Errorf("bead Draft: got false want true")
	}
}

func TestPRCreate_NoDraftFlag(t *testing.T) {
	resetPRWriteFlags()
	fv, _ := swapFakes(t)

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{
		"pr", "create",
		"--repo", "foo/bar",
		"--title", "Ready",
		"--head", "feat/x",
		"--body", "hi",
		"--no-draft",
	})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v (stderr=%s)", err, stderr.String())
	}
	if len(fv.createCalls) != 1 || fv.createCalls[0].draft {
		t.Fatalf("expected draft=false with --no-draft; got %+v", fv.createCalls)
	}
}

func TestPRCreate_BodyStdin(t *testing.T) {
	resetPRWriteFlags()
	fv, _ := swapFakes(t)

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetIn(strings.NewReader("line1\nline2\n"))
	rootCmd.SetArgs([]string{
		"pr", "create",
		"--repo", "foo/bar",
		"--title", "Stdin Body",
		"--head", "feat/y",
		"--body-stdin",
	})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v (stderr=%s)", err, stderr.String())
	}
	if len(fv.createCalls) != 1 || fv.createCalls[0].body != "line1\nline2\n" {
		t.Fatalf("body via stdin not propagated: %+v", fv.createCalls)
	}
}

func TestPRCreate_BodyFile(t *testing.T) {
	resetPRWriteFlags()
	fv, _ := swapFakes(t)

	tmp := t.TempDir()
	path := filepath.Join(tmp, "body.md")
	if err := os.WriteFile(path, []byte("file body"), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{
		"pr", "create",
		"--repo", "foo/bar",
		"--title", "T",
		"--head", "feat/z",
		"--body-file", path,
	})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v (stderr=%s)", err, stderr.String())
	}
	if len(fv.createCalls) != 1 || fv.createCalls[0].body != "file body" {
		t.Fatalf("body-file not propagated: %+v", fv.createCalls)
	}
}

func TestPRCreate_MultipleBodySources(t *testing.T) {
	resetPRWriteFlags()
	_, _ = swapFakes(t)

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{
		"pr", "create",
		"--repo", "foo/bar",
		"--title", "T",
		"--head", "h",
		"--body", "a",
		"--body-stdin",
	})

	if err := rootCmd.Execute(); err == nil {
		t.Fatalf("expected error for multiple body sources, got none")
	}
}

func TestPRCreate_MissingTitle(t *testing.T) {
	resetPRWriteFlags()
	_, _ = swapFakes(t)

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"pr", "create", "--repo", "foo/bar", "--head", "h", "--body", "b"})
	if err := rootCmd.Execute(); err == nil {
		t.Fatal("expected error for missing --title")
	}
}

func TestPRUpdate_NoBody_NoOp(t *testing.T) {
	resetPRWriteFlags()
	fv, _ := swapFakes(t)

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"pr", "update", "5", "--repo", "foo/bar"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v (stderr=%s)", err, stderr.String())
	}
	if len(fv.updateCalls) != 0 {
		t.Fatalf("expected no-op when no body provided; got %+v", fv.updateCalls)
	}
}

func TestPRUpdate_BodyStdin(t *testing.T) {
	resetPRWriteFlags()
	fv, _ := swapFakes(t)

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetIn(strings.NewReader("updated"))
	rootCmd.SetArgs([]string{"pr", "update", "5", "--repo", "foo/bar", "--body-stdin"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v (stderr=%s)", err, stderr.String())
	}
	if len(fv.updateCalls) != 1 || fv.updateCalls[0].body != "updated" {
		t.Fatalf("update body not propagated: %+v", fv.updateCalls)
	}
}

func TestPRClose_CallsClose(t *testing.T) {
	resetPRWriteFlags()
	fv, _ := swapFakes(t)

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"pr", "close", "7", "--repo", "foo/bar"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v (stderr=%s)", err, stderr.String())
	}
	if len(fv.closeCalls) != 1 || fv.closeCalls[0].num != 7 {
		t.Fatalf("close not called as expected: %+v", fv.closeCalls)
	}
}

func TestPRReady_SetsDraftFalse(t *testing.T) {
	resetPRWriteFlags()
	fv, _ := swapFakes(t)

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"pr", "ready", "9", "--repo", "foo/bar"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v (stderr=%s)", err, stderr.String())
	}
	if len(fv.setDraft) != 1 || fv.setDraft[0].draft {
		t.Fatalf("expected SetDraft(false); got %+v", fv.setDraft)
	}
	if !strings.Contains(stdout.String(), "ready for review") {
		t.Errorf("expected ready-for-review message; got %q", stdout.String())
	}
}

func TestPRDraft_SetsDraftTrue(t *testing.T) {
	resetPRWriteFlags()
	fv, _ := swapFakes(t)

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"pr", "draft", "9", "--repo", "foo/bar"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v (stderr=%s)", err, stderr.String())
	}
	if len(fv.setDraft) != 1 || !fv.setDraft[0].draft {
		t.Fatalf("expected SetDraft(true); got %+v", fv.setDraft)
	}
}

func TestPRAutomerge_OnPrintsWarning(t *testing.T) {
	resetPRWriteFlags()
	fv, _ := swapFakes(t)

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"pr", "automerge", "on", "11", "--repo", "foo/bar"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v (stderr=%s)", err, stderr.String())
	}
	if !strings.Contains(stderr.String(), "WARNING: automerge is a human-only verb") {
		t.Errorf("expected automerge warning on stderr; got %q", stderr.String())
	}
	if len(fv.automerge) != 1 || !fv.automerge[0].enabled {
		t.Fatalf("expected SetAutomerge(true); got %+v", fv.automerge)
	}
}

func TestPRAutomerge_OffPrintsWarning(t *testing.T) {
	resetPRWriteFlags()
	fv, _ := swapFakes(t)

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"pr", "automerge", "off", "12", "--repo", "foo/bar"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v (stderr=%s)", err, stderr.String())
	}
	if !strings.Contains(stderr.String(), "WARNING: automerge is a human-only verb") {
		t.Errorf("expected automerge warning on stderr; got %q", stderr.String())
	}
	if len(fv.automerge) != 1 || fv.automerge[0].enabled {
		t.Fatalf("expected SetAutomerge(false); got %+v", fv.automerge)
	}
}

func TestPRMerge_PrintsWarning(t *testing.T) {
	resetPRWriteFlags()
	fv, _ := swapFakes(t)

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"pr", "merge", "13", "--repo", "foo/bar"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v (stderr=%s)", err, stderr.String())
	}
	if !strings.Contains(stderr.String(), "WARNING: merge is a human-only verb") {
		t.Errorf("expected merge warning on stderr; got %q", stderr.String())
	}
	if len(fv.mergeCalls) != 1 || fv.mergeCalls[0].num != 13 {
		t.Fatalf("expected Merge(13); got %+v", fv.mergeCalls)
	}
}

func TestResolveBody_ConflictingSources(t *testing.T) {
	_, err := resolveBody(nil, "x", "y", false)
	if err == nil {
		t.Fatal("expected error for multiple body sources")
	}
}

func TestResolveBody_BodyFlag(t *testing.T) {
	got, err := resolveBody(nil, "literal", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if got != "literal" {
		t.Errorf("got %q want literal", got)
	}
}

func TestSplitCSV(t *testing.T) {
	if got := splitCSV(""); got != nil {
		t.Errorf("empty: got %v want nil", got)
	}
	if got := splitCSV("a, b ,c"); len(got) != 3 || got[1] != "b" {
		t.Errorf("split: got %v", got)
	}
}

// Confirm the createErr surface bubbles up.
func TestPRCreate_PropagatesError(t *testing.T) {
	resetPRWriteFlags()
	fv, _ := swapFakes(t)
	fv.createErr = errors.New("boom")

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"pr", "create", "--repo", "foo/bar", "--title", "t", "--head", "h", "--body", "b"})

	if err := rootCmd.Execute(); err == nil {
		t.Fatal("expected error from CreatePR to surface")
	}
}
