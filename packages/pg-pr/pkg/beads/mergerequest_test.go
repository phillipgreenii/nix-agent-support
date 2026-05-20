package beads

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
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
	initCmd := exec.Command("bd", "init", "--prefix", prefix)
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
	cmd := exec.Command("bd", "config", "set", key, val)
	cmd.Dir = dir
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Logf("bd config set output: %s", out)
		return err
	}
	return nil
}

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
	}
	if mr.Status != "open" {
		t.Fatalf("status: got %q want open", mr.Status)
	}
	if mr.Type != "merge-request" {
		t.Fatalf("type: got %q want merge-request", mr.Type)
	}
	if mr.Fields.Repo != "foo/bar" || mr.Fields.PRNumber != 7 {
		t.Fatalf("metadata: %+v", mr.Fields)
	}
	if mr.Fields.State != "open" {
		t.Fatalf("state metadata: got %q", mr.Fields.State)
	}
	if mr.Fields.LastSyncedAt == "" {
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
