package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/builtin"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/ledger"
)

var bg = context.Background()

var t0 = time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

// fakeProc answers process start times from a map; a missing pid is gone.
type fakeProc map[int]time.Time

func (f fakeProc) StartTime(pid int) (time.Time, bool) { t, ok := f[pid]; return t, ok }

type env struct {
	l    *ledger.Ledger
	d    *Deps
	proc fakeProc
	now  *time.Time
}

const testCfg = `
[[scope]]
name = "demo"
prefixes = ["/work/demo"]
`

func setup(t *testing.T) *env {
	t.Helper()
	root := t.TempDir()
	ce := config.Env{ConfigHome: filepath.Join(root, "cfg"), StateHome: filepath.Join(root, "state"), CacheHome: filepath.Join(root, "cache")}
	if err := os.MkdirAll(ce.Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ce.Path(), []byte(testCfg), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(ce, builtin.FS())
	if err != nil {
		t.Fatal(err)
	}
	l, err := ledger.OpenPath(bg, filepath.Join(root, "state", "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	now := t0
	clock := func() time.Time { return now }
	l.SetClock(clock)
	proc := fakeProc{4242: t0.Add(-time.Hour)}
	d := &Deps{Ledger: l, Cfg: cfg, Now: clock, PID: 4242, Proc: proc}
	return &env{l: l, d: d, proc: proc, now: &now}
}

func (e *env) run(p Params) (Result, error) {
	if p.Lens == "" {
		p.Lens = "agent-workflow"
	}
	if p.Scope == "" {
		p.Scope = "demo"
	}
	if p.Duration == "" && p.From == "" {
		p.Duration = "24h"
	}
	return Run(bg, e.d, p)
}

func (e *env) count(t *testing.T, table string) int {
	t.Helper()
	var n int
	if err := e.l.DB().QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestWindowsOfAnyDurationBehaveIdentically(t *testing.T) {
	for _, d := range []string{"1h", "24h", "7d", "90d"} {
		e := setup(t)
		res, err := e.run(Params{Duration: d})
		if err != nil {
			t.Fatalf("%s: %v", d, err)
		}
		dur, _ := ParseDuration(d)
		if res.Run.State != ledger.RunFiled || res.Run.ItemsFiled != 0 {
			t.Errorf("%s: %+v", d, res.Run)
		}
		from, _ := time.Parse(time.RFC3339Nano, res.Run.WindowFrom)
		to, _ := time.Parse(time.RFC3339Nano, res.Run.WindowTo)
		if !to.Equal(t0) || to.Sub(from) != dur {
			t.Errorf("%s: window %s..%s", d, res.Run.WindowFrom, res.Run.WindowTo)
		}
		if _, err := e.l.GetLock(bg, "agent-workflow", "demo"); !errors.Is(err, ledger.ErrNotFound) {
			t.Errorf("%s: lock left behind: %v", d, err)
		}
	}
}

func TestResolveWindowRules(t *testing.T) {
	w, err := ResolveWindow("2026-03-10T00:00:00-05:00", "2026-03-11T00:00:00-05:00", "", "", t0)
	if err != nil || w.From != time.Date(2026, 3, 10, 5, 0, 0, 0, time.UTC) || w.To.Sub(w.From) != 24*time.Hour {
		t.Fatalf("offset window: %+v %v", w, err)
	}
	if w.FromText() != "2026-03-10T05:00:00Z" {
		t.Errorf("stored text %s", w.FromText())
	}
	w, err = ResolveWindow("", "", "2d", "2026-03-10T07:00:00+02:00", t0)
	if err != nil || !w.To.Equal(time.Date(2026, 3, 10, 5, 0, 0, 0, time.UTC)) || w.To.Sub(w.From) != 48*time.Hour {
		t.Errorf("at window: %+v %v", w, err)
	}
	bad := [][4]string{
		{"", "", "", ""},
		{"2026-03-10T00:00:00Z", "", "", ""},
		{"2026-03-10T00:00:00Z", "2026-03-11T00:00:00Z", "1h", ""},
		{"2026-03-11T00:00:00Z", "2026-03-10T00:00:00Z", "", ""},
		{"2026-03-10T00:00:00Z", "2026-03-10T00:00:00Z", "", ""},
		{"", "", "0s", ""},
		{"", "", "-1h", ""},
		{"", "", "soon", ""},
		{"", "", "1h", "yesterday"},
		{"2026-03-10T00:00:00Z", "2026-03-11T00:00:00Z", "", "2026-03-11T00:00:00Z"},
		{"x", "y", "", ""},
	}
	for _, b := range bad {
		if _, err := ResolveWindow(b[0], b[1], b[2], b[3], t0); err == nil {
			t.Errorf("accepted %v", b)
		}
	}
}

func TestUnconfiguredScopeIsUsageErrorBeforeAnyRow(t *testing.T) {
	e := setup(t)
	_, err := e.run(Params{Scope: "nope", RunKey: "k"})
	var ue *UsageError
	if !errors.As(err, &ue) || !contains(ue.Msg, "demo") {
		t.Fatalf("err = %v", err)
	}
	if e.count(t, "run") != 0 || e.count(t, "run_lock") != 0 {
		t.Error("rows written")
	}
	// Exit 2 wins over a busy lock.
	if _, err := e.run(Params{}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.run(Params{Lens: "nolens"}); !errors.As(err, &ue) {
		t.Errorf("unknown lens: %v", err)
	}
	if _, err := e.run(Params{Profile: "nope"}); !errors.As(err, &ue) {
		t.Errorf("unknown profile: %v", err)
	}
	if _, err := e.run(Params{Analyst: "robot"}); !errors.As(err, &ue) {
		t.Errorf("unknown analyst: %v", err)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && func() bool { return indexOf(s, sub) >= 0 }()
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestRunKeyIdempotency(t *testing.T) {
	e := setup(t)
	key := "agent-workflow/demo/24h/2026-03-10T12:00:00Z"
	first, err := e.run(Params{RunKey: key})
	if err != nil || first.Run.RunKey != key || first.Run.State != ledger.RunFiled {
		t.Fatalf("%+v %v", first.Run, err)
	}
	var af *AlreadyFiledError
	if _, err := e.run(Params{RunKey: key}); !errors.As(err, &af) {
		t.Fatalf("repeat: %v", err)
	}
	if e.count(t, "run") != 1 {
		t.Errorf("repeat wrote a row")
	}
	// A manual run over an already reviewed window runs.
	for i := 0; i < 2; i++ {
		if _, err := e.run(Params{}); err != nil {
			t.Fatalf("manual run %d: %v", i, err)
		}
	}
	// A key whose last run Failed starts a new row.
	if _, err := e.l.DB().Exec(`UPDATE run SET state='Failed' WHERE run_key=?`, key); err != nil {
		t.Fatal(err)
	}
	again, err := e.run(Params{RunKey: key})
	if err != nil || again.Run.ID == first.Run.ID {
		t.Fatalf("retry after failure: %+v %v", again.Run, err)
	}
}

func TestRunRecordsPidStartTimeProfileAndLabel(t *testing.T) {
	e := setup(t)
	res, err := e.run(Params{Profile: "health-check", ScheduleID: "sched-1", Analyst: "none"})
	if err != nil {
		t.Fatal(err)
	}
	r := res.Run
	if r.PID != 4242 || !r.StartedAt.Equal(t0.Add(-time.Hour)) || r.Profile != "health-check" || r.ScheduleID != "sched-1" || r.Analyst != "none" {
		t.Errorf("%+v", r)
	}
	res, _ = e.run(Params{})
	if res.Run.Profile != "deep-review" || res.Run.Analyst != "ccpool" {
		t.Errorf("default profile/analyst: %+v", res.Run)
	}
	res, _ = e.run(Params{EvidenceOnly: true})
	if res.Run.Analyst != "none" {
		t.Errorf("evidence-only analyst %s", res.Run.Analyst)
	}
}

func TestBusyLockWritesNoRow(t *testing.T) {
	e := setup(t)
	if err := e.l.WithTx(bg, func(tx *ledger.Tx) error {
		r, err := tx.InsertRun(bg, ledger.Run{Lens: "agent-workflow", Scope: "demo", PID: 4242, StartedAt: t0.Add(-time.Hour)})
		if err != nil {
			return err
		}
		return tx.AcquireLock(bg, "agent-workflow", "demo", r.ID, 4242, t0.Add(-time.Hour))
	}); err != nil {
		t.Fatal(err)
	}
	_, err := e.run(Params{})
	if !errors.Is(err, ledger.ErrLockBusy) {
		t.Fatalf("err = %v", err)
	}
	if e.count(t, "run") != 1 {
		t.Errorf("busy run wrote a row")
	}
	// Another scope's lock is independent.
	if _, err := e.l.DB().Exec(`DELETE FROM run_lock`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.run(Params{}); err != nil {
		t.Fatal(err)
	}
}

func addPending(t *testing.T, e *env, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if err := e.l.WithTx(bg, func(tx *ledger.Tx) error {
			_, err := tx.CreateTopic(bg, ledger.NewTopic{DefinedIn: "a.md", SubjectKey: "s" + strconv.Itoa(i), Kind: "stale", Lens: "agent-workflow"})
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLensSkipsWhenPendingCapReached(t *testing.T) {
	e := setup(t)
	addPending(t, e, 12)
	res, err := e.run(Params{})
	var sk *SkippedError
	if !errors.As(err, &sk) {
		t.Fatalf("err = %v", err)
	}
	r, _ := e.l.GetRun(bg, sk.RunID)
	if r.State != ledger.RunSkipped || r.SkipReason == "" || r.EndedAt.IsZero() || res.Run.ID != r.ID {
		t.Errorf("%+v", r)
	}
	if e.count(t, "topic") != 12 || e.count(t, "occurrence") != 0 {
		t.Error("a skipped run filed something")
	}
	if _, err := e.l.GetLock(bg, "agent-workflow", "demo"); !errors.Is(err, ledger.ErrNotFound) {
		t.Error("lock not released")
	}
}

func TestSkippedRunDoesNotBlockItsKey(t *testing.T) {
	e := setup(t)
	addPending(t, e, 12)
	var sk *SkippedError
	if _, err := e.run(Params{RunKey: "k"}); !errors.As(err, &sk) {
		t.Fatal(err)
	}
	if _, err := e.l.DB().Exec(`UPDATE topic SET state='Rejected'`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.run(Params{RunKey: "k"}); err != nil {
		t.Fatalf("retry after skip: %v", err)
	}
}

func TestLensPausesAfterThreeAllRejectedRuns(t *testing.T) {
	e := setup(t)
	for i := 0; i < 3; i++ {
		res, err := e.run(Params{})
		if err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		var tp ledger.Topic
		if err := e.l.WithTx(bg, func(tx *ledger.Tx) error {
			var err error
			tp, err = tx.CreateTopic(bg, ledger.NewTopic{DefinedIn: "a.md", SubjectKey: "p" + strconv.Itoa(i), Kind: "stale", Lens: "agent-workflow"})
			if err != nil {
				return err
			}
			return tx.AddOccurrence(bg, tp.ID, res.Run.ID, "demo", "agent-workflow", "", "")
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := e.l.Rule(bg, ledger.Ruling{TopicID: tp.ID, Ruling: ledger.RulingRejected}); err != nil {
			t.Fatal(err)
		}
	}
	var sk *SkippedError
	if _, err := e.run(Params{}); !errors.As(err, &sk) {
		t.Fatalf("fourth run: %v", err)
	}
	if p, _ := e.l.IsLensPaused(bg, "agent-workflow"); !p {
		t.Error("lens not paused in the ledger")
	}
	r, _ := e.l.GetRun(bg, sk.RunID)
	if r.State != ledger.RunSkipped || !contains(r.SkipReason, "paused") {
		t.Errorf("%+v", r)
	}
	// Another lens is unaffected.
	if _, err := e.run(Params{Lens: "config-health"}); err != nil {
		t.Errorf("other lens: %v", err)
	}
}

func TestRunPromotesWaitingTopicsAtStart(t *testing.T) {
	e := setup(t)
	if err := e.l.WithTx(bg, func(tx *ledger.Tx) error {
		_, err := tx.CreateTopic(bg, ledger.NewTopic{DefinedIn: "a.md", SubjectKey: "w", Kind: "stale", Lens: "agent-workflow", Waiting: true})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.run(Params{}); err != nil {
		t.Fatal(err)
	}
	c, _ := e.l.Counts(bg)
	if c.Waiting != 0 || c.Pending != 1 {
		t.Errorf("%+v", c)
	}
}

type failStage struct{}

func (failStage) Name() string                      { return "boom" }
func (failStage) Run(context.Context, *State) error { return errors.New("boom") }

func TestStageErrorFailsRunAndCleansUp(t *testing.T) {
	e := setup(t)
	staging := filepath.Join(t.TempDir(), "stage")
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatal(err)
	}
	var closed []string
	e.d.CloseSession = func(_ context.Context, id string) error { closed = append(closed, id); return nil }
	e.d.Evidence = stageFunc(func(ctx context.Context, s *State) error {
		return e.l.WithTx(ctx, func(tx *ledger.Tx) error {
			r, _ := tx.GetRun(ctx, s.Run.ID)
			r.StagingPath, r.SessionID = staging, "sess-1"
			return tx.UpdateRun(ctx, r)
		})
	})
	e.d.Analysts = map[string]Analyst{"ccpool": failAnalyst{}}
	_, err := e.run(Params{})
	if err == nil || err.Error() != "boom" {
		t.Fatalf("err = %v", err)
	}
	r, _ := e.l.GetRun(bg, 1)
	if r.State != ledger.RunFailed || r.SkipReason != "boom" {
		t.Errorf("%+v", r)
	}
	if _, err := os.Stat(staging); !os.IsNotExist(err) {
		t.Error("staging not deleted")
	}
	if len(closed) != 1 || closed[0] != "sess-1" {
		t.Errorf("closed = %v", closed)
	}
	if _, err := e.l.GetLock(bg, "agent-workflow", "demo"); !errors.Is(err, ledger.ErrNotFound) {
		t.Error("lock not released")
	}
}

type stageFunc func(context.Context, *State) error

func (stageFunc) Name() string                            { return "fn" }
func (f stageFunc) Run(c context.Context, s *State) error { return f(c, s) }

type failAnalyst struct{}

func (failAnalyst) Name() string                          { return "fail" }
func (failAnalyst) Analyse(context.Context, *State) error { return errors.New("boom") }

func TestInteractiveRunWaitsForAnalyst(t *testing.T) {
	e := setup(t)
	res, err := e.run(Params{Analyst: "interactive"})
	if err != nil || res.Run.State != ledger.RunAwaitingAnalyst {
		t.Fatalf("%+v %v", res.Run, err)
	}
	if _, err := e.l.GetLock(bg, "agent-workflow", "demo"); !errors.Is(err, ledger.ErrNotFound) {
		t.Error("lock held by a finished process")
	}
	// A later run's reaper leaves it alone before the TTL.
	if _, err := e.run(Params{}); err != nil {
		t.Fatal(err)
	}
	if r, _ := e.l.GetRun(bg, res.Run.ID); r.State != ledger.RunAwaitingAnalyst {
		t.Errorf("state %s", r.State)
	}
}

func seedRunning(t *testing.T, e *env, pid int, started time.Time, staging string) ledger.Run {
	t.Helper()
	var r ledger.Run
	if err := e.l.WithTx(bg, func(tx *ledger.Tx) error {
		var err error
		r, err = tx.InsertRun(bg, ledger.Run{Lens: "agent-workflow", Scope: "demo", PID: pid, StartedAt: started, StagingPath: staging, SessionID: "sess"})
		if err != nil {
			return err
		}
		return tx.AcquireLock(bg, r.Lens, r.Scope, r.ID, pid, started)
	}); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestReaperUsesStoredStartTime(t *testing.T) {
	e := setup(t)
	started := t0.Add(-2 * time.Hour)
	staging := filepath.Join(t.TempDir(), "stage")
	_ = os.MkdirAll(staging, 0o755)
	var closed []string
	e.d.CloseSession = func(_ context.Context, id string) error { closed = append(closed, id); return nil }

	// A dead pid: the row is Failed, its staging deleted, its session closed.
	r := seedRunning(t, e, 7001, started, staging)
	got, err := Reap(bg, e.d)
	if err != nil || len(got) != 1 || got[0].RunID != r.ID {
		t.Fatalf("%v %v", got, err)
	}
	cur, _ := e.l.GetRun(bg, r.ID)
	if cur.State != ledger.RunFailed || cur.EndedAt.IsZero() {
		t.Errorf("%+v", cur)
	}
	if _, err := os.Stat(staging); !os.IsNotExist(err) {
		t.Error("staging kept")
	}
	if len(closed) != 1 {
		t.Errorf("closed = %v", closed)
	}
	if _, err := e.l.GetLock(bg, "agent-workflow", "demo"); !errors.Is(err, ledger.ErrNotFound) {
		t.Error("lock kept")
	}

	// The same pid still alive with the stored start time is left alone.
	e.proc[7002] = started
	live := seedRunning(t, e, 7002, started, "")
	if got, _ := Reap(bg, e.d); len(got) != 0 {
		t.Errorf("reaped a live run: %v", got)
	}
	if c, _ := e.l.GetRun(bg, live.ID); c.State != ledger.RunRunning {
		t.Errorf("state %s", c.State)
	}

	// A recycled pid (alive, different start time) is not the original process.
	e.proc[7002] = started.Add(30 * time.Minute)
	if got, _ := Reap(bg, e.d); len(got) != 1 || got[0].RunID != live.ID {
		t.Errorf("recycled pid not reaped: %v", got)
	}
}

func TestReaperAwaitingAnalystTTL(t *testing.T) {
	e := setup(t)
	staging := filepath.Join(t.TempDir(), "stage")
	_ = os.MkdirAll(staging, 0o755)
	var r ledger.Run
	if err := e.l.WithTx(bg, func(tx *ledger.Tx) error {
		var err error
		r, err = tx.InsertRun(bg, ledger.Run{Lens: "agent-workflow", Scope: "demo", State: ledger.RunAwaitingAnalyst, StagingPath: staging})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	ttl := time.Duration(e.d.Cfg.Limits.InteractiveTTLMinutes) * time.Minute
	*e.now = t0.Add(ttl - time.Minute)
	if got, _ := Reap(bg, e.d); len(got) != 0 {
		t.Fatalf("reaped before the TTL: %v", got)
	}
	if _, err := os.Stat(staging); err != nil {
		t.Error("staging deleted early")
	}
	*e.now = t0.Add(ttl)
	got, err := Reap(bg, e.d)
	if err != nil || len(got) != 1 || got[0].RunID != r.ID {
		t.Fatalf("%v %v", got, err)
	}
	if c, _ := e.l.GetRun(bg, r.ID); c.State != ledger.RunFailed {
		t.Errorf("state %s", c.State)
	}
	if _, err := os.Stat(staging); !os.IsNotExist(err) {
		t.Error("staging kept after the TTL")
	}
}

func TestReaperReleasesOrphanLockAndStartsRun(t *testing.T) {
	e := setup(t)
	r := seedRunning(t, e, 7003, t0.Add(-time.Hour), "")
	// A new run reaps the dead holder first, then takes the lock.
	res, err := e.run(Params{})
	if err != nil {
		t.Fatal(err)
	}
	if old, _ := e.l.GetRun(bg, r.ID); old.State != ledger.RunFailed {
		t.Errorf("old run %s", old.State)
	}
	if res.Run.State != ledger.RunFiled {
		t.Errorf("%+v", res.Run)
	}
}

func TestRemoveStagingRefusesShallowPaths(t *testing.T) {
	for _, p := range []string{"/", "/tmp", "relative/dir", ""} {
		if err := removeStaging(p); err == nil {
			t.Errorf("accepted %q", p)
		}
	}
}

func TestPSInspectorSeesOwnProcessNotAGoneOne(t *testing.T) {
	var p PSInspector
	if _, ok := p.StartTime(0); ok {
		t.Error("pid 0 found")
	}
	if _, ok := p.StartTime(2147483000); ok {
		t.Error("absent pid found")
	}
	// A sandbox without a usable ps cannot read any start time: skip the
	// positive check there.
	if _, ok := p.StartTime(os.Getpid()); !ok {
		t.Skip("ps cannot read the test process (sandboxed build)")
	}
}
