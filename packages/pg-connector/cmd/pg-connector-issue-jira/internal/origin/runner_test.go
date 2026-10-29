package origin

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/groupsvc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/query"
)

var (
	_ Auth                = (*AuthState)(nil)
	_ query.Auth          = (*AuthState)(nil)
	_ groupsvc.AuthMarker = (*AuthState)(nil)
)

const text429 = "pjira search: status 429 Too Many Requests"

// fakeInner is the inner internal.Runner of the tests.
type fakeInner struct {
	mu     sync.Mutex
	calls  [][]string
	handle func(args []string) (string, error)
}

func (f *fakeInner) Run(_ context.Context, args ...string) (string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, args)
	h := f.handle
	f.mu.Unlock()
	if h != nil {
		return h(args)
	}
	return "{}", nil
}

func (f *fakeInner) Binary() string { return "/abs/pjira" }

func (f *fakeInner) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

type obsRec struct {
	mu      sync.Mutex
	results []string
}

func (o *obsRec) Request(result string, _ time.Duration) {
	o.mu.Lock()
	o.results = append(o.results, result)
	o.mu.Unlock()
}

func (o *obsRec) got() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.results...)
}

type authRec struct {
	marked, cleared int
}

func (a *authRec) MarkUnauthenticated(error) { a.marked++ }
func (a *authRec) Clear()                    { a.cleared++ }

type rig struct {
	clock *governor.ManualClock
	gov   *governor.Governor
	inner *fakeInner
	obs   *obsRec
	run   *Runner
	mu    sync.Mutex
	spent []governor.SpendEvent
}

type rigOpts struct {
	rate        int
	concurrency int
	pause       time.Duration
	jitter      func() float64
	retryAfter  func(string) (time.Duration, bool)
	auth        Auth
}

func newRig(t *testing.T, o rigOpts) *rig {
	t.Helper()
	if o.rate == 0 {
		o.rate = 2
	}
	if o.concurrency == 0 {
		o.concurrency = 2
	}
	if o.pause == 0 {
		o.pause = 60 * time.Second
	}
	if o.jitter == nil {
		o.jitter = func() float64 { return 0 }
	}
	r := &rig{
		clock: governor.NewManualClock(time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)),
		inner: &fakeInner{},
		obs:   &obsRec{},
	}
	cfg := config.Defaults()
	cfg.RequestRatePerSecond = o.rate
	g, err := governor.New(GovernorConfig(cfg), governor.Options{
		Clock: r.clock,
		OnSpend: func(e governor.SpendEvent) {
			r.mu.Lock()
			r.spent = append(r.spent, e)
			r.mu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("governor.New: %v", err)
	}
	r.gov = g
	run, err := New(Options{
		Inner: r.inner, Governor: g, Bucket: Bucket, Concurrency: o.concurrency,
		DefaultPause: o.pause, Clock: r.clock, Jitter: o.jitter, RetryAfter: o.retryAfter,
		Observer: r.obs, Auth: o.auth,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	r.run = run
	return r
}

func (r *rig) spends() []governor.SpendEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]governor.SpendEvent(nil), r.spent...)
}

func bg(ctx context.Context) context.Context {
	return WithCall(ctx, Call{Class: governor.ClassKeepAlive, Purpose: governor.PurposeRefresh, Group: "summary", BatchSize: 3})
}

func (r *rig) pauseLeft(t *testing.T) time.Duration {
	t.Helper()
	until, _, ok := r.run.Paused()
	if !ok {
		t.Fatal("no pause in force, want one")
	}
	return until.Sub(r.clock.Now())
}

// 429 on the next inner call(s).
func (r *rig) fail429() {
	r.inner.mu.Lock()
	r.inner.handle = func([]string) (string, error) { return "", errors.New(text429) }
	r.inner.mu.Unlock()
}

func (r *rig) succeed() {
	r.inner.mu.Lock()
	r.inner.handle = nil
	r.inner.mu.Unlock()
}

// ---- GovernorConfig -------------------------------------------------------

func TestGovernorConfig_OneRequestsBucketWithOneSecondWindow(t *testing.T) {
	cfg := config.Defaults()
	cfg.RequestRatePerSecond = 7
	cfg.RateReserveRequests = 13
	gc := GovernorConfig(cfg)
	if len(gc.Declared) != 1 {
		t.Fatalf("declared = %d buckets, want 1", len(gc.Declared))
	}
	s := gc.Declared[0]
	if s.Bucket != "requests" || s.Unit != "requests" {
		t.Errorf("bucket = %q unit %q, want requests/requests", s.Bucket, s.Unit)
	}
	if s.Limits.Cap != 7 || s.Limits.Reserve != 13 || s.Limits.Window != time.Second {
		t.Errorf("limits = %+v, want cap 7, reserve 13 (rate_reserve_requests), window 1s", s.Limits)
	}
	if gc.BackgroundShare != 0.9 {
		t.Errorf("background share = %v, want 0.9", gc.BackgroundShare)
	}
	if err := gc.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

// ---- New --------------------------------------------------------------------

func TestNew_RejectsAnUnusableConfiguration(t *testing.T) {
	g, _ := governor.New(GovernorConfig(config.Defaults()), governor.Options{})
	ok := Options{Inner: &fakeInner{}, Governor: g, Concurrency: 1, DefaultPause: time.Second}
	if _, err := New(ok); err != nil {
		t.Fatalf("valid options: %v", err)
	}
	for name, mut := range map[string]func(*Options){
		"no inner":         func(o *Options) { o.Inner = nil },
		"no governor":      func(o *Options) { o.Governor = nil },
		"zero concurrency": func(o *Options) { o.Concurrency = 0 },
		"zero pause":       func(o *Options) { o.DefaultPause = 0 },
	} {
		o := ok
		mut(&o)
		if _, err := New(o); err == nil {
			t.Errorf("%s: New accepted it", name)
		}
	}
}

func TestBinary_IsTheInnerRunnersConfiguredPath(t *testing.T) {
	r := newRig(t, rigOpts{})
	if got := r.run.Binary(); got != "/abs/pjira" {
		t.Errorf("Binary = %q, want the inner runner's", got)
	}
}

// ---- the request-rate cap is hard (T-11) -----------------------------------

func TestRateCap_ThirdCallInOneSecondOfATwoPerSecondBucketIsRefused(t *testing.T) {
	r := newRig(t, rigOpts{rate: 2})
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if _, err := r.run.Run(ctx, "issue", "--", "ABC-1"); err != nil {
			t.Fatalf("call %d: %v", i+1, err)
		}
	}
	_, err := r.run.Run(ctx, "issue", "--", "ABC-1")
	if !errors.Is(err, governor.ErrRefused) {
		t.Fatalf("third call err = %v, want a governor refusal", err)
	}
	var ref *governor.Refusal
	if !errors.As(err, &ref) || ref.Kind != governor.RefusedCap {
		t.Fatalf("refusal = %+v, want the cap", ref)
	}
	if n := r.inner.count(); n != 2 {
		t.Fatalf("inner ran %d times, want 2 (the refused call must not run pjira)", n)
	}
	// The window rolls: a second later the bucket admits again.
	r.clock.Advance(1100 * time.Millisecond)
	if _, err := r.run.Run(ctx, "issue", "--", "ABC-1"); err != nil {
		t.Fatalf("call after the window rolled: %v", err)
	}
	if got, want := r.obs.got(), []string{"ok", "ok", "refused", "ok"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("observer results = %v, want %v", got, want)
	}
}

// ---- the concurrency cap is hard (T-11) ------------------------------------

func TestConcurrencyCap_ThirdConcurrentCallWaitsUntilItsContextEnds(t *testing.T) {
	r := newRig(t, rigOpts{rate: 20, concurrency: 2})
	release := make(chan struct{})
	started := make(chan struct{}, 8)
	r.inner.handle = func([]string) (string, error) {
		started <- struct{}{}
		<-release
		return "{}", nil
	}
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = r.run.Run(context.Background(), "issue", "--", "ABC-1")
		}()
	}
	<-started
	<-started

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := r.run.Run(ctx, "issue", "--", "ABC-2")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("third concurrent call err = %v, want the context's deadline error", err)
	}
	if n := r.inner.count(); n != 2 {
		t.Fatalf("inner ran %d times, want 2 (the waiting call must not start)", n)
	}
	// The waiting call never reached the process: its ticket was released, not
	// settled, so only the two running calls hold a reservation and nothing is
	// spent yet.
	st, _ := r.gov.State().Bucket(Bucket)
	if st.InFlight != 2 || st.Spent != 0 {
		t.Errorf("in flight %d spent %d, want 2 and 0 while two run and one was released", st.InFlight, st.Spent)
	}
	close(release)
	wg.Wait()
	st, _ = r.gov.State().Bucket(Bucket)
	if st.InFlight != 0 || st.Spent != 2 {
		t.Errorf("after: in flight %d spent %d, want 0 and 2", st.InFlight, st.Spent)
	}
}

func TestConcurrencyCap_NeverMoreThanTheCapRunAtOnce(t *testing.T) {
	r := newRig(t, rigOpts{rate: 100, concurrency: 2})
	var cur, peak atomic.Int32
	r.inner.handle = func([]string) (string, error) {
		n := cur.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		cur.Add(-1)
		return "{}", nil
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := r.run.Run(context.Background(), "issue", "--", "ABC-1"); err != nil {
				t.Errorf("Run: %v", err)
			}
		}()
	}
	wg.Wait()
	if p := peak.Load(); p > 2 || p < 1 {
		t.Fatalf("peak concurrency = %d, want it capped at 2", p)
	}
}

func TestConcurrencyCap_SlotIsReleasedWhenTheProcessFails(t *testing.T) {
	r := newRig(t, rigOpts{rate: 100, concurrency: 1})
	r.inner.handle = func([]string) (string, error) { return "", errors.New("pjira: boom") }
	for i := 0; i < 4; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, err := r.run.Run(ctx, "issue", "--", "ABC-1")
		cancel()
		if errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("call %d waited for a slot that a failed call never released", i+1)
		}
		var ce *CallError
		if !errors.As(err, &ce) || ce.Kind != KindError {
			t.Fatalf("call %d err = %v, want a CallError of kind error", i+1, err)
		}
	}
}

// ---- 429 pause rules (T-11) -------------------------------------------------

func TestPause_OneRateLimitPausesBackgroundForTheDefault(t *testing.T) {
	r := newRig(t, rigOpts{})
	r.fail429()
	_, err := r.run.Run(bg(context.Background()), "search", "--jql", "x", "--all")
	if !IsRateLimited(err) {
		t.Fatalf("err = %v, want rate limited", err)
	}
	if got := r.pauseLeft(t); got != 60*time.Second {
		t.Fatalf("pause = %s, want the 60s default", got)
	}
	r.succeed()
	before := r.inner.count()
	_, err = r.run.Run(bg(context.Background()), "search", "--jql", "x", "--all")
	if !errors.Is(err, governor.ErrRefused) {
		t.Fatalf("background call during the pause err = %v, want the governor's refusal", err)
	}
	var ref *governor.Refusal
	if !errors.As(err, &ref) || ref.Kind != governor.RefusedPaused {
		t.Fatalf("refusal = %+v, want the paused kind", ref)
	}
	if r.inner.count() != before {
		t.Fatal("a background call ran during the pause")
	}
	// The pause ends at its time.
	r.clock.Advance(61 * time.Second)
	if _, _, ok := r.run.Paused(); ok {
		t.Fatal("still paused after the pause ended")
	}
	if _, err := r.run.Run(bg(context.Background()), "search", "--jql", "x", "--all"); err != nil {
		t.Fatalf("background call after the pause: %v", err)
	}
}

func TestPause_InteractiveCallDuringThePauseGetsErrPausedWithoutRunning(t *testing.T) {
	r := newRig(t, rigOpts{})
	r.fail429()
	_, _ = r.run.Run(bg(context.Background()), "search")
	r.succeed()
	before := r.inner.count()
	for name, ctx := range map[string]context.Context{
		"no call":     context.Background(),
		"interactive": WithCall(context.Background(), Call{Class: governor.ClassInteractive, Purpose: governor.PurposePassthrough}),
		"invalidated": WithCall(context.Background(), Call{Class: governor.ClassInvalidated, Purpose: governor.PurposeRefresh}),
	} {
		_, err := r.run.Run(ctx, "issue", "--", "ABC-1")
		if !errors.Is(err, ErrPaused) {
			t.Errorf("%s: err = %v, want ErrPaused", name, err)
			continue
		}
		var pe *PausedError
		if !errors.As(err, &pe) || !pe.Until.Equal(r.clock.Now().Add(60*time.Second)) {
			t.Errorf("%s: pause end = %+v, want now+60s named", name, pe)
		}
		if !strings.Contains(err.Error(), "2026-10-24T12:01:00Z") {
			t.Errorf("%s: %q does not name the pause end", name, err)
		}
		if errors.Is(err, governor.ErrRefused) {
			t.Errorf("%s: an interactive refusal must not be the governor's", name)
		}
	}
	if r.inner.count() != before {
		t.Fatal("an interactive call ran during the pause")
	}
	if got := r.obs.got(); got[len(got)-1] != "refused" {
		t.Errorf("observer last result = %q, want refused", got[len(got)-1])
	}
}

func TestPause_ConsecutiveRateLimitsDoubleWithJitterAndSuccessResets(t *testing.T) {
	jit := 0.0
	r := newRig(t, rigOpts{jitter: func() float64 { return jit }})
	r.fail429()
	call := func() error {
		_, err := r.run.Run(bg(context.Background()), "search")
		return err
	}
	// The doubling is exact when jitter is zero.
	for i, want := range []time.Duration{60 * time.Second, 120 * time.Second, 240 * time.Second, 480 * time.Second} {
		if err := call(); !IsRateLimited(err) {
			t.Fatalf("call %d err = %v, want rate limited", i+1, err)
		}
		if got := r.pauseLeft(t); got != want {
			t.Fatalf("pause %d = %s, want %s", i+1, got, want)
		}
		r.clock.Advance(want + time.Second)
	}
	// Jitter is added on top, up to a fifth of the pause, and the pause is
	// never shorter than the doubled base.
	jit = 0.5
	if err := call(); !IsRateLimited(err) {
		t.Fatal(err)
	}
	if got, want := r.pauseLeft(t), 960*time.Second+96*time.Second; got != want {
		t.Fatalf("pause with jitter 0.5 = %s, want %s (960s + 10%%)", got, want)
	}
	r.clock.Advance(2000 * time.Second)
	jit = 0.999999
	r.succeed()
	if err := call(); err != nil {
		t.Fatalf("success: %v", err)
	}
	// The first non-429 call reset the doubling: the next 429 pauses for the
	// base again.
	jit = 0
	r.fail429()
	r.clock.Advance(time.Second)
	if err := call(); !IsRateLimited(err) {
		t.Fatal(err)
	}
	if got := r.pauseLeft(t); got != 60*time.Second {
		t.Fatalf("pause after a success = %s, want the 60s base again", got)
	}
}

func TestPause_JitterNeverExceedsAFifth(t *testing.T) {
	r := newRig(t, rigOpts{jitter: func() float64 { return 0.9999999 }})
	r.fail429()
	_, _ = r.run.Run(bg(context.Background()), "search")
	left := r.pauseLeft(t)
	if left < 60*time.Second || left > 72*time.Second {
		t.Fatalf("pause = %s, want within [60s, 72s]", left)
	}
}

func TestPause_NonRateLimitFailureResetsTheDoubling(t *testing.T) {
	r := newRig(t, rigOpts{})
	r.fail429()
	call := func() { _, _ = r.run.Run(bg(context.Background()), "search") }
	call()
	r.clock.Advance(61 * time.Second)
	r.inner.mu.Lock()
	r.inner.handle = func([]string) (string, error) { return "", errors.New("pjira: boom") }
	r.inner.mu.Unlock()
	call()
	r.clock.Advance(time.Second)
	r.fail429()
	call()
	if got := r.pauseLeft(t); got != 60*time.Second {
		t.Fatalf("pause = %s, want the base after a non-429 failure", got)
	}
}

func TestPause_RefusedCallDoesNotResetTheDoubling(t *testing.T) {
	r := newRig(t, rigOpts{})
	r.fail429()
	call := func() error {
		_, err := r.run.Run(bg(context.Background()), "search")
		return err
	}
	_ = call()
	r.succeed()
	// Refused during the pause (never reached the process), and an interactive
	// ErrPaused too.
	if err := call(); !errors.Is(err, governor.ErrRefused) {
		t.Fatalf("err = %v, want a refusal", err)
	}
	if _, err := r.run.Run(context.Background(), "issue"); !errors.Is(err, ErrPaused) {
		t.Fatalf("err = %v, want ErrPaused", err)
	}
	r.clock.Advance(61 * time.Second)
	r.fail429()
	_ = call()
	if got := r.pauseLeft(t); got != 120*time.Second {
		t.Fatalf("pause = %s, want 120s: refused calls must not reset the doubling", got)
	}
}

func TestPause_StatedRetryAfterIsAFloorAndTheDoublingBase(t *testing.T) {
	stated := map[string]time.Duration{"retry-after=30": 30 * time.Second, "retry-after=500": 500 * time.Second}
	r := newRig(t, rigOpts{retryAfter: func(text string) (time.Duration, bool) {
		for k, d := range stated {
			if strings.Contains(text, k) {
				return d, true
			}
		}
		return 0, false
	}})
	fail := func(extra string) {
		r.inner.mu.Lock()
		r.inner.handle = func([]string) (string, error) { return "", errors.New(text429 + " " + extra) }
		r.inner.mu.Unlock()
		_, _ = r.run.Run(bg(context.Background()), "search")
	}
	fail("retry-after=30")
	if got := r.pauseLeft(t); got != 30*time.Second {
		t.Fatalf("first pause = %s, want the stated 30s (not the 60s default)", got)
	}
	r.clock.Advance(31 * time.Second)
	fail("retry-after=30")
	if got := r.pauseLeft(t); got != 60*time.Second {
		t.Fatalf("second pause = %s, want twice the first base (60s)", got)
	}
	r.clock.Advance(61 * time.Second)
	fail("retry-after=500")
	if got := r.pauseLeft(t); got != 500*time.Second {
		t.Fatalf("third pause = %s, want at least the stated 500s (doubling alone gives 120s)", got)
	}
	r.clock.Advance(501 * time.Second)
	fail("no stated delay")
	if got := r.pauseLeft(t); got != 240*time.Second {
		t.Fatalf("fourth pause = %s, want the doubled base 240s when none is stated", got)
	}
}

func TestPause_DoublingStopsAtMaxPause(t *testing.T) {
	r := newRig(t, rigOpts{pause: 40 * time.Minute})
	r.fail429()
	call := func() { _, _ = r.run.Run(bg(context.Background()), "search") }
	call()
	if got := r.pauseLeft(t); got != 40*time.Minute {
		t.Fatalf("first = %s", got)
	}
	r.clock.Advance(41 * time.Minute)
	call()
	if got := r.pauseLeft(t); got != MaxPause {
		t.Fatalf("second = %s, want the %s ceiling (40m doubled is 80m)", got, MaxPause)
	}
	if MaxPause != time.Hour {
		t.Fatalf("MaxPause = %s, want one hour", MaxPause)
	}
}

func TestPause_OnlyTheMatchedMarkerIsARateLimit(t *testing.T) {
	r := newRig(t, rigOpts{})
	r.inner.handle = func([]string) (string, error) { return "", errors.New("pjira: issue PROJ-429 not found") }
	_, err := r.run.Run(bg(context.Background()), "issue", "--", "PROJ-429")
	if IsRateLimited(err) {
		t.Fatal("an issue key PROJ-429 read as a throttle")
	}
	if _, _, ok := r.run.Paused(); ok {
		t.Fatal("a non-429 failure paused the background")
	}
}

// ---- settlement, release, observer, auth ------------------------------------

func TestSettle_EachCallSettlesCostOneWithItsOutcomeAndCallFields(t *testing.T) {
	r := newRig(t, rigOpts{rate: 10})
	ctx := WithCall(context.Background(), Call{Class: governor.ClassKeepAlive, Purpose: governor.PurposeRefresh, Group: "summary", BatchSize: 4})
	_, _ = r.run.Run(ctx, "search")
	r.inner.handle = func([]string) (string, error) { return "", errors.New("pjira: issue ABC-1: status 401 Unauthorized") }
	_, _ = r.run.Run(WithCall(context.Background(), Call{Purpose: governor.PurposePassthrough}), "issue", "--", "ABC-1")
	r.inner.handle = func([]string) (string, error) { return "", errors.New("pjira: boom") }
	_, _ = r.run.Run(context.Background(), "issue", "--", "ABC-1")
	r.fail429()
	_, _ = r.run.Run(bg(context.Background()), "search")

	sp := r.spends()
	want := []struct {
		outcome string
		purpose governor.Purpose
		group   string
		batch   int
	}{
		{"ok", governor.PurposeRefresh, "summary", 4},
		{"unauthenticated", governor.PurposePassthrough, "", 0},
		{"error", governor.PurposeInteractive, "", 0},
		{"rate_limited", governor.PurposeRefresh, "summary", 3},
	}
	if len(sp) != len(want) {
		t.Fatalf("recorded %d spends, want %d: %+v", len(sp), len(want), sp)
	}
	for i, w := range want {
		e := sp[i]
		if e.Cost != 1 || e.Outcome != w.outcome || e.Purpose != w.purpose || e.Group != w.group || e.BatchSize != w.batch || e.Bucket != Bucket {
			t.Errorf("spend %d = %+v, want cost 1 outcome %s purpose %s group %q batch %d", i, e, w.outcome, w.purpose, w.group, w.batch)
		}
	}
	if sp[0].Class != governor.ClassKeepAlive || sp[1].Class != governor.ClassInteractive {
		t.Errorf("classes = %v, %v", sp[0].Class, sp[1].Class)
	}
}

func TestSettle_RefusedCallRecordsNothing(t *testing.T) {
	r := newRig(t, rigOpts{rate: 1})
	_, _ = r.run.Run(context.Background(), "issue")
	if _, err := r.run.Run(context.Background(), "issue"); !errors.Is(err, governor.ErrRefused) {
		t.Fatalf("err = %v, want refusal", err)
	}
	if n := len(r.spends()); n != 1 {
		t.Fatalf("%d spends recorded, want only the call that ran", n)
	}
	if st, _ := r.gov.State().Bucket(Bucket); st.InFlight != 0 {
		t.Fatalf("in flight = %d after a refusal", st.InFlight)
	}
}

func TestObserver_IsToldEachResult(t *testing.T) {
	r := newRig(t, rigOpts{rate: 10})
	r.inner.handle = func([]string) (string, error) { return "{}", nil }
	_, _ = r.run.Run(context.Background(), "a")
	r.inner.handle = func([]string) (string, error) { return "", errors.New("pjira: boom") }
	_, _ = r.run.Run(context.Background(), "b")
	r.inner.handle = func([]string) (string, error) { return "", errors.New("status 403 Forbidden") }
	_, _ = r.run.Run(context.Background(), "c")
	r.fail429()
	_, _ = r.run.Run(bg(context.Background()), "d")
	_, _ = r.run.Run(bg(context.Background()), "e") // paused: refused
	want := "ok,error,unauthenticated,rate_limited,refused"
	if got := strings.Join(r.obs.got(), ","); got != want {
		t.Fatalf("results = %s, want %s", got, want)
	}
}

func TestAuth_FailureMarksAndSuccessClears(t *testing.T) {
	a := NewAuthState()
	r := newRig(t, rigOpts{rate: 10, auth: a})
	r.inner.handle = func([]string) (string, error) { return "", errors.New("pjira: issue ABC-1: status 401 Unauthorized") }
	_, err := r.run.Run(context.Background(), "issue", "--", "ABC-1")
	if !IsUnauthenticated(err) {
		t.Fatalf("err = %v, want unauthenticated", err)
	}
	if bad, e := a.Unauthenticated(); !bad || e == nil {
		t.Fatalf("auth = %v, %v, want marked with the error", bad, e)
	}
	// A plain failure neither clears nor marks it.
	r.inner.handle = func([]string) (string, error) { return "", errors.New("pjira: boom") }
	_, _ = r.run.Run(context.Background(), "issue", "--", "ABC-1")
	if bad, _ := a.Unauthenticated(); !bad {
		t.Fatal("a non-auth failure cleared the marker")
	}
	r.inner.handle = nil
	if _, err := r.run.Run(context.Background(), "issue", "--", "ABC-1"); err != nil {
		t.Fatal(err)
	}
	if bad, e := a.Unauthenticated(); bad || e != nil {
		t.Fatalf("auth = %v, %v, want cleared by the success", bad, e)
	}
}

func TestAuthMarker_OptionIsCalled(t *testing.T) {
	rec := &authRec{}
	r := newRig(t, rigOpts{rate: 10, auth: rec})
	_, _ = r.run.Run(context.Background(), "a")
	r.inner.handle = func([]string) (string, error) { return "", errors.New("status 401 Unauthorized") }
	_, _ = r.run.Run(context.Background(), "b")
	if rec.cleared != 1 || rec.marked != 1 {
		t.Fatalf("cleared %d marked %d, want 1 and 1", rec.cleared, rec.marked)
	}
}

func TestAuthState_IdentityAndZeroValue(t *testing.T) {
	a := NewAuthState()
	if bad, e := a.Unauthenticated(); bad || e != nil {
		t.Fatalf("new state = %v, %v", bad, e)
	}
	if a.Identity() != "" {
		t.Fatalf("identity = %q, want empty when unknown", a.Identity())
	}
	a.SetIdentity("someone")
	if a.Identity() != "someone" {
		t.Fatalf("identity = %q", a.Identity())
	}
	boom := errors.New("x")
	a.MarkUnauthenticated(boom)
	if bad, e := a.Unauthenticated(); !bad || e != boom {
		t.Fatalf("marked state = %v, %v", bad, e)
	}
	a.Clear()
	if bad, _ := a.Unauthenticated(); bad {
		t.Fatal("Clear did not clear")
	}
	if a.Identity() != "someone" {
		t.Fatal("Clear must not forget the identity")
	}
}

// ---- typed helpers and the Backend over the origin --------------------------

func TestTypedHelpers_ArgsAndDecode(t *testing.T) {
	r := newRig(t, rigOpts{rate: 10})
	r.inner.handle = func(args []string) (string, error) {
		if args[0] == "search" {
			return `{"items":[{"key":"ABC-1"}],"truncated":true}`, nil
		}
		return `{"key":"ABC-9","summary":"s"}`, nil
	}
	res, err := r.run.Search(context.Background(), "project = ABC", []string{"changelog", "comments"})
	if err != nil || !res.Truncated || len(res.Items) != 1 {
		t.Fatalf("Search = %+v, %v", res, err)
	}
	if _, err := r.run.Search(context.Background(), "project = ABC", nil); err != nil {
		t.Fatal(err)
	}
	iss, err := r.run.Issue(context.Background(), "ABC-9")
	if err != nil || iss.Key != "ABC-9" {
		t.Fatalf("Issue = %+v, %v", iss, err)
	}
	got := [][]string{r.inner.calls[0], r.inner.calls[1], r.inner.calls[2]}
	want := [][]string{
		{"search", "--jql", "project = ABC", "--all", "--expand", "changelog,comments"},
		{"search", "--jql", "project = ABC", "--all"},
		{"issue", "--", "ABC-9"},
	}
	for i := range want {
		if fmt.Sprint(got[i]) != fmt.Sprint(want[i]) {
			t.Errorf("call %d args = %q, want %q", i, got[i], want[i])
		}
	}
	r.inner.handle = func([]string) (string, error) { return "not json", nil }
	if _, err := r.run.Search(context.Background(), "x", nil); err == nil {
		t.Error("Search accepted undecodable output")
	}
	if _, err := r.run.Issue(context.Background(), "ABC-9"); err == nil {
		t.Error("Issue accepted undecodable output")
	}
}

func TestBackendOverTheOriginRunsGoverned(t *testing.T) {
	r := newRig(t, rigOpts{rate: 1})
	r.inner.handle = func([]string) (string, error) { return `{"key":"ABC-1","summary":"s"}`, nil }
	b := internal.New(r.run)
	if _, err := b.Show(context.Background(), "ABC-1"); err != nil {
		t.Fatalf("Show: %v", err)
	}
	if _, err := b.Show(context.Background(), "ABC-1"); err == nil {
		t.Fatal("a second Show in the same second passed a 1-per-second bucket")
	}
	if n := len(r.spends()); n != 1 {
		t.Fatalf("%d spends, want 1", n)
	}
}

// ---- the inner runner passes exactly the configured environment -------------

func TestInnerRunner_ExecsByAbsolutePathWithOnlyThePjiraEnv(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "pjira")
	body := "#!/bin/sh\nexec /usr/bin/env\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PG_ORIGIN_LEAK_PROBE", "leaked")
	t.Setenv("JIRA_API_TOKEN", "leaked-too")

	cfg := config.Defaults()
	cfg.PjiraPath = script
	cfg.PjiraEnv = map[string]string{"JIRA_BASE_URL": "https://example.invalid", "JIRA_EMAIL": "a@example.invalid"}
	inner := NewCLIRunner(cfg)

	g, err := governor.New(GovernorConfig(cfg), governor.Options{})
	if err != nil {
		t.Fatal(err)
	}
	run, err := New(Options{Inner: inner, Governor: g, Concurrency: 1, DefaultPause: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if run.Binary() != script || !filepath.IsAbs(run.Binary()) {
		t.Fatalf("Binary = %q, want the absolute pjira_path %q", run.Binary(), script)
	}
	out, err := run.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var got []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		k, _, _ := strings.Cut(line, "=")
		switch k {
		case "PWD", "SHLVL", "_", "OLDPWD": // set by the shell the script starts under
			continue
		}
		got = append(got, line)
	}
	sort.Strings(got)
	want := []string{"JIRA_BASE_URL=https://example.invalid", "JIRA_EMAIL=a@example.invalid"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("pjira saw environment %q, want exactly %q", got, want)
	}
}

func TestInnerRunner_EmptyEnvIsNotInherited(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "pjira")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexec /usr/bin/env\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PG_ORIGIN_LEAK_PROBE", "leaked")
	cfg := config.Defaults()
	cfg.PjiraPath = script
	inner := NewCLIRunner(cfg)
	if inner.Env == nil {
		t.Fatal("Env is nil: the process environment would be inherited")
	}
	out, err := inner.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "PG_ORIGIN_LEAK_PROBE") {
		t.Fatalf("an empty pjira_env inherited the process environment: %q", out)
	}
}

func TestNotFoundIsItsOwnKindNotAnOriginError(t *testing.T) {
	r := newRig(t, rigOpts{rate: 100})
	r.inner.handle = func([]string) (string, error) { return "", errors.New("pjira: issue zr-abc12 not found") }
	_, err := r.run.Run(context.Background(), "issue", "--", "zr-abc12")
	var ce *CallError
	if !errors.As(err, &ce) || ce.Kind != KindNotFound {
		t.Fatalf("err = %v, want a CallError of kind not_found", err)
	}
}
