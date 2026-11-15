package provision

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestWorktreeProvisioner(t *testing.T) {
	runProvisionerSuite(t, func(t *testing.T, cfg WorktreeConfig) Provisioner {
		w, err := NewWorktree(cfg)
		if err != nil {
			t.Fatal(err)
		}
		return w
	})
}

// A slot is registered with `git worktree lock` so git and every janitor that
// honours the lock leave it alone.
func TestWorktreeCreateLocksTheSlot(t *testing.T) {
	f := newFixture(t)
	w, err := NewWorktree(WorktreeConfig{Repo: f.repo, Env: hermeticEnv, Timeout: 20 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Create(context.Background(), f.slot, f.c1); err != nil {
		t.Fatal(err)
	}
	if list := run(t, f.repo, "worktree", "list", "--porcelain"); !strings.Contains(list, "locked "+LockReason) {
		t.Fatalf("slot worktree not locked with %q:\n%s", LockReason, list)
	}
	cmd := exec.CommandContext(t.Context(), "git", "-C", f.repo, "worktree", "remove", f.slot)
	cmd.Env = append(os.Environ(), hermeticEnv...)
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("git removed a locked slot: %s", out)
	}
}
