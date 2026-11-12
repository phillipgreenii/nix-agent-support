package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/builtin"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/ccpool"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/ledger"
)

const goldenDir = "../../testdata/review-item"

// goldenEvidence is a test evidence stage: it copies the synthetic golden run
// into an evidence directory and a staging directory, as the real stage does.
type goldenEvidence struct{}

func (goldenEvidence) Name() string { return "evidence" }

func (goldenEvidence) Run(ctx context.Context, s *State) error {
	ev := filepath.Join(s.Deps.Cfg.Env.CacheHome, "ev")
	if err := copyTree(goldenDir, ev); err != nil {
		return err
	}
	st, err := makeStaging()
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(filepath.Join(goldenDir, "raw.txt"))
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(st, "raw.txt"), raw, 0o600); err != nil {
		return err
	}
	return updateRun(ctx, s, func(r *ledger.Run) { r.EvidenceDir, r.StagingPath = ev, st })
}

func copyTree(src, dst string) error {
	return filepath.Walk(src, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if fi.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
	})
}

// fakeCcpool is the seam fake. Its capacity never exits 8; start and reply
// can.
type fakeCcpool struct {
	capacity   ccpool.Capacity
	startErr   error
	replyErrs  []error  // per reply call; nil entries succeed
	finals     []string // final message per turn (index = turn number)
	unknownFor int      // result polls that report unknown before the answer
	pending    bool     // the turn never finishes
	state      string
	onReply    func(n int, prompt string)

	started   []ccpool.StartOpts
	prompts   []string
	closed    []string
	cancelled []string
	polls     int
}

func (f *fakeCcpool) Capacity(context.Context) (ccpool.Capacity, error) { return f.capacity, nil }
func (f *fakeCcpool) Start(_ context.Context, o ccpool.StartOpts) error {
	f.started = append(f.started, o)
	return f.startErr
}

func (f *fakeCcpool) Reply(_ context.Context, _, p string) (string, error) {
	n := len(f.prompts)
	f.prompts = append(f.prompts, p)
	if f.onReply != nil {
		f.onReply(n, p)
	}
	if n < len(f.replyErrs) && f.replyErrs[n] != nil {
		return "", f.replyErrs[n]
	}
	return fmt.Sprintf("turn-%d", n), nil
}

func (f *fakeCcpool) Result(_ context.Context, turn string) (string, ccpool.ResultStatus, error) {
	f.polls++
	if f.pending {
		return "", ccpool.ResultPending, nil
	}
	if f.polls <= f.unknownFor {
		return "", ccpool.ResultUnknown, nil
	}
	var n int
	_, _ = fmt.Sscanf(turn, "turn-%d", &n)
	return f.finals[n], ccpool.ResultDone, nil
}

func (f *fakeCcpool) State(context.Context, string) (string, error) {
	if f.state == "" {
		return ccpool.StateWorking, nil
	}
	return f.state, nil
}

func (f *fakeCcpool) Cancel(_ context.Context, id string) error {
	f.cancelled = append(f.cancelled, id)
	return nil
}

func (f *fakeCcpool) Close(_ context.Context, id, _ string) error {
	f.closed = append(f.closed, id)
	return nil
}

type ccEnv struct {
	*env
	fake *fakeCcpool
}

const ccCfg = `
[[scope]]
name = "demo-scope"
prefixes = ["/work/demo"]
`

func setupCcpool(t *testing.T, cfgText string, minutes int) *ccEnv {
	t.Helper()
	root := t.TempDir()
	ce := config.Env{ConfigHome: filepath.Join(root, "cfg"), StateHome: filepath.Join(root, "state"), CacheHome: filepath.Join(root, "cache")}
	if err := os.MkdirAll(ce.Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ce.Path(), []byte(cfgText), 0o644); err != nil {
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
	fake := &fakeCcpool{capacity: ccpool.Capacity{Free: 2}}
	sleep := func(ctx context.Context, d time.Duration) error {
		now = now.Add(d)
		return ctx.Err()
	}
	d := &Deps{
		Ledger: l, Cfg: cfg, Now: clock, PID: 4242, Proc: proc, Evidence: goldenEvidence{},
		Analysts: map[string]Analyst{"ccpool": &CcpoolAnalyst{Client: fake, Sleep: sleep}},
		CloseSession: func(_ context.Context, id string) error {
			fake.closed = append(fake.closed, id)
			return nil
		},
	}
	return &ccEnv{env: &env{l: l, d: d, proc: proc, now: &now}, fake: fake}
}

func (e *ccEnv) runLens(t *testing.T) (Result, error) {
	t.Helper()
	return Run(bg, e.d, Params{Lens: "agent-workflow", Scope: "demo-scope", Duration: "24h", Analyst: "ccpool"})
}

func goldenFinal(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(goldenDir, "findings.json"))
	if err != nil {
		t.Fatal(err)
	}
	return "Here are the findings.\n```json\n" + strings.TrimSpace(string(b)) + "\n```\n"
}

func lastRun(t *testing.T, e *ccEnv) ledger.Run {
	t.Helper()
	runs, err := e.l.RunsInStates(bg, ledger.RunFiled, ledger.RunFailed, ledger.RunSkipped, ledger.RunRunning)
	if err != nil || len(runs) == 0 {
		t.Fatalf("runs: %v %v", runs, err)
	}
	return runs[len(runs)-1]
}

func TestConfigFromHandWrittenFile(t *testing.T) {
	e := setupCcpool(t, ccCfg, 0)
	if !e.d.Cfg.ConfigPresent {
		t.Fatal("the config.toml was not read")
	}
	e.fake.finals = []string{goldenFinal(t)}
	res, err := e.runLens(t)
	if err != nil {
		t.Fatal(err)
	}
	r := res.Run
	if r.State != ledger.RunFiled || r.ItemsFiled != 1 || r.Analyst != "ccpool" {
		t.Fatalf("%+v", r)
	}
	if r.Model == "" || r.Profile == "" || r.PromptHash == "" {
		t.Errorf("model, profile and prompt hash must be recorded: %+v", r)
	}
	if len(e.fake.prompts) != 1 || r.RepairCount != 0 {
		t.Errorf("prompts %d repairs %d", len(e.fake.prompts), r.RepairCount)
	}
	if len(e.fake.closed) != 1 || e.fake.closed[0] != fmt.Sprint(r.ID) {
		t.Errorf("session not closed: %v", e.fake.closed)
	}
	if r.StagingPath != "" {
		t.Errorf("staging kept: %s", r.StagingPath)
	}
	if _, err := e.l.GetLock(bg, "agent-workflow", "demo-scope"); !errors.Is(err, ledger.ErrNotFound) {
		t.Errorf("lock left behind: %v", err)
	}
}

func TestBuiltInDefaultsDriveTheSession(t *testing.T) {
	e := setupCcpool(t, ccCfg, 0)
	e.fake.finals = []string{goldenFinal(t)}
	if _, err := e.runLens(t); err != nil {
		t.Fatal(err)
	}
	o := e.fake.started[0]
	want := []string{"Read", "Grep", "Glob", AgentQTool}
	if strings.Join(o.AllowedTools, ",") != strings.Join(want, ",") {
		t.Errorf("tools %v, want %v", o.AllowedTools, want)
	}
	neutral := filepath.Join(e.d.Cfg.Env.StateHome, "pg-review", "analyst-cwd")
	if o.Cwd != neutral {
		t.Errorf("cwd %q, want %q", o.Cwd, neutral)
	}
	if fi, err := os.Stat(neutral); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("neutral dir: %v %v", fi, err)
	}
	if o.PermissionMode != "dontAsk" || o.ExternalID == "" {
		t.Errorf("%+v", o)
	}
	var sawRun bool
	for _, kv := range o.Env {
		if kv == RunEnvVar+"="+o.ExternalID {
			sawRun = true
		}
	}
	if !sawRun {
		t.Errorf("run env missing: %v", o.Env)
	}
}

func TestMalformedFinalMessageGetsOneRepairReply(t *testing.T) {
	e := setupCcpool(t, ccCfg, 0)
	e.fake.finals = []string{"no findings here", goldenFinal(t)}
	res, err := e.runLens(t)
	if err != nil {
		t.Fatal(err)
	}
	if len(e.fake.prompts) != 2 || res.Run.RepairCount != 1 || res.Run.State != ledger.RunFiled {
		t.Fatalf("prompts %d, %+v", len(e.fake.prompts), res.Run)
	}
	if !strings.Contains(e.fake.prompts[1], "parse") {
		t.Errorf("repair reply should name the parse failure:\n%s", e.fake.prompts[1])
	}

	// A second malformed message fails the run and stores nothing partial.
	e2 := setupCcpool(t, ccCfg, 0)
	bad := strings.Replace(goldenFinal(t), `"end": true`, `"end": false`, 1)
	e2.fake.finals = []string{bad, bad}
	_, err = e2.runLens(t)
	if err == nil || !strings.Contains(err.Error(), "after one repair") {
		t.Fatalf("want a rejection after one repair, got %v", err)
	}
	if len(e2.fake.prompts) != 2 {
		t.Errorf("prompts %d, want 2 (no third message)", len(e2.fake.prompts))
	}
	if r := lastRun(t, e2); r.State != ledger.RunFailed || r.RepairCount != 1 {
		t.Errorf("%+v", r)
	}
	if n := e2.count(t, "topic"); n != 0 {
		t.Errorf("%d topics stored from a rejected run", n)
	}
}

func TestValidationFailureRepairListsEveryFailure(t *testing.T) {
	e := setupCcpool(t, ccCfg, 0)
	bad := strings.Replace(goldenFinal(t), `"kind": "wasted-retries"`, `"kind": "not-a-kind"`, 1)
	e.fake.finals = []string{bad, goldenFinal(t)}
	if _, err := e.runLens(t); err != nil {
		t.Fatal(err)
	}
	if len(e.fake.prompts) != 2 || !strings.Contains(e.fake.prompts[1], "not-a-kind") {
		t.Errorf("repair prompt should carry the offending token:\n%v", e.fake.prompts[1:])
	}
}

func TestResultExit1AloneIsNotTerminal(t *testing.T) {
	e := setupCcpool(t, ccCfg, 0)
	e.fake.finals = []string{goldenFinal(t)}
	e.fake.unknownFor = 4
	res, err := e.runLens(t)
	if err != nil || res.Run.State != ledger.RunFiled {
		t.Fatalf("%v %+v", err, res.Run)
	}
	if e.fake.polls != 5 {
		t.Errorf("polls %d, want 5", e.fake.polls)
	}
}

func TestErroredSessionFailsTheRun(t *testing.T) {
	e := setupCcpool(t, ccCfg, 0)
	e.fake.finals = []string{""}
	e.fake.unknownFor = 100
	e.fake.state = ccpool.StateError
	_, err := e.runLens(t)
	if err == nil || !strings.Contains(err.Error(), "errored") {
		t.Fatalf("%v", err)
	}
	if r := lastRun(t, e); r.State != ledger.RunFailed {
		t.Errorf("%+v", r)
	}
	if len(e.fake.closed) == 0 {
		t.Error("session not closed")
	}
}

func TestTimeLimitCancelsRunAndClosesSession(t *testing.T) {
	e := setupCcpool(t, ccCfg, 0)
	e.fake.pending = true
	_, err := e.runLens(t)
	if err == nil || !strings.Contains(err.Error(), "time limit") {
		t.Fatalf("%v", err)
	}
	r := lastRun(t, e)
	if r.State != ledger.RunFailed {
		t.Errorf("%+v", r)
	}
	if len(e.fake.cancelled) != 1 || len(e.fake.closed) == 0 {
		t.Errorf("cancelled %v closed %v", e.fake.cancelled, e.fake.closed)
	}
	if _, err := os.Stat(r.StagingPath); r.StagingPath == "" || !os.IsNotExist(err) {
		t.Errorf("staging directory %q survives: %v", r.StagingPath, err)
	}
	// The deep-review profile allows 45 minutes: the clock moved that far.
	if got := (*e.now).Sub(t0); got < 45*time.Minute {
		t.Errorf("clock advanced %s", got)
	}
	if _, err := e.l.GetLock(bg, "agent-workflow", "demo-scope"); !errors.Is(err, ledger.ErrNotFound) {
		t.Errorf("lock left behind: %v", err)
	}
}

func TestStagingIsDeletedWhenTheRunFails(t *testing.T) {
	e := setupCcpool(t, ccCfg, 0)
	e.fake.pending = true
	var staging string
	e.d.Evidence = stageSpy{inner: goldenEvidence{}, out: &staging}
	if _, err := e.runLens(t); err == nil {
		t.Fatal("want a failure")
	}
	if staging == "" {
		t.Fatal("staging path not seen")
	}
	if _, err := os.Stat(staging); !os.IsNotExist(err) {
		t.Errorf("staging directory survives: %v", err)
	}
}

type stageSpy struct {
	inner Stage
	out   *string
}

func (s stageSpy) Name() string { return "evidence" }
func (s stageSpy) Run(ctx context.Context, st *State) error {
	if err := s.inner.Run(ctx, st); err != nil {
		return err
	}
	*s.out = st.Run.StagingPath
	return nil
}

func TestUsageLimitGateExit8RecordsSkippedRun(t *testing.T) {
	limit := &ccpool.UsageLimit{Window: "5h", UsedPct: 100, ResetsAt: t0.Add(2 * time.Hour)}
	cases := map[string]func(f *fakeCcpool){
		"capacity free 0 with a usage limit": func(f *fakeCcpool) { f.capacity = ccpool.Capacity{Free: 0, UsageLimit: limit} },
		"new exits 8":                        func(f *fakeCcpool) { f.startErr = &ccpool.ExitError{Cmd: "new", Code: 8} },
		"reply exits 8":                      func(f *fakeCcpool) { f.replyErrs = []error{&ccpool.ExitError{Cmd: "reply", Code: 8}} },
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			e := setupCcpool(t, ccCfg, 0)
			setup(e.fake)
			_, err := e.runLens(t)
			var sk *SkippedError
			if !errors.As(err, &sk) || !strings.Contains(sk.Reason, "usage limit") {
				t.Fatalf("want a skipped run for the usage limit, got %v", err)
			}
			r := lastRun(t, e)
			if r.State != ledger.RunSkipped || !strings.Contains(r.SkipReason, "usage limit") {
				t.Errorf("%+v", r)
			}
			if n := e.count(t, "topic"); n != 0 {
				t.Errorf("%d topics", n)
			}
			if name == "capacity free 0 with a usage limit" && len(e.fake.started) != 0 {
				t.Error("a session was started although the pool is at its usage limit")
			}
			if len(e.fake.started) > 0 && len(e.fake.closed) == 0 {
				t.Error("started session not closed")
			}
		})
	}
}

func TestBusyReplyRecordsSkippedRun(t *testing.T) {
	e := setupCcpool(t, ccCfg, 0)
	e.fake.replyErrs = []error{&ccpool.ExitError{Cmd: "reply", Code: 5}}
	_, err := e.runLens(t)
	var sk *SkippedError
	if !errors.As(err, &sk) || sk.Reason != "ccpool busy" {
		t.Fatalf("%v", err)
	}
	if r := lastRun(t, e); r.State != ledger.RunSkipped {
		t.Errorf("%+v", r)
	}
}

func TestScratchTreeModificationFailsTheRun(t *testing.T) {
	e := setupCcpool(t, ccCfg, 0)
	e.fake.finals = []string{goldenFinal(t)}
	e.fake.onReply = func(int, string) {
		for _, kv := range e.fake.started[0].Env {
			if v, ok := strings.CutPrefix(kv, ScratchEnvVar+"="); ok {
				_ = os.WriteFile(filepath.Join(v, "stray.txt"), []byte("x"), 0o644)
			}
		}
	}
	_, err := e.runLens(t)
	if err == nil || !strings.Contains(err.Error(), "scratch worktree") {
		t.Fatalf("%v", err)
	}
	if r := lastRun(t, e); r.State != ledger.RunFailed {
		t.Errorf("%+v", r)
	}
	if n := e.count(t, "topic"); n != 0 {
		t.Errorf("%d topics stored", n)
	}
}

func TestConfigDirectoryChangeFailsTheRun(t *testing.T) {
	e := setupCcpool(t, ccCfg, 0)
	e.fake.finals = []string{goldenFinal(t)}
	e.fake.onReply = func(int, string) {
		_ = os.WriteFile(filepath.Join(e.d.Cfg.Env.Dir(), "extra.toml"), []byte("x"), 0o644)
	}
	if _, err := e.runLens(t); err == nil || !strings.Contains(err.Error(), "configuration directory") {
		t.Fatalf("%v", err)
	}
}

func TestPlantedSecretInFinalMessageIsRedactedBeforeStorage(t *testing.T) {
	e := setupCcpool(t, ccCfg, 0)
	final := strings.Replace(goldenFinal(t), `"problem": "`, `"problem": "The key `+fakeSecret+` appears. `, 1)
	if final == goldenFinal(t) {
		t.Fatal("secret not planted")
	}
	e.fake.finals = []string{final}
	res, err := e.runLens(t)
	if err != nil {
		t.Fatal(err)
	}
	if res.Run.State != ledger.RunFiled {
		t.Fatalf("%+v", res.Run)
	}
	rows, err := e.l.DB().Query(`SELECT name FROM sqlite_master WHERE type='table'`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		tables = append(tables, n)
	}
	rows.Close()
	for _, tb := range tables {
		r, err := e.l.DB().Query(`SELECT * FROM ` + tb)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := r.Columns()
		for r.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			_ = r.Scan(ptrs...)
			if strings.Contains(fmt.Sprint(vals...), "ABCDEFGHIJKLMNOPQRSTUVWX") {
				t.Errorf("table %s stores the planted secret", tb)
			}
		}
		_ = r.Close()
	}
}

func TestSIGTERMClosesTheSessionAndFailsTheRun(t *testing.T) {
	e := setupCcpool(t, ccCfg, 0)
	e.fake.pending = true
	ctx, cancel := context.WithCancel(bg)
	e.d.Analysts["ccpool"] = &CcpoolAnalyst{Client: e.fake, Sleep: func(ctx context.Context, _ time.Duration) error {
		cancel()
		return ctx.Err()
	}}
	_, err := Run(ctx, e.d, Params{Lens: "agent-workflow", Scope: "demo-scope", Duration: "24h", Analyst: "ccpool"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("%v", err)
	}
	r := lastRun(t, e)
	if r.State != ledger.RunFailed || len(e.fake.closed) == 0 {
		t.Errorf("%+v closed %v", r, e.fake.closed)
	}
}
