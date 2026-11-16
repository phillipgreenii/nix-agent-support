package gittemplate

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/phillipgreenii/x/gitfixture"
)

var seeds int

var tpl = New("pb-gittemplate", func(ctx context.Context, r *gitfixture.Repo) error {
	seeds++
	_, err := r.Commit(ctx, "seed", map[string]string{"f.txt": "x\n"})
	return err
})

func TestMain(m *testing.M) {
	code := m.Run()
	tpl.Close()
	os.Exit(code)
}

func head(t *testing.T, r *gitfixture.Repo) string {
	t.Helper()
	out, err := r.Client.Run(t.Context(), "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

// Instances are real, independent repositories: a commit in one is invisible in
// another and in the template, the seed runs once, and both start at the same
// seeded commit.
func TestInstance_isIndependentRealRepositoryAndSeedsOnce(t *testing.T) {
	a := tpl.Instance(t)
	b := tpl.Instance(t)
	if a.Dir == b.Dir {
		t.Fatalf("instances share a directory %s", a.Dir)
	}
	seed := head(t, a)
	if head(t, b) != seed {
		t.Fatalf("instances start at different commits")
	}
	if _, err := a.Commit(t.Context(), "only in a", map[string]string{"g.txt": "y\n"}); err != nil {
		t.Fatal(err)
	}
	if head(t, a) == seed {
		t.Fatalf("commit did not advance instance a")
	}
	if got := head(t, b); got != seed {
		t.Fatalf("instance b moved to %s after a commit in a", got)
	}
	if got := head(t, tpl.repo); got != seed {
		t.Fatalf("template moved to %s after a commit in an instance", got)
	}
	if status, err := b.Client.Run(t.Context(), "status", "--porcelain"); err != nil || strings.TrimSpace(string(status)) != "" {
		t.Fatalf("instance b not clean: %q (%v)", status, err)
	}
	if seeds != 1 {
		t.Fatalf("seed ran %d times, want 1", seeds)
	}
}
