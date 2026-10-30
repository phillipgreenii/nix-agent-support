package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router-handlers/internal/ccpool"
	"github.com/phillipgreenii/pg-router-handlers/internal/config"
	"github.com/phillipgreenii/pg-router-handlers/internal/executor"
	"github.com/phillipgreenii/pg-router-handlers/internal/rolefile"
	"github.com/phillipgreenii/pg-router-handlers/internal/roles"
	"github.com/phillipgreenii/pg-router/conformance"
)

// Dispatch admission control (bead pg2-fx5x0l.5, INV-CCH-41, ADR 0117): with
// inlineSweeps off, a dispatch runs only its role's orphans inline (one unit,
// one shared pool listing), reconciles closed-bead sessions on capacity pressure
// behind a per-pool negative cache, and runs none of the janitor's passes.

// poolCC is an orphanCC that counts List and Capacity calls and answers Capacity
// from its rows: the pool is full until a row has been closed.
type poolCC struct {
	*orphanCC
	cmu      sync.Mutex
	lists    int
	caps     int
	usageHit bool
}

func (c *poolCC) List(ctx context.Context) ([]ccpool.Session, error) {
	c.cmu.Lock()
	c.lists++
	c.cmu.Unlock()
	return c.orphanCC.List(ctx)
}

func (c *poolCC) Capacity(context.Context) (ccpool.Capacity, error) {
	c.cmu.Lock()
	c.caps++
	c.cmu.Unlock()
	if c.usageHit {
		return ccpool.Capacity{MaxSessions: 2, Counted: 2, UsageLimit: &ccpool.UsageLimit{Window: "five_hour", UsedPct: 100, ResetsAt: orphanNowT.Add(time.Hour)}}, nil
	}
	c.orphanCC.mu.Lock()
	defer c.orphanCC.mu.Unlock()
	for _, s := range c.sessions {
		if s.CloseReason != "" {
			return ccpool.Capacity{MaxSessions: 2, Counted: 1, Free: 1}, nil
		}
	}
	return ccpool.Capacity{MaxSessions: 2, Counted: 2}, nil
}

func (c *poolCC) Ensure(context.Context, string, string, string, map[string]string, map[string]string) error {
	return os.ErrInvalid // the launch fails: reaching it proves the gate passed
}

func (c *poolCC) counts() (lists, caps int) {
	c.cmu.Lock()
	defer c.cmu.Unlock()
	return c.lists, c.caps
}

// pressureRow is an idle session of another bead (zr-x) holding a pool slot.
func pressureRow(id, bead string) ccpool.Session {
	return ccpool.Session{
		ExternalID: "pg-router-worker-" + id, Name: "pg-router-worker-" + bead, State: ccpool.StateIdle, Live: true,
		Meta: map[string]string{ccpool.MetaKeyBead: bead, ccpool.MetaKeyRole: "other", ccpool.MetaKeyPool: ccpool.PoolName},
	}
}

type admissionEnv struct {
	cc      *poolCC
	bd      *orphanBD
	lockDir string
	cfgPath string
	now     time.Time
}

func newAdmissionEnv(t *testing.T, launchExtra string, sessions ...ccpool.Session) *admissionEnv {
	t.Helper()
	cc := &poolCC{orphanCC: &orphanCC{sessions: sessions}}
	bd := &orphanBD{issues: map[string]string{
		"zr-w": `{"id":"zr-w","status":"open"}`,
		"zr-x": `{"id":"zr-x","status":"closed"}`,
		"zr-y": `{"id":"zr-y","status":"in_progress"}`,
	}}
	cfgPath, _ := launchConfig(t, `,"inlineSweeps":false,"RepoRoot":"`+t.TempDir()+`"`+launchExtra)
	return &admissionEnv{cc: cc, bd: bd, lockDir: t.TempDir(), cfgPath: cfgPath, now: orphanNowT}
}

// dispatch runs one dispatch of bead zr-w by the worker role through serveDispatch
// and returns the exit code and the decoded reply.
func (e *admissionEnv) dispatch(t *testing.T, event string) (int, map[string]any) {
	t.Helper()
	_, cfg := workerRole("", "")
	mk := func(c config.Config, _ roles.Role) executor.Deps {
		return executor.Deps{CC: e.cc, BD: e.bd, Cfg: c, LockDir: e.lockDir, Now: func() time.Time { return e.now }}
	}
	var out bytes.Buffer
	code := serveDispatch(context.Background(), strings.NewReader(request(event, "worker", cfg)), &out, e.cfgPath, mk)
	var reply map[string]any
	_ = json.Unmarshal(out.Bytes(), &reply)
	return code, reply
}

func (e *admissionEnv) showsOf(bead string) int {
	n := 0
	for _, c := range e.bd.callsCopy() {
		if strings.HasPrefix(c, "show "+bead+" ") || c == "show "+bead {
			n++
		}
	}
	return n
}

func (b *orphanBD) callsCopy() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.calls...)
}

func TestAdmission_fullPoolWithClosedBeadSessionFreesASlotAndLaunches(t *testing.T) {
	e := newAdmissionEnv(t, "", pressureRow("a", "zr-x"), pressureRow("b", "zr-y"))
	code, reply := e.dispatch(t, defaultEvent)
	if code != conformance.ExitBusy || reply["reason"] != "launch-failed" {
		t.Fatalf("exit=%d reply=%v, want the dispatch to pass the gate and reach the (failing) launch", code, reply)
	}
	// The first close is the closed-bead session; the second is the purge of the
	// abandoned launch the test's failing Ensure leaves behind.
	if got := e.cc.closed(); len(got) != 2 || got[0] != "pg-router-worker-a" || strings.Contains(got[1], "worker-b") {
		t.Fatalf("closed = %v, want the closed-bead session first and never the open-bead one", got)
	}
	if _, caps := e.cc.counts(); caps != 2 {
		t.Fatalf("capacity calls = %d, want 2 (the check and one re-check)", caps)
	}
}

func TestAdmission_fullPoolNothingReclaimableDeclinesOnceAndStampsTheNegativeCache(t *testing.T) {
	e := newAdmissionEnv(t, "", pressureRow("b", "zr-y"), pressureRow("c", "zr-y"))
	code, reply := e.dispatch(t, defaultEvent)
	if code != conformance.ExitBusy || reply["reason"] != "at-capacity" {
		t.Fatalf("exit=%d reply=%v, want the at-capacity decline", code, reply)
	}
	// The wire carries no detail for an at-capacity decline (only launch-failed
	// does): the alert contract is the reason and kind alone. The error text that
	// says a reconcile was tried is asserted at the executor.
	if _, has := reply["detail"]; has {
		t.Errorf("an at-capacity reply must carry no detail: %v", reply)
	}
	if _, caps := e.cc.counts(); caps != 2 {
		t.Fatalf("capacity calls = %d, want 2: reconcile at most once, no loop", caps)
	}
	if len(e.cc.closed()) != 0 {
		t.Fatalf("closed = %v, want nothing", e.cc.closed())
	}
	stamps, _ := filepath.Glob(filepath.Join(e.lockDir, closedReconcileStampPrefix+"*.stamp"))
	if len(stamps) != 1 {
		t.Fatalf("stamps = %v, want one negative-cache stamp", stamps)
	}
}

func TestAdmission_secondDeclineInsideTheIntervalDoesNotReconcile(t *testing.T) {
	e := newAdmissionEnv(t, "", pressureRow("b", "zr-y"))
	e.dispatch(t, defaultEvent)
	if n := e.showsOf("zr-y"); n != 1 {
		t.Fatalf("first decline: bead lookups = %d, want 1", n)
	}
	e.now = e.now.Add(10 * time.Second) // inside the 30 s default
	code, reply := e.dispatch(t, defaultEvent)
	if code != conformance.ExitBusy || reply["reason"] != "at-capacity" {
		t.Fatalf("exit=%d reply=%v", code, reply)
	}
	if n := e.showsOf("zr-y"); n != 1 {
		t.Fatalf("second decline: bead lookups = %d, want still 1 (no reconcile)", n)
	}
	e.now = e.now.Add(time.Minute) // past the interval
	e.dispatch(t, defaultEvent)
	if n := e.showsOf("zr-y"); n != 2 {
		t.Fatalf("after the interval: bead lookups = %d, want 2 (reconciled again)", n)
	}
}

func TestAdmission_intervalIsConfigurableAndZeroDisablesTheCache(t *testing.T) {
	e := newAdmissionEnv(t, `,"declineReconcileMinInterval":0`, pressureRow("b", "zr-y"))
	e.dispatch(t, defaultEvent)
	e.dispatch(t, defaultEvent)
	if n := e.showsOf("zr-y"); n != 2 {
		t.Fatalf("bead lookups = %d, want 2: a zero interval reconciles every decline", n)
	}
}

func TestAdmission_usageLimitSkipsTheReconcile(t *testing.T) {
	e := newAdmissionEnv(t, "", pressureRow("a", "zr-x"))
	e.cc.usageHit = true
	code, reply := e.dispatch(t, defaultEvent)
	if code != conformance.ExitBusy || reply["reason"] != "at-capacity" {
		t.Fatalf("exit=%d reply=%v", code, reply)
	}
	if len(e.cc.closed()) != 0 || e.showsOf("zr-x") != 0 {
		t.Fatalf("a usage limit must skip the reconcile; closed=%v", e.cc.closed())
	}
}

// One pool listing serves the orphan unit and the prior-session plan when there
// is no orphan; the usage-limit decline reads nothing else.
func TestAdmission_noOrphanMeansOneSharedListForOrphanUnitAndPlan(t *testing.T) {
	e := newAdmissionEnv(t, "", pressureRow("a", "zr-x"))
	e.cc.usageHit = true
	e.dispatch(t, defaultEvent)
	if lists, _ := e.cc.counts(); lists != 1 {
		t.Fatalf("list calls = %d, want 1 shared by the orphan unit and the plan", lists)
	}
}

func expiredOrphan(id, bead, state, eventID string) ccpool.Session {
	s := pressureRow(id, bead)
	s.Name = "pg-router-worker-" + bead
	s.State = ccpool.SessionState(state)
	s.Meta[ccpool.MetaKeyRole] = "worker"
	s.Meta[ccpool.MetaKeyLeaseUntil] = orphanNowT.Add(-time.Minute).Format(time.RFC3339)
	s.Meta[ccpool.MetaKeyLaunchedAt] = orphanNowT.Add(-10 * time.Minute).Format(time.RFC3339)
	if eventID != "" {
		s.Meta[ccpool.MetaKeyEventID] = eventID
	}
	return s
}

func orphanMarked(cc *poolCC, id string) bool {
	cc.orphanCC.mu.Lock()
	defer cc.orphanCC.mu.Unlock()
	for _, m := range cc.orphanCC.metaSets {
		if m[0] == id && m[1] == ccpool.MetaKeyOrphanReclaimed {
			return true
		}
	}
	return false
}

// Orphans of OTHER beads of the role are handled in the same dispatch, before
// the plan and the capacity check, and the reclaim frees the slot.
func TestAdmission_orphansOfOtherBeadsAreHandledBeforeTheCapacityCheck(t *testing.T) {
	e := newAdmissionEnv(t, "", expiredOrphan("o1", "zr-y", "idle", ""), pressureRow("b", "zr-y"))
	code, reply := e.dispatch(t, defaultEvent)
	if !orphanMarked(e.cc, "pg-router-worker-o1") {
		t.Fatalf("the orphan of another bead was not reclaimed; closed=%v", e.cc.closed())
	}
	if code != conformance.ExitBusy || reply["reason"] != "launch-failed" {
		t.Fatalf("exit=%d reply=%v, want the reclaimed orphan's slot to let the dispatch reach the launch", code, reply)
	}
	if lists, _ := e.cc.counts(); lists < 2 {
		t.Fatalf("list calls = %d, want the plan to re-read the pool after a reclaim", lists)
	}
}

// An expired-lease orphan of the SAME bead and role is reclaimed inline whatever
// event it carries, and the dispatch then launches its own session rather than
// declining it as in flight (the plan's former I-1 risk).
func TestAdmission_sameBeadExpiredLeaseOrphanIsReclaimedAndTheDispatchLaunches(t *testing.T) {
	for name, tc := range map[string]struct{ state, event string }{
		"same event id":      {"idle", "e-1"},
		"different event id": {"idle", "e-other"},
	} {
		t.Run(name, func(t *testing.T) {
			row := expiredOrphan("o2", "zr-w", tc.state, tc.event)
			e := newAdmissionEnv(t, "", row)
			code, reply := e.dispatch(t, defaultEvent)
			if !orphanMarked(e.cc, row.ExternalID) {
				t.Fatalf("the idle orphan was not reclaimed inline; reply=%v", reply)
			}
			if reply["reason"] == "session-in-flight" {
				t.Fatalf("exit=%d reply=%v: an expired lease must not read as in flight", code, reply)
			}
		})
	}
}

// A row with no lease is never an orphan.
func TestAdmission_rowWithoutLeaseIsNeverAnOrphan(t *testing.T) {
	row := expiredOrphan("o3", "zr-w", "idle", "e-other")
	delete(row.Meta, ccpool.MetaKeyLeaseUntil)
	e := newAdmissionEnv(t, "", row)
	e.dispatch(t, defaultEvent)
	if orphanMarked(e.cc, row.ExternalID) {
		t.Fatal("a row with no lease was reclaimed as an orphan")
	}
}

// With inlineSweeps off a dispatch runs none of the janitor's passes: not the
// closed-bead reconcile before capacity, the default-pool pass, the worktree
// sweep or the lock GC.
func TestAdmission_inlineSweepsOffRunsOnlyTheOrphanUnitBeforeCapacity(t *testing.T) {
	t.Setenv("CCPOOL_POOL", "")
	wtDir := filepath.Join(t.TempDir(), "worktrees")
	if err := os.MkdirAll(filepath.Join(wtDir, "zr-leaked"), 0o755); err != nil {
		t.Fatal(err)
	}
	msgs := sweepDispatch(t, worktreeRoleCfg(`"poolDir":"`+t.TempDir()+`",`), `,"inlineSweeps":false,"WorktreeDir":"`+wtDir+`"`)
	for _, m := range msgs {
		switch m {
		case "reconcile: list failed", "reconcile: list failed (default pool)", "worktree sweep: session list failed":
			t.Fatalf("inlineSweeps=false must not run %q; got messages=%v", m, msgs)
		}
	}
	if !containsSubstring(msgs, "orphan reconcile: list failed") {
		t.Fatalf("the orphan unit must still run inline; got messages=%v", msgs)
	}
}

// The own-bead worktree check keeps its guards: a leaked worktree of the
// dispatched bead (no row, old enough) is examined, a fresh one is left alone.
func TestPoolReconciler_ownWorktreeCheckSkipsWhatIsNotACandidate(t *testing.T) {
	wtDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wtDir, "zr-w"), 0o755); err != nil { // a directory with no .git file
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.WorktreeDir = wtDir
	role := roles.Role{Name: "worker", CCPool: &roles.CCPoolConfig{}}
	cc := &poolCC{orphanCC: &orphanCC{}}
	r := newPoolReconciler(role, cfg, executor.Deps{CC: cc, Cfg: cfg, LockDir: t.TempDir()})
	if err := r.ReclaimLeakedWorktree(context.Background(), "zr-w"); err != nil {
		t.Fatal(err)
	}
	if lists, _ := cc.counts(); lists != 0 {
		t.Fatalf("list calls = %d: a non-candidate must cost no ccpool call", lists)
	}
	for _, bad := range []string{"", "..", "a/b"} {
		if err := r.ReclaimLeakedWorktree(context.Background(), bad); err != nil {
			t.Fatalf("%q: %v", bad, err)
		}
	}
	if newPoolReconciler(roles.Role{Name: "plain"}, cfg, executor.Deps{}) != nil {
		t.Fatal("a role without a ccpool block has no reconciler")
	}
}

func TestConfig_declineReconcileMinIntervalDefaultsAndLoads(t *testing.T) {
	if got := config.Default().DeclineReconcileMinInterval; got != 30*time.Second {
		t.Errorf("default = %v, want 30s", got)
	}
	p := filepath.Join(t.TempDir(), "c.json")
	if err := os.WriteFile(p, []byte(`{"declineReconcileMinInterval":5000000000}`), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := rolefile.LoadConfig(p)
	if err != nil || c.DeclineReconcileMinInterval != 5*time.Second {
		t.Errorf("LoadConfig: %v, interval=%v, want 5s", err, c.DeclineReconcileMinInterval)
	}
}
