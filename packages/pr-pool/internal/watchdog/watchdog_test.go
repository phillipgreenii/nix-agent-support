package watchdog

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/phillipgreenii/pr-pool/internal/budget"
	"github.com/phillipgreenii/pr-pool/internal/ccpool"
	"github.com/phillipgreenii/pr-pool/internal/eventlog"
	"github.com/phillipgreenii/pr-pool/internal/usage"
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
}

func (f *fakeCC) Ensure(context.Context, string, string, string, map[string]string) error {
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
	return nil
}
func (f *fakeCC) List(context.Context) ([]ccpool.Session, error) { return f.list, nil }

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
