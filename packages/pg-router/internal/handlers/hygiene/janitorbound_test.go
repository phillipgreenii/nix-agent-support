package hygiene

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/phillipgreenii/x/gitclient"

	"github.com/phillipgreenii/pg-router/internal/handlers/ccpool"
	"github.com/phillipgreenii/pg-router/internal/handlers/config"
	"github.com/phillipgreenii/pg-router/internal/handlers/executor"
	"github.com/phillipgreenii/pg-router/internal/handlers/roles"
	"github.com/phillipgreenii/pg-router/internal/handlers/worktree"
)

// Janitor cut-stage record, bounded trash purge, closed-bead resume, cached
// status timeouts and the run-deadline flag.

// cancelOnList cancels a context when the sweep lists sessions.
type cancelOnList struct {
	ccpool.Runner
	cancel context.CancelFunc
}

func (c cancelOnList) ListLight(ctx context.Context) ([]ccpool.Session, error) {
	c.cancel()
	return nil, ccpool.ErrNoFacetsUnsupported
}

func (c cancelOnList) List(ctx context.Context) ([]ccpool.Session, error) {
	c.cancel()
	return c.Runner.List(ctx)
}

// runCutSweep runs the real worktree sweep as a janitor pass whose context the
// hook cancels, and returns the run's cut stages.
func runCutSweep(t *testing.T, h *sweepHarness, hook func(cancel context.CancelFunc)) []cutStage {
	t.Helper()
	h.leak("zr-k")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hook(cancel)
	j := &janitorRun{stats: map[string]*passStat{}}
	j.runPass(ctx, passWorktree, "worker", h.deps, func(ctx context.Context) int {
		return sweepLeakedWorktrees(ctx, h.role, h.deps, h.env)
	})
	return j.cutStages
}

// Criterion 1: a role context that expires inside each pass, and each stage of
// the worktree sweep, is recorded as the cut stage.
func TestCutStage_worktreeSweepRecordsListProbeAndScan(t *testing.T) {
	cases := map[string]func(h *sweepHarness) func(cancel context.CancelFunc){
		phaseList: func(h *sweepHarness) func(context.CancelFunc) {
			return func(cancel context.CancelFunc) { h.deps.CC = cancelOnList{Runner: h.cc, cancel: cancel} }
		},
		phaseProbe: func(h *sweepHarness) func(context.CancelFunc) {
			return func(cancel context.CancelFunc) {
				h.env.cwds = func(context.Context) ([]string, error) { cancel(); return nil, nil }
			}
		},
		phaseScan: func(h *sweepHarness) func(context.CancelFunc) {
			return func(cancel context.CancelFunc) {
				inner := h.env.open
				h.env.open = func(ctx context.Context, dir string) (gitclient.WorktreeManager, error) {
					cancel()
					return inner(ctx, dir)
				}
				h.env.cwds = func(context.Context) ([]string, error) { return nil, nil }
			}
		},
	}
	for phase, mk := range cases {
		t.Run(phase, func(t *testing.T) {
			h := newSweepHarness(t)
			got := runCutSweep(t, h, mk(h))
			want := []cutStage{{Role: "worker", Pass: passWorktree, Phase: phase}}
			if fmt.Sprint(got) != fmt.Sprint(want) {
				t.Errorf("cut stages = %v, want %v", got, want)
			}
		})
	}
}

func TestCutStage_otherPassesAndFirstCutOnly(t *testing.T) {
	cases := []struct {
		pass, phase string
	}{
		{passClosedBead, phaseReconcile},
		{passClosedBead, phasePurgeRetry},
		{passOrphan, ""},
		{passDefaultPool, ""},
	}
	for _, tc := range cases {
		t.Run(tc.pass+"/"+tc.phase, func(t *testing.T) {
			j := &janitorRun{stats: map[string]*passStat{}}
			ctx, cancel := context.WithCancel(context.Background())
			j.runPass(ctx, tc.pass, "r1", executor.Deps{}, func(ctx context.Context) int {
				countsFrom(ctx).enter(tc.phase)
				cancel()
				return 0
			})
			// A later pass of the same role starts on the dead context: not a new cut.
			j.runPass(ctx, passWorktree, "r1", executor.Deps{}, func(context.Context) int { return 0 })
			want := []cutStage{{Role: "r1", Pass: tc.pass, Phase: tc.phase}}
			if fmt.Sprint(j.cutStages) != fmt.Sprint(want) {
				t.Errorf("cut stages = %v, want %v", j.cutStages, want)
			}
		})
	}
}

// The janitor_run row carries the cut stages and run_deadline_reached.
func TestJanitorRow_cutStagesAndRunDeadlineFlag(t *testing.T) {
	t.Run("role share cut, run deadline not reached", func(t *testing.T) {
		f := newJanitorFixture(t, [2]string{"a", ""}, [2]string{"b", ""})
		f.opts.deadline = 600 * time.Millisecond
		rec := &recorder{}
		p := rec.passes()
		p.worktree = func(ctx context.Context, role roles.Role, _ config.Config, _ executor.Deps) int {
			if role.Name == "a" {
				countsFrom(ctx).enter(phaseScan)
				<-ctx.Done() // a's share is half the run
			}
			return 0
		}
		f.run(p)
		ev := f.events()
		if len(ev) != 1 {
			t.Fatalf("events = %v", ev)
		}
		if ev[0]["outcome"] != janitorDeadline {
			t.Errorf("outcome = %v", ev[0]["outcome"])
		}
		if ev[0]["run_deadline_reached"] != false {
			t.Errorf("run_deadline_reached = %v, want false for a share cut", ev[0]["run_deadline_reached"])
		}
		cs, _ := ev[0]["cut_stages"].([]any)
		if len(cs) != 1 {
			t.Fatalf("cut_stages = %v", ev[0]["cut_stages"])
		}
		m := cs[0].(map[string]any)
		if m["role"] != "a" || m["pass"] != passWorktree || m["phase"] != phaseScan {
			t.Errorf("cut stage = %v", m)
		}
	})
	t.Run("run deadline reached", func(t *testing.T) {
		f := newJanitorFixture(t, [2]string{"a", ""})
		f.opts.deadline = 300 * time.Millisecond
		rec := &recorder{}
		p := rec.passes()
		p.worktree = func(ctx context.Context, _ roles.Role, _ config.Config, _ executor.Deps) int {
			<-ctx.Done()
			return 0
		}
		f.run(p)
		ev := f.events()
		if len(ev) != 1 || ev[0]["run_deadline_reached"] != true || ev[0]["outcome"] != janitorDeadline {
			t.Errorf("events = %v, want outcome deadline and run_deadline_reached true", ev)
		}
	})
	t.Run("completed", func(t *testing.T) {
		f := newJanitorFixture(t, [2]string{"a", ""})
		f.run((&recorder{}).passes())
		ev := f.events()
		if len(ev) != 1 || ev[0]["run_deadline_reached"] != false || ev[0]["outcome"] != janitorCompleted {
			t.Errorf("events = %v", ev)
		}
	})
}

// Criterion 2: the trash purge is bounded by the pass context's time and runs
// once per dir within a janitor run.
func TestTrashBudget(t *testing.T) {
	if got := trashBudget(context.Background(), time.Minute); got != time.Minute {
		t.Errorf("no deadline: %v, want the base", got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if got := trashBudget(ctx, 3*time.Minute); got <= 5*time.Second || got > 10*time.Second {
		t.Errorf("20s left, base 3m: %v, want about half of the time left", got)
	}
	if got := trashBudget(ctx, time.Second); got != time.Second {
		t.Errorf("base below the share: %v, want the base", got)
	}
	dead, cancel2 := context.WithTimeout(context.Background(), -time.Second)
	defer cancel2()
	if got := trashBudget(dead, time.Minute); got > 0 {
		t.Errorf("expired context: %v, want no budget", got)
	}
}

func (h *sweepHarness) trashWith(bead string) string {
	h.t.Helper()
	trash := filepath.Join(h.wtDir, trashDirName, h.trashName(bead, time.Hour))
	for i := 0; i < 5; i++ {
		d := filepath.Join(trash, "d"+string(rune('a'+i)))
		if err := os.MkdirAll(d, 0o755); err != nil {
			h.t.Fatal(err)
		}
		for j := 0; j < 10; j++ {
			if err := os.WriteFile(filepath.Join(d, "f"+string(rune('a'+j))), []byte("x"), 0o644); err != nil {
				h.t.Fatal(err)
			}
		}
	}
	h.age(trash)
	return trash
}

func TestPurgeTrash_drainBudgetFollowsTheRoleShare(t *testing.T) {
	h := newSweepHarness(t)
	h.trashWith("zr-k")
	var left time.Duration
	old := pacedRemoval
	t.Cleanup(func() { pacedRemoval = old })
	pacedRemoval = pacedRemover{worktree.Removal{
		BatchSize: 20,
		Pause:     time.Second,
		Sleep: func(ctx context.Context, _ time.Duration) error {
			if dl, ok := ctx.Deadline(); ok && left == 0 {
				left = time.Until(dl)
			}
			return context.Canceled
		},
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	sweepLeakedWorktrees(ctx, h.role, h.deps, h.env)
	if left <= 0 || left > 10*time.Second {
		t.Errorf("drain ran with %v left, want at most half of the 20s share (not the 3m default)", left)
	}
}

func TestPurgeTrash_oncePerDirWithinOneRun(t *testing.T) {
	h := newSweepHarness(t)
	trash := h.trashWith("zr-k")
	var pauses int
	old := pacedRemoval
	t.Cleanup(func() { pacedRemoval = old })
	pacedRemoval = pacedRemover{worktree.Removal{
		BatchSize: 20,
		Pause:     time.Second,
		Sleep: func(_ context.Context, _ time.Duration) error {
			pauses++
			if pauses == 1 {
				return context.Canceled // the first role's purge is cut after one batch
			}
			return nil
		},
	}}
	run := withTrashPurged(context.Background())
	h.sweep2(run)
	if !h.exists(trash) || pauses != 1 {
		t.Fatalf("first role: trash exists=%v pauses=%d, want a partial drain", h.exists(trash), pauses)
	}
	h.sweep2(run) // a second role on the same dir, same run
	if pauses != 1 {
		t.Errorf("a second role in the same run re-drained the dir (pauses=%d, want 1)", pauses)
	}
	h.sweep2(withTrashPurged(context.Background())) // the next run
	if h.exists(trash) {
		t.Error("the next run must finish the trash")
	}
}

func (h *sweepHarness) sweep2(ctx context.Context) int {
	return sweepLeakedWorktrees(ctx, h.role, h.deps, h.env)
}

func TestServeJanitor_carriesOneTrashSetAcrossRoles(t *testing.T) {
	f := newJanitorFixture(t, [2]string{"a", ""}, [2]string{"b", ""})
	rec := &recorder{}
	p := rec.passes()
	var firsts []bool
	p.worktree = func(ctx context.Context, _ roles.Role, _ config.Config, _ executor.Deps) int {
		firsts = append(firsts, firstPurge(ctx, "/shared/dir"))
		return 0
	}
	f.run(p)
	if fmt.Sprint(firsts) != "[true false]" {
		t.Errorf("firstPurge per role = %v, want [true false]", firsts)
	}
}

// Criterion 3 (chosen: stop between candidates and resume): 80 candidates and a
// slow bd; the first run is cut after 40 and the second continues after the
// last finished one, so no candidate is skipped or examined twice and the pool
// is finished across the two runs.
type countingBD struct {
	calls  []string
	cancel context.CancelFunc
	after  int
}

func (b *countingBD) Run(_ context.Context, args ...string) (string, error) {
	b.calls = append(b.calls, args[1])
	if b.cancel != nil && len(b.calls) == b.after {
		b.cancel() // the share expires during this (slow) call
	}
	return `{"status":"open"}`, nil
}

func eightySessions() []ccpool.Session {
	var ss []ccpool.Session
	for i := 0; i < 80; i++ {
		bead := fmt.Sprintf("zr-%03d", i)
		ss = append(ss, ccpool.Session{
			ExternalID: "pg-router-worker-" + bead,
			State:      ccpool.StateIdle,
			Meta:       map[string]string{ccpool.MetaKeyBead: bead},
		})
	}
	return ss
}

func TestReconcileClosedBead_resumesAfterACutInsteadOfRestarting(t *testing.T) {
	lockDir := t.TempDir()
	cc := &fakeCC{ListSeq: [][]ccpool.Session{eightySessions()}}
	open := (&fakeWorktreeOpener{}).Open

	ctx1, cancel1 := context.WithCancel(context.Background())
	bd1 := &countingBD{cancel: cancel1, after: 40}
	reconcileClosedBeadSessions(withResume(ctx1, lockDir, "pool"), cc, open, bd1, "pg-router-", "/repo", "", nil)
	if len(bd1.calls) != 40 {
		t.Fatalf("run 1 examined %d candidates, want it to stop between candidates at the cut (40)", len(bd1.calls))
	}

	bd2 := &countingBD{}
	reconcileClosedBeadSessions(withResume(context.Background(), lockDir, "pool"), cc, open, bd2, "pg-router-", "/repo", "", nil)
	// The candidate in flight when run 1 was cut is repeated; the other 39 are not.
	if len(bd2.calls) != 41 || bd2.calls[0] != bd1.calls[39] {
		t.Fatalf("run 2 examined %d starting at %v, want 41 starting at the cut candidate %v", len(bd2.calls), bd2.calls[:1], bd1.calls[39])
	}
	seen := map[string]bool{}
	for _, c := range append(append([]string{}, bd1.calls...), bd2.calls...) {
		seen[c] = true
	}
	if len(seen) != 80 {
		t.Errorf("across the two runs %d distinct candidates were examined, want all 80", len(seen))
	}
	if m, _ := filepath.Glob(filepath.Join(lockDir, cursorFilePrefix+"*")); len(m) != 0 {
		t.Errorf("a finished walk must clear the cursor, found %v", m)
	}

	// Without a cut a whole pool of 80 is examined in one run.
	bd3 := &countingBD{}
	reconcileClosedBeadSessions(withResume(context.Background(), lockDir, "pool"), cc, open, bd3, "pg-router-", "/repo", "", nil)
	if len(bd3.calls) != 80 {
		t.Errorf("an uncut run examined %d, want 80", len(bd3.calls))
	}
}

func TestReconcileClosedBead_withoutResumeStillStopsBetweenCandidates(t *testing.T) {
	cc := &fakeCC{ListSeq: [][]ccpool.Session{eightySessions()}}
	ctx, cancel := context.WithCancel(context.Background())
	bd := &countingBD{cancel: cancel, after: 10}
	reconcileClosedBeadSessions(ctx, cc, (&fakeWorktreeOpener{}).Open, bd, "pg-router-", "/repo", "", nil)
	if len(bd.calls) != 10 {
		t.Errorf("examined %d after the cut, want 10", len(bd.calls))
	}
}

// Criterion 4: a git status that timed out is cached like a keep decision.
type statusFailWM struct {
	gitclient.WorktreeManager
	err   error
	calls *atomic.Int64
}

func (s statusFailWM) Status(context.Context) ([]gitclient.StatusEntry, error) {
	s.calls.Add(1)
	return nil, s.err
}

func (s statusFailWM) IsTracked(ctx context.Context, p string) (bool, error) {
	return s.WorktreeManager.(gitclient.StatusReader).IsTracked(ctx, p)
}

func (s statusFailWM) loc() gitclient.Locator { return s.WorktreeManager.(gitclient.Locator) }

func (s statusFailWM) CurrentBranch(ctx context.Context) (string, error) {
	return s.loc().CurrentBranch(ctx)
}

func (s statusFailWM) Toplevel(ctx context.Context) (string, error) { return s.loc().Toplevel(ctx) }

func (s statusFailWM) CommonDir(ctx context.Context) (string, error) { return s.loc().CommonDir(ctx) }

func (s statusFailWM) RemoteURL(ctx context.Context, n string) (string, error) {
	return s.loc().RemoteURL(ctx, n)
}

func (h *sweepHarness) failStatusWith(err error) *atomic.Int64 {
	var n atomic.Int64
	inner := h.env.open
	h.env.open = func(ctx context.Context, dir string) (gitclient.WorktreeManager, error) {
		wm, e := inner(ctx, dir)
		if e != nil || dir == h.repo {
			return wm, e
		}
		return statusFailWM{WorktreeManager: wm, err: err, calls: &n}, nil
	}
	return &n
}

func TestStatusTimeout_isCachedSoTheNextRunSkipsIt(t *testing.T) {
	h := newSweepHarness(t)
	path := h.leak("zr-k")
	calls := h.failStatusWith(fmt.Errorf("git status: %w", context.DeadlineExceeded))
	if got := h.sweep(); got != 0 {
		t.Fatalf("removed %d", got)
	}
	if calls.Load() != 1 || len(h.keepFiles()) != 1 {
		t.Fatalf("run 1: status calls=%d keep records=%v, want 1 and one record", calls.Load(), h.keepFiles())
	}
	if got := h.sweep(); got != 0 || calls.Load() != 1 {
		t.Errorf("run 2: removed=%d status calls=%d, want the cached timeout skipped (still 1)", got, calls.Load())
	}
	h.wantKept(path, "zr-k")
	h.nowFixed = h.nowFixed.Add(h.deps.Cfg.WorktreeKeepRecheck + time.Second)
	h.sweep()
	if calls.Load() != 2 {
		t.Errorf("after the recheck window status calls=%d, want 2", calls.Load())
	}
}

func TestStatusError_otherFailureIsNotCached(t *testing.T) {
	h := newSweepHarness(t)
	h.leak("zr-k")
	calls := h.failStatusWith(errors.New("fatal: corrupt index"))
	h.sweep()
	h.sweep()
	if calls.Load() != 2 || len(h.keepFiles()) != 0 {
		t.Errorf("status calls=%d keep=%v, want a plain error retried (2) and not cached", calls.Load(), h.keepFiles())
	}
}

func TestResumeCursor_isPerPoolAndPrefix(t *testing.T) {
	dir := t.TempDir()
	a, _ := resumeFrom(withResume(context.Background(), dir, "p1"), "pg-router-")
	b, _ := resumeFrom(withResume(context.Background(), dir, "p2"), "pg-router-")
	if a.path == b.path || !strings.HasPrefix(filepath.Base(a.path), cursorFilePrefix) {
		t.Errorf("cursor paths %q %q must differ per pool", a.path, b.path)
	}
	if _, ok := resumeFrom(context.Background(), "x"); ok {
		t.Error("no resume without withResume")
	}
}
