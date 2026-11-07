package executor

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/phillipgreenii/pg-router-handlers/internal/ccpool"
	"github.com/phillipgreenii/pg-router-handlers/internal/checkoutpool"
	"github.com/phillipgreenii/pg-router-handlers/internal/dtest"
	"github.com/phillipgreenii/pg-router-handlers/internal/item"
	"github.com/phillipgreenii/pg-router-handlers/internal/roles"
)

// fakePool is a checkoutpool.Client double recording every call.
type fakePool struct {
	mu         sync.Mutex
	leaseErr   error
	releaseErr error
	leases     []checkoutpool.LeaseRequest
	released   []string
	renewed    []string
	path       string
}

func (p *fakePool) Lease(_ context.Context, req checkoutpool.LeaseRequest) (checkoutpool.Lease, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.leases = append(p.leases, req)
	if p.leaseErr != nil {
		return checkoutpool.Lease{}, p.leaseErr
	}
	return checkoutpool.Lease{ID: "lease-1", Path: p.path, Slot: "slot-01"}, nil
}

func (p *fakePool) Renew(_ context.Context, id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.renewed = append(p.renewed, id)
	return nil
}

func (p *fakePool) Release(_ context.Context, id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.released = append(p.released, id)
	return p.releaseErr
}

func (p *fakePool) releases() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.released...)
}

func poolWorkerRole(t *testing.T) roles.Role {
	t.Helper()
	cfg := fastCfg()
	r := workerRole(cfg)
	r.CCPool.Isolation = roles.IsolationConfig{Type: CheckoutPoolIsolation, Ref: "origin/main"}
	return r
}

func poolDeps(t *testing.T, cc *dtest.FakeCC, bd *dtest.ScriptBD, fp *fakePool, ext string) Deps {
	t.Helper()
	cfg := fastCfg()
	cfg.RepoRoot = "/canonical/clone"
	cfg.WorktreeDir = t.TempDir()
	deps := newExec(cc, bd, cfg).deps
	deps.ExternalID = ext
	deps.Git = &dtest.NoopGit{}
	deps.GitOpener = (&dtest.NoopGitOpener{}).Open
	deps.Pool = fp
	return deps
}

func closedWorkerCC(ext string) (*dtest.FakeCC, *dtest.ScriptBD) {
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-w": {"in_progress", "closed"}}}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{{ExternalID: ext, Live: true, State: ccpool.StateWorking}}}}
	return cc, bd
}

// The pool isolation leases keyed on the dispatched session's external id with
// the bead id only as the affinity hint, runs the session in the leased path,
// exports the lease id as an environment variable (and on the row), and releases
// the lease when the dispatch tears down.
func TestDispatch_checkoutPool_leasesBySessionAndReleasesOnTeardown(t *testing.T) {
	const ext = "pg-router-worker-zr-w"
	cc, bd := closedWorkerCC(ext)
	leased := t.TempDir()
	fp := &fakePool{path: leased}
	deps := poolDeps(t, cc, bd, fp, ext)

	_, err := ccpoolExecutor{}.Dispatch(context.Background(), DispatchContext{Role: poolWorkerRole(t), Item: item.Item{ID: "zr-w"}}, deps)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if len(fp.leases) != 1 {
		t.Fatalf("leases = %+v, want exactly one", fp.leases)
	}
	got := fp.leases[0]
	if got.Owner != ext || got.Bead != "zr-w" || got.Repo != "/canonical/clone" || got.Ref != "origin/main" {
		t.Errorf("lease request = %+v, want owner=%s bead=zr-w repo=/canonical/clone ref=origin/main", got, ext)
	}
	if cc.EnsuredCwd != leased {
		t.Errorf("session cwd = %q, want the leased path %q", cc.EnsuredCwd, leased)
	}
	if cc.EnsuredEnv[CheckoutLeaseEnv] != "lease-1" || cc.EnsuredEnv["WORKSPACE_ROOT"] != leased {
		t.Errorf("env = %v, want %s=lease-1 and WORKSPACE_ROOT=%s", cc.EnsuredEnv, CheckoutLeaseEnv, leased)
	}
	if cc.EnsuredMeta[ccpool.MetaKeyCheckoutLease] != "lease-1" {
		t.Errorf("row meta = %v, want the lease id under %s", cc.EnsuredMeta, ccpool.MetaKeyCheckoutLease)
	}
	if rel := fp.releases(); len(rel) != 1 || rel[0] != "lease-1" {
		t.Errorf("releases = %v, want [lease-1]", rel)
	}
}

// A role that names no isolation type never touches the pool.
func TestDispatch_checkoutPool_notUsedByDefault(t *testing.T) {
	const ext = "pg-router-worker-zr-w"
	cc, bd := closedWorkerCC(ext)
	fp := &fakePool{path: t.TempDir()}
	deps := poolDeps(t, cc, bd, fp, ext)
	if _, err := (ccpoolExecutor{}).Dispatch(context.Background(), DispatchContext{Role: workerRole(deps.Cfg), Item: item.Item{ID: "zr-w"}}, deps); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if len(fp.leases) != 0 || len(fp.releases()) != 0 {
		t.Errorf("the default isolation touched the pool: leases=%v releases=%v", fp.leases, fp.releases())
	}
	if _, ok := cc.EnsuredEnv[CheckoutLeaseEnv]; ok {
		t.Errorf("the lease env var is set without pool isolation: %v", cc.EnsuredEnv)
	}
}

// An exhausted pool is the busy decline (ErrPoolAtCapacity) and mutates no
// bead and launches no session.
func TestDispatch_checkoutPool_exhaustedDeclinesWithoutMutatingBeads(t *testing.T) {
	const ext = "pg-router-worker-zr-w"
	cc, bd := closedWorkerCC(ext)
	fp := &fakePool{leaseErr: checkoutpool.ErrExhausted}
	deps := poolDeps(t, cc, bd, fp, ext)

	res, err := ccpoolExecutor{}.Dispatch(context.Background(), DispatchContext{Role: poolWorkerRole(t), Item: item.Item{ID: "zr-w"}}, deps)
	if !errors.Is(err, ErrPoolAtCapacity) {
		t.Fatalf("err = %v, want ErrPoolAtCapacity (the busy decline)", err)
	}
	if len(res.Actions) != 0 {
		t.Errorf("result = %+v, want no bead action", res)
	}
	if len(bd.Updates) != 0 {
		t.Errorf("beads were mutated: %v", bd.Updates)
	}
	if len(cc.Ensured) != 0 {
		t.Errorf("a session was launched: %v", cc.Ensured)
	}
}

// A launch that fails after the lease was taken gives the checkout back (the
// abandoned-worktree reclaim, pool variant).
func TestDispatch_checkoutPool_launchFailureReleasesLease(t *testing.T) {
	const ext = "pg-router-worker-zr-w"
	cc, bd := closedWorkerCC(ext)
	cc.EnsureErr = errors.New("ccpool new failed")
	cc.ListSeq = [][]ccpool.Session{nil}
	fp := &fakePool{path: t.TempDir()}
	deps := poolDeps(t, cc, bd, fp, ext)

	_, err := ccpoolExecutor{}.Dispatch(context.Background(), DispatchContext{Role: poolWorkerRole(t), Item: item.Item{ID: "zr-w"}}, deps)
	if err == nil {
		t.Fatal("want the launch failure")
	}
	if rel := fp.releases(); len(rel) != 1 || rel[0] != "lease-1" {
		t.Errorf("releases = %v, want [lease-1] after a failed launch", rel)
	}
}

// A live session keeps its checkout: an abandoned-launch reclaim never releases
// under a session that is still running.
func TestDispatch_checkoutPool_launchFailureKeepsLeaseOfLiveSession(t *testing.T) {
	const ext = "pg-router-worker-zr-w"
	cc, bd := closedWorkerCC(ext)
	cc.EnsureErr = errors.New("ccpool new failed")
	fp := &fakePool{path: t.TempDir()}
	deps := poolDeps(t, cc, bd, fp, ext)

	_, _ = ccpoolExecutor{}.Dispatch(context.Background(), DispatchContext{Role: poolWorkerRole(t), Item: item.Item{ID: "zr-w"}}, deps)
	if rel := fp.releases(); len(rel) != 0 {
		t.Errorf("releases = %v, want none while the session is live", rel)
	}
}

func TestNewIsolation_checkoutPoolOwnerAndHint(t *testing.T) {
	fp := &fakePool{path: "/slot/tree"}
	deps := Deps{Cfg: testCfg("/clone", ""), ExternalID: "sess-9", Pool: fp}
	iso := newIsolation(roles.IsolationConfig{Type: CheckoutPoolIsolation}, deps)
	got, err := iso.Ensure(context.Background(), "bead-7")
	if err != nil || got != "/slot/tree" {
		t.Fatalf("Ensure = %q, %v", got, err)
	}
	if l := fp.leases[0]; l.Owner != "sess-9" || l.Bead != "bead-7" || l.Ref != DefaultPoolRef {
		t.Errorf("lease = %+v, want owner sess-9, hint bead-7, default ref", l)
	}
	if lh, ok := iso.(leaseHolder); !ok || lh.leaseID() != "lease-1" {
		t.Errorf("isolation does not expose the lease id")
	}
}

func TestNewIsolation_checkoutPoolLowDiskIsLowDisk(t *testing.T) {
	deps := Deps{Cfg: testCfg("/clone", ""), ExternalID: "s", Pool: &fakePool{leaseErr: checkoutpool.ErrLowDisk}}
	_, err := newIsolation(roles.IsolationConfig{Type: CheckoutPoolIsolation}, deps).Ensure(context.Background(), "b")
	if !errors.Is(err, ErrLowDisk) {
		t.Fatalf("err = %v, want ErrLowDisk", err)
	}
}
