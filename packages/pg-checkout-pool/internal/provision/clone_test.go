package provision

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// skipUnlessCloneable skips where directory clonefile is not available (Linux,
// or a volume without clone support).
func skipUnlessCloneable(t *testing.T) {
	t.Helper()
	base := t.TempDir()
	if err := os.Mkdir(filepath.Join(base, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := cloneDir(filepath.Join(base, "src"), filepath.Join(base, "dst")); errors.Is(err, ErrCloneUnsupported) {
		t.Skipf("directory clone unsupported here: %v", err)
	} else if err != nil {
		t.Fatal(err)
	}
}

func newCloneProv(t *testing.T, cfg WorktreeConfig) Provisioner {
	c, err := NewClone(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestCloneProvisioner(t *testing.T) {
	skipUnlessCloneable(t)
	runProvisionerSuite(t, newCloneProv)
}

func TestCloneIsIndependentRepository(t *testing.T) {
	skipUnlessCloneable(t)
	f := newFixture(t)
	c := newCloneProv(t, WorktreeConfig{Repo: f.repo, Env: hermeticEnv})
	ctx := context.Background()
	if err := c.Create(ctx, f.slot, f.c1); err != nil {
		t.Fatal(err)
	}
	if got := run(t, f.slot, "rev-parse", "--git-common-dir"); got != ".git" {
		t.Errorf("slot git dir %q, want its own .git", got)
	}
	if got := run(t, f.slot, "config", "core.checkStat"); got != "minimal" {
		t.Errorf("checkStat %q", got)
	}
	// The same branch can be checked out in the golden and a slot at once.
	if _, err := c.Reset(ctx, f.slot, f.c2, "main"); err != nil {
		t.Fatal(err)
	}
	if got := run(t, f.repo, "branch", "--show-current"); got != "main" {
		t.Errorf("golden moved to %q", got)
	}
	// A commit made in the golden after the clone is fetched on demand.
	write(t, filepath.Join(f.repo, "late.txt"), "late\n")
	run(t, f.repo, "add", ".")
	run(t, f.repo, "commit", "-q", "-m", "c3")
	c3 := run(t, f.repo, "rev-parse", "HEAD")
	if _, err := c.Reset(ctx, f.slot, c3, "lease/s1"); err != nil {
		t.Fatalf("reset to later golden commit: %v", err)
	}
	if !exists(filepath.Join(f.slot, "late.txt")) {
		t.Error("late.txt missing after reset to c3")
	}
	if err := c.Create(ctx, f.slot, f.c1); err == nil {
		t.Error("create over an existing slot succeeded")
	}
}
