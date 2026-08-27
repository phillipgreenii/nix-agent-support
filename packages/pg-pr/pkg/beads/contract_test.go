//go:build contract

package beads

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// ----------------------------------------------------------------------
// Real-bd integration tests (contract label)
// ----------------------------------------------------------------------
//
// Every test in this file shells out to the REAL `bd` CLI (embedded-Dolt
// mode, isolated per test under t.TempDir() — it never touches this
// machine's shared dolt server on :25252, by design; see pg2-5ek6b for the
// observed exception where it apparently DID, still not fully explained).
// That isolation makes each call correct, but not cheap: every `bd`
// invocation pays real embedded-Dolt engine startup (roughly 0.5-1.5s
// measured on a quiet machine, more under load), and a single test
// typically issues several (init, config set, create, dep add, close, ...).
// Multiplied across the ~20 workspace-creating tests here, a *serial* run
// legitimately takes minutes, and under heavy concurrent load from sibling
// sessions on a shared dev machine it can run past ten minutes — this is
// what pg2-8tpoz observed as an apparent "hang" in
// TestTickCache_OpenProcessingByPR_IgnoresClosedCycles; that test is not
// stuck, the whole file is just slow by construction. Every such test
// therefore calls t.Parallel() (workspaceCounter below exists exactly to
// keep their bd issue prefixes collision-free under that concurrency) to
// bound the wall-clock cost. Do NOT run `go test -tags contract ./...` (or
// even `./pkg/beads/...`) bare/unbounded in this package for the same
// reason pg2-8tpoz was filed: always pass an explicit generous -timeout or
// run it backgrounded, per this workspace's Bash-timeout rules.
//
// pg2-5ek6b: this file exists BECAUSE these tests drive a real external
// system (the `bd` CLI, which in turn talks to a dolt engine) — exactly
// what the pg-test-runner "contract" label is for (phillipg-nix-repo-base's
// docs/superpowers/specs/2026-08-24-pg-test-runner-design.md). They were
// previously interleaved with pure-unit tests in action_test.go,
// deptree_test.go, mergerequest_test.go, processingcycle_test.go, and
// tickcache_test.go, so they ran under the commit-time
// `go test -race -run ^(Test|Example) ./...` unit gate and collided with
// each other and with the shared per-user dolt server under load. Moved
// here, behind the `contract` build tag, they no longer compile into the
// default unit run at all (the Go compiler excludes an unmatched
// `//go:build` file outright, not merely skips running it) and instead run
// only via `go test -tags contract -p 1 ./...` (mirroring packages/pb and
// packages/ccpool's own contract suites), or under `nix flake check`'s
// hermetic checks.pg-pr-go-tests tier.

// workspaceCounter generates unique prefixes so parallel tests get distinct
// bd issue IDs.
var workspaceCounter int64

// newBDWorkspace boots a fresh bd workspace under t.TempDir() and returns a
// Client whose runner targets it. Skips the test if `bd` is not on PATH.
func newBDWorkspace(t *testing.T) (*Client, *CLIRunner) {
	t.Helper()
	if _, err := exec.LookPath("bd"); err != nil {
		t.Skip("bd not on PATH")
	}
	dir := t.TempDir()
	n := atomic.AddInt64(&workspaceCounter, 1)
	prefix := strings.ReplaceAll(t.Name(), "/", "_")
	if len(prefix) > 12 {
		prefix = prefix[:12]
	}
	prefix = strings.ToLower(prefix)
	prefix = sanitizePrefix(prefix)
	if prefix == "" {
		prefix = "bd"
	}
	// Append counter to avoid collisions across t.Run subtests.
	prefix = sanitizePrefix(prefix) + "x"
	// bd prefix must be alphanumeric (we'll add hex suffix from counter).
	prefix = trimToAlphanumeric(prefix)
	// Use a short numeric tag to make the prefix unique per test invocation.
	prefix = prefix + alnumOf(n)

	env := buildCleanEnv()
	initCtx, initCancel := context.WithTimeout(context.Background(), realBDSetupTimeout)
	defer initCancel()
	// --skip-agents/--skip-hooks/-q/--non-interactive: this is a throwaway
	// fixture torn down at test end, so skip the AGENTS.md/CLAUDE.md/git-hooks
	// generation and interactive-mode side effects bd init otherwise performs
	// (a real git commit included) — pure overhead multiplied across the
	// package's dozens of newBDWorkspace calls.
	initCmd := exec.CommandContext(initCtx, "bd", "init", "--prefix", prefix,
		"--non-interactive", "-q", "--skip-agents", "--skip-hooks")
	initCmd.Dir = dir
	initCmd.Env = env
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("bd init: %v\n%s", err, out)
	}

	// Register the merge-request and feedback custom types. The warning bd
	// emits about "types.custom is not a recognized key" is cosmetic; the
	// value is honored.
	if err := bdConfigSet(t, dir, env, "types.custom", "merge-request,feedback"); err != nil {
		t.Fatalf("bd config set types.custom: %v", err)
	}

	runner := &CLIRunner{Dir: dir, Env: env}
	return NewClientWithRunner(runner), runner
}

// buildCleanEnv strips BEADS_DIR/WORKSPACE_ROOT so bd doesn't bind to the
// outer workspace. PATH and HOME are preserved.
func buildCleanEnv() []string {
	out := make([]string, 0, len(os.Environ()))
	for _, kv := range os.Environ() {
		k := kv
		if i := strings.Index(kv, "="); i > 0 {
			k = kv[:i]
		}
		switch k {
		case "BEADS_DIR", "WORKSPACE_ROOT", "ZR_MACHINE_SUPPORT_WORKSPACE_ROOT":
			continue
		}
		out = append(out, kv)
	}
	return out
}

func bdConfigSet(t *testing.T, dir string, env []string, key, val string) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), realBDSetupTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bd", "config", "set", key, val)
	cmd.Dir = dir
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Logf("bd config set output: %s", out)
		return err
	}
	return nil
}

// realBDSetupTimeout bounds the one-time `bd init` / `bd config set`
// subprocesses newBDWorkspace runs to boot an isolated per-test workspace.
// Mirrors internal/sync/sync_test.go's identically-named constant in this
// same module: bd init has been measured at ~19s standalone but 3+ minutes
// under heavy host load (the tc-8myb incident); pg2-kc0f0 observed the same
// shape here (TestTickCache_OpenProcessingByPR_IgnoresClosedCycles ran ~601s
// before hitting go test's default 10-minute timeout during concurrent
// /pb:drain-beads sessions). 5 minutes is generous enough to tolerate that
// load case while still failing fast — and diagnosably — well short of the
// package's default test budget if `bd` genuinely wedges.
const realBDSetupTimeout = 5 * time.Minute

func sanitizePrefix(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func trimToAlphanumeric(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// alnumOf returns a base36 encoding of n.
func alnumOf(n int64) string {
	if n == 0 {
		return "0"
	}
	const alphabet = "0123456789abcdefghijklmnopqrstuvwxyz"
	out := ""
	for n > 0 {
		out = string(alphabet[n%36]) + out
		n /= 36
	}
	return out
}

// ----------------------------------------------------------------------
// Tests
// ----------------------------------------------------------------------

// ----------------------------------------------------------------------
// Fixtures moved from deptree_test.go (real-bd dep-tree helpers)
// ----------------------------------------------------------------------

func createChildBead(t *testing.T, runner *CLIRunner, parentID, title string) string {
	t.Helper()
	out, err := runner.Run(context.Background(),
		"create", "--title", title, "--type", "task", "--priority", "2", "--silent")
	if err != nil {
		t.Fatalf("bd create: %v", err)
	}
	id := strings.TrimSpace(out)
	if id == "" {
		t.Fatalf("bd create returned empty id (stdout=%q)", out)
	}
	if _, err := runner.Run(context.Background(), "dep", "add", id, parentID); err != nil {
		t.Fatalf("bd dep add %s %s: %v", id, parentID, err)
	}
	return id
}

func addLabel(t *testing.T, runner *CLIRunner, id, label string) {
	t.Helper()
	if _, err := runner.Run(context.Background(), "label", "add", id, label); err != nil {
		t.Fatalf("bd label add: %v", err)
	}
}

func closeBead(t *testing.T, runner *CLIRunner, id string) {
	t.Helper()
	// --force bypasses bd's "blocked by open issues" guard, which would
	// otherwise refuse to close a bead whose parent (the merge-request) is
	// still open. The guard is fine for production but gets in the way of
	// these isolated unit tests, which only care about the status flip.
	if _, err := runner.Run(context.Background(), "close", id, "--force"); err != nil {
		t.Fatalf("bd close: %v", err)
	}
}

// ----------------------------------------------------------------------
// Fixture moved from tickcache_test.go
// ----------------------------------------------------------------------

func realBDCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)
	return ctx
}

// ----------------------------------------------------------------------
// Tests moved from action_test.go
// ----------------------------------------------------------------------

func TestCreateAction_CreatesAndLinks(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c, _ := newBDWorkspace(t)

	prID, _, _ := c.EnsureMergeRequest(ctx, "", MergeRequestFields{Repo: "foo/bar", PRNumber: 12})

	actID, err := c.CreateAction(ctx, CreateActionInput{
		MergeRequestID: prID,
		Kind:           ActionKindApplySuggestion,
		BdType:         "task",
		Title:          "Apply reviewer suggestion: rename X",
	})
	if err != nil {
		t.Fatalf("CreateAction: %v", err)
	}
	if actID == "" {
		t.Fatalf("expected non-empty action ID")
	}

	// Verify the action is a child of the merge-request bead.
	children, err := c.ListChildrenOfPR(ctx, prID)
	if err != nil {
		t.Fatalf("ListChildrenOfPR: %v", err)
	}
	found := false
	for _, id := range children {
		if id == actID {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected action %s under PR %s, got %v", actID, prID, children)
	}
}

func TestCreateAction_DefaultsBdTypeToTask(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c, _ := newBDWorkspace(t)
	prID, _, _ := c.EnsureMergeRequest(ctx, "", MergeRequestFields{Repo: "a/b", PRNumber: 1})

	actID, err := c.CreateAction(ctx, CreateActionInput{
		MergeRequestID: prID,
		Kind:           ActionKindFixCI,
		Title:          "fix CI",
	})
	if err != nil {
		t.Fatalf("CreateAction: %v", err)
	}
	if actID == "" {
		t.Fatalf("expected non-empty action id")
	}
}

// ----------------------------------------------------------------------
// Tests moved from deptree_test.go
// ----------------------------------------------------------------------

func TestDepTreeUp_Empty(t *testing.T) {
	t.Parallel()
	c, _ := newBDWorkspace(t)
	ctx := context.Background()
	mr, _, err := c.EnsureMergeRequest(ctx, "MR-empty", MergeRequestFields{Repo: "x/y", PRNumber: 1})
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.DepTreeUp(ctx, mr)
	if err != nil {
		t.Fatalf("DepTreeUp: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want empty, got %+v", got)
	}
}

func TestDepTreeUp_WithChildren(t *testing.T) {
	t.Parallel()
	c, runner := newBDWorkspace(t)
	ctx := context.Background()
	mr, _, err := c.EnsureMergeRequest(ctx, "MR-children", MergeRequestFields{Repo: "x/y", PRNumber: 2})
	if err != nil {
		t.Fatal(err)
	}
	a := createChildBead(t, runner, mr, "A")
	b := createChildBead(t, runner, mr, "B")
	cc := createChildBead(t, runner, mr, "C")
	addLabel(t, runner, b, "human")
	addLabel(t, runner, cc, "human")
	closeBead(t, runner, cc)

	got, err := c.DepTreeUp(ctx, mr)
	if err != nil {
		t.Fatalf("DepTreeUp: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len: got %d want 3 — %+v", len(got), got)
	}

	// DepTreeUp no longer populates labels; overlay them via the workspace's
	// human-labeled set the same way production does.
	set, err := c.HumanLabeledBeads(ctx)
	if err != nil {
		t.Fatalf("HumanLabeledBeads: %v", err)
	}
	ApplyHumanLabels(got, set)

	byID := map[string]DepNode{}
	for _, n := range got {
		byID[n.ID] = n
	}
	if byID[a].Status == "closed" {
		t.Errorf("A should be open, got %+v", byID[a])
	}
	if !hasLabel(byID[b].Labels, "human") {
		t.Errorf("B should have human label after overlay, got %+v", byID[b].Labels)
	}
	if byID[cc].Status != "closed" {
		t.Errorf("C should be closed, got %q", byID[cc].Status)
	}

	// Root must not appear in the returned list.
	if _, ok := byID[mr]; ok {
		t.Errorf("root %s should not appear in DepTreeUp output", mr)
	}

	// AllNonClosedHumanLabeled: A is non-closed but not human-labeled → false.
	if AllNonClosedHumanLabeled(got) {
		t.Error("expected false (A is non-closed but unlabeled)")
	}

	// After labeling A as human, all non-closed deps carry the label → true.
	addLabel(t, runner, a, "human")
	got2, err := c.DepTreeUp(ctx, mr)
	if err != nil {
		t.Fatal(err)
	}
	set2, err := c.HumanLabeledBeads(ctx)
	if err != nil {
		t.Fatalf("HumanLabeledBeads: %v", err)
	}
	ApplyHumanLabels(got2, set2)
	if !AllNonClosedHumanLabeled(got2) {
		t.Errorf("expected true after labeling A; got deps=%+v", got2)
	}
}

func TestHumanLabeledBeads_EmptyWorkspace(t *testing.T) {
	t.Parallel()
	c, _ := newBDWorkspace(t)
	set, err := c.HumanLabeledBeads(context.Background())
	if err != nil {
		t.Fatalf("HumanLabeledBeads: %v", err)
	}
	if len(set) != 0 {
		t.Errorf("want empty set, got %+v", set)
	}
}

func TestHumanLabeledBeads_OnlyHumanLabeled(t *testing.T) {
	t.Parallel()
	c, runner := newBDWorkspace(t)
	ctx := context.Background()

	// Anchor: a merge-request bead lets us reuse createChildBead so each
	// child has a real parent edge (bd dep add requires both ids).
	mr, _, err := c.EnsureMergeRequest(ctx, "MR-anchor", MergeRequestFields{Repo: "x/y", PRNumber: 99})
	if err != nil {
		t.Fatal(err)
	}

	// Two human-labeled beads + one with a different label + one unlabeled.
	a := createChildBead(t, runner, mr, "A")
	b := createChildBead(t, runner, mr, "B")
	cc := createChildBead(t, runner, mr, "C")
	d := createChildBead(t, runner, mr, "D")
	addLabel(t, runner, a, "human")
	addLabel(t, runner, b, "human")
	addLabel(t, runner, cc, "needs-triage")

	set, err := c.HumanLabeledBeads(ctx)
	if err != nil {
		t.Fatalf("HumanLabeledBeads: %v", err)
	}
	if !set[a] {
		t.Errorf("expected %s in set", a)
	}
	if !set[b] {
		t.Errorf("expected %s in set", b)
	}
	if set[cc] {
		t.Errorf("did not expect %s in set (different label)", cc)
	}
	if set[d] {
		t.Errorf("did not expect %s in set (unlabeled)", d)
	}
}

// ----------------------------------------------------------------------
// Tests moved from mergerequest_test.go
// ----------------------------------------------------------------------

func TestEnsureMergeRequest_CreatesWhenAbsent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c, _ := newBDWorkspace(t)

	id, alreadyClosed, err := c.EnsureMergeRequest(ctx, "WIP feature", MergeRequestFields{
		Repo:     "foo/bar",
		PRNumber: 7,
		State:    "open",
		Branch:   "feat/x",
		Base:     "main",
		Author:   "alice",
		URL:      "https://github.com/foo/bar/pull/7",
	})
	if err != nil {
		t.Fatalf("EnsureMergeRequest: %v", err)
	}
	if id == "" {
		t.Fatalf("expected non-empty ID")
	}
	if alreadyClosed {
		t.Fatalf("expected alreadyClosed=false on first create")
	}

	mr, err := c.GetMergeRequest(ctx, id)
	if err != nil {
		t.Fatalf("GetMergeRequest: %v", err)
	}
	if mr == nil {
		t.Fatalf("bead not found after create")
	} else if mr.Status != "open" {
		t.Fatalf("status: got %q want open", mr.Status)
	} else if mr.Type != "merge-request" {
		t.Fatalf("type: got %q want merge-request", mr.Type)
	} else if mr.Fields.Repo != "foo/bar" || mr.Fields.PRNumber != 7 {
		t.Fatalf("metadata: %+v", mr.Fields)
	} else if mr.Fields.State != "open" {
		t.Fatalf("state metadata: got %q", mr.Fields.State)
	} else if mr.Fields.LastSyncedAt == "" {
		t.Fatalf("expected last_synced_at to be populated")
	}
}

func TestEnsureMergeRequest_UpdatesWhenPresent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c, _ := newBDWorkspace(t)

	id1, _, err := c.EnsureMergeRequest(ctx, "first", MergeRequestFields{
		Repo: "foo/bar", PRNumber: 7, State: "open",
	})
	if err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	id2, alreadyClosed, err := c.EnsureMergeRequest(ctx, "second", MergeRequestFields{
		Repo: "foo/bar", PRNumber: 7, State: "ready",
	})
	if err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	if id1 != id2 {
		t.Fatalf("expected same ID across upserts: %q vs %q", id1, id2)
	}
	if alreadyClosed {
		t.Fatalf("expected alreadyClosed=false on open bead")
	}
	mr, err := c.GetMergeRequest(ctx, id1)
	if err != nil {
		t.Fatalf("GetMergeRequest: %v", err)
	}
	if mr.Fields.State != "ready" {
		t.Fatalf("state: got %q want ready", mr.Fields.State)
	}
}

func TestEnsureMergeRequest_SkipsClosedBead(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c, _ := newBDWorkspace(t)

	id, _, err := c.EnsureMergeRequest(ctx, "", MergeRequestFields{
		Repo: "foo/bar", PRNumber: 7,
	})
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if err := c.CloseMergeRequest(ctx, id, "merged"); err != nil {
		t.Fatalf("CloseMergeRequest: %v", err)
	}
	id2, alreadyClosed, err := c.EnsureMergeRequest(ctx, "", MergeRequestFields{
		Repo: "foo/bar", PRNumber: 7, State: "open",
	})
	if err != nil {
		t.Fatalf("re-ensure after close: %v", err)
	}
	if id2 != id {
		t.Fatalf("expected same ID, got %q vs %q", id2, id)
	}
	if !alreadyClosed {
		t.Fatalf("expected alreadyClosed=true on closed bead")
	}
	mr, _ := c.GetMergeRequest(ctx, id)
	if mr.Status != "closed" {
		t.Fatalf("status: got %q want closed", mr.Status)
	}
	// And the state metadata should NOT have been overwritten with "open".
	if mr.Fields.State == "open" {
		t.Fatalf("closed bead's state metadata was overwritten back to open")
	}
}

func TestCloseMergeRequest_Idempotent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c, _ := newBDWorkspace(t)

	id, _, err := c.EnsureMergeRequest(ctx, "", MergeRequestFields{
		Repo: "foo/bar", PRNumber: 9,
	})
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if err := c.CloseMergeRequest(ctx, id, "merged"); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := c.CloseMergeRequest(ctx, id, "merged"); err != nil {
		t.Fatalf("second close should be a no-op, got: %v", err)
	}
}

func TestListMergeRequests_OpenOnly(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c, _ := newBDWorkspace(t)

	openID, _, err := c.EnsureMergeRequest(ctx, "", MergeRequestFields{Repo: "x/y", PRNumber: 1})
	if err != nil {
		t.Fatal(err)
	}
	closedID, _, err := c.EnsureMergeRequest(ctx, "", MergeRequestFields{Repo: "x/y", PRNumber: 2})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.CloseMergeRequest(ctx, closedID, "merged"); err != nil {
		t.Fatal(err)
	}

	openOnly, err := c.ListMergeRequests(ctx, false)
	if err != nil {
		t.Fatalf("list open: %v", err)
	}
	if len(openOnly) != 1 || openOnly[0].ID != openID {
		t.Fatalf("expected single open bead %q, got %+v", openID, openOnly)
	}

	all, err := c.ListMergeRequests(ctx, true)
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 beads with --all, got %d", len(all))
	}
}

func TestFindByRepoAndNumber_HitAndMiss(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c, _ := newBDWorkspace(t)

	id, _, err := c.EnsureMergeRequest(ctx, "", MergeRequestFields{Repo: "foo/bar", PRNumber: 31})
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}

	got, err := c.FindByRepoAndNumber(ctx, "foo/bar", 31)
	if err != nil {
		t.Fatalf("FindByRepoAndNumber: %v", err)
	}
	if got == nil || got.ID != id {
		t.Fatalf("expected to find %s, got %+v", id, got)
	}

	miss, err := c.FindByRepoAndNumber(ctx, "foo/bar", 999)
	if err != nil {
		t.Fatalf("miss path: %v", err)
	}
	if miss != nil {
		t.Fatalf("expected nil for unknown pr, got %+v", miss)
	}
}

// TestSetPriority_RoundTripsThroughRealBD guards the json:"priority" contract
// against a real bd workspace: it creates a bead, sets its priority via
// SetPriority (which shells out to `bd update -p`), then reads it back via
// both GetMergeRequest and ListMergeRequests to confirm Priority survives the
// bd list --json round trip (a regression the unit tests above — which
// construct bdIssue directly and never touch JSON — cannot catch).
func TestSetPriority_RoundTripsThroughRealBD(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c, _ := newBDWorkspace(t)

	id, _, err := c.EnsureMergeRequest(ctx, "", MergeRequestFields{Repo: "foo/bar", PRNumber: 42})
	if err != nil {
		t.Fatalf("EnsureMergeRequest: %v", err)
	}

	if err := c.SetPriority(ctx, id, 1); err != nil {
		t.Fatalf("SetPriority: %v", err)
	}

	got, err := c.GetMergeRequest(ctx, id)
	if err != nil {
		t.Fatalf("GetMergeRequest: %v", err)
	}
	if got == nil {
		t.Fatalf("bead %s not found after SetPriority", id)
	}
	if got.Priority != 1 {
		t.Fatalf("GetMergeRequest Priority = %d, want 1", got.Priority)
	}

	all, err := c.ListMergeRequests(ctx, false)
	if err != nil {
		t.Fatalf("ListMergeRequests: %v", err)
	}
	var found *MergeRequest
	for i := range all {
		if all[i].ID == id {
			found = &all[i]
		}
	}
	if found == nil {
		t.Fatalf("bead %s not found in ListMergeRequests", id)
	}
	if found.Priority != 1 {
		t.Fatalf("ListMergeRequests Priority = %d, want 1", found.Priority)
	}
}

// TestDraftFlag_RoundTripsThroughRealBD guards the pg2-4dz88.10 fix against a
// real bd workspace: it creates a bead with draft:true, then clears it via a
// SECOND `--metadata` update (UpdateMergeRequest, mirroring the encoder's
// explicit-false emission), and confirms the clear actually persists through
// real bd's merge-rather-than-replace `--metadata` semantics — the exact
// mechanism the bug exploited (an absent key leaves the previously-stored
// true in place forever). Modeled on TestSetPriority_RoundTripsThroughRealBD.
func TestDraftFlag_RoundTripsThroughRealBD(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c, _ := newBDWorkspace(t)

	id, _, err := c.EnsureMergeRequest(ctx, "", MergeRequestFields{
		Repo: "foo/bar", PRNumber: 42, State: "open", Draft: true,
	})
	if err != nil {
		t.Fatalf("EnsureMergeRequest: %v", err)
	}

	got, err := c.GetMergeRequest(ctx, id)
	if err != nil {
		t.Fatalf("GetMergeRequest: %v", err)
	}
	if got == nil {
		t.Fatalf("bead %s not found after create", id)
	}
	if !got.Fields.Draft {
		t.Fatalf("expected draft=true after create, got %+v", got.Fields)
	}

	if err := c.UpdateMergeRequest(ctx, id, MergeRequestFields{
		Repo: "foo/bar", PRNumber: 42, State: "open", Draft: false,
	}); err != nil {
		t.Fatalf("UpdateMergeRequest (clear draft): %v", err)
	}

	got, err = c.GetMergeRequest(ctx, id)
	if err != nil {
		t.Fatalf("GetMergeRequest after clear: %v", err)
	}
	if got == nil {
		t.Fatalf("bead %s not found after clearing draft", id)
	}
	if got.Fields.Draft {
		t.Fatalf("expected draft=false after clearing, got true (bd --metadata merge regression)")
	}

	all, err := c.ListMergeRequests(ctx, false)
	if err != nil {
		t.Fatalf("ListMergeRequests: %v", err)
	}
	var found *MergeRequest
	for i := range all {
		if all[i].ID == id {
			found = &all[i]
		}
	}
	if found == nil {
		t.Fatalf("bead %s not found in ListMergeRequests", id)
	}
	if found.Fields.Draft {
		t.Fatalf("ListMergeRequests: expected draft=false, got true")
	}
}

// TestNewClientForRepo_HitsRepoWorkspace creates two real bd workspaces in
// distinct temp dirs and verifies that NewClientForRepo(dirA) writes to
// workspace A only — beads created on the A-scoped client are not visible
// from the B-scoped client.
func TestNewClientForRepo_HitsRepoWorkspace(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	if _, err := exec.LookPath("bd"); err != nil {
		t.Skip("bd not on PATH")
	}

	// Boot two independent workspaces using the same helper used elsewhere in
	// the package; we then construct fresh Clients via NewClientForRepo to
	// confirm that the public constructor actually targets the right dir.
	clientA, runnerA := newBDWorkspace(t)
	clientB, runnerB := newBDWorkspace(t)
	_ = clientA
	_ = clientB

	// Build new clients via NewClientForRepo and reuse the prepared env so
	// BEADS_DIR/WORKSPACE_ROOT leakage is excluded. We pierce into the inner
	// runner to set Env without changing the API surface.
	pubA := NewClientForRepo(runnerA.Dir)
	pubA.Runner.(*CLIRunner).Env = runnerA.Env
	pubB := NewClientForRepo(runnerB.Dir)
	pubB.Runner.(*CLIRunner).Env = runnerB.Env

	idA, _, err := pubA.EnsureMergeRequest(ctx, "in A", MergeRequestFields{
		Repo: "monoA/mod", PRNumber: 1,
	})
	if err != nil {
		t.Fatalf("EnsureMergeRequest in A: %v", err)
	}

	// Workspace B must NOT see the bead created in A.
	gotB, err := pubB.GetMergeRequest(ctx, idA)
	if err != nil {
		t.Fatalf("lookup in B: %v", err)
	}
	if gotB != nil {
		t.Fatalf("bead %s leaked from workspace A into workspace B", idA)
	}

	// And A must still see its own bead.
	gotA, err := pubA.GetMergeRequest(ctx, idA)
	if err != nil {
		t.Fatalf("lookup in A: %v", err)
	}
	if gotA == nil {
		t.Fatalf("bead %s not visible from its own workspace", idA)
	}
}

// ----------------------------------------------------------------------
// Tests moved from processingcycle_test.go
// ----------------------------------------------------------------------

func TestCreateProcessingCycle_CreatesAndLinks(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c, _ := newBDWorkspace(t)

	prID, _, err := c.EnsureMergeRequest(ctx, "", MergeRequestFields{Repo: "foo/bar", PRNumber: 7})
	if err != nil {
		t.Fatalf("ensure MR: %v", err)
	}

	cycleID, err := c.CreateProcessingCycle(ctx, CreateProcessingCycleInput{PRBeadID: prID, Key: "foo/bar#7", Mine: false})
	if err != nil {
		t.Fatalf("CreateProcessingCycle: %v", err)
	}
	if cycleID == "" {
		t.Fatalf("expected non-empty cycle ID")
	}

	// Verify the open-cycle finder picks it up.
	id, found, err := c.FindOpenProcessingCycle(ctx, prID)
	if err != nil {
		t.Fatalf("FindOpenProcessingCycle: %v", err)
	}
	if !found {
		t.Fatalf("expected to find open cycle for %s", prID)
	}
	if id != cycleID {
		t.Fatalf("expected %s, got %s", cycleID, id)
	}
}

func TestFindOpenProcessingCycle_NoneOpen(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c, _ := newBDWorkspace(t)

	prID, _, err := c.EnsureMergeRequest(ctx, "", MergeRequestFields{Repo: "foo/bar", PRNumber: 9})
	if err != nil {
		t.Fatalf("ensure MR: %v", err)
	}
	id, found, err := c.FindOpenProcessingCycle(ctx, prID)
	if err != nil {
		t.Fatalf("FindOpenProcessingCycle: %v", err)
	}
	if found || id != "" {
		t.Fatalf("expected no open cycle, got id=%q found=%v", id, found)
	}
}

func TestFindOpenProcessingCycle_AfterClose(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c, _ := newBDWorkspace(t)

	prID, _, err := c.EnsureMergeRequest(ctx, "", MergeRequestFields{Repo: "foo/bar", PRNumber: 11})
	if err != nil {
		t.Fatalf("ensure MR: %v", err)
	}
	cycleID, err := c.CreateProcessingCycle(ctx, CreateProcessingCycleInput{PRBeadID: prID, Key: "foo/bar#11", Mine: false})
	if err != nil {
		t.Fatalf("CreateProcessingCycle: %v", err)
	}
	if err := c.CloseProcessingCycle(ctx, cycleID, "done"); err != nil {
		t.Fatalf("CloseProcessingCycle: %v", err)
	}

	_, found, err := c.FindOpenProcessingCycle(ctx, prID)
	if err != nil {
		t.Fatalf("FindOpenProcessingCycle: %v", err)
	}
	if found {
		t.Fatalf("expected no open cycle after close")
	}
}

func TestListChildrenOfPR(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c, _ := newBDWorkspace(t)

	prID, _, err := c.EnsureMergeRequest(ctx, "", MergeRequestFields{Repo: "foo/bar", PRNumber: 21})
	if err != nil {
		t.Fatalf("ensure MR: %v", err)
	}
	cycleID, err := c.CreateProcessingCycle(ctx, CreateProcessingCycleInput{PRBeadID: prID, Key: "foo/bar#21", Mine: false})
	if err != nil {
		t.Fatalf("CreateProcessingCycle: %v", err)
	}

	children, err := c.ListChildrenOfPR(ctx, prID)
	if err != nil {
		t.Fatalf("ListChildrenOfPR: %v", err)
	}
	found := false
	for _, id := range children {
		if id == cycleID {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected cycle %s in children, got %v", cycleID, children)
	}
}

// ----------------------------------------------------------------------
// Tests moved from tickcache_test.go
// ----------------------------------------------------------------------

func TestLoadTickCache_EmptyWorkspace(t *testing.T) {
	t.Parallel()
	c, _ := newBDWorkspace(t)
	cache := c.LoadTickCache(context.Background())
	if cache == nil {
		t.Fatal("LoadTickCache returned nil")
	}
	if len(cache.HumanLabeled) != 0 {
		t.Errorf("expected empty HumanLabeled, got %d", len(cache.HumanLabeled))
	}
	if len(cache.MergeRequestsByID) != 0 {
		t.Errorf("expected empty MergeRequestsByID, got %d", len(cache.MergeRequestsByID))
	}
	if len(cache.OpenProcessingByPR) != 0 {
		t.Errorf("expected empty OpenProcessingByPR, got %d", len(cache.OpenProcessingByPR))
	}
	if len(cache.FeedbackByCycle) != 0 {
		t.Errorf("expected empty FeedbackByCycle, got %d", len(cache.FeedbackByCycle))
	}
}

func TestLoadTickCache_PRWithOpenCycleAndFeedback(t *testing.T) {
	t.Parallel()
	c, runner := newBDWorkspace(t)
	ctx := context.Background()

	// Workspace setup: one PR bead, one open processing-cycle under it,
	// one feedback bead under the cycle.
	prID, _, err := c.EnsureMergeRequest(ctx, "test PR", MergeRequestFields{Repo: "x/y", PRNumber: 1})
	if err != nil {
		t.Fatal(err)
	}
	cycleID, err := c.CreateProcessingCycle(ctx, CreateProcessingCycleInput{PRBeadID: prID, Key: "x/y#1", Mine: false})
	if err != nil {
		t.Fatal(err)
	}
	// Create a feedback bead directly via bd CLI so the cache can find it
	// under TypeFeedback and attach it to the cycle in FeedbackByCycle.
	out, err := runner.Run(ctx,
		"create", "--type=feedback", "--title", "test feedback",
		"--metadata", `{"kind":"comment-thread","fingerprint":"fp-abc"}`,
		"--silent")
	if err != nil {
		t.Fatal(err)
	}
	fbID := strings.TrimSpace(out)
	if fbID == "" {
		t.Fatal("bd create returned empty id")
	}
	if _, err := runner.Run(ctx, "dep", "add", fbID, cycleID, "--type=parent-child", "--no-cycle-check"); err != nil {
		t.Fatalf("dep add: %v", err)
	}

	cache := c.LoadTickCache(ctx)
	if cache == nil {
		t.Fatal("LoadTickCache returned nil")
	}

	// MergeRequestsByID contains the PR.
	if _, ok := cache.MergeRequestsByID[prID]; !ok {
		t.Errorf("MergeRequestsByID missing %s; have %v", prID, cache.MergeRequestsByID)
	}

	// OpenProcessingByPR maps prID → cycleID.
	if got := cache.OpenProcessingByPR[prID]; got != cycleID {
		t.Errorf("OpenProcessingByPR[%s] = %q, want %q", prID, got, cycleID)
	}

	// FeedbackByCycle maps cycleID → [fb].
	fbs := cache.FeedbackByCycle[cycleID]
	if len(fbs) != 1 || fbs[0].ID != fbID {
		t.Errorf("FeedbackByCycle[%s] = %+v, want [%s]", cycleID, fbs, fbID)
	}

	// Lookup helpers.
	gotCycle, ok := cache.OpenCycleFor(prID)
	if !ok || gotCycle != cycleID {
		t.Errorf("OpenCycleFor(%s) = (%q, %v), want (%q, true)", prID, gotCycle, ok, cycleID)
	}
	gotMR, ok := cache.FindMergeRequest("x/y", 1)
	if !ok || gotMR.ID != prID {
		t.Errorf("FindMergeRequest(x/y, 1) = (%+v, %v), want id=%s", gotMR, ok, prID)
	}
}

func TestLoadTickCache_DepsUpByPR(t *testing.T) {
	t.Parallel()
	c, runner := newBDWorkspace(t)
	ctx := context.Background()

	// A merge-request with three descendants: a direct child task (open),
	// a direct child task (closed), and a grandchild task two hops up.
	prID, _, err := c.EnsureMergeRequest(ctx, "deps PR", MergeRequestFields{Repo: "x/y", PRNumber: 11})
	if err != nil {
		t.Fatal(err)
	}
	a := createChildBead(t, runner, prID, "direct-open")
	b := createChildBead(t, runner, prID, "direct-closed")
	closeBead(t, runner, b)
	g := createChildBead(t, runner, a, "grandchild")

	cache := c.LoadTickCache(ctx)
	deps, ok := cache.DepsUpFor(prID)
	if !ok {
		t.Fatalf("DepsUpFor(%s) ok=false; cache built for known PR should always return ok=true", prID)
	}
	byID := map[string]DepNode{}
	for _, d := range deps {
		byID[d.ID] = d
	}
	if _, has := byID[a]; !has {
		t.Errorf("DepsUpByPR missing direct child %s; got %+v", a, deps)
	}
	if _, has := byID[b]; !has {
		t.Errorf("DepsUpByPR missing closed direct child %s; got %+v", b, deps)
	}
	if byID[b].Status != "closed" {
		t.Errorf("DepsUpByPR child %s should carry status=closed; got %+v", b, byID[b])
	}
	if _, has := byID[g]; !has {
		t.Errorf("DepsUpByPR missing transitive grandchild %s; got %+v", g, deps)
	}
	// Root must not be included in its own dep list.
	if _, has := byID[prID]; has {
		t.Errorf("DepsUpByPR should not include the root %s", prID)
	}
}

func TestTickCache_OpenProcessingByPR_IgnoresClosedCycles(t *testing.T) {
	t.Parallel()
	c, runner := newBDWorkspace(t)
	ctx := realBDCtx(t)

	prID, _, err := c.EnsureMergeRequest(ctx, "PR", MergeRequestFields{Repo: "x/y", PRNumber: 7})
	if err != nil {
		t.Fatal(err)
	}
	cycleID, err := c.CreateProcessingCycle(ctx, CreateProcessingCycleInput{PRBeadID: prID, Key: "x/y#7", Mine: false})
	if err != nil {
		t.Fatal(err)
	}
	// Close the cycle so LoadTickCache's open-task list skips it.
	if _, err := runner.Run(ctx, "close", cycleID, "--force"); err != nil {
		t.Fatalf("close cycle: %v", err)
	}

	cache := c.LoadTickCache(ctx)
	if got, ok := cache.OpenCycleFor(prID); ok {
		t.Errorf("OpenCycleFor returned closed cycle: (%q, %v)", got, ok)
	}
}
