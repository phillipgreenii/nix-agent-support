package repo_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-gwm/internal/gitrun/gitruntest"
	"github.com/phillipgreenii/pg-gwm/internal/repo"
	"github.com/phillipgreenii/x/gitfixture"
	"github.com/phillipgreenii/x/gittest"
)

func TestResolveSameRepoFromEverywhere(t *testing.T) {
	for _, sep := range []string{"", "gitdirs/app.git"} {
		t.Run("sep="+sep, func(t *testing.T) {
			r := gittest.New(t, gitfixture.RepoOptions{InitialCommit: true, SeparateGitDir: sep})
			g := gitruntest.New(t, r)
			ctx := context.Background()
			wt := filepath.Join(r.Root(), "linked")
			if _, err := r.Client.Run(ctx, "worktree", "add", "-b", "feat", wt); err != nil {
				t.Fatal(err)
			}
			sub := filepath.Join(r.Dir, "a", "b")
			wsub := filepath.Join(wt, "c")
			for _, d := range []string{sub, wsub} {
				if err := os.MkdirAll(d, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			// git lists the git directory itself as the main worktree when
			// the repository was made with --separate-git-dir (it records no
			// checkout path), so that is the Main every path agrees on.
			wantMain := r.Dir
			if sep != "" {
				wantMain = filepath.Join(r.Root(), sep)
			}
			var first repo.Repo
			for i, p := range []string{r.Dir, wt, sub, wsub} {
				got, err := repo.Resolve(ctx, g, p)
				if err != nil {
					t.Fatalf("%s: %v", p, err)
				}
				if got.Main != wantMain {
					t.Fatalf("Main = %q, want %q", got.Main, wantMain)
				}
				if i == 0 {
					first = got
				} else if got != first {
					t.Fatalf("%s: %+v != %+v", p, got, first)
				}
			}
			if first.Name != filepath.Base(wantMain) || !strings.HasPrefix(first.Label, first.Name+"-") || len(first.Label) != len(first.Name)+1+6 {
				t.Fatalf("repo = %+v", first)
			}
		})
	}
}

func TestResolveOutsideRepo(t *testing.T) {
	r := gittest.New(t, gitfixture.RepoOptions{InitialCommit: true})
	g := gitruntest.New(t, r)
	_, err := repo.Resolve(context.Background(), g, r.Root())
	if !errors.Is(err, repo.ErrNotARepo) {
		t.Fatalf("err = %v", err)
	}
}

func TestResolveDeletedDir(t *testing.T) {
	r := gittest.New(t, gitfixture.RepoOptions{InitialCommit: true})
	g := gitruntest.New(t, r)
	gone := filepath.Join(r.Dir, "gone")
	_, err := repo.Resolve(context.Background(), g, gone)
	if !errors.Is(err, repo.ErrCwdGone) {
		t.Fatalf("err = %v", err)
	}
	if msg := err.Error(); !strings.Contains(msg, "--repo") || !strings.Contains(msg, "pg-gwm preserved list") {
		t.Fatalf("hint missing: %v", msg)
	}
}

func TestLabelDiffersForSameName(t *testing.T) {
	a := gittest.New(t, gitfixture.RepoOptions{Name: "app", InitialCommit: true})
	b := gittest.New(t, gitfixture.RepoOptions{Name: "app", InitialCommit: true})
	ra, err := repo.Resolve(context.Background(), gitruntest.New(t, a), a.Dir)
	if err != nil {
		t.Fatal(err)
	}
	rb, err := repo.Resolve(context.Background(), gitruntest.New(t, b), b.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if ra.Name != rb.Name || ra.Label == rb.Label {
		t.Fatalf("a=%+v b=%+v", ra, rb)
	}
}
