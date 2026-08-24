package beads

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// ----------------------------------------------------------------------
// Real-bd integration helpers
// ----------------------------------------------------------------------

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
	initCmd := exec.CommandContext(initCtx, "bd", "init", "--prefix", prefix)
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

func TestEnsureMergeRequest_CreatesWhenAbsent(t *testing.T) {
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

func TestEnsureMergeRequest_Validates(t *testing.T) {
	ctx := context.Background()
	c := NewClientWithRunner(&fakeRunner{})

	if _, _, err := c.EnsureMergeRequest(ctx, "", MergeRequestFields{}); err == nil {
		t.Fatalf("expected validation error on empty input")
	}
	if _, _, err := c.EnsureMergeRequest(ctx, "", MergeRequestFields{Repo: "a/b"}); err == nil {
		t.Fatalf("expected validation error when pr_number is missing")
	}
}

// fakeRunner is a no-op runner used to test argument-validation paths that
// must fail BEFORE bd is invoked.
type fakeRunner struct{}

func (f *fakeRunner) Run(_ context.Context, _ ...string) (string, error) {
	return "", nil
}

func TestFindByRepoAndNumber_HitAndMiss(t *testing.T) {
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

func TestFindByRepoAndNumber_Validates(t *testing.T) {
	c := NewClientWithRunner(&fakeRunner{})
	if _, err := c.FindByRepoAndNumber(context.Background(), "", 1); err == nil {
		t.Fatalf("expected error on empty repo")
	}
	if _, err := c.FindByRepoAndNumber(context.Background(), "a/b", 0); err == nil {
		t.Fatalf("expected error on zero pr_number")
	}
}

// TestNewClientForRepo_SetsRunnerDir verifies the constructed Client's inner
// CLIRunner has the requested Dir, so bd will be invoked with that path as
// cwd and pick up the monorepo's `.beads/` workspace.
func TestNewClientForRepo_SetsRunnerDir(t *testing.T) {
	dir := "/tmp/some-monorepo-root"
	c := NewClientForRepo(dir)
	if c == nil {
		t.Fatalf("expected non-nil Client")
	} else if cli, ok := c.Runner.(*CLIRunner); !ok {
		t.Fatalf("expected runner to be *CLIRunner, got %T", c.Runner)
	} else if cli.Dir != dir {
		t.Fatalf("Dir: got %q want %q", cli.Dir, dir)
	}
}

// TestNewClientForRepo_EmptyDirMatchesNewClient documents that passing "" is
// equivalent to NewClient() — both yield a Client whose runner has no Dir
// and therefore inherits the process cwd.
func TestNewClientForRepo_EmptyDirMatchesNewClient(t *testing.T) {
	c := NewClientForRepo("")
	cli, ok := c.Runner.(*CLIRunner)
	if !ok {
		t.Fatalf("expected runner to be *CLIRunner, got %T", c.Runner)
	}
	if cli.Dir != "" {
		t.Fatalf("Dir: got %q want empty (cwd-discovered)", cli.Dir)
	}
}

// coOwnedRunner returns canned (empty) output and records calls, for
// asserting the exact `update <id> --add-label/--remove-label co-owned`
// arguments SetMergeRequestCoOwned sends to bd.
type coOwnedRunner struct {
	calls [][]string
}

func (r *coOwnedRunner) Run(_ context.Context, args ...string) (string, error) {
	r.calls = append(r.calls, args)
	return "", nil
}

func (r *coOwnedRunner) lastCall() []string {
	if len(r.calls) == 0 {
		return nil
	}
	return r.calls[len(r.calls)-1]
}

// TestSetMergeRequestCoOwned asserts coOwned=true sends
// `update <id> --add-label co-owned` and coOwned=false sends
// `update <id> --remove-label co-owned`.
func TestSetMergeRequestCoOwned(t *testing.T) {
	t.Run("coOwned=true adds the label", func(t *testing.T) {
		r := &coOwnedRunner{}
		c := NewClientWithRunner(r)
		if err := c.SetMergeRequestCoOwned(context.Background(), "mr-1", true); err != nil {
			t.Fatalf("SetMergeRequestCoOwned: %v", err)
		}
		got := r.lastCall()
		want := []string{"update", "mr-1", "--add-label", "co-owned"}
		if len(got) != len(want) {
			t.Fatalf("call = %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("call = %v, want %v", got, want)
			}
		}
	})

	t.Run("coOwned=false removes the label", func(t *testing.T) {
		r := &coOwnedRunner{}
		c := NewClientWithRunner(r)
		if err := c.SetMergeRequestCoOwned(context.Background(), "mr-1", false); err != nil {
			t.Fatalf("SetMergeRequestCoOwned: %v", err)
		}
		got := r.lastCall()
		want := []string{"update", "mr-1", "--remove-label", "co-owned"}
		if len(got) != len(want) {
			t.Fatalf("call = %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("call = %v, want %v", got, want)
			}
		}
	})
}

// TestSetMergeRequestCoOwned_Validates asserts an empty id is rejected before
// bd is invoked.
func TestSetMergeRequestCoOwned_Validates(t *testing.T) {
	c := NewClientWithRunner(&fakeRunner{})
	if err := c.SetMergeRequestCoOwned(context.Background(), "", true); err == nil {
		t.Fatalf("expected validation error on empty id")
	}
}

// TestSetPriority asserts SetPriority sends `update <id> -p <n>` to bd.
// Reuses coOwnedRunner (a generic calls-recorder already defined above) per
// controller guidance rather than inventing a new recorder or an assertCalled
// method on fakeRunner.
func TestSetPriority(t *testing.T) {
	r := &coOwnedRunner{}
	c := NewClientWithRunner(r)
	if err := c.SetPriority(context.Background(), "mr-1", 1); err != nil {
		t.Fatalf("SetPriority: %v", err)
	}
	got := r.lastCall()
	want := []string{"update", "mr-1", "-p", "1"}
	if len(got) != len(want) {
		t.Fatalf("call = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("call = %v, want %v", got, want)
		}
	}
}

// TestSetPriority_Validates asserts an empty id is rejected before bd is
// invoked.
func TestSetPriority_Validates(t *testing.T) {
	r := &coOwnedRunner{}
	c := NewClientWithRunner(r)
	if err := c.SetPriority(context.Background(), "", 1); err == nil {
		t.Fatalf("expected validation error on empty id")
	}
	if len(r.calls) != 0 {
		t.Fatalf("expected no bd calls on validation failure, got %v", r.calls)
	}
}

// TestSetPriority_ClampsRange asserts out-of-range priorities are clamped to
// [0,4] rather than passed through or rejected.
func TestSetPriority_ClampsRange(t *testing.T) {
	t.Run("negative clamps to 0", func(t *testing.T) {
		r := &coOwnedRunner{}
		c := NewClientWithRunner(r)
		if err := c.SetPriority(context.Background(), "mr-1", -5); err != nil {
			t.Fatalf("SetPriority: %v", err)
		}
		got := r.lastCall()
		want := []string{"update", "mr-1", "-p", "0"}
		if len(got) != len(want) {
			t.Fatalf("call = %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("call = %v, want %v", got, want)
			}
		}
	})

	t.Run("above 4 clamps to 4", func(t *testing.T) {
		r := &coOwnedRunner{}
		c := NewClientWithRunner(r)
		if err := c.SetPriority(context.Background(), "mr-1", 9); err != nil {
			t.Fatalf("SetPriority: %v", err)
		}
		got := r.lastCall()
		want := []string{"update", "mr-1", "-p", "4"}
		if len(got) != len(want) {
			t.Fatalf("call = %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("call = %v, want %v", got, want)
			}
		}
	})
}

// TestAddLabel asserts AddLabel sends `update <id> --add-label <label>` to bd.
// Used by the conflict->priority reconciler (pg2-tsgkj) to stash the baseline
// priority in a `pbase:<n>` label.
func TestAddLabel(t *testing.T) {
	r := &coOwnedRunner{}
	c := NewClientWithRunner(r)
	if err := c.AddLabel(context.Background(), "mr-1", "pbase:2"); err != nil {
		t.Fatalf("AddLabel: %v", err)
	}
	got := r.lastCall()
	want := []string{"update", "mr-1", "--add-label", "pbase:2"}
	if len(got) != len(want) {
		t.Fatalf("call = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("call = %v, want %v", got, want)
		}
	}
}

// TestRemoveLabel asserts RemoveLabel sends `update <id> --remove-label
// <label>` to bd. Used by the conflict->priority reconciler (pg2-tsgkj) to
// drop the `pbase:<n>` marker once the baseline priority is restored.
func TestRemoveLabel(t *testing.T) {
	r := &coOwnedRunner{}
	c := NewClientWithRunner(r)
	if err := c.RemoveLabel(context.Background(), "mr-1", "pbase:2"); err != nil {
		t.Fatalf("RemoveLabel: %v", err)
	}
	got := r.lastCall()
	want := []string{"update", "mr-1", "--remove-label", "pbase:2"}
	if len(got) != len(want) {
		t.Fatalf("call = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("call = %v, want %v", got, want)
		}
	}
}

// TestBdIssueToMergeRequest_ParsesPriorityAndLabels pins the fix for
// bdIssueToMergeRequest previously dropping labels entirely and not
// surfacing priority at all.
func TestBdIssueToMergeRequest_ParsesPriorityAndLabels(t *testing.T) {
	iss := bdIssue{
		ID: "bd-2", Priority: 3, Labels: []string{"co-owned", "pbase:2"},
		Metadata: map[string]any{"repo": "o/r", "pr_number": float64(5)},
	}
	mr := bdIssueToMergeRequest(iss)
	if mr.Priority != 3 {
		t.Errorf("Priority = %d, want 3", mr.Priority)
	}
	if len(mr.Labels) != 2 {
		t.Errorf("Labels = %v, want 2", mr.Labels)
	}
	if mr.Labels[0] != "co-owned" || mr.Labels[1] != "pbase:2" {
		t.Errorf("Labels = %v, want [co-owned pbase:2]", mr.Labels)
	}
}

// TestSetPriority_RoundTripsThroughRealBD guards the json:"priority" contract
// against a real bd workspace: it creates a bead, sets its priority via
// SetPriority (which shells out to `bd update -p`), then reads it back via
// both GetMergeRequest and ListMergeRequests to confirm Priority survives the
// bd list --json round trip (a regression the unit tests above — which
// construct bdIssue directly and never touch JSON — cannot catch).
func TestSetPriority_RoundTripsThroughRealBD(t *testing.T) {
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

// TestNewClientForRepo_HitsRepoWorkspace creates two real bd workspaces in
// distinct temp dirs and verifies that NewClientForRepo(dirA) writes to
// workspace A only — beads created on the A-scoped client are not visible
// from the B-scoped client.
func TestNewClientForRepo_HitsRepoWorkspace(t *testing.T) {
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
// Diff-before-write (write-amplification elimination: pg2-ojqz5 FB-1/2/4)
// ----------------------------------------------------------------------

// mrDiffRunner is a fake Runner for the diff-before-write proofs. It returns a
// canned bd-list envelope for any READ (`bd list ...`) so the diff logic has a
// current stored state to compare against, and records EVERY call so a test can
// assert whether a WRITE (each of which, against real bd, produces a Dolt
// commit) was issued. Distinct from coOwnedRunner, which returns "" for every
// call (i.e. "bead not found") and so cannot exercise the already-in-desired-
// state skip path.
type mrDiffRunner struct {
	listJSON string
	calls    [][]string
}

func (r *mrDiffRunner) Run(_ context.Context, args ...string) (string, error) {
	r.calls = append(r.calls, append([]string(nil), args...))
	if len(args) > 0 && args[0] == "list" {
		return r.listJSON, nil
	}
	return "", nil
}

// writeCalls returns only the recorded calls that MUTATE bd state — the ones
// that, against real bd, produce a Dolt commit. Reads (`list`) are excluded.
func (r *mrDiffRunner) writeCalls() [][]string {
	var w [][]string
	for _, c := range r.calls {
		if len(c) == 0 {
			continue
		}
		switch c[0] {
		case "update", "create", "close", "dep":
			w = append(w, c)
		}
	}
	return w
}

// cannedList marshals issues into the bd 1.0.4+ list envelope parseBDList reads.
func cannedList(t *testing.T, issues ...bdIssue) string {
	t.Helper()
	env := struct {
		Data          []bdIssue `json:"data"`
		SchemaVersion int       `json:"schema_version"`
	}{Data: issues, SchemaVersion: 1}
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal canned list: %v", err)
	}
	return string(b)
}

// storedMR is the current-state bead the daemon re-observes each refresh: an
// open merge-request whose metadata already reflects the upstream PR.
func storedMR() bdIssue {
	return bdIssue{
		ID: "mr-1", Title: "foo/bar#7", Status: "open", Type: "merge-request",
		Metadata: map[string]any{
			"repo":           "foo/bar",
			"pr_number":      float64(7),
			"state":          "open",
			"branch":         "feat/x",
			"base":           "main",
			"author":         "alice",
			"url":            "https://github.com/foo/bar/pull/7",
			"last_synced_at": "2020-01-01T00:00:00Z",
		},
	}
}

// TestEnsureMergeRequest_NoOpDoesNotWrite is the core FB-1/FB-2 proof: a refresh
// whose ONLY delta from the stored bead is a fresh last_synced_at (exactly what
// the per-minute daemon produces) issues NO bd write/commit — killing the 428k
// no-op 'nothing to commit' commits.
func TestEnsureMergeRequest_NoOpDoesNotWrite(t *testing.T) {
	ctx := context.Background()
	r := &mrDiffRunner{listJSON: cannedList(t, storedMR())}
	c := NewClientWithRunner(r)

	id, alreadyClosed, err := c.EnsureMergeRequest(ctx, "foo/bar#7", MergeRequestFields{
		Repo: "foo/bar", PRNumber: 7, State: "open", Branch: "feat/x", Base: "main",
		Author: "alice", URL: "https://github.com/foo/bar/pull/7",
		LastSyncedAt: "2026-07-25T12:00:00Z", // the ONLY "change": a new poll timestamp
	})
	if err != nil {
		t.Fatalf("EnsureMergeRequest: %v", err)
	}
	if id != "mr-1" || alreadyClosed {
		t.Fatalf("id=%q alreadyClosed=%v, want mr-1/false", id, alreadyClosed)
	}
	if w := r.writeCalls(); len(w) != 0 {
		t.Fatalf("expected ZERO bd writes for a last_synced_at-only refresh, got %v", w)
	}
}

// TestEnsureMergeRequest_RealChangeWritesOnce proves a refresh that changes a
// REAL field (state open->ready) still issues exactly one bd update/commit, and
// that the fresh last_synced_at rides along with it.
func TestEnsureMergeRequest_RealChangeWritesOnce(t *testing.T) {
	ctx := context.Background()
	r := &mrDiffRunner{listJSON: cannedList(t, storedMR())}
	c := NewClientWithRunner(r)

	if _, _, err := c.EnsureMergeRequest(ctx, "foo/bar#7", MergeRequestFields{
		Repo: "foo/bar", PRNumber: 7, State: "ready", // real change
		LastSyncedAt: "2026-07-25T12:00:00Z",
	}); err != nil {
		t.Fatalf("EnsureMergeRequest: %v", err)
	}
	w := r.writeCalls()
	if len(w) != 1 {
		t.Fatalf("expected exactly one bd write on a real change, got %d: %v", len(w), w)
	}
	if w[0][0] != "update" || w[0][1] != "mr-1" {
		t.Fatalf("expected `update mr-1 --metadata ...`, got %v", w[0])
	}
}

// TestSetMergeRequestCoOwned_SkipsWhenAlreadyInDesiredState proves the daemon's
// per-tick co-owned re-assertion issues NO bd write when the label already
// matches the desired state (FB-4). The current labels are read from the
// scripted list; the diff then suppresses the redundant add/remove-label.
func TestSetMergeRequestCoOwned_SkipsWhenAlreadyInDesiredState(t *testing.T) {
	ctx := context.Background()

	t.Run("label present, desired co-owned -> no write", func(t *testing.T) {
		iss := storedMR()
		iss.Labels = []string{"co-owned"}
		r := &mrDiffRunner{listJSON: cannedList(t, iss)}
		c := NewClientWithRunner(r)
		if err := c.SetMergeRequestCoOwned(ctx, "mr-1", true); err != nil {
			t.Fatalf("SetMergeRequestCoOwned: %v", err)
		}
		if w := r.writeCalls(); len(w) != 0 {
			t.Fatalf("expected no write when already co-owned, got %v", w)
		}
	})

	t.Run("label absent, desired not-co-owned -> no write", func(t *testing.T) {
		r := &mrDiffRunner{listJSON: cannedList(t, storedMR())} // no labels
		c := NewClientWithRunner(r)
		if err := c.SetMergeRequestCoOwned(ctx, "mr-1", false); err != nil {
			t.Fatalf("SetMergeRequestCoOwned: %v", err)
		}
		if w := r.writeCalls(); len(w) != 0 {
			t.Fatalf("expected no write when already not-co-owned, got %v", w)
		}
	})
}

// TestSetMergeRequestCoOwned_WritesOnChange proves a genuine co-owned transition
// still issues exactly one add/remove-label write.
func TestSetMergeRequestCoOwned_WritesOnChange(t *testing.T) {
	ctx := context.Background()

	t.Run("label absent, desired co-owned -> add-label", func(t *testing.T) {
		r := &mrDiffRunner{listJSON: cannedList(t, storedMR())}
		c := NewClientWithRunner(r)
		if err := c.SetMergeRequestCoOwned(ctx, "mr-1", true); err != nil {
			t.Fatalf("SetMergeRequestCoOwned: %v", err)
		}
		w := r.writeCalls()
		if len(w) != 1 || w[0][2] != "--add-label" || w[0][3] != "co-owned" {
			t.Fatalf("expected one `update mr-1 --add-label co-owned`, got %v", w)
		}
	})

	t.Run("label present, desired not-co-owned -> remove-label", func(t *testing.T) {
		iss := storedMR()
		iss.Labels = []string{"co-owned"}
		r := &mrDiffRunner{listJSON: cannedList(t, iss)}
		c := NewClientWithRunner(r)
		if err := c.SetMergeRequestCoOwned(ctx, "mr-1", false); err != nil {
			t.Fatalf("SetMergeRequestCoOwned: %v", err)
		}
		w := r.writeCalls()
		if len(w) != 1 || w[0][2] != "--remove-label" || w[0][3] != "co-owned" {
			t.Fatalf("expected one `update mr-1 --remove-label co-owned`, got %v", w)
		}
	})
}

// ---------------------------------------------------------------------------
// ReconcileMergeRequest: read-once + single-write projection (pg2-pz7y8).
// ---------------------------------------------------------------------------

// reconcileHarness runs the exact two-call sequence beadsbridge.Handler.project
// runs for one pr.opened/updated tick: ONE fresh read (FindByRepoAndNumberUncached),
// then ONE combined create-or-update (ReconcileMergeRequest). Tests below
// exercise this pair together, then inspect the recording runner's calls to
// prove the read/write counts and the combined args.
func reconcileHarness(ctx context.Context, c *Client, repo string, pr int, userTitle string, fields MergeRequestFields, coOwned, hasConflict, actsAsMine bool) (string, bool, error) {
	existing, err := c.FindByRepoAndNumberUncached(ctx, repo, pr)
	if err != nil {
		return "", false, err
	}
	return c.ReconcileMergeRequest(ctx, existing, userTitle, fields, coOwned, hasConflict, actsAsMine)
}

// splitReadsWrites partitions recorded runner calls into `list` reads and
// every other (mutating) verb.
func splitReadsWrites(calls [][]string) (reads, writes int) {
	for _, c := range calls {
		if len(c) == 0 {
			continue
		}
		if c[0] == "list" {
			reads++
		} else {
			writes++
		}
	}
	return reads, writes
}

// mrDiffCreateRunner is mrDiffRunner plus a fixed ID returned for `create`
// calls, so ReconcileMergeRequest's not-found (create) path can be exercised
// without a real bd workspace.
type mrDiffCreateRunner struct {
	mrDiffRunner
	createID string
}

func (r *mrDiffCreateRunner) Run(ctx context.Context, args ...string) (string, error) {
	out, err := r.mrDiffRunner.Run(ctx, args...)
	if len(args) > 0 && args[0] == "create" {
		return r.createID, err
	}
	return out, err
}

// TestReconcileMergeRequest_NoOpDoesNotWrite proves a refresh whose fields,
// co-owned state, and conflict state ALL already match stored state issues
// exactly ONE read and ZERO writes — the read-once/write-once no-op case is
// now cheaper than before the refactor (previously a no-change tick still
// spent a SECOND read via the old GetMergeRequestUncached).
func TestReconcileMergeRequest_NoOpDoesNotWrite(t *testing.T) {
	ctx := context.Background()
	r := &mrDiffRunner{listJSON: cannedList(t, storedMR())}
	c := NewClientWithRunner(r)

	id, alreadyClosed, err := reconcileHarness(ctx, c, "foo/bar", 7, "foo/bar#7", MergeRequestFields{
		Repo: "foo/bar", PRNumber: 7, State: "open", Branch: "feat/x", Base: "main",
		Author: "alice", URL: "https://github.com/foo/bar/pull/7",
		LastSyncedAt: "2026-07-25T12:00:00Z", // FB-1: last_synced_at-only is not a change
	}, false, false, true) // not co-owned, no conflict — matches stored (no labels)
	if err != nil {
		t.Fatalf("reconcileHarness: %v", err)
	}
	if id != "mr-1" || alreadyClosed {
		t.Fatalf("id=%q alreadyClosed=%v, want mr-1/false", id, alreadyClosed)
	}
	reads, writes := splitReadsWrites(r.calls)
	if reads != 1 {
		t.Fatalf("expected exactly 1 read, got %d: %v", reads, r.calls)
	}
	if writes != 0 {
		t.Fatalf("expected ZERO writes for a full no-op tick, got %d: %v", writes, r.calls)
	}
}

// TestReconcileMergeRequest_ClosedBeadNoWrites proves an existing CLOSED bead
// short-circuits BEFORE any field/co-owned/priority diff is even attempted —
// not merely that the diffs happen to no-op. This is the precise contract
// TestPROpenedClosedParentSkipsDraftReview (internal/beadsbridge) used to pin
// at the bridge-fake level; it now lives here, where the short-circuit
// actually executes.
func TestReconcileMergeRequest_ClosedBeadNoWrites(t *testing.T) {
	ctx := context.Background()
	iss := storedMR()
	iss.Status = "closed"
	r := &mrDiffRunner{listJSON: cannedList(t, iss)}
	c := NewClientWithRunner(r)

	// coOwned=true and hasConflict=true would, on an OPEN bead, both demand a
	// write. On a closed bead neither may be attempted.
	id, alreadyClosed, err := reconcileHarness(ctx, c, "foo/bar", 7, "foo/bar#7", MergeRequestFields{
		Repo: "foo/bar", PRNumber: 7, State: "ready",
	}, true, true, true)
	if err != nil {
		t.Fatalf("reconcileHarness: %v", err)
	}
	if !alreadyClosed || id != "mr-1" {
		t.Fatalf("id=%q alreadyClosed=%v, want mr-1/true", id, alreadyClosed)
	}
	_, writes := splitReadsWrites(r.calls)
	if writes != 0 {
		t.Fatalf("expected ZERO writes for a closed bead, got %d: %v", writes, r.calls)
	}
}

// TestReconcileMergeRequest_CombinedChangeSingleWrite proves a tick that needs
// ALL THREE mutations at once — a real field change, a co-owned label flip,
// and a first-conflict priority nudge — still issues exactly ONE combined
// `bd update` call, not three separate ones.
func TestReconcileMergeRequest_CombinedChangeSingleWrite(t *testing.T) {
	ctx := context.Background()
	iss := storedMR()
	iss.Priority = 2 // no labels yet: not co-owned, no pbase baseline
	r := &mrDiffRunner{listJSON: cannedList(t, iss)}
	c := NewClientWithRunner(r)

	id, alreadyClosed, err := reconcileHarness(ctx, c, "foo/bar", 7, "foo/bar#7", MergeRequestFields{
		Repo: "foo/bar", PRNumber: 7, State: "ready", // real field change (open -> ready)
	}, true, true, true) // desired co-owned=true, conflict=true, mine
	if err != nil {
		t.Fatalf("reconcileHarness: %v", err)
	}
	if alreadyClosed || id != "mr-1" {
		t.Fatalf("id=%q alreadyClosed=%v, want mr-1/false", id, alreadyClosed)
	}
	reads, writes := splitReadsWrites(r.calls)
	if reads != 1 || writes != 1 {
		t.Fatalf("expected 1 read + 1 combined write, got reads=%d writes=%d: %v", reads, writes, r.calls)
	}
	w := r.writeCalls()
	joined := strings.Join(w[0], " ")
	if w[0][0] != "update" || w[0][1] != "mr-1" {
		t.Fatalf("expected `update mr-1 ...`, got %v", w[0])
	}
	if !strings.Contains(joined, "--metadata") {
		t.Fatalf("expected the field change folded into the combined write, got %v", w[0])
	}
	if !strings.Contains(joined, "--add-label pbase:2") {
		t.Fatalf("expected the pbase baseline stash folded into the combined write, got %v", w[0])
	}
	if !strings.Contains(joined, "--add-label co-owned") {
		t.Fatalf("expected the co-owned label folded into the combined write, got %v", w[0])
	}
	if !strings.Contains(joined, "-p 1") {
		t.Fatalf("expected the nudged priority (2 -> 1, mine conflict) folded into the combined write, got %v", w[0])
	}
}

// TestReconcileMergeRequest_CreatesWhenAbsent proves the not-found path issues
// exactly ONE `bd create` call, with a conflict-nudged priority and a co-owned
// label already folded in when the very first tick already carries them —
// reproducing what a subsequent read of the freshly created bead would show,
// without spending that read (see bdDefaultPriority's doc).
func TestReconcileMergeRequest_CreatesWhenAbsent(t *testing.T) {
	ctx := context.Background()
	r := &mrDiffCreateRunner{mrDiffRunner: mrDiffRunner{listJSON: cannedList(t)}, createID: "mr-new-1"}
	c := NewClientWithRunner(r)

	id, alreadyClosed, err := reconcileHarness(ctx, c, "foo/bar", 8, "foo/bar#8", MergeRequestFields{
		Repo: "foo/bar", PRNumber: 8, State: "open",
	}, true, true, true) // co-owned + conflict already true on the first tick
	if err != nil {
		t.Fatalf("reconcileHarness: %v", err)
	}
	if alreadyClosed {
		t.Fatalf("expected alreadyClosed=false on create")
	}
	if id != "mr-new-1" {
		t.Fatalf("id=%q, want mr-new-1", id)
	}
	reads, writes := splitReadsWrites(r.calls)
	if reads != 1 || writes != 1 {
		t.Fatalf("expected 1 read + 1 create, got reads=%d writes=%d: %v", reads, writes, r.calls)
	}
	create := r.calls[len(r.calls)-1]
	if create[0] != "create" {
		t.Fatalf("expected a create call, got %v", create)
	}
	joined := strings.Join(create, " ")
	if !strings.Contains(joined, "-l pbase:2,co-owned") {
		t.Fatalf("expected combined labels (pbase baseline + co-owned) in the create call, got %v", create)
	}
	if !strings.Contains(joined, "-p 1") {
		t.Fatalf("expected the nudged priority (bdDefaultPriority=2 -> 1, mine conflict) in the create call, got %v", create)
	}
}

// TestMergeRequestPriorityDelta pins the pure conflict-priority decision
// (relocated verbatim from internal/beadsbridge/bridge.go's former
// reconcilePriority — see pg2-pz7y8) across its four cases: mine raises and
// stashes, team lowers and stashes, a repeated conflicting tick is a no-op,
// and a clear restores the exact baseline.
func TestMergeRequestPriorityDelta(t *testing.T) {
	cases := []struct {
		name                string
		curPriority         int
		curLabels           []string
		actsAsMine          bool
		hasConflict         bool
		wantAdd, wantRemove []string
		wantPriority        int
		wantSetPriority     bool
	}{
		{
			name:        "mine first conflict raises and stashes baseline",
			curPriority: 2, curLabels: nil, actsAsMine: true, hasConflict: true,
			wantAdd: []string{"pbase:2"}, wantPriority: 1, wantSetPriority: true,
		},
		{
			name:        "team first conflict lowers and stashes baseline",
			curPriority: 2, curLabels: nil, actsAsMine: false, hasConflict: true,
			wantAdd: []string{"pbase:2"}, wantPriority: 3, wantSetPriority: true,
		},
		{
			name:        "repeated conflict is idempotent no-op",
			curPriority: 1, curLabels: []string{"pbase:2"}, actsAsMine: true, hasConflict: true,
			wantSetPriority: false,
		},
		{
			name:        "conflict cleared restores exact baseline",
			curPriority: 1, curLabels: []string{"pbase:2"}, actsAsMine: true, hasConflict: false,
			wantRemove: []string{"pbase:2"}, wantPriority: 2, wantSetPriority: true,
		},
		{
			name:        "no conflict no baseline is a no-op",
			curPriority: 2, curLabels: nil, actsAsMine: true, hasConflict: false,
			wantSetPriority: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			addLabels, removeLabels, priority, setPriority := mergeRequestPriorityDelta(tc.curPriority, tc.curLabels, tc.actsAsMine, tc.hasConflict)
			if !equalStringSlices(addLabels, tc.wantAdd) {
				t.Errorf("addLabels: got %v want %v", addLabels, tc.wantAdd)
			}
			if !equalStringSlices(removeLabels, tc.wantRemove) {
				t.Errorf("removeLabels: got %v want %v", removeLabels, tc.wantRemove)
			}
			if setPriority != tc.wantSetPriority {
				t.Errorf("setPriority: got %v want %v", setPriority, tc.wantSetPriority)
			}
			if setPriority && priority != tc.wantPriority {
				t.Errorf("priority: got %d want %d", priority, tc.wantPriority)
			}
		})
	}
}

// equalStringSlices compares two string slices treating nil and empty as equal.
func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
