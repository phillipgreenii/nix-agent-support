package executor

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router-handlers/internal/ccpool"
	"github.com/phillipgreenii/pg-router-handlers/internal/dtest"
	"github.com/phillipgreenii/pg-router-handlers/internal/item"
)

// Dispatch admission control (bead pg2-fx5x0l.5, INV-CCH-41): with inlineSweeps
// off, a full pool with no usage limit is reconciled once through the Reconciler
// seam and capacity is re-checked once; the decline keeps its reason.

// seqCapCC serves a scripted sequence of Capacity answers (the last repeats) and
// counts the calls.
type seqCapCC struct {
	*dtest.FakeCC
	mu    sync.Mutex
	caps  []ccpool.Capacity
	errAt int // 1-based Capacity call that fails; 0 = never
	calls int
}

func (c *seqCapCC) Capacity(context.Context) (ccpool.Capacity, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if c.errAt == c.calls {
		return ccpool.Capacity{}, errors.New("capacity boom")
	}
	i := c.calls - 1
	if i >= len(c.caps) {
		i = len(c.caps) - 1
	}
	return c.caps[i], nil
}

func (c *seqCapCC) capacityCalls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// fakeReconciler records its calls and answers from its fields.
type fakeReconciler struct {
	mu      sync.Mutex
	outcome ReconcileOutcome
	err     error
	closed  int
	wtBeads []string
	wtErr   error
}

func (f *fakeReconciler) ReconcileClosedBeads(context.Context) (ReconcileOutcome, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed++
	return f.outcome, f.err
}

func (f *fakeReconciler) ReclaimLeakedWorktree(_ context.Context, bead string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.wtBeads = append(f.wtBeads, bead)
	return f.wtErr
}

func (f *fakeReconciler) reconciles() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

var (
	fullPool = ccpool.Capacity{MaxSessions: 4, Counted: 4, Free: 0}
	freePool = ccpool.Capacity{MaxSessions: 4, Counted: 3, Free: 1}
)

// admissionRun builds a run over a scripted capacity sequence. inline is the
// inlineSweeps value; rec may be nil (no Reconciler).
func admissionRun(t *testing.T, inline bool, rec Reconciler, caps ...ccpool.Capacity) (*ccpoolRun, *seqCapCC, DispatchContext) {
	t.Helper()
	cfg := fastCfg()
	cfg.InlineSweeps = inline
	cfg.WorktreeDir = t.TempDir()
	cc := &seqCapCC{FakeCC: &dtest.FakeCC{EnsureErr: errors.New("stop here: launch fails")}, caps: caps}
	e := newExec(cc.FakeCC, &dtest.ScriptBD{}, cfg)
	e.deps.CC = cc
	e.deps.GitOpener = (&dtest.NoopGitOpener{}).Open
	e.deps.Reconciler = rec
	return e, cc, DispatchContext{Role: workerRole(cfg), Item: item.Item{ID: "zr-w"}}
}

func TestAdmission_reconcileFreesASlotAndTheDispatchProceeds(t *testing.T) {
	rec := &fakeReconciler{outcome: ReconcileOutcome{Closed: 2}}
	e, cc, d := admissionRun(t, false, rec, fullPool, freePool)
	_, err := e.run(context.Background(), d)
	if !errors.Is(err, ErrLaunchFailed) {
		t.Fatalf("err = %v, want the dispatch to pass the gate and reach the launch (ErrLaunchFailed)", err)
	}
	if rec.reconciles() != 1 || cc.capacityCalls() != 2 {
		t.Fatalf("reconciles=%d capacity calls=%d, want 1 and 2", rec.reconciles(), cc.capacityCalls())
	}
	if len(cc.Ensured) != 1 {
		t.Fatalf("Ensured = %v, want the launch attempted once", cc.Ensured)
	}
}

func TestAdmission_nothingReclaimableDeclinesOnceWithoutLooping(t *testing.T) {
	rec := &fakeReconciler{}
	e, cc, d := admissionRun(t, false, rec, fullPool)
	_, err := e.run(context.Background(), d)
	if !errors.Is(err, ErrPoolAtCapacity) {
		t.Fatalf("err = %v, want ErrPoolAtCapacity", err)
	}
	if !strings.Contains(err.Error(), "counted=4 max=4 preserved=0") || !strings.Contains(err.Error(), "a closed-bead reconcile was tried") {
		t.Errorf("detail %q must keep the counts and say a reconcile was tried", err)
	}
	if rec.reconciles() != 1 || cc.capacityCalls() != 2 {
		t.Fatalf("reconciles=%d capacity calls=%d, want exactly 1 and 2 (no loop)", rec.reconciles(), cc.capacityCalls())
	}
	if len(cc.Ensured) != 0 {
		t.Fatalf("nothing may launch: %v", cc.Ensured)
	}
}

func TestAdmission_usageLimitSkipsTheReconcile(t *testing.T) {
	rec := &fakeReconciler{}
	full := fullPool
	full.UsageLimit = &ccpool.UsageLimit{Window: "five_hour", UsedPct: 100, ResetsAt: time.Date(2026, 10, 6, 17, 0, 0, 0, time.UTC)}
	e, cc, d := admissionRun(t, false, rec, full)
	_, err := e.run(context.Background(), d)
	if !errors.Is(err, ErrPoolAtCapacity) || !strings.Contains(err.Error(), "usage limit") {
		t.Fatalf("err = %v, want the usage-limit decline", err)
	}
	if rec.reconciles() != 0 || cc.capacityCalls() != 1 {
		t.Fatalf("reconciles=%d capacity calls=%d, want 0 and 1", rec.reconciles(), cc.capacityCalls())
	}
}

func TestAdmission_inlineSweepsOnNeverReconcilesOnPressure(t *testing.T) {
	rec := &fakeReconciler{outcome: ReconcileOutcome{Closed: 1}}
	e, cc, d := admissionRun(t, true, rec, fullPool, freePool)
	_, err := e.run(context.Background(), d)
	if !errors.Is(err, ErrPoolAtCapacity) || strings.Contains(err.Error(), "reconcile") {
		t.Fatalf("err = %v, want today's plain at-capacity decline", err)
	}
	if rec.reconciles() != 0 || len(rec.wtBeads) != 0 || cc.capacityCalls() != 1 {
		t.Fatalf("reconciles=%d worktree checks=%v capacity calls=%d, want none and 1", rec.reconciles(), rec.wtBeads, cc.capacityCalls())
	}
}

func TestAdmission_nilOrFailingReconcilerDeclinesCleanly(t *testing.T) {
	for name, rec := range map[string]Reconciler{
		"nil":     nil,
		"failing": &fakeReconciler{err: errors.New("reconcile boom")},
	} {
		t.Run(name, func(t *testing.T) {
			e, cc, d := admissionRun(t, false, rec, fullPool, freePool)
			_, err := e.run(context.Background(), d)
			if !errors.Is(err, ErrPoolAtCapacity) {
				t.Fatalf("err = %v, want ErrPoolAtCapacity", err)
			}
			if len(cc.Ensured) != 0 {
				t.Fatalf("a failed reconcile must not launch: %v", cc.Ensured)
			}
			if name == "failing" && cc.capacityCalls() != 1 {
				t.Errorf("capacity calls = %d, want 1 (no re-check after a failed reconcile)", cc.capacityCalls())
			}
		})
	}
}

func TestAdmission_rateLimitedReconcileDeclinesWithoutRecheck(t *testing.T) {
	rec := &fakeReconciler{outcome: ReconcileOutcome{Skipped: true}}
	e, cc, d := admissionRun(t, false, rec, fullPool, freePool)
	_, err := e.run(context.Background(), d)
	if !errors.Is(err, ErrPoolAtCapacity) || !strings.Contains(err.Error(), "found nothing recently") {
		t.Fatalf("err = %v, want an at-capacity decline noting the recent reconcile", err)
	}
	if cc.capacityCalls() != 1 {
		t.Fatalf("capacity calls = %d, want 1: a skipped reconcile changed nothing", cc.capacityCalls())
	}
}

func TestAdmission_capacityUnreadableAfterReconcileFailsClosed(t *testing.T) {
	rec := &fakeReconciler{outcome: ReconcileOutcome{Closed: 1}}
	e, cc, d := admissionRun(t, false, rec, fullPool, freePool)
	cc.errAt = 2
	_, err := e.run(context.Background(), d)
	if !errors.Is(err, ErrPoolCapacityUnknown) || errors.Is(err, ErrPoolAtCapacity) {
		t.Fatalf("err = %v, want ErrPoolCapacityUnknown", err)
	}
}

func TestAdmission_ownWorktreeCheckedBeforeIsolationOnlyWhenAdmissionIsOn(t *testing.T) {
	rec := &fakeReconciler{wtErr: errors.New("worktree check boom")}
	e, cc, d := admissionRun(t, false, rec, freePool)
	_, err := e.run(context.Background(), d)
	if !errors.Is(err, ErrLaunchFailed) {
		t.Fatalf("err = %v, want the dispatch to continue past a failed own-worktree check", err)
	}
	if len(rec.wtBeads) != 1 || rec.wtBeads[0] != "zr-w" || len(cc.Ensured) != 1 {
		t.Fatalf("worktree checks=%v ensured=%v, want one check of zr-w before the launch", rec.wtBeads, cc.Ensured)
	}

	rec = &fakeReconciler{}
	e, _, d = admissionRun(t, true, rec, freePool)
	if _, err := e.run(context.Background(), d); !errors.Is(err, ErrLaunchFailed) {
		t.Fatal(err)
	}
	if len(rec.wtBeads) != 0 {
		t.Fatalf("inlineSweeps on: the whole-directory sweep already ran, so no own-bead check; got %v", rec.wtBeads)
	}
}
