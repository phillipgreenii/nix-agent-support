package hygiene

import (
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-router/internal/handlers/ccpool"
	"github.com/phillipgreenii/pg-router/internal/handlers/gwm"
	"github.com/phillipgreenii/pg-router/internal/handlers/roles"
	"github.com/phillipgreenii/x/cmdrun"
	"github.com/phillipgreenii/x/cmdruntest"
)

// These tests answer pg-gwm with cmdruntest fakes behind the real gwm.CLI, so
// the argv the handler sends is asserted exactly.

func gwmVerb(verb string) func(cmdrun.Call) bool {
	return func(c cmdrun.Call) bool { return c.Name == "pg-gwm" && len(c.Args) > 0 && c.Args[0] == verb }
}

func gwmArgs(f *cmdruntest.Fake, verb string) [][]string {
	var out [][]string
	for _, c := range f.Calls() {
		if gwmVerb(verb)(c) {
			out = append(out, c.Args)
		}
	}
	return out
}

func hasArgs(args []string, want ...string) bool {
	return strings.Contains(" "+strings.Join(args, " ")+" ", " "+strings.Join(want, " ")+" ")
}

// useGWMFake returns teardown ports whose pg-gwm client is a fake, and the fake.
func useGWMFake(t *testing.T, repo string) (teardownPorts, *cmdruntest.Fake) {
	t.Helper()
	f := cmdruntest.New(t)
	tp := testPorts()
	tp.gwm = &gwm.CLI{Exec: f.Runner(), RepoRoot: repo}
	return tp, f
}

func gwmRow(bead, cwd string) ccpool.Session {
	return ccpool.Session{
		ExternalID: "pg-router-worker-" + bead, State: ccpool.StateIdle, CWD: cwd,
		Meta: map[string]string{
			ccpool.MetaKeyBead: bead, ccpool.MetaKeyRole: "worker", ccpool.MetaKeyGWMLease: "l-1",
		},
	}
}

func gwmRefusal(code int, name string) cmdrun.Result {
	return cmdrun.Result{Code: code, Stdout: []byte(`{"error":{"name":"` + name + `","message":"refused"}}`)}
}

func TestReconcileReleasesByName(t *testing.T) {
	t.Run("releases by name and never removes the worktree", func(t *testing.T) {
		h := newSweepHarness(t)
		wt := h.leak("zr-k")
		tp, f := useGWMFake(t, h.repo)
		f.On(gwmVerb("release")).Return(cmdrun.Result{})
		cc := &fakeCC{}
		s := gwmRow("zr-k", wt)
		if !tp.closeSession(t.Context(), cc, h.opener, h.repo, h.wtDir, s, false) {
			t.Fatal("the row must be purged")
		}
		rel := gwmArgs(f, "release")
		if len(rel) != 1 || !hasArgs(rel[0], "--name", "worker/zr-k") || !hasArgs(rel[0], "--format=json") {
			t.Fatalf("release calls = %v, want one by name worker/zr-k", rel)
		}
		if !h.exists(wt) {
			t.Error("a gwm session's worktree is pg-gwm's to remove, never git worktree remove")
		}
		if !h.branchExists("zr-k") {
			t.Error("a branch a worktree still holds must be kept")
		}
		if len(cc.ClosedPurge) == 0 || !cc.ClosedPurge[len(cc.ClosedPurge)-1] {
			t.Errorf("the row must end purged; ClosedPurge=%v", cc.ClosedPurge)
		}
	})
	t.Run("an anchor branch with no unique commit and no worktree is deleted", func(t *testing.T) {
		h := newSweepHarness(t)
		h.git(h.repo, "branch", "pg-router/zr-m")
		tp, f := useGWMFake(t, h.repo)
		f.On(gwmVerb("release")).Return(cmdrun.Result{})
		if !tp.closeSession(t.Context(), &fakeCC{}, h.opener, h.repo, h.wtDir, gwmRow("zr-m", h.wtDir+"/gone"), false) {
			t.Fatal("the row must be purged")
		}
		if h.branchExists("zr-m") {
			t.Error("the merged, unheld anchor branch must be deleted")
		}
	})
	t.Run("an anchor branch with a unique commit is kept", func(t *testing.T) {
		h := newSweepHarness(t)
		wt := h.addWorktree("zr-u")
		h.git(wt, "commit", "--allow-empty", "-m", "unique")
		h.git(h.repo, "worktree", "remove", "--force", wt)
		tp, f := useGWMFake(t, h.repo)
		f.On(gwmVerb("release")).Return(cmdrun.Result{})
		if !tp.closeSession(t.Context(), &fakeCC{}, h.opener, h.repo, h.wtDir, gwmRow("zr-u", wt), false) {
			t.Fatal("the row must be purged")
		}
		if !h.branchExists("zr-u") {
			t.Error("a branch holding a commit the canonical HEAD lacks must be kept")
		}
	})
	t.Run("a dirty tree is expired by name", func(t *testing.T) {
		h := newSweepHarness(t)
		tp, f := useGWMFake(t, h.repo)
		f.On(gwmVerb("release")).Return(gwmRefusal(23, "dirty"))
		f.On(gwmVerb("expire")).Return(cmdrun.Result{})
		if !tp.closeSession(t.Context(), &fakeCC{}, h.opener, h.repo, h.wtDir, gwmRow("zr-d", h.wtDir+"/gone"), false) {
			t.Fatal("the row must be purged")
		}
		exp := gwmArgs(f, "expire")
		if len(exp) != 1 || !hasArgs(exp[0], "--name", "worker/zr-d") {
			t.Errorf("expire calls = %v, want one by name worker/zr-d", exp)
		}
	})
}

func TestSessionWithoutGWMLeaseTearsDownTheLegacyWay(t *testing.T) {
	h := newSweepHarness(t)
	wt := h.leak("zr-l")
	tp, f := useGWMFake(t, h.repo) // no rule: any pg-gwm call fails the test
	s := gwmRow("zr-l", wt)
	delete(s.Meta, ccpool.MetaKeyGWMLease)
	if !tp.closeSession(t.Context(), &fakeCC{}, h.opener, h.repo, h.wtDir, s, false) {
		t.Fatal("the row must be purged")
	}
	if h.exists(wt) || h.branchExists("zr-l") {
		t.Error("a legacy session's worktree and anchor branch are removed the old way")
	}
	if n := len(f.Calls()); n != 0 {
		t.Errorf("pg-gwm calls = %d, want 0", n)
	}
}

func TestPurgePendingReleaseFails(t *testing.T) {
	h := newSweepHarness(t)
	tp, f := useGWMFake(t, h.repo)
	s := gwmRow("zr-p", h.wtDir+"/gone")
	s.CloseReason = "handler"
	s.Meta[ccpool.MetaKeyPurgePending] = purgeMarkerValue

	f.On(gwmVerb("release")).Nth(1).Return(cmdrun.Result{Code: 1, Stderr: []byte("boom")})
	cc := &fakeCC{}
	purged, attempted := tp.retryPurgePending(t.Context(), cc, h.opener, h.repo, h.wtDir, nil, []ccpool.Session{s}, s)
	if purged || !attempted {
		t.Fatalf("purged=%v attempted=%v, want a failed attempt", purged, attempted)
	}
	for _, p := range cc.ClosedPurge {
		if p {
			t.Fatal("a failed release must not purge the row; it keeps purge_pending for the retry")
		}
	}

	// not-found counts as a completed release step.
	f.On(gwmVerb("release")).Return(gwmRefusal(4, "not-found"))
	purged, attempted = tp.retryPurgePending(t.Context(), cc, h.opener, h.repo, h.wtDir, nil, []ccpool.Session{s}, s)
	if !purged || !attempted {
		t.Fatalf("after not-found: purged=%v attempted=%v, want purged", purged, attempted)
	}
}

func TestOrphanPassRenewsLiveOrphan(t *testing.T) {
	row := orphanRow(ccpool.StateWorking)
	row.Meta[ccpool.MetaKeyGWMLease] = "l-1"
	h := newOrphanHarness(t, row)
	f := cmdruntest.New(t)
	f.On(gwmVerb("renew")).Return(cmdrun.Result{})
	h.deps.GWM = &gwm.CLI{Exec: f.Runner(), RepoRoot: "/repo"}
	role := orphanRole(roles.CloseOnly, 0) // no time budget: the working orphan is left running
	h.run(role)
	got := gwmArgs(f, "renew")
	if len(got) != 1 || !hasArgs(got[0], "--ttl", "30m") || got[0][len(got[0])-1] != "l-1" {
		t.Errorf("renew calls = %v, want one renew of l-1 with --ttl 30m", got)
	}
}
