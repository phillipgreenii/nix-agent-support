package watchdog

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/phillipgreenii/pr-pool/internal/budget"
	"github.com/phillipgreenii/pr-pool/internal/ccpool"
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

func (f *fakeCC) Ensure(context.Context, string, string, map[string]string) error { return nil }
func (f *fakeCC) Send(_ context.Context, _, prompt string, m ccpool.SendMode) error {
	flag := "queue"
	f.sent = append(f.sent, flag+":"+prompt)
	return nil
}
func (f *fakeCC) Cancel(context.Context, string) error { f.cancels++; return nil }
func (f *fakeCC) Close(_ context.Context, n string) error {
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
	cc := &fakeCC{list: []ccpool.Session{{Name: "s", Live: true, CWD: "/repo"}}}
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
	cc := &fakeCC{list: []ccpool.Session{{Name: "s", Live: true}}}
	wd := newWD(r, cc, &recBD{}, tokBudget(1000))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := wd.Run(ctx, "s", "zr-1"); !errors.Is(err, context.Canceled) {
		t.Errorf("want context.Canceled, got %v", err)
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
