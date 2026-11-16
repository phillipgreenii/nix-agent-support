package state_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-gwm/internal/gitrun/gitruntest"
	"github.com/phillipgreenii/pg-gwm/internal/repo"
	"github.com/phillipgreenii/pg-gwm/internal/state"
	"github.com/phillipgreenii/x/gitfixture"
	"github.com/phillipgreenii/x/gittest"
)

// TestStateSharedAcrossCheckouts: every checkout of a repository resolves the
// same common git directory, so every one reads and writes one state.json, and
// git's own housekeeping leaves it alone.
func TestStateSharedAcrossCheckouts(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	r := gittest.New(t, gitfixture.RepoOptions{InitialCommit: true})
	g := gitruntest.New(t, r)
	ctx := context.Background()
	managed := filepath.Join(r.Root(), "managed")
	unmanaged := filepath.Join(r.Root(), "unmanaged")
	for _, a := range [][]string{
		{"worktree", "add", "--detach", managed},
		{"worktree", "lock", "--reason", "pg-gwm w-aaaaaaaaaa", managed},
		{"worktree", "add", "-b", "feat", unmanaged},
	} {
		if _, err := r.Client.Run(ctx, a...); err != nil {
			t.Fatal(err)
		}
	}
	var stores []state.Store
	for _, p := range []string{r.Dir, managed, unmanaged} {
		rp, err := repo.Resolve(ctx, g, p)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		stores = append(stores, state.Open(rp.CommonDir))
	}
	for _, s := range stores[1:] {
		if s.Dir != stores[0].Dir {
			t.Fatalf("state dir %q != %q", s.Dir, stores[0].Dir)
		}
	}
	// Write from the unmanaged worktree, read from the others.
	l, err := stores[2].Lock(ctx, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	l.State().Worktrees = []state.Worktree{{ID: "w-aaaaaaaaaa", Path: managed, Status: state.Available}}
	if err := l.Commit(); err != nil {
		t.Fatal(err)
	}
	l.Unlock()
	check := func(when string) {
		t.Helper()
		for i, s := range stores {
			st, err := s.ReadUnlocked()
			if err != nil || len(st.Worktrees) != 1 || st.Worktrees[0].ID != "w-aaaaaaaaaa" {
				t.Fatalf("%s: store %d reads %+v, %v", when, i, st, err)
			}
		}
	}
	check("after write")
	before, err := os.ReadFile(filepath.Join(stores[0].Dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range [][]string{{"gc", "--prune=now"}, {"worktree", "prune"}} {
		if _, err := r.Client.Run(ctx, a...); err != nil {
			t.Fatal(err)
		}
	}
	check("after gc and prune")
	after, _ := os.ReadFile(filepath.Join(stores[0].Dir, "state.json"))
	if string(before) != string(after) {
		t.Fatal("state.json changed")
	}
}
