package hygiene

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/internal/handlers/ccpool"
	"github.com/phillipgreenii/pg-router/internal/handlers/config"
	"github.com/phillipgreenii/pg-router/internal/handlers/executor"
	"github.com/phillipgreenii/pg-router/internal/handlers/roles"
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
			j.runPass(ctx, passLockGC, "r1", executor.Deps{}, func(context.Context) int { return 0 })
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
		p.orphan = func(ctx context.Context, role roles.Role, _ config.Config, _ executor.Deps) int {
			if role.Name == "a" {
				countsFrom(ctx).enter(phaseReconcile)
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
		if m["role"] != "a" || m["pass"] != passOrphan || m["phase"] != phaseReconcile {
			t.Errorf("cut stage = %v", m)
		}
	})
	t.Run("run deadline reached", func(t *testing.T) {
		f := newJanitorFixture(t, [2]string{"a", ""})
		f.opts.deadline = 300 * time.Millisecond
		rec := &recorder{}
		p := rec.passes()
		p.orphan = func(ctx context.Context, _ roles.Role, _ config.Config, _ executor.Deps) int {
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
