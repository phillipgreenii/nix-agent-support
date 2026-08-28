package worktree

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/x/gitclient"
)

// recWTM records every CreateWorktree call a fake Opener's client receives.
type recWTM struct {
	calls []struct {
		path, branch string
		opts         gitclient.CreateWorktreeOptions
	}
}

func (m *recWTM) CreateWorktree(_ context.Context, path, branch string, opts gitclient.CreateWorktreeOptions) error {
	m.calls = append(m.calls, struct {
		path, branch string
		opts         gitclient.CreateWorktreeOptions
	}{path, branch, opts})
	return nil
}

func (m *recWTM) RemoveWorktree(context.Context, string, bool) error { return nil }
func (m *recWTM) PruneWorktrees(context.Context) error               { return nil }

// recOpener is a fake Opener: it reports gitclient.ErrNotARepository for any
// dir in missingAt, and otherwise succeeds, returning the shared recWTM so
// CreateWorktree calls are observable.
type recOpener struct {
	calls     []string
	missingAt map[string]bool
	wtm       *recWTM
}

func newRecOpener(missingAt map[string]bool) *recOpener {
	return &recOpener{missingAt: missingAt, wtm: &recWTM{}}
}

func (o *recOpener) Open(_ context.Context, dir string) (gitclient.WorktreeManager, error) {
	o.calls = append(o.calls, dir)
	if o.missingAt[dir] {
		return nil, gitclient.ErrNotARepository
	}
	return o.wtm, nil
}

func TestEnsure_createsFreshPerBeadWorktree(t *testing.T) {
	wtDir := t.TempDir()
	want := filepath.Join(wtDir, "zr-6bq.3")
	o := newRecOpener(map[string]bool{want: true}) // the target path doesn't exist yet

	got, err := Ensure(context.Background(), o.Open, wtDir, "/repo", "zr-6bq.3")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
	if len(o.wtm.calls) != 1 {
		t.Fatalf("expected exactly one CreateWorktree call, got %d: %+v", len(o.wtm.calls), o.wtm.calls)
	}
	c := o.wtm.calls[0]
	if c.path != want || c.branch != "pr-pool/zr-6bq.3" || !c.opts.ResetBranch {
		t.Errorf("CreateWorktree(%q, %q, %+v); want (%q, %q, ResetBranch=true)", c.path, c.branch, c.opts, want, "pr-pool/zr-6bq.3")
	}
	// The create call must anchor at repoRoot, not the worktree path itself.
	if len(o.calls) < 2 || o.calls[1] != "/repo" {
		t.Errorf("expected the second open to anchor at repoRoot; opens=%v", o.calls)
	}
}

func TestEnsure_reusesExistingWorktree(t *testing.T) {
	wtDir := t.TempDir()
	path := filepath.Join(wtDir, "zr-1")
	o := newRecOpener(nil) // nothing missing: path probe succeeds ⇒ reuse

	got, err := Ensure(context.Background(), o.Open, wtDir, "/repo", "zr-1")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if got != path {
		t.Errorf("path = %q, want %q", got, path)
	}
	if len(o.wtm.calls) != 0 {
		t.Errorf("existing worktree must be reused, not re-added; calls=%+v", o.wtm.calls)
	}
	if len(o.calls) != 1 || o.calls[0] != path {
		t.Errorf("reuse path must open only the target path once; opens=%v", o.calls)
	}
}

func TestEnsure_probeErrorOtherThanNotARepositoryPropagates(t *testing.T) {
	wtDir := t.TempDir()
	sentinel := context.Canceled
	open := func(context.Context, string) (gitclient.WorktreeManager, error) {
		return nil, sentinel
	}

	_, err := Ensure(context.Background(), open, wtDir, "/repo", "zr-2")
	if err == nil {
		t.Fatal("expected an error to propagate rather than fall through to create")
	}
}
