package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
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

	// getPRResult/getPRErr control GetPR's return, for the `pr wip on` /
	// `pr ready` + WIP tests that need to drive a specific live PR shape
	// (ready, draft, merged, closed). nil getPRResult falls back to the
	// bare default below, matching every write test that predates WIP and
	// never needed GetPR to return anything meaningful.
	getPRResult *api.PR
	getPRErr    error
	getPRCalls  int
}

type writeCreateCall struct {
	repo, title, body, head, base string
	draft                         bool
	reviewers                     []string
	labels                        []string
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
	f.getPRCalls++
	if f.getPRErr != nil {
		return nil, f.getPRErr
	}
	if f.getPRResult != nil {
		out := *f.getPRResult
		out.Repo = repo
		out.Number = n
		return &out, nil
	}
	return &api.PR{Repo: repo, Number: n}, nil
}
func (f *writeFakeVCS) ListMyPRs(context.Context, string) ([]api.PR, error) { return nil, nil }
func (f *writeFakeVCS) ListTeamPRs(context.Context, string, []string) ([]api.PR, error) {
	return nil, nil
}

func (f *writeFakeVCS) CreatePR(_ context.Context, repo string, draft bool, title, body, head, base string, reviewers, labels []string) (*api.PR, error) {
	f.createCalls = append(f.createCalls, writeCreateCall{
		repo: repo, title: title, body: body, head: head, base: base, draft: draft,
		reviewers: reviewers, labels: labels,
	})
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
func (f *writeFakeVCS) PostReview(context.Context, string, int, string, string, []api.Comment) (*api.Review, error) {
	return nil, nil
}

func (f *writeFakeVCS) ListReviews(context.Context, string, int) ([]api.Review, error) {
	return nil, nil
}

var _ vcs.Provider = (*writeFakeVCS)(nil)

// fakeBeadsClient is the in-memory beads client used by pr_write tests.
type fakeBeadsClient struct {
	ensureCalls    []beads.MergeRequestFields
	closeCalls     []string
	ensureErr      error
	findResult     *beads.MergeRequest
	findErr        error
	findRepo       string
	findNumber     int
	closeReasonLog []string
}

func (f *fakeBeadsClient) EnsureMergeRequest(_ context.Context, _ string, fields beads.MergeRequestFields) (string, bool, error) {
	f.ensureCalls = append(f.ensureCalls, fields)
	if f.ensureErr != nil {
		return "", false, f.ensureErr
	}
	return "test-bd-1", false, nil
}

func (f *fakeBeadsClient) CloseMergeRequest(_ context.Context, id, reason string) error {
	f.closeCalls = append(f.closeCalls, id)
	f.closeReasonLog = append(f.closeReasonLog, reason)
	return nil
}

func (f *fakeBeadsClient) FindByRepoAndNumber(_ context.Context, repo string, n int) (*beads.MergeRequest, error) {
	f.findRepo = repo
	f.findNumber = n
	return f.findResult, f.findErr
}

func swapFakes(t *testing.T) (*writeFakeVCS, *fakeBeadsClient) {
	t.Helper()
	fv := &writeFakeVCS{}
	fb := &fakeBeadsClient{}
	prevV := vcsProviderFor
	prevB := beadsClientForPR
	vcsProviderFor = func(string) vcs.Provider { return fv }
	beadsClientForPR = func(string) beadsMergeRequestClient { return fb }
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

func TestPRCreate_ReviewersAndLabels_PushedToVCS(t *testing.T) {
	resetPRWriteFlags()
	fv, fb := swapFakes(t)

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{
		"pr", "create",
		"--repo", "foo/bar",
		"--title", "T",
		"--head", "feat/x",
		"--body", "b",
		"--reviewers", "alice, bob",
		"--labels", "ci, area/cli",
	})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v (stderr=%s)", err, stderr.String())
	}
	if len(fv.createCalls) != 1 {
		t.Fatalf("createCalls: %+v", fv.createCalls)
	}
	got := fv.createCalls[0]
	if len(got.reviewers) != 2 || got.reviewers[0] != "alice" || got.reviewers[1] != "bob" {
		t.Errorf("reviewers: got %v want [alice bob]", got.reviewers)
	}
	if len(got.labels) != 2 || got.labels[0] != "ci" || got.labels[1] != "area/cli" {
		t.Errorf("labels: got %v want [ci area/cli]", got.labels)
	}
	if len(fb.ensureCalls) != 1 {
		t.Errorf("expected merge-request bead to be recorded")
	}
}

func TestPRCreate_NoReviewersOrLabels_EmptySlices(t *testing.T) {
	resetPRWriteFlags()
	fv, _ := swapFakes(t)

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{
		"pr", "create",
		"--repo", "foo/bar",
		"--title", "T",
		"--head", "h",
		"--body", "b",
	})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v (stderr=%s)", err, stderr.String())
	}
	if len(fv.createCalls) != 1 {
		t.Fatalf("createCalls: %+v", fv.createCalls)
	}
	got := fv.createCalls[0]
	if len(got.reviewers) != 0 || len(got.labels) != 0 {
		t.Errorf("reviewers/labels should be empty: %+v", got)
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

func TestPRClose_AlsoClosesBead(t *testing.T) {
	resetPRWriteFlags()
	fv, fb := swapFakes(t)
	fb.findResult = &beads.MergeRequest{
		ID:     "mr-7",
		Status: "open",
		Type:   beads.TypeMergeRequest,
		Fields: beads.MergeRequestFields{Repo: "foo/bar", PRNumber: 7},
	}

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"pr", "close", "7", "--repo", "foo/bar"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v (stderr=%s)", err, stderr.String())
	}
	if len(fv.closeCalls) != 1 || fv.closeCalls[0].num != 7 {
		t.Fatalf("vcs.Close not called: %+v", fv.closeCalls)
	}
	if fb.findRepo != "foo/bar" || fb.findNumber != 7 {
		t.Fatalf("FindByRepoAndNumber args: got %q/%d", fb.findRepo, fb.findNumber)
	}
	if len(fb.closeCalls) != 1 || fb.closeCalls[0] != "mr-7" {
		t.Fatalf("CloseMergeRequest not called as expected: %+v", fb.closeCalls)
	}
	if len(fb.closeReasonLog) != 1 || !strings.Contains(fb.closeReasonLog[0], "pg-pr pr close") {
		t.Fatalf("close reason missing 'pg-pr pr close': %+v", fb.closeReasonLog)
	}
	if !strings.Contains(stdout.String(), "Closed merge-request bead mr-7") {
		t.Errorf("stdout should mention bead close: %q", stdout.String())
	}
}

func TestPRClose_NoBead_StillSucceeds(t *testing.T) {
	resetPRWriteFlags()
	fv, fb := swapFakes(t)
	// findResult left nil — no bead exists for this PR.

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"pr", "close", "8", "--repo", "foo/bar"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v (stderr=%s)", err, stderr.String())
	}
	if len(fv.closeCalls) != 1 {
		t.Fatalf("vcs.Close should still run: %+v", fv.closeCalls)
	}
	if len(fb.closeCalls) != 0 {
		t.Fatalf("CloseMergeRequest should not run when bead missing: %+v", fb.closeCalls)
	}
	if strings.Contains(stdout.String(), "Closed merge-request bead") {
		t.Errorf("stdout should not mention bead close when none found: %q", stdout.String())
	}
}

func TestPRClose_AlreadyClosedBead_SkipsClose(t *testing.T) {
	resetPRWriteFlags()
	_, fb := swapFakes(t)
	fb.findResult = &beads.MergeRequest{
		ID:     "mr-9",
		Status: "closed",
		Type:   beads.TypeMergeRequest,
		Fields: beads.MergeRequestFields{Repo: "foo/bar", PRNumber: 9},
	}

	rootCmd.SetOut(io_discard)
	rootCmd.SetErr(io_discard)
	rootCmd.SetArgs([]string{"pr", "close", "9", "--repo", "foo/bar"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(fb.closeCalls) != 0 {
		t.Fatalf("expected CloseMergeRequest to skip closed bead: %+v", fb.closeCalls)
	}
}

func TestPRClose_FindError_Warns(t *testing.T) {
	resetPRWriteFlags()
	_, fb := swapFakes(t)
	fb.findErr = errors.New("bd offline")

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"pr", "close", "10", "--repo", "foo/bar"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute should not fail just because bead lookup failed: %v", err)
	}
	if !strings.Contains(stderr.String(), "WARNING: failed to look up merge-request bead") {
		t.Errorf("expected warning on stderr; got %q", stderr.String())
	}
}

// TestPRReady_SetsDraftFalse also proves the "no store at all" half of fork
// #2's ruling: with no store file present, clearWIPOverrideIfSet is a
// silent no-op and `pr ready` behaves exactly as it did before this leaf.
// setListStateHome(t) (pr_list_test.go) points XDG_STATE_HOME at a fresh
// temp dir WITHOUT creating the "pg-pr" subdir, so store.DefaultPath()
// genuinely does not exist — this also keeps the test from ever touching a
// real developer/CI machine's actual pg-pr store (isolation requirement).
func TestPRReady_SetsDraftFalse(t *testing.T) {
	resetPRWriteFlags()
	fv, _ := swapFakes(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())

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
	if strings.Contains(stderr.String(), "OVERRIDE") {
		t.Errorf("expected no WIP-override message with no store at all; got stderr=%q", stderr.String())
	}
	if _, statErr := os.Stat(store.DefaultPath()); statErr == nil {
		t.Errorf("pr ready created a store file at %s when none existed; it must not (mirrors pr_view.go's loadPRView contract)", store.DefaultPath())
	}
}

// TestPRReady_ClearsWIPOverride_WhenSet pins fork #2's ruling
// (pg2-4dz88.4.4, operator 2026-08-24): `pr ready` on a PR whose
// store-recorded WIP flag is true clears WIP as an explicit, logged
// override, then proceeds to call SetDraft(false) exactly as before.
func TestPRReady_ClearsWIPOverride_WhenSet(t *testing.T) {
	resetPRWriteFlags()
	fv, _ := swapFakes(t)
	setListStateHome(t)
	seedListStore(t, store.PullRequest{Repo: "foo/bar", Number: 30, Ownership: "mine", State: "open"})
	setStoreWIP(t, "foo/bar", 30, true)

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"pr", "ready", "30", "--repo", "foo/bar"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v (stderr=%s)", err, stderr.String())
	}
	if len(fv.setDraft) != 1 || fv.setDraft[0].draft {
		t.Fatalf("expected SetDraft(false) to still fire; got %+v", fv.setDraft)
	}
	if !strings.Contains(stderr.String(), "OVERRIDE") || !strings.Contains(stderr.String(), "WIP") {
		t.Errorf("expected a logged WIP-override message on stderr; got %q", stderr.String())
	}
	if got := getStoreWIP(t, "foo/bar", 30); got {
		t.Errorf("expected WIP cleared in the store after `pr ready`, got WIP=%v", got)
	}
}

// TestPRReady_NoOverride_WhenWIPFalse asserts the override message is
// pinned to the WIP=true case only: a stored, non-WIP PR gets no override
// noise on `pr ready`.
func TestPRReady_NoOverride_WhenWIPFalse(t *testing.T) {
	resetPRWriteFlags()
	fv, _ := swapFakes(t)
	setListStateHome(t)
	seedListStore(t, store.PullRequest{Repo: "foo/bar", Number: 31, Ownership: "mine", State: "open"})

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"pr", "ready", "31", "--repo", "foo/bar"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v (stderr=%s)", err, stderr.String())
	}
	if len(fv.setDraft) != 1 || fv.setDraft[0].draft {
		t.Fatalf("expected SetDraft(false); got %+v", fv.setDraft)
	}
	if strings.Contains(stderr.String(), "OVERRIDE") {
		t.Errorf("expected no WIP-override message when WIP is already false; got stderr=%q", stderr.String())
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

// ----------------------------------------------------------------------
// pr wip {on, off} (pg2-4dz88.4.4, fork #3 ruling)
// ----------------------------------------------------------------------

// setStoreWIP is a small test helper wrapping store.DB.SetWIP against
// store.DefaultPath() (the caller has already pointed XDG_STATE_HOME at a
// temp dir via setListStateHome).
func setStoreWIP(t *testing.T, repo string, num int, wip bool) {
	t.Helper()
	db, err := store.Open(store.DefaultPath())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = db.Close() }()
	if err := db.SetWIP(context.Background(), repo, num, wip); err != nil {
		t.Fatalf("SetWIP: %v", err)
	}
}

// getStoreWIP reads back the WIP flag for (repo, num) from the default
// store path.
func getStoreWIP(t *testing.T, repo string, num int) bool {
	t.Helper()
	db, err := store.Open(store.DefaultPath())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = db.Close() }()
	pr, err := db.GetPR(context.Background(), repo, num)
	if err != nil || pr == nil {
		t.Fatalf("GetPR(%s#%d): pr=%v err=%v", repo, num, pr, err)
	}
	return pr.WIP
}

// TestPRWipOn_ReadyPR_ConvertsToDraft pins the acceptance criterion:
// "Toggling WIP on a currently-ready PR calls SetDraft(true) against the
// provider exactly once, and the PR reads as draft on the next
// observation."
func TestPRWipOn_ReadyPR_ConvertsToDraft(t *testing.T) {
	resetPRWriteFlags()
	fv, _ := swapFakes(t)
	setListStateHome(t)
	seedListStore(t, store.PullRequest{Repo: "foo/bar", Number: 40, Ownership: "mine", State: "open"})
	fv.getPRResult = &api.PR{State: "open", Draft: false}

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"pr", "wip", "on", "40", "--repo", "foo/bar"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v (stderr=%s)", err, stderr.String())
	}
	if len(fv.setDraft) != 1 || !fv.setDraft[0].draft {
		t.Fatalf("expected exactly one SetDraft(true) call; got %+v", fv.setDraft)
	}
	if !strings.Contains(stdout.String(), "marked WIP") || !strings.Contains(stdout.String(), "converted to draft") {
		t.Errorf("expected both confirmation lines; got %q", stdout.String())
	}
	if !getStoreWIP(t, "foo/bar", 40) {
		t.Error("expected WIP=true persisted in the store")
	}
}

// TestPRWipOn_AlreadyDraftPR_NoUpstreamCall pins the "already draft ->
// no-op" branch: WIP is still persisted, but no redundant SetDraft call is
// made and no "converted to draft" line is printed.
func TestPRWipOn_AlreadyDraftPR_NoUpstreamCall(t *testing.T) {
	resetPRWriteFlags()
	fv, _ := swapFakes(t)
	setListStateHome(t)
	seedListStore(t, store.PullRequest{Repo: "foo/bar", Number: 41, Ownership: "mine", State: "open"})
	fv.getPRResult = &api.PR{State: "open", Draft: true}

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"pr", "wip", "on", "41", "--repo", "foo/bar"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v (stderr=%s)", err, stderr.String())
	}
	if len(fv.setDraft) != 0 {
		t.Fatalf("expected NO SetDraft calls for an already-draft PR; got %+v", fv.setDraft)
	}
	if strings.Contains(stdout.String(), "converted to draft") {
		t.Errorf("expected no 'converted to draft' line; got %q", stdout.String())
	}
	if !getStoreWIP(t, "foo/bar", 41) {
		t.Error("expected WIP=true persisted in the store even though no upstream call fired")
	}
}

// TestPRWipOn_ProviderSetDraftError_Propagates proves that a failure from
// the provider's SetDraft call (surfaced through sync.ApplyWIP) is not
// swallowed: `pr wip on` still errors, even though WIP has already been
// durably persisted in the store by that point (the operator's stated
// intent is recorded regardless of a transient upstream failure).
func TestPRWipOn_ProviderSetDraftError_Propagates(t *testing.T) {
	resetPRWriteFlags()
	fv, _ := swapFakes(t)
	setListStateHome(t)
	seedListStore(t, store.PullRequest{Repo: "foo/bar", Number: 44, Ownership: "mine", State: "open"})
	fv.getPRResult = &api.PR{State: "open", Draft: false}
	fv.genericErr = errors.New("upstream boom")

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"pr", "wip", "on", "44", "--repo", "foo/bar"})

	if err := rootCmd.Execute(); err == nil {
		t.Fatal("expected the provider's SetDraft error to surface")
	}
	if len(fv.setDraft) != 1 {
		t.Fatalf("expected exactly one attempted SetDraft(true) call; got %+v", fv.setDraft)
	}
	if !getStoreWIP(t, "foo/bar", 44) {
		t.Error("expected WIP=true still persisted despite the upstream failure")
	}
}

// TestPRWipOn_MergedOrClosedPR_NoUpstreamCall pins the acceptance
// criterion: "A merged or closed PR carrying wip=true receives no upstream
// draft-toggle call."
func TestPRWipOn_MergedOrClosedPR_NoUpstreamCall(t *testing.T) {
	for _, tc := range []struct {
		name string
		pr   api.PR
	}{
		{"merged", api.PR{State: "open", Merged: true}},
		{"closed", api.PR{State: "closed"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetPRWriteFlags()
			fv, _ := swapFakes(t)
			setListStateHome(t)
			seedListStore(t, store.PullRequest{Repo: "foo/bar", Number: 42, Ownership: "mine", State: "open"})
			fv.getPRResult = &tc.pr

			var stdout, stderr bytes.Buffer
			rootCmd.SetOut(&stdout)
			rootCmd.SetErr(&stderr)
			rootCmd.SetArgs([]string{"pr", "wip", "on", "42", "--repo", "foo/bar"})

			if err := rootCmd.Execute(); err != nil {
				t.Fatalf("execute: %v (stderr=%s)", err, stderr.String())
			}
			if len(fv.setDraft) != 0 {
				t.Fatalf("expected NO upstream draft-toggle call for a %s PR; got %+v", tc.name, fv.setDraft)
			}
			if !getStoreWIP(t, "foo/bar", 42) {
				t.Error("expected WIP=true still persisted in the store")
			}
		})
	}
}

// TestPRWipOn_UnknownPR_Errors: the provider's GetPR failing surfaces as a
// command error (the unknown-PR error path for `pr wip on`).
func TestPRWipOn_UnknownPR_Errors(t *testing.T) {
	resetPRWriteFlags()
	fv, _ := swapFakes(t)
	setListStateHome(t)
	fv.getPRErr = errors.New("not found")

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"pr", "wip", "on", "999", "--repo", "foo/bar"})

	if err := rootCmd.Execute(); err == nil {
		t.Fatal("expected an error when the provider cannot find the PR")
	}
	if len(fv.setDraft) != 0 {
		t.Fatalf("expected no SetDraft call when the PR fetch failed; got %+v", fv.setDraft)
	}
}

// TestPRWipOff_ClearsWIP_NoUpstreamCall pins the acceptance criterion:
// "Toggling WIP off a currently-draft PR does NOT itself call
// SetDraft(false) -- a test proves no upstream write happens at the moment
// of the toggle." `pr wip off` never even fetches the live PR (getPRCalls
// stays 0), which is the strongest form of that proof.
func TestPRWipOff_ClearsWIP_NoUpstreamCall(t *testing.T) {
	resetPRWriteFlags()
	fv, _ := swapFakes(t)
	setListStateHome(t)
	seedListStore(t, store.PullRequest{Repo: "foo/bar", Number: 43, Ownership: "mine", State: "open"})
	setStoreWIP(t, "foo/bar", 43, true)

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"pr", "wip", "off", "43", "--repo", "foo/bar"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v (stderr=%s)", err, stderr.String())
	}
	if len(fv.setDraft) != 0 {
		t.Fatalf("expected NO SetDraft call; got %+v", fv.setDraft)
	}
	if fv.getPRCalls != 0 {
		t.Fatalf("expected `pr wip off` to never fetch the live PR at all; got %d GetPR calls", fv.getPRCalls)
	}
	if !strings.Contains(stdout.String(), "WIP cleared") {
		t.Errorf("expected a WIP-cleared confirmation line; got %q", stdout.String())
	}
	if got := getStoreWIP(t, "foo/bar", 43); got {
		t.Error("expected WIP=false persisted in the store")
	}
}

// TestPRWipOff_UnknownPR_Errors: SetWIP against a PR pg-pr has never
// observed (no store row) surfaces as a command error (the unknown-PR
// error path for `pr wip off`), matching pg2-4dz88.4.2's fork #6 ruling
// (the setter errors rather than silently no-opping on a missing row).
func TestPRWipOff_UnknownPR_Errors(t *testing.T) {
	resetPRWriteFlags()
	_, _ = swapFakes(t)
	setListStateHome(t)

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"pr", "wip", "off", "999", "--repo", "foo/bar"})

	if err := rootCmd.Execute(); err == nil {
		t.Fatal("expected an error when no store row exists for this PR")
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

// ----------------------------------------------------------------------
// --generate-description tests
// ----------------------------------------------------------------------

// stubGenerateDescription replaces the package-level generateDescription
// hook with one that records the resolved agentCLI + skillPath and
// returns a canned body. Cleanup restores the prior fn.
func stubGenerateDescription(t *testing.T, body string, err error) *recordedAgentCall {
	t.Helper()
	rec := &recordedAgentCall{}
	prev := generateDescription
	generateDescription = func(_ context.Context, agentCLI, skillPath string) (string, error) {
		rec.agentCLI = agentCLI
		rec.skillPath = skillPath
		rec.called = true
		return body, err
	}
	t.Cleanup(func() { generateDescription = prev })
	return rec
}

type recordedAgentCall struct {
	called    bool
	agentCLI  string
	skillPath string
}

// writeStubSkill writes a placeholder SKILL.md so resolveSkillPath's
// existence check passes. Returns the path.
func writeStubSkill(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(p, []byte("# stub skill\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPRCreate_GenerateDescription_HappyPath(t *testing.T) {
	resetPRWriteFlags()
	fv, _ := swapFakes(t)
	skill := writeStubSkill(t)
	rec := stubGenerateDescription(t, "generated body", nil)

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{
		"pr", "create",
		"--repo", "foo/bar",
		"--title", "Gen Test",
		"--head", "feat/g",
		"--generate-description",
		"--agent-cli", "/usr/bin/fake-agent",
		"--skill-path", skill,
	})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v (stderr=%s)", err, stderr.String())
	}
	if !rec.called {
		t.Fatal("generateDescription was not invoked")
	}
	if rec.agentCLI != "/usr/bin/fake-agent" {
		t.Errorf("agentCLI: got %q want /usr/bin/fake-agent", rec.agentCLI)
	}
	if rec.skillPath != skill {
		t.Errorf("skillPath: got %q want %q", rec.skillPath, skill)
	}
	if len(fv.createCalls) != 1 || fv.createCalls[0].body != "generated body" {
		t.Fatalf("body not propagated to CreatePR: %+v", fv.createCalls)
	}
}

func TestPRUpdate_GenerateDescription_HappyPath(t *testing.T) {
	resetPRWriteFlags()
	fv, _ := swapFakes(t)
	skill := writeStubSkill(t)
	_ = stubGenerateDescription(t, "updated by agent", nil)

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{
		"pr", "update", "42",
		"--repo", "foo/bar",
		"--generate-description",
		"--agent-cli", "/bin/fake",
		"--skill-path", skill,
	})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v (stderr=%s)", err, stderr.String())
	}
	if len(fv.updateCalls) != 1 || fv.updateCalls[0].body != "updated by agent" {
		t.Fatalf("body not propagated to UpdatePR: %+v", fv.updateCalls)
	}
}

func TestPRCreate_GenerateDescription_ConflictsWithBody(t *testing.T) {
	resetPRWriteFlags()
	_, _ = swapFakes(t)
	skill := writeStubSkill(t)
	stubGenerateDescription(t, "x", nil) // shouldn't fire

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{
		"pr", "create",
		"--repo", "foo/bar",
		"--title", "T",
		"--head", "h",
		"--body", "literal",
		"--generate-description",
		"--agent-cli", "/bin/fake",
		"--skill-path", skill,
	})

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected mutual-exclusion error")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("error should mention mutual exclusion; got %v", err)
	}
}

func TestPRUpdate_GenerateDescription_ConflictsWithBodyStdin(t *testing.T) {
	resetPRWriteFlags()
	_, _ = swapFakes(t)
	skill := writeStubSkill(t)
	stubGenerateDescription(t, "x", nil)

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetIn(strings.NewReader(""))
	rootCmd.SetArgs([]string{
		"pr", "update", "7",
		"--repo", "foo/bar",
		"--body-stdin",
		"--generate-description",
		"--agent-cli", "/bin/fake",
		"--skill-path", skill,
	})

	if err := rootCmd.Execute(); err == nil {
		t.Fatal("expected mutual-exclusion error")
	}
}

func TestPRCreate_GenerateDescription_MissingAgentCLI(t *testing.T) {
	resetPRWriteFlags()
	_, _ = swapFakes(t)
	skill := writeStubSkill(t)
	// Don't stub generateDescription; we should fail before invoking.

	// Strip zr-agent from PATH so LookPath returns no match.
	t.Setenv("PATH", "/nonexistent")
	t.Setenv(agentCLIEnv, "")

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{
		"pr", "create",
		"--repo", "foo/bar",
		"--title", "T",
		"--head", "h",
		"--generate-description",
		"--skill-path", skill,
	})

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected missing-agent-CLI error")
	}
	if !strings.Contains(err.Error(), "PG_PR_AGENT_CLI") || !strings.Contains(err.Error(), "zr-agent") {
		t.Errorf("error should suggest PG_PR_AGENT_CLI / zr-agent; got %v", err)
	}
	if !strings.Contains(err.Error(), skill) {
		t.Errorf("error should include skill path %q; got %v", skill, err)
	}
}

func TestPRCreate_GenerateDescription_MissingSkill(t *testing.T) {
	resetPRWriteFlags()
	_, _ = swapFakes(t)

	missing := filepath.Join(t.TempDir(), "does-not-exist.md")

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{
		"pr", "create",
		"--repo", "foo/bar",
		"--title", "T",
		"--head", "h",
		"--generate-description",
		"--agent-cli", "/bin/fake",
		"--skill-path", missing,
	})

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected skill-missing error")
	}
	if !strings.Contains(err.Error(), "skill file not found") {
		t.Errorf("error should mention skill file; got %v", err)
	}
}

func TestPRCreate_GenerateDescription_AgentEmptyOutput(t *testing.T) {
	resetPRWriteFlags()
	_, _ = swapFakes(t)
	skill := writeStubSkill(t)
	stubGenerateDescription(t, "   \n  ", nil) // whitespace-only, trims to ""

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{
		"pr", "create",
		"--repo", "foo/bar",
		"--title", "T",
		"--head", "h",
		"--generate-description",
		"--agent-cli", "/bin/fake",
		"--skill-path", skill,
	})

	if err := rootCmd.Execute(); err == nil {
		t.Fatal("expected empty-body error")
	}
}

func TestPRCreate_GenerateDescription_AgentEnvVarFallback(t *testing.T) {
	resetPRWriteFlags()
	fv, _ := swapFakes(t)
	skill := writeStubSkill(t)
	rec := stubGenerateDescription(t, "body-from-env-agent", nil)

	t.Setenv(agentCLIEnv, "/opt/env-agent")
	t.Setenv("PATH", "/nonexistent") // ensure no zr-agent on PATH

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{
		"pr", "create",
		"--repo", "foo/bar",
		"--title", "T",
		"--head", "h",
		"--generate-description",
		"--skill-path", skill,
	})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v (stderr=%s)", err, stderr.String())
	}
	if rec.agentCLI != "/opt/env-agent" {
		t.Errorf("env var fallback ignored; agentCLI=%q", rec.agentCLI)
	}
	if len(fv.createCalls) != 1 || fv.createCalls[0].body != "body-from-env-agent" {
		t.Fatalf("body not propagated: %+v", fv.createCalls)
	}
}

func TestPRCreate_GenerateDescription_SkillEnvVarFallback(t *testing.T) {
	resetPRWriteFlags()
	_, _ = swapFakes(t)
	skill := writeStubSkill(t)
	rec := stubGenerateDescription(t, "ok", nil)

	t.Setenv(skillPathEnv, skill)

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{
		"pr", "create",
		"--repo", "foo/bar",
		"--title", "T",
		"--head", "h",
		"--generate-description",
		"--agent-cli", "/bin/fake",
	})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v (stderr=%s)", err, stderr.String())
	}
	if rec.skillPath != skill {
		t.Errorf("skill env var fallback ignored; skillPath=%q want %q", rec.skillPath, skill)
	}
}

func TestPRCreate_GenerateDescription_AgentFailure(t *testing.T) {
	resetPRWriteFlags()
	_, _ = swapFakes(t)
	skill := writeStubSkill(t)
	stubGenerateDescription(t, "", errors.New("agent crashed"))

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{
		"pr", "create",
		"--repo", "foo/bar",
		"--title", "T",
		"--head", "h",
		"--generate-description",
		"--agent-cli", "/bin/fake",
		"--skill-path", skill,
	})

	if err := rootCmd.Execute(); err == nil {
		t.Fatal("expected agent failure to propagate")
	}
}

// TestGenerateDescription_SubprocessIntegration covers the real
// exec.CommandContext path by pointing --agent-cli at /bin/cat, which
// echoes the skill contents back to stdout. This proves the wiring end
// to end without depending on the live zr-agent.
func TestGenerateDescription_SubprocessIntegration(t *testing.T) {
	resetPRWriteFlags()
	fv, _ := swapFakes(t)

	// Don't stub generateDescription — we want the real one.
	skill := writeStubSkill(t)

	cat, err := exec.LookPath("cat")
	if err != nil {
		t.Skip("cat not on PATH")
	}

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{
		"pr", "create",
		"--repo", "foo/bar",
		"--title", "Integ",
		"--head", "h",
		"--generate-description",
		"--agent-cli", cat,
		"--skill-path", skill,
	})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v (stderr=%s)", err, stderr.String())
	}
	if len(fv.createCalls) != 1 {
		t.Fatalf("CreatePR not called: %+v", fv.createCalls)
	}
	if !strings.Contains(fv.createCalls[0].body, "stub skill") {
		t.Errorf("body should contain skill text; got %q", fv.createCalls[0].body)
	}
}
