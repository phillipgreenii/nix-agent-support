package executor

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/ccpool"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/config"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/dtest"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/item"
)

// The park note (bead pg2-78mxs): every path that parks a bead as human with a
// release (the add-human death, and the second consecutive needs_input stall)
// writes one comment, through the same bd runner, BEFORE the label-and-release
// update. These tests drive both real paths with a scripted git so nothing
// touches a repository.

// parkGit answers `git -C dir args...` from a table keyed by the args.
type parkGit struct {
	out  map[string]string
	fail bool
}

func (g parkGit) Output(_ context.Context, _ string, args ...string) (string, error) {
	if g.fail {
		return "", errors.New("git unavailable")
	}
	if v, ok := g.out[strings.Join(args, " ")]; ok {
		return v, nil
	}
	return "", errors.New("unscripted git call: " + strings.Join(args, " "))
}

func drainGit(bead string) parkGit {
	return parkGit{out: map[string]string{
		"worktree list --porcelain": "worktree /repo\nHEAD aaa\nbranch refs/heads/main\n\n" +
			"worktree /repo/.worktrees/" + bead + "\nHEAD bbb\nbranch refs/heads/drain/" + bead + "\n\n",
		"config --get pgii-integrate-branch.primaryBranch": "main\n",
		"rev-list --count main..drain/" + bead:             "3\n",
		"status --porcelain":                               "",
	}}
}

func TestParkNote_deathNamesTheDrainWorktree(t *testing.T) {
	cfg := fastCfg()
	cfg.RepoRoot = "/repo"
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"bw-1": {"in_progress", "in_progress", "in_progress"}}}
	row := ccpool.Session{ExternalID: "pg-router-worker-bw-1", Live: false, State: ccpool.StateErrored, CloseReason: "handler"}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{row}, {row}, {row}}}
	e := newExec(cc, bd, cfg)
	e.deps.ParkGit = drainGit("bw-1")
	d := DispatchContext{Role: workerRole(cfg), Item: item.Item{ID: "bw-1"}}
	if err := e.waitDone(context.Background(), nil, d, "pg-router-worker-bw-1"); err == nil {
		t.Fatal("death must return an error")
	}
	if len(bd.Comments) != 1 {
		t.Fatalf("exactly one park note, got %q", bd.Comments)
	}
	note := bd.Comments[0]
	for _, want := range []string{
		"comment bw-1 Handler park note:",
		"session died before completing",
		"ccpool close reason: handler",
		"pg-router-worker-bw-1", "role worker",
		"worktree /repo/.worktrees/bw-1 (branch drain/bw-1)",
		"3 commit(s) ahead of main",
		"No person-question was identified",
		"resume-or-land",
	} {
		if !strings.Contains(note, want) {
			t.Errorf("the park note must contain %q; got:\n%s", want, note)
		}
	}
	if !dtest.HasUpdate(bd, "update bw-1 --add-label human --status=open --assignee=") {
		t.Errorf("the bead must still be parked and released; updates=%v", bd.Updates)
	}
}

// Discovery that fails is reported inside the note and never stops the park.
func TestParkNote_failingGitStillParks(t *testing.T) {
	cfg := fastCfg()
	cfg.RepoRoot = "/repo"
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"bw-1": {"in_progress", "in_progress", "in_progress"}}}
	row := ccpool.Session{ExternalID: "pg-router-worker-bw-1", Live: false, State: ccpool.StateErrored, CloseReason: "handler"}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{row}, {row}, {row}}}
	e := newExec(cc, bd, cfg)
	e.deps.ParkGit = parkGit{fail: true}
	d := DispatchContext{Role: workerRole(cfg), Item: item.Item{ID: "bw-1"}}
	if err := e.waitDone(context.Background(), nil, d, "pg-router-worker-bw-1"); err == nil {
		t.Fatal("death must return an error")
	}
	if len(bd.Comments) != 1 || !strings.Contains(bd.Comments[0], "not determined") {
		t.Fatalf("the note must say discovery failed; comments=%q", bd.Comments)
	}
	if !dtest.HasUpdate(bd, "update bw-1 --add-label human --status=open --assignee=") {
		t.Errorf("a failing git must not stop the park; updates=%v", bd.Updates)
	}
}

// A death that applies on_failure=unclaim parks nothing as human: no note.
func TestParkNote_unclaimDeathWritesNoNote(t *testing.T) {
	cfg := fastCfg()
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"bw-1": {"in_progress", "in_progress", "in_progress"}}}
	row := ccpool.Session{ExternalID: "pg-router-worker-bw-1", Live: false, State: ccpool.StateErrored, CloseReason: "handler"}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{row}, {row}, {row}}}
	e := newExec(cc, bd, cfg)
	e.deps.ParkGit = parkGit{fail: true}
	d := DispatchContext{Role: workerRole(cfg), Item: item.Item{ID: "bw-1"}}
	d.Role.CCPool.OnFailure = "unclaim"
	if err := e.waitDone(context.Background(), nil, d, "pg-router-worker-bw-1"); err == nil {
		t.Fatal("death must return an error")
	}
	if len(bd.Comments) != 0 {
		t.Errorf("a plain release is not a park; comments=%q", bd.Comments)
	}
}

func TestParkNote_stallNamesTheDrainWorktree(t *testing.T) {
	rig := newStallRig(t, releaseRole(config.Default()), func(c *config.Config) { c.RepoRoot = "/repo" })
	rig.e.deps.ParkGit = drainGit(relBead)
	rig.bd.labels = []string{drainStallLabel}
	in := stallT0.Add(time.Minute)
	rig.lastWrite = in.Add(-5 * time.Second)
	rig.cc.script = needsInputBetween(in, time.Time{})
	err := rig.run(stallT0.Add(3 * time.Hour))
	if ns := mustStalled(t, err); !ns.escalated {
		t.Fatalf("second strike must escalate: %+v", ns)
	}
	w := rig.bd.writes()
	if len(w) != 2 || !strings.HasPrefix(w[0], "comment "+relBead+" Handler park note:") || w[1] != releaseHumanBd {
		t.Fatalf("writes = %q, want the park note then one human+release", w)
	}
	for _, want := range []string{"worktree /repo/.worktrees/" + relBead + " (branch drain/" + relBead + ")", "3 commit(s) ahead of main", "bound 15m0s", "No person-question was identified"} {
		if !strings.Contains(w[0], want) {
			t.Errorf("the park note must contain %q; got:\n%s", want, w[0])
		}
	}
}
