package parknote

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type fakeGit struct {
	out   map[string]string
	calls []string
}

func (g *fakeGit) Output(_ context.Context, dir string, args ...string) (string, error) {
	k := strings.Join(args, " ")
	g.calls = append(g.calls, dir+": "+k)
	if v, ok := g.out[k]; ok {
		return v, nil
	}
	return "", errors.New("unscripted: " + k)
}

func baseInput(g Git) Input {
	return Input{
		Trigger: "session died before completing", Detail: []string{"ccpool close reason: handler"},
		Session: "ext-1", Display: "pool-drain-b-1", Role: "drain", Bead: "b-1",
		Dirs: []string{"/repo", "/repo", ""}, Git: g,
	}
}

func TestBuild_everyRequiredElement(t *testing.T) {
	g := &fakeGit{out: map[string]string{
		"worktree list --porcelain":                        "worktree /repo\nHEAD a\nbranch refs/heads/main\n\nworktree /repo/.worktrees/b-1\nHEAD b\nbranch refs/heads/drain/b-1\n\nworktree /repo/.worktrees/other\nHEAD c\nbranch refs/heads/drain/other\n",
		"config --get pgii-integrate-branch.primaryBranch": "trunk\n",
		"rev-list --count trunk..drain/b-1":                "2\n",
	}}
	note := Build(context.Background(), baseInput(g))
	for _, want := range []string{
		Marker, "Trigger: session died before completing", "ccpool close reason: handler",
		"Session: ext-1 (pool-drain-b-1), role drain",
		"worktree /repo/.worktrees/b-1 (branch drain/b-1): 2 commit(s) ahead of trunk",
		"No person-question was identified", "resume-or-land",
	} {
		if !strings.Contains(note, want) {
			t.Errorf("missing %q in:\n%s", want, note)
		}
	}
	if strings.Contains(note, "drain/other") {
		t.Errorf("another bead's worktree must not be reported:\n%s", note)
	}
	// "/repo" listed twice and "" once: searched once.
	n := 0
	for _, c := range g.calls {
		if strings.HasSuffix(c, "worktree list --porcelain") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("duplicate and empty dirs must be skipped; worktree list ran %d times", n)
	}
}

func TestBuild_zeroAheadAndBranchWithoutWorktree(t *testing.T) {
	g := &fakeGit{out: map[string]string{
		"worktree list --porcelain":                        "worktree /repo\nHEAD a\nbranch refs/heads/main\n",
		"rev-parse --verify --quiet refs/heads/drain/b-1":  "x\n",
		"config --get pgii-integrate-branch.primaryBranch": "",
		"symbolic-ref --short refs/remotes/origin/HEAD":    "origin/main\n",
		"rev-list --count main..drain/b-1":                 "0\n",
	}}
	note := Build(context.Background(), baseInput(g))
	if !strings.Contains(note, "branch drain/b-1 (no worktree): no commits ahead of main") {
		t.Errorf("a branch with no worktree and no commits:\n%s", note)
	}
}

func TestBuild_nothingFound(t *testing.T) {
	g := &fakeGit{out: map[string]string{"worktree list --porcelain": "worktree /repo\nHEAD a\nbranch refs/heads/main\n"}}
	note := Build(context.Background(), baseInput(g))
	if !strings.Contains(note, "none found for b-1 (searched: /repo)") || !strings.Contains(note, "No person-question was identified") {
		t.Errorf("an empty search must be stated, and the note still complete:\n%s", note)
	}
}

func TestBuild_gitFailureDegrades(t *testing.T) {
	note := Build(context.Background(), baseInput(&fakeGit{}))
	if !strings.Contains(note, "not determined") || !strings.Contains(note, "No person-question was identified") {
		t.Errorf("a failing git must degrade the note, not drop it:\n%s", note)
	}
}

type hangGit struct{}

func (hangGit) Output(ctx context.Context, _ string, _ ...string) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

// A git that never answers cannot hold the park: each call is bounded, and so
// is the whole discovery.
func TestBuild_hangingGitIsBounded(t *testing.T) {
	oldG, oldD := gitTimeout, discoverTimeout
	gitTimeout, discoverTimeout = 50*time.Millisecond, 120*time.Millisecond
	defer func() { gitTimeout, discoverTimeout = oldG, oldD }()
	in := baseInput(hangGit{})
	in.Dirs = []string{"/a", "/b", "/c", "/d"}
	start := time.Now()
	note := Build(context.Background(), in)
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("discovery took %s with a hanging git", d)
	}
	if !strings.Contains(note, "No person-question was identified") {
		t.Errorf("note incomplete:\n%s", note)
	}
}

func TestBuild_noBeadID(t *testing.T) {
	in := baseInput(&fakeGit{})
	in.Bead = ""
	if note := Build(context.Background(), in); !strings.Contains(note, "no bead id") {
		t.Errorf("got:\n%s", note)
	}
}
