package origin

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
)

func ptr[T any](v T) *T { return &v }

// readingInner plays pjira: each run answers with the next scripted response,
// reporting the reading (when it has one) the way CLIRunner does.
type readingInner struct {
	mu    sync.Mutex
	steps []step
	calls int
}

type step struct {
	rl  *internal.RateLimit
	err error
}

func (f *readingInner) Run(ctx context.Context, _ ...string) (string, error) {
	f.mu.Lock()
	var s step
	if f.calls < len(f.steps) {
		s = f.steps[f.calls]
	}
	f.calls++
	f.mu.Unlock()
	if s.rl != nil {
		internal.ReportRateLimit(ctx, *s.rl)
	}
	return "{}", s.err
}

func (f *readingInner) Binary() string { return "/abs/pjira" }

type floorRig struct {
	clock *governor.ManualClock
	gov   *governor.Governor
	inner *readingInner
	run   *Runner
}

func newFloorRig(t *testing.T, floor int, steps ...step) *floorRig {
	t.Helper()
	cfg := config.Defaults()
	cfg.RequestRatePerSecond = 50 // far above the test's own spend: only the floor can refuse
	cfg.RateReserveRequests = floor
	clk := governor.NewManualClock(time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC))
	gov, err := governor.New(GovernorConfig(cfg), governor.Options{Clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	inner := &readingInner{steps: steps}
	run, err := New(Options{
		Inner: inner, Governor: gov, Concurrency: 2, DefaultPause: 60 * time.Second, Clock: clk,
		Jitter: func() float64 { return 0 }, Floor: int64(floor),
	})
	if err != nil {
		t.Fatal(err)
	}
	return &floorRig{clock: clk, gov: gov, inner: inner, run: run}
}

func rl200(limit, remaining int64) step {
	return step{rl: &internal.RateLimit{Status: 200, Limit: ptr(limit), Remaining: ptr(remaining), NearLimit: ptr(remaining*5 < limit)}}
}

func isReserveRefusal(err error) (*governor.Refusal, bool) {
	var r *governor.Refusal
	if errors.As(err, &r) && r.Kind == governor.RefusedReserve {
		return r, true
	}
	return nil, false
}

// The floor of the bead's design: with a reading that leaves fewer than the
// floor, the next request of EVERY class is refused, naming the reserve, until
// the burst window of the reading has passed.
func TestFloor_RefusesBelowTheFloorForEveryClassUntilTheWindowPasses(t *testing.T) {
	r := newFloorRig(t, 20, rl200(100, 21))
	// First request: no reading yet, admitted; its response says 21 remain.
	if _, err := r.run.Run(bg(context.Background()), "search"); err != nil {
		t.Fatal(err)
	}
	// 21 remain; this request would leave 21 - 1 = 20, which is not below the floor of 20: admitted.
	r.inner.steps = append(r.inner.steps, rl200(100, 20))
	if _, err := r.run.Run(bg(context.Background()), "search"); err != nil {
		t.Fatalf("a request leaving exactly the floor must be admitted: %v", err)
	}
	// The origin now says 20 remain: another request would leave 19, below the floor.
	for _, ctx := range []context.Context{
		bg(context.Background()), // background (class 4)
		context.Background(),     // interactive (class 1)
		WithCall(context.Background(), Call{Class: governor.ClassInvalidated, Purpose: governor.PurposeWrite}),
	} {
		_, err := r.run.Run(ctx, "search")
		ref, ok := isReserveRefusal(err)
		if !ok {
			t.Fatalf("want a reserve refusal, got %v", err)
		}
		if ref.Limit != 20 || !strings.Contains(ref.Reason, "below reserve") || !strings.Contains(ref.Reason, "reserve 20") {
			t.Errorf("refusal = %+v, want it to name the reserve of 20", ref)
		}
		if !ref.RetryAt.After(r.clock.Now()) {
			t.Errorf("RetryAt = %v, want a time after now so a scheduler holds for the window only", ref.RetryAt)
		}
	}
	if r.inner.calls != 2 {
		t.Errorf("a refused request must not reach pjira: calls = %d, want 2", r.inner.calls)
	}
	// The reading is one burst window old: the bucket is full again.
	r.clock.Advance(BurstWindow + time.Millisecond)
	r.inner.steps = append(r.inner.steps, rl200(100, 90))
	if _, err := r.run.Run(bg(context.Background()), "search"); err != nil {
		t.Fatalf("after the window the floor must lift: %v", err)
	}
}

func TestFloor_ARequestWithNoReadingYetIsNeverRefusedByTheFloor(t *testing.T) {
	r := newFloorRig(t, 20)
	if _, err := r.run.Run(bg(context.Background()), "search"); err != nil {
		t.Fatalf("no reading, no floor: %v", err)
	}
	if _, ok := r.run.Readings().Latest(); ok {
		t.Error("a response with no reading must leave the holder empty")
	}
}

func TestFloor_ZeroHoldsNothingBackButStopsAtNoneLeft(t *testing.T) {
	r := newFloorRig(t, 0, rl200(100, 1), rl200(100, 0))
	for i := 0; i < 2; i++ {
		if _, err := r.run.Run(bg(context.Background()), "search"); err != nil {
			t.Fatalf("call %d with floor 0 and remaining 1 then 0: %v", i, err)
		}
	}
	// The origin says none is left: even a floor of 0 does not send into a certain 429.
	if _, err := r.run.Run(bg(context.Background()), "search"); err == nil {
		t.Fatal("remaining 0 must refuse even with a floor of 0")
	}
}

// A tenant whose own limit is at or below the floor would be refused forever;
// its readings are shown but never enforced.
func TestFloor_ALimitAtOrBelowTheFloorIsNotEnforced(t *testing.T) {
	r := newFloorRig(t, 20, rl200(20, 3), rl200(20, 3), rl200(20, 3))
	for i := 0; i < 3; i++ {
		if _, err := r.run.Run(bg(context.Background()), "search"); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	l, ok := r.run.Readings().Latest()
	if !ok || *l.Remaining != 3 || *l.Limit != 20 {
		t.Errorf("the reading must still be recorded for status and gauges: %+v ok=%v", l, ok)
	}
}

// A response with a limit but no remaining count cannot drive admission.
func TestFloor_AReadingWithoutRemainingDoesNotDriveAdmission(t *testing.T) {
	r := newFloorRig(t, 20, step{rl: &internal.RateLimit{Status: 200, Limit: ptr(int64(100)), NearLimit: ptr(false)}})
	if _, err := r.run.Run(bg(context.Background()), "search"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.run.Run(bg(context.Background()), "search"); err != nil {
		t.Fatalf("no remaining count, no enforcement: %v", err)
	}
	if l, ok := r.run.Readings().Latest(); !ok || l.Limit == nil || l.Remaining != nil {
		t.Errorf("holder = %+v ok=%v", l, ok)
	}
}

// The recorded spend event carries the origin's remaining, so events.jsonl
// shows how close each call ran.
func TestFloor_TheSpendEventCarriesTheOriginsRemaining(t *testing.T) {
	cfg := config.Defaults()
	clk := governor.NewManualClock(time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC))
	var mu sync.Mutex
	var evs []governor.SpendEvent
	gov, err := governor.New(GovernorConfig(cfg), governor.Options{Clock: clk, OnSpend: func(e governor.SpendEvent) {
		mu.Lock()
		evs = append(evs, e)
		mu.Unlock()
	}})
	if err != nil {
		t.Fatal(err)
	}
	run, err := New(Options{Inner: &readingInner{steps: []step{rl200(100, 64)}}, Governor: gov, Concurrency: 1, DefaultPause: time.Minute, Clock: clk, Floor: 20})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := run.Run(bg(context.Background()), "search"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(evs) != 1 || evs[0].Remaining != 64 {
		t.Fatalf("events = %+v, want one with Remaining 64", evs)
	}
}

// A 429 that states Retry-After pauses background work for at least that long;
// without a stated delay the default pause applies.
func TestRetryAfterFromTheReadingSizesThePause(t *testing.T) {
	st := step{
		rl:  &internal.RateLimit{Status: 429, Limit: ptr(int64(100)), Remaining: ptr(int64(0)), NearLimit: ptr(true), RetryAfterSeconds: 11, Reset: "2026-10-27T12:00:11Z", Reason: "jira-burst-based"},
		err: errors.New(text429),
	}
	r := newFloorRig(t, 20, st)
	if _, err := r.run.Run(bg(context.Background()), "search"); !IsRateLimited(err) {
		t.Fatalf("err = %v, want rate limited", err)
	}
	until, _, ok := r.run.Paused()
	if !ok {
		t.Fatal("no pause")
	}
	if got := until.Sub(r.clock.Now()); got != 11*time.Second {
		t.Errorf("pause = %v, want the stated 11s (no jitter in this rig)", got)
	}

	r2 := newFloorRig(t, 20, step{rl: &internal.RateLimit{Status: 429, Remaining: ptr(int64(0))}, err: errors.New(text429)})
	if _, err := r2.run.Run(bg(context.Background()), "search"); !IsRateLimited(err) {
		t.Fatal(err)
	}
	until, _, _ = r2.run.Paused()
	if got := until.Sub(r2.clock.Now()); got != 60*time.Second {
		t.Errorf("pause = %v, want the default 60s when no delay is stated", got)
	}
}

// The reading a 429 carried is recorded (it is the freshest), including its
// stated reset, and refuses requests until that reset.
func TestA429ReadingIsRecordedAndHoldsUntilItsReset(t *testing.T) {
	r := newFloorRig(t, 20, step{
		rl:  &internal.RateLimit{Status: 429, Limit: ptr(int64(100)), Remaining: ptr(int64(0)), Reset: "2026-10-27T12:00:05Z", RetryAfterSeconds: 5},
		err: errors.New(text429),
	})
	_, _ = r.run.Run(bg(context.Background()), "search")
	l, ok := r.run.Readings().Latest()
	if !ok || l.Status != 429 || l.Reset == "" {
		t.Fatalf("latest = %+v ok=%v", l, ok)
	}
	if at, ok := l.ResetAt(); !ok || !at.Equal(time.Date(2026, 10, 27, 12, 0, 5, 0, time.UTC)) {
		t.Errorf("ResetAt = %v ok=%v", at, ok)
	}
	// An interactive call is refused by the 429 pause; it names the pause or the floor, never reaches pjira.
	if _, err := r.run.Run(context.Background(), "issue"); err == nil {
		t.Fatal("want a refusal")
	}
	if r.inner.calls != 1 {
		t.Errorf("calls = %d, want 1", r.inner.calls)
	}
}

func TestReadings_CurrentExpiresAndABareRetryAfterIsNotAReading(t *testing.T) {
	rs := NewReadings()
	t0 := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	rs.Record(internal.RateLimit{Status: 503, RetryAfterSeconds: 3}, t0)
	if _, ok := rs.Latest(); ok {
		t.Error("a bare Retry-After on a 5xx is not a budget reading")
	}
	rs.Record(internal.RateLimit{Status: 200, Remaining: ptr(int64(50)), Limit: ptr(int64(100))}, t0)
	if _, ok := rs.Current(t0.Add(ReadingMaxAge)); !ok {
		t.Error("a reading exactly ReadingMaxAge old is still current")
	}
	if _, ok := rs.Current(t0.Add(ReadingMaxAge + time.Second)); ok {
		t.Error("an older reading is not current")
	}
	if l, ok := rs.Latest(); !ok || l.Age(t0.Add(-time.Hour)) != 0 {
		t.Errorf("age is never negative: %+v", l)
	}
}

func TestLatest_ResetAtParsesOnlyRFC3339(t *testing.T) {
	for in, want := range map[string]bool{"": false, "soon": false, "1761566411": false, "2026-10-27T12:00:11Z": true} {
		if _, ok := (Latest{RateLimit: internal.RateLimit{Reset: in}}).ResetAt(); ok != want {
			t.Errorf("Reset %q: ok = %v, want %v", in, ok, want)
		}
	}
}
