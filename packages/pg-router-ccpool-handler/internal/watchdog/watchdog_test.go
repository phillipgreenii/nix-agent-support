package watchdog

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/budget"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/ccpool"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/eventlog"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/usage"
)

type fakeReader struct {
	seq []usage.Snapshot
	i   int
}

func (f *fakeReader) Read(context.Context, string) (usage.Snapshot, error) {
	s := f.seq[min(f.i, len(f.seq)-1)]
	f.i++
	return s, nil
}

type fakeCC struct {
	sent    []string // "<flag>:<prompt>"
	cancels int
	closed  []string
	list    []ccpool.Session
	ops     []string // "meta:<key>" / "close", in call order
}

func (f *fakeCC) Ensure(context.Context, string, string, string, map[string]string, map[string]string) error {
	return nil
}

func (f *fakeCC) Send(_ context.Context, _, prompt string, m ccpool.SendMode) error {
	flag := "queue"
	f.sent = append(f.sent, flag+":"+prompt)
	return nil
}
func (f *fakeCC) Cancel(context.Context, string) error { f.cancels++; return nil }
func (f *fakeCC) Close(_ context.Context, n string, _ bool) error {
	f.closed = append(f.closed, n)
	f.ops = append(f.ops, "close")
	return nil
}
func (f *fakeCC) List(context.Context) ([]ccpool.Session, error) { return f.list, nil }

func (f *fakeCC) Capacity(context.Context) (ccpool.Capacity, error) {
	return ccpool.Capacity{Free: 1}, nil
}

func (f *fakeCC) SetMeta(_ context.Context, _, key, _ string) error {
	f.ops = append(f.ops, "meta:"+key)
	return nil
}

type recBD struct{ calls []string }

func (r *recBD) Run(_ context.Context, args ...string) (string, error) {
	out := ""
	for i, a := range args {
		if i > 0 {
			out += " "
		}
		out += a
	}
	r.calls = append(r.calls, out)
	return "", nil
}

func (r *recBD) has(s string) bool {
	for _, c := range r.calls {
		if c == s {
			return true
		}
	}
	return false
}

func tokBudget(maxTok budget.Limit) budget.Budget {
	return budget.Budget{
		Tokens:     maxTok,
		Thresholds: budget.Thresholds{Reminder: 0.725, Cancel: 0.90, Hard: 1.00},
		Prices:     usage.DefaultPrices(),
	}
}

func newWD(r usage.Reader, cc ccpool.Runner, bd *recBD, b budget.Budget) *Watchdog {
	return &Watchdog{
		Reader: r, CC: cc, BD: bd, Budget: b,
		RepoRoot: "/repo", WorktreeDir: "/wt",
		ReminderMsg: "near limit", WrapUpMsg: "wrap up now",
		Git: noopGit{}, Now: func() time.Time { return time.Unix(0, 0) }, Poll: time.Millisecond,
	}
}

type noopGit struct{}

func (noopGit) Run(context.Context, string, ...string) error { return nil }

func TestRun_firesEachLevelOnceThenHardStop(t *testing.T) {
	// token cap 1000; ramp crosses 72.5% -> 90% -> 100%
	r := &fakeReader{seq: []usage.Snapshot{{OutputTokens: 700}, {OutputTokens: 730}, {OutputTokens: 920}, {OutputTokens: 1000}}}
	cc := &fakeCC{list: []ccpool.Session{{ExternalID: "s", Live: true, CWD: "/repo"}}}
	bd := &recBD{}
	wd := newWD(r, cc, bd, tokBudget(1000))
	err := wd.Run(context.Background(), "s", "zr-1")
	if !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("want ErrBudgetExceeded, got %v", err)
	}
	// one queued reminder, one wrap-up; 2 cancels (90% + 100%)
	if len(cc.sent) != 2 {
		t.Errorf("want reminder+wrapup queued, got %v", cc.sent)
	}
	if cc.cancels != 2 {
		t.Errorf("want 2 cancels (90%% + 100%%), got %d", cc.cancels)
	}
	// terminal: comment + unclaim (NOT human)
	if !bd.has("comment zr-1 interrupted — budget") && !hasPrefix(bd.calls, "comment zr-1") {
		t.Errorf("missing budget note; calls=%v", bd.calls)
	}
	if !bd.has("update zr-1 --status=open --assignee=") {
		t.Errorf("hard stop must unclaim; calls=%v", bd.calls)
	}
	for _, c := range bd.calls {
		if c == "update zr-1 --add-label human" {
			t.Errorf("hard stop must NOT add human")
		}
	}
}

// pg2-yukh #3a: the budget reminder a worker receives MUST name the bead so a
// context-less model cannot guess a different target. Assert the queued reminder
// contains the bead id and does NOT contain the ambiguous bare phrase "this bead".
func TestRun_reminderIsBeadExplicit(t *testing.T) {
	// Ramp crosses 72.5% (reminder) then on to 100% (hard stop) so Run terminates.
	r := &fakeReader{seq: []usage.Snapshot{{OutputTokens: 730}, {OutputTokens: 1000}}}
	cc := &fakeCC{list: []ccpool.Session{{ExternalID: "s", Live: true, CWD: "/repo"}}}
	bd := &recBD{}
	wd := newWD(r, cc, bd, tokBudget(1000))
	// Use the bead-explicit template form (set by config in Step 3).
	wd.ReminderMsg = "You are nearing your budget for bead {{.BeadID}} — start wrapping up: record progress with bd comment {{.BeadID}}."
	_ = wd.Run(context.Background(), "s", "zr-6bq.3")
	if len(cc.sent) == 0 {
		t.Fatal("expected a queued reminder")
	}
	got := cc.sent[0] // "queue:<prompt>"
	if !strings.Contains(got, "zr-6bq.3") {
		t.Errorf("reminder must name the bead; got %q", got)
	}
	if strings.Contains(got, "this bead") {
		t.Errorf("reminder must not use the ambiguous 'this bead'; got %q", got)
	}
}

// pg2-yukh #3b: when the model has NOT started a turn (the dropped-nudge case),
// the watchdog MUST NOT send the reminder or wrap-up — those would be the model's
// FIRST prompt, the exact incident. It may still hard-stop the budget (that path
// unclaims, it does not nudge).
func TestRun_noReminderBeforeFirstModelTurn(t *testing.T) {
	r := &fakeReader{seq: []usage.Snapshot{{OutputTokens: 700}, {OutputTokens: 730}, {OutputTokens: 920}, {OutputTokens: 1000}}}
	cc := &fakeCC{list: []ccpool.Session{{ExternalID: "s", Live: true, CWD: "/repo"}}}
	bd := &recBD{}
	wd := newWD(r, cc, bd, tokBudget(1000))
	// Model never started a turn ⇒ no first-turn signal.
	wd.FirstTurnStarted = func(string) bool { return false }
	err := wd.Run(context.Background(), "s", "zr-1")
	if !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("hard stop must still fire, got %v", err)
	}
	// No reminder/wrap-up was QUEUED to the model (those are the harmful first prompt).
	for _, s := range cc.sent {
		t.Errorf("no model nudge may be sent before the first turn; got %q", s)
	}
}

func TestRun_reminderFiresAfterFirstModelTurn(t *testing.T) {
	r := &fakeReader{seq: []usage.Snapshot{{OutputTokens: 730}, {OutputTokens: 1000}}}
	cc := &fakeCC{list: []ccpool.Session{{ExternalID: "s", Live: true, CWD: "/repo"}}}
	bd := &recBD{}
	wd := newWD(r, cc, bd, tokBudget(1000))
	wd.FirstTurnStarted = func(string) bool { return true } // a turn happened
	_ = wd.Run(context.Background(), "s", "zr-1")
	if len(cc.sent) == 0 {
		t.Error("reminder must fire once the model has taken a turn")
	}
}

func TestRun_ctxCancelReturnsCtxErr(t *testing.T) {
	r := &fakeReader{seq: []usage.Snapshot{{OutputTokens: 0}}}
	cc := &fakeCC{list: []ccpool.Session{{ExternalID: "s", Live: true}}}
	wd := newWD(r, cc, &recBD{}, tokBudget(1000))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := wd.Run(ctx, "s", "zr-1"); !errors.Is(err, context.Canceled) {
		t.Errorf("want context.Canceled, got %v", err)
	}
}

// pg2-c1vp / review follow-up: when the hard stop fires but ClaimTerminal reports
// the bead-poll already owns the outcome, Run must run NO terminal bead mutation
// and exit with ctx.Err() once cancelled — the mirror of the orchestrator's
// TestWaitDone_lostRace_* tests for the watchdog side of the race.
func TestRun_lostClaimSkipsTerminal(t *testing.T) {
	r := &fakeReader{seq: []usage.Snapshot{{OutputTokens: 5000}}} // immediately >100% of a 1000 cap
	cc := &fakeCC{list: []ccpool.Session{{ExternalID: "s", Live: true, CWD: "/repo"}}}
	bd := &recBD{}
	wd := newWD(r, cc, bd, tokBudget(1000))
	wd.ClaimTerminal = func() bool { return false } // bead-poll already owns the terminal outcome

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- wd.Run(ctx, "s", "zr-1") }()
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("lost-claim Run should return ctx.Err(), got %v", err)
	}
	if hasPrefix(bd.calls, "update zr-1 --status=open") || hasPrefix(bd.calls, "comment zr-1") {
		t.Errorf("lost claim must NOT mutate the bead; calls=%v", bd.calls)
	}
	if cc.cancels != 0 || len(cc.closed) != 0 {
		t.Errorf("lost claim must NOT run the terminal ccpool sequence; cancels=%d closed=%v", cc.cancels, cc.closed)
	}
}

func hasPrefix(calls []string, p string) bool {
	for _, c := range calls {
		if len(c) >= len(p) && c[:len(p)] == p {
			return true
		}
	}
	return false
}

// TestRun_emitsEventsWhenLogSet verifies that when a Watchdog has Log set, the
// reminder, cancel, and hard_stop events are actually written to the JSONL file.
// Prior to this fix no test set Log, so the emit path was untested end-to-end.
func TestRun_emitsEventsWhenLogSet(t *testing.T) {
	// token ramp: crosses 72.5% -> 90% -> 100%
	r := &fakeReader{seq: []usage.Snapshot{
		{OutputTokens: 730},
		{OutputTokens: 920},
		{OutputTokens: 1000},
	}}
	cc := &fakeCC{list: []ccpool.Session{{ExternalID: "s", Live: true, CWD: "/repo"}}}
	bd := &recBD{}

	logPath := t.TempDir() + "/events.jsonl"
	lw, err := eventlog.New(logPath)
	if err != nil {
		t.Fatalf("eventlog.New: %v", err)
	}
	defer func() { _ = lw.Close() }()

	wd := newWD(r, cc, bd, tokBudget(1000))
	wd.Git = noopGit{}
	wd.Log = lw

	runErr := wd.Run(context.Background(), "s", "zr-1")
	if !errors.Is(runErr, ErrBudgetExceeded) {
		t.Fatalf("want ErrBudgetExceeded, got %v", runErr)
	}
	_ = lw.Close()

	// parse all JSONL lines and collect event kinds
	f, err := os.Open(logPath)
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	defer func() { _ = f.Close() }()

	kinds := map[string]int{}
	levelByKind := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var rec map[string]any
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			t.Fatalf("bad JSON line %q: %v", sc.Text(), err)
		}
		// Every record must carry the standard fields and no legacy ts.
		for _, k := range []string{"time", "level", "msg"} {
			if _, ok := rec[k]; !ok {
				t.Errorf("record missing required field %q: %v", k, rec)
			}
		}
		if _, ok := rec["ts"]; ok {
			t.Errorf("legacy ts field must be gone: %v", rec)
		}
		if k, ok := rec["kind"].(string); ok {
			kinds[k]++
			if lvl, ok := rec["level"].(string); ok {
				levelByKind[k] = lvl
			}
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}

	for _, want := range []string{"reminder", "cancel", "hard_stop"} {
		if kinds[want] == 0 {
			t.Errorf("expected %q event in log; got kinds=%v", want, kinds)
		}
	}
	wantLevels := map[string]string{"reminder": "info", "cancel": "warn", "hard_stop": "error"}
	for kind, want := range wantLevels {
		if got := levelByKind[kind]; got != want {
			t.Errorf("kind %q level = %q, want %q", kind, got, want)
		}
	}
}

// pg2-irowq: the hard-stop error text and the hard_stop eventlog record carry
// role/pool/bead/session/limit/used/cap/elapsed, and the message starts with
// the stable sentinel "session budget exceeded".
func TestRun_budgetErrorCarriesContext(t *testing.T) {
	cases := []struct {
		name      string
		b         budget.Budget
		snap      usage.Snapshot
		limit     string
		used, cap float64
		elapsed   time.Duration
	}{
		{"tokens", tokBudget(1000), usage.Snapshot{OutputTokens: 1000}, "tokens", 1000, 1000, 0},
		{"time", budget.Budget{Time: 25 * time.Minute, Thresholds: budget.Thresholds{Reminder: 0.725, Cancel: 0.9, Hard: 1}}, usage.Snapshot{}, "time", 1500, 1500, 25 * time.Minute},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logPath := t.TempDir() + "/events.jsonl"
			lw, err := eventlog.New(logPath)
			if err != nil {
				t.Fatal(err)
			}
			cc := &fakeCC{list: []ccpool.Session{{ExternalID: "s", Live: true, CWD: "/repo"}}}
			wd := newWD(&fakeReader{seq: []usage.Snapshot{tc.snap}}, cc, &recBD{}, tc.b)
			wd.Log, wd.Role, wd.Pool = lw, "worker", "pool-a"
			var now time.Time = time.Unix(0, 0)
			wd.Now = func() time.Time { t := now; now = now.Add(tc.elapsed); return t }
			runErr := wd.Run(context.Background(), "s", "zr-1")
			_ = lw.Close()
			if !errors.Is(runErr, ErrBudgetExceeded) {
				t.Fatalf("errors.Is(ErrBudgetExceeded) must hold, got %v", runErr)
			}
			msg := runErr.Error()
			if !strings.HasPrefix(msg, "session budget exceeded") {
				t.Errorf("sentinel must lead the message: %q", msg)
			}
			for _, want := range []string{"role=worker", "pool=pool-a", "bead=zr-1", "session=s", "limit=" + tc.limit, "used=", "cap=", "elapsed="} {
				if !strings.Contains(msg, want) {
					t.Errorf("message %q missing %q", msg, want)
				}
			}
			var be *BudgetError
			if !errors.As(runErr, &be) || string(be.Limit) != tc.limit || be.Used != tc.used || be.Cap != tc.cap {
				t.Errorf("BudgetError = %+v", be)
			}
			raw, _ := os.ReadFile(logPath)
			var hard map[string]any
			for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
				var rec map[string]any
				if json.Unmarshal([]byte(line), &rec) == nil && rec["kind"] == "hard_stop" {
					hard = rec
				}
			}
			if hard == nil {
				t.Fatalf("no hard_stop record in %s", raw)
			}
			for k, want := range map[string]any{"role": "worker", "pool": "pool-a", "bead": "zr-1", "session": "s", "limit": tc.limit, "used": tc.used, "cap": tc.cap} {
				if hard[k] != want {
					t.Errorf("hard_stop[%q] = %v, want %v", k, hard[k], want)
				}
			}
			if _, ok := hard["elapsed"]; !ok {
				t.Errorf("hard_stop missing elapsed: %v", hard)
			}
			if strings.Contains(msg, "prompt") {
				t.Errorf("no prompt content allowed: %q", msg)
			}
		})
	}
}

// Watchdog.Start: elapsed time is measured from the supplied start (an absorbed
// session's recorded launch time), not from when Run begins (bead pg2-g2u9m,
// INV-CCH-18). With a 25m time budget and a launch 30 minutes in the past, the
// very first poll is already over budget and hard-stops; with the zero Start
// the same run begins its clock "now" and does not.
func TestRun_startFromLaunchTimeHardStopsImmediately(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	timeBudget := budget.Budget{
		Time:       25 * time.Minute,
		Thresholds: budget.Thresholds{Reminder: 0.725, Cancel: 0.90, Hard: 1.00},
	}
	r := &fakeReader{seq: []usage.Snapshot{{}}}
	cc := &fakeCC{list: []ccpool.Session{{ExternalID: "s", Live: true, CWD: "/repo"}}}
	bd := &recBD{}
	wd := newWD(r, cc, bd, timeBudget)
	wd.Now = func() time.Time { return now }
	wd.Start = now.Add(-30 * time.Minute)
	if err := wd.Run(context.Background(), "s", "zr-1"); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("a session launched 30m ago is over a 25m budget on the first poll; got %v", err)
	}
	var be *BudgetError
	_ = errors.As(wd.Run(context.Background(), "s", "zr-1"), &be)
	if be == nil || be.Limit != budget.LimitTime || be.Elapsed != 30*time.Minute {
		t.Errorf("BudgetError = %+v, want time limit with 30m elapsed", be)
	}
}

func TestRun_zeroStartMeasuresFromNow(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	timeBudget := budget.Budget{
		Time:       25 * time.Minute,
		Thresholds: budget.Thresholds{Reminder: 0.725, Cancel: 0.90, Hard: 1.00},
	}
	r := &fakeReader{seq: []usage.Snapshot{{}}}
	cc := &fakeCC{list: []ccpool.Session{{ExternalID: "s", Live: true, CWD: "/repo"}}}
	wd := newWD(r, cc, &recBD{}, timeBudget)
	wd.Now = func() time.Time { return now } // frozen: elapsed stays 0
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := wd.Run(ctx, "s", "zr-1"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("zero Start must not hard-stop a fresh run, got %v", err)
	}
}

// HardStop drives the same terminal sequence for a session nobody is metering.
func TestHardStop_runsTerminalSequence(t *testing.T) {
	cc := &fakeCC{list: []ccpool.Session{{ExternalID: "s", Live: true, CWD: "/elsewhere"}}}
	bd := &recBD{}
	wd := newWD(&fakeReader{seq: []usage.Snapshot{{}}}, cc, bd, budget.Budget{})
	be := &BudgetError{Role: "worker", Pool: "default", Bead: "zr-1", Session: "s", Limit: budget.LimitTime, Used: 1800, Cap: 1500, Elapsed: 30 * time.Minute}
	HardStop(context.Background(), wd, "s", "zr-1", be)
	if len(cc.closed) != 1 || cc.closed[0] != "s" {
		t.Errorf("hard stop must close the session, closed=%v", cc.closed)
	}
	if cc.cancels != 1 {
		t.Errorf("hard stop issues the 2nd cancel, got %d", cc.cancels)
	}
	if !bd.has("update zr-1 --status=open --assignee=") {
		t.Errorf("hard stop must unclaim; calls=%v", bd.calls)
	}
}
