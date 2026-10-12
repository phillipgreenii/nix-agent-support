package governor

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)

// testConfig is the production shape: the GraphQL cap of 4000 with the
// reserve of 1000, the REST caps of 3000 per hour and 20 per minute, and a
// background share of 0.9.
func testConfig() Config {
	return Config{
		GraphQL:         Limits{Cap: 4000, Reserve: 1000},
		RESTCore:        Limits{Cap: 3000},
		RESTSearch:      Limits{Cap: 20},
		BackgroundShare: 0.9,
	}
}

func newGov(t *testing.T, cfg Config, opts Options) (*Governor, *ManualClock) {
	t.Helper()
	clk := NewManualClock(t0)
	if opts.Clock == nil {
		opts.Clock = clk
	} else if mc, ok := opts.Clock.(*ManualClock); ok {
		clk = mc
	}
	g, err := New(cfg, opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return g, clk
}

func purposeOf(c Class) Purpose {
	switch c {
	case ClassInteractive:
		return PurposeInteractive
	case ClassInvalidated:
		return PurposeSettle
	case ClassQuery:
		return PurposeQuery
	case ClassKeepAlive:
		return PurposeRefresh
	default:
		return PurposeFill
	}
}

// spend admits and settles one request, returning the admission error.
func spend(t *testing.T, g *Governor, b Bucket, c Class, cost int64) error {
	t.Helper()
	tk, err := g.Admit(Request{Bucket: b, Class: c, Cost: cost})
	if err != nil {
		return err
	}
	if err := tk.Settle(Spend{Purpose: purposeOf(c), Cost: cost, Outcome: "ok"}); err != nil {
		t.Fatalf("Settle: %v", err)
	}
	return nil
}

// spendUntilRefused spends cost per request until the governor declines, and
// returns the total spent and the refusal.
func spendUntilRefused(t *testing.T, g *Governor, b Bucket, c Class, cost int64) (int64, *Refusal) {
	t.Helper()
	var total int64
	for i := 0; i < 100000; i++ {
		err := spend(t, g, b, c, cost)
		if err != nil {
			var r *Refusal
			if !errors.As(err, &r) {
				t.Fatalf("non-refusal error: %v", err)
			}
			return total, r
		}
		total += cost
	}
	t.Fatal("never refused")
	return 0, nil
}

func kindOf(err error) RefusalKind {
	var r *Refusal
	if errors.As(err, &r) {
		return r.Kind
	}
	return 0
}

func TestBackgroundShareStopsEachBucket(t *testing.T) {
	cases := []struct {
		name       string
		bucket     Bucket
		mutate     func(*Config)
		cap, bgCap int64
	}{
		{"graphql 4000", BucketGraphQL, func(c *Config) {}, 4000, 3600},
		{"graphql 1500", BucketGraphQL, func(c *Config) { c.GraphQL.Cap = 1500 }, 1500, 1350},
		{"rest core 3000", BucketRESTCore, func(c *Config) {}, 3000, 2700},
		{"rest search 20", BucketRESTSearch, func(c *Config) {}, 20, 18},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig()
			tc.mutate(&cfg)
			g, _ := newGov(t, cfg, Options{})

			// Classes 3 to 5 together stop at the share, whichever mixes spend.
			var bg int64
			for i := 0; ; i++ {
				c := Class(3 + i%3)
				if err := spend(t, g, tc.bucket, c, 1); err != nil {
					if kindOf(err) != RefusedBackgroundShare {
						t.Fatalf("background refusal kind = %v (%v), want background share", kindOf(err), err)
					}
					if !strings.Contains(err.Error(), "background share") || !strings.Contains(err.Error(), string(tc.bucket)) {
						t.Errorf("reason %q does not name the bucket and the share", err)
					}
					break
				}
				bg++
			}
			if bg != tc.bgCap {
				t.Fatalf("background spent %d of %d, want %d", bg, tc.cap, tc.bgCap)
			}

			// The remainder is for classes 1 and 2, up to the cap and no further.
			var fg int64
			for i := 0; ; i++ {
				c := Class(1 + i%2)
				if err := spend(t, g, tc.bucket, c, 1); err != nil {
					if kindOf(err) != RefusedCap || !strings.Contains(err.Error(), "cap reached") {
						t.Fatalf("foreground refusal = %v, want the cap", err)
					}
					break
				}
				fg++
			}
			if bg+fg != tc.cap {
				t.Fatalf("total spent %d, want exactly the cap %d", bg+fg, tc.cap)
			}
			bs, _ := g.State().Bucket(tc.bucket)
			if bs.Spent != tc.cap || bs.BackgroundSpent != tc.bgCap || bs.BackgroundCap != tc.bgCap {
				t.Errorf("state = %+v", bs)
			}
		})
	}
}

func TestNoClassSpendsPastTheCap(t *testing.T) {
	for _, b := range Buckets() {
		for c := ClassInteractive; c <= ClassFill; c++ {
			g, _ := newGov(t, testConfig(), Options{})
			bs, _ := g.State().Bucket(b)
			limit := bs.Cap
			if c.Background() {
				limit = bs.BackgroundCap
			}
			// A cost that does not divide the limit still never overshoots.
			total, r := spendUntilRefused(t, g, b, c, 7)
			if total > limit || total+7 <= limit {
				t.Errorf("%s class %v spent %d against a limit of %d", b, c, total, limit)
			}
			if r.Kind != RefusedCap && r.Kind != RefusedBackgroundShare {
				t.Errorf("%s class %v refusal kind %v", b, c, r.Kind)
			}
			// What fits still fits: the request that exactly reaches the limit is admitted.
			if left := limit - total; left > 0 {
				if err := spend(t, g, b, c, left); err != nil {
					t.Errorf("%s class %v: %d left but refused: %v", b, c, left, err)
				}
			}
		}
	}
}

func TestPriorityOrderOfAdmission(t *testing.T) {
	cfg := testConfig()
	cfg.GraphQL = Limits{Cap: 100, Reserve: 50}
	admitted := func(g *Governor) map[Class]RefusalKind {
		out := map[Class]RefusalKind{}
		for c := ClassInteractive; c <= ClassFill; c++ {
			err := g.Check(Request{Bucket: BucketGraphQL, Class: c, Cost: 1})
			out[c] = kindOf(err)
			if err != nil && !errors.Is(err, ErrRefused) {
				t.Fatalf("class %v: %v", c, err)
			}
		}
		return out
	}
	none := RefusalKind(0)
	want := func(g *Governor, label string, exp map[Class]RefusalKind) {
		t.Helper()
		got := admitted(g)
		for c, k := range exp {
			if got[c] != k {
				t.Errorf("%s: class %v refusal = %v, want %v", label, c, got[c], k)
			}
		}
	}

	g, _ := newGov(t, cfg, Options{})
	want(g, "empty", map[Class]RefusalKind{1: none, 2: none, 3: none, 4: none, 5: none})

	// 90 spent: the share (90 of 100) is used up, so 5, 4, 3 go first.
	if _, r := spendUntilRefused(t, g, BucketGraphQL, ClassKeepAlive, 90); r == nil {
		t.Fatal("no refusal")
	}
	want(g, "background share reached", map[Class]RefusalKind{
		1: none, 2: none, 3: RefusedBackgroundShare, 4: RefusedBackgroundShare, 5: RefusedBackgroundShare,
	})

	// Then the foreground spends the last tenth, and the cap stops everyone.
	if err := spend(t, g, BucketGraphQL, ClassInvalidated, 10); err != nil {
		t.Fatal(err)
	}
	want(g, "cap reached", map[Class]RefusalKind{
		1: RefusedCap, 2: RefusedCap, 3: RefusedCap, 4: RefusedCap, 5: RefusedCap,
	})

	// A reserve reading stops every class before the cap does.
	g2, _ := newGov(t, cfg, Options{})
	if err := g2.Observe(Reading{Bucket: BucketGraphQL, Remaining: 50, ResetAt: t0.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	want(g2, "at the reserve", map[Class]RefusalKind{
		1: RefusedReserve, 2: RefusedReserve, 3: RefusedReserve, 4: RefusedReserve, 5: RefusedReserve,
	})

	// A pause stops only the background.
	g3, _ := newGov(t, cfg, Options{})
	if err := g3.Pause(BucketGraphQL, time.Minute, "secondary rate limit"); err != nil {
		t.Fatal(err)
	}
	want(g3, "paused", map[Class]RefusalKind{
		1: none, 2: none, 3: RefusedPaused, 4: RefusedPaused, 5: RefusedPaused,
	})
}

func TestReserveProtectsEveryBucket(t *testing.T) {
	cfg := testConfig()
	cfg.RESTCore.Reserve = 500
	cfg.RESTSearch.Reserve = 5
	reserves := map[Bucket]int64{BucketGraphQL: 1000, BucketRESTCore: 500, BucketRESTSearch: 5}
	for _, b := range Buckets() {
		reserve := reserves[b]
		for _, class := range []Class{ClassInteractive, ClassInvalidated, ClassQuery, ClassFill} {
			g, clk := newGov(t, cfg, Options{})
			reset := t0.Add(30 * time.Minute)
			if err := g.Observe(Reading{Bucket: b, Remaining: reserve + 5, Limit: 5000, ResetAt: reset}); err != nil {
				t.Fatal(err)
			}
			if err := g.Check(Request{Bucket: b, Class: class, Cost: 5}); err != nil {
				t.Errorf("%s class %v: spend that leaves exactly the reserve refused: %v", b, class, err)
			}
			err := g.Check(Request{Bucket: b, Class: class, Cost: 6})
			var r *Refusal
			if !errors.As(err, &r) || r.Kind != RefusedReserve {
				t.Fatalf("%s class %v: spend below the reserve = %v, want a reserve refusal", b, class, err)
			}
			if !strings.Contains(r.Reason, "below reserve") || r.Limit != reserve || !r.RetryAt.Equal(reset) {
				t.Errorf("%s: refusal %+v does not name the reserve and the reset", b, r)
			}

			// What is spent since the reading counts against it too.
			if err := spend(t, g, b, ClassInteractive, 3); err != nil {
				t.Fatal(err)
			}
			if kindOf(g.Check(Request{Bucket: b, Class: class, Cost: 3})) != RefusedReserve {
				t.Errorf("%s class %v: a reading minus the spend since it must still protect the reserve", b, class)
			}

			// The window resets: the reading no longer binds.
			clk.Set(reset)
			if err := g.Check(Request{Bucket: b, Class: class, Cost: 6}); err != nil {
				t.Errorf("%s class %v: after the reset the reading must not bind: %v", b, class, err)
			}
		}
	}
}

func TestNothingSpendsBelowTheReserveWhateverTheCapSays(t *testing.T) {
	g, _ := newGov(t, testConfig(), Options{})
	reading := Reading{Remaining: 1010, ResetAt: t0.Add(time.Hour)}
	tk, err := g.Admit(Request{Bucket: BucketGraphQL, Class: ClassInteractive, Cost: 10})
	if err != nil {
		t.Fatal(err)
	}
	if err := tk.Settle(Spend{Purpose: PurposeInteractive, Cost: 10, Reading: &Reading{Remaining: 1000, ResetAt: reading.ResetAt}}); err != nil {
		t.Fatal(err)
	}
	for c := ClassInteractive; c <= ClassFill; c++ {
		err := g.Check(Request{Bucket: BucketGraphQL, Class: c, Cost: 1})
		var r *Refusal
		if !errors.As(err, &r) || r.Kind != RefusedReserve || !strings.Contains(r.Reason, "reserve 1000") {
			t.Errorf("class %v at the reserve: %v", c, err)
		}
	}
	if bs, _ := g.State().Bucket(BucketGraphQL); bs.Spent != 10 || bs.Remaining != 1000 {
		t.Errorf("state = %+v", bs)
	}
}

func TestFallingReadingsTightenAdmission(t *testing.T) {
	g, clk := newGov(t, testConfig(), Options{})
	const reserve = 1000
	reset := t0.Add(time.Hour)
	// Another process drains the token: this daemon spends nothing, yet the
	// largest request it may make shrinks with every reading.
	for _, remaining := range []int64{5000, 3000, 1500, 1100, 1000} {
		clk.Advance(time.Minute)
		if err := g.Observe(Reading{Bucket: BucketGraphQL, Remaining: remaining, Limit: 5000, ResetAt: reset}); err != nil {
			t.Fatal(err)
		}
		for _, class := range []Class{ClassInteractive, ClassQuery} {
			// The most a class may spend is its own ceiling (the cap, or the
			// background share of it) or what the reading leaves above the
			// reserve, whichever is smaller.
			ceiling := int64(4000)
			if class.Background() {
				ceiling = 3600
			}
			limit := min(ceiling, remaining-reserve)
			if limit > 0 {
				if err := g.Check(Request{Bucket: BucketGraphQL, Class: class, Cost: limit}); err != nil {
					t.Errorf("remaining %d class %v: cost %d refused: %v", remaining, class, limit, err)
				}
			}
			if err := g.Check(Request{Bucket: BucketGraphQL, Class: class, Cost: limit + 1}); err == nil {
				t.Errorf("remaining %d class %v: cost %d admitted past the limit", remaining, class, limit+1)
			}
		}
		if bs, _ := g.State().Bucket(BucketGraphQL); bs.Spent != 0 || bs.Remaining != remaining {
			t.Errorf("remaining %d: state %+v (the daemon spent nothing)", remaining, bs)
		}
	}
	// A reading from the next window relaxes it again.
	clk.Advance(time.Minute)
	if err := g.Observe(Reading{Bucket: BucketGraphQL, Remaining: 5000, ResetAt: reset.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := g.Check(Request{Bucket: BucketGraphQL, Class: ClassQuery, Cost: 100}); err != nil {
		t.Errorf("after a fresh window: %v", err)
	}
	// A late response from the earlier window is not allowed to tighten it back.
	if err := g.Observe(Reading{Bucket: BucketGraphQL, Remaining: 1000, ResetAt: reset}); err != nil {
		t.Fatal(err)
	}
	if err := g.Check(Request{Bucket: BucketGraphQL, Class: ClassQuery, Cost: 100}); err != nil {
		t.Errorf("a stale reading from the previous window bound the new one: %v", err)
	}
}

type pauseEvent struct {
	bucket Bucket
	reason string
	until  time.Time
}

func TestRetryAfterPausesBackgroundForExactlyTheStatedTime(t *testing.T) {
	var mu sync.Mutex
	var events []pauseEvent
	var g *Governor
	g, clk := newGov(t, testConfig(), Options{OnPauseChange: func(b Bucket, reason string, until time.Time) {
		_ = g.State() // a callback may call back into the governor
		mu.Lock()
		defer mu.Unlock()
		events = append(events, pauseEvent{b, reason, until})
	}})
	snapshot := func() []pauseEvent {
		mu.Lock()
		defer mu.Unlock()
		return append([]pauseEvent(nil), events...)
	}

	until := t0.Add(90 * time.Second)
	if err := g.Pause(BucketRESTCore, 90*time.Second, "secondary rate limit"); err != nil {
		t.Fatal(err)
	}
	if ev := snapshot(); len(ev) != 1 || ev[0] != (pauseEvent{BucketRESTCore, "secondary rate limit", until}) {
		t.Fatalf("pause start events = %+v", ev)
	}
	if bs, _ := g.State().Bucket(BucketRESTCore); !bs.Paused || bs.PauseReason != "secondary rate limit" || !bs.PausedUntil.Equal(until) {
		t.Errorf("state = %+v", bs)
	}
	// Other buckets are untouched.
	if err := g.Check(Request{Bucket: BucketGraphQL, Class: ClassQuery, Cost: 1}); err != nil {
		t.Errorf("an unpaused bucket refused: %v", err)
	}

	for _, at := range []time.Duration{0, 30 * time.Second, 89*time.Second + 999*time.Millisecond} {
		clk.Set(t0.Add(at))
		for c := ClassQuery; c <= ClassFill; c++ {
			err := g.Check(Request{Bucket: BucketRESTCore, Class: c, Cost: 1})
			var r *Refusal
			if !errors.As(err, &r) || r.Kind != RefusedPaused || !r.RetryAt.Equal(until) || !strings.Contains(r.Reason, "secondary rate limit") {
				t.Fatalf("t+%v class %v during the pause: %v", at, c, err)
			}
		}
		for _, c := range []Class{ClassInteractive, ClassInvalidated} {
			if err := spend(t, g, BucketRESTCore, c, 1); err != nil {
				t.Fatalf("t+%v class %v must stay admitted during the pause: %v", at, c, err)
			}
		}
	}
	if len(snapshot()) != 1 {
		t.Fatalf("the pause ended early: %+v", snapshot())
	}

	// At exactly the stated time the pause is over and the end is reported.
	clk.Set(until)
	for c := ClassQuery; c <= ClassFill; c++ {
		if err := g.Check(Request{Bucket: BucketRESTCore, Class: c, Cost: 1}); err != nil {
			t.Errorf("class %v after the pause: %v", c, err)
		}
	}
	ev := snapshot()
	if len(ev) != 2 || ev[1] != (pauseEvent{BucketRESTCore, "secondary rate limit", time.Time{}}) {
		t.Fatalf("pause end events = %+v", ev)
	}
	if bs, _ := g.State().Bucket(BucketRESTCore); bs.Paused {
		t.Errorf("still paused: %+v", bs)
	}
}

func TestPauseExtendsNeverShortens(t *testing.T) {
	var events []pauseEvent
	g, clk := newGov(t, testConfig(), Options{OnPauseChange: func(b Bucket, reason string, until time.Time) {
		events = append(events, pauseEvent{b, reason, until})
	}})
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(g.Pause(BucketGraphQL, 60*time.Second, "first"))
	must(g.Pause(BucketGraphQL, 30*time.Second, "shorter"))
	if len(events) != 1 {
		t.Fatalf("a shorter pause fired a change: %+v", events)
	}
	must(g.Pause(BucketGraphQL, 120*time.Second, "longer"))
	if len(events) != 2 || events[1].reason != "longer" || !events[1].until.Equal(t0.Add(120*time.Second)) {
		t.Fatalf("extension events = %+v", events)
	}
	clk.Set(t0.Add(61 * time.Second))
	if err := g.Check(Request{Bucket: BucketGraphQL, Class: ClassQuery, Cost: 1}); kindOf(err) != RefusedPaused {
		t.Fatalf("the extended pause ended with the first one: %v", err)
	}

	// A zero or negative stated time pauses nothing; PauseAll holds every bucket.
	must(g.Pause(BucketRESTSearch, 0, ""))
	g.PauseAll(time.Minute, "")
	for _, b := range Buckets() {
		bs, _ := g.State().Bucket(b)
		if !bs.Paused {
			t.Errorf("%s not paused by PauseAll", b)
		}
	}
	if events[len(events)-1].reason != DefaultPauseReason {
		t.Errorf("default reason = %q", events[len(events)-1].reason)
	}
	clk.Set(t0.Add(5 * time.Minute))
	g.Tick()
	if events[len(events)-1].until != (time.Time{}) {
		t.Errorf("Tick did not report the end: %+v", events)
	}
}

func TestRefusalIsTypedAndNamesTheLimit(t *testing.T) {
	g, clk := newGov(t, testConfig(), Options{})
	if err := spend(t, g, BucketRESTSearch, ClassInteractive, 20); err != nil {
		t.Fatal(err)
	}
	clk.Advance(10 * time.Second)
	_, err := g.Admit(Request{Bucket: BucketRESTSearch, Class: ClassInteractive, Cost: 1})
	var r *Refusal
	if !errors.As(err, &r) || !errors.Is(err, ErrRefused) {
		t.Fatalf("err = %v, want a *Refusal matching ErrRefused", err)
	}
	if r.Kind != RefusedCap || r.Bucket != BucketRESTSearch || r.Class != ClassInteractive || r.Limit != 20 {
		t.Errorf("refusal = %+v", r)
	}
	if !strings.Contains(r.Reason, "rest_search cap reached") || r.Error() != r.Reason {
		t.Errorf("reason = %q", r.Reason)
	}
	// The spend leaves the window a minute after it was made.
	if !r.RetryAt.Equal(t0.Add(time.Minute)) {
		t.Errorf("RetryAt = %v, want %v", r.RetryAt, t0.Add(time.Minute))
	}
	clk.Set(r.RetryAt)
	if err := spend(t, g, BucketRESTSearch, ClassInteractive, 1); err != nil {
		t.Errorf("not admitted at RetryAt: %v", err)
	}
	// A request larger than the cap never fits and says so by having no retry time.
	_, err = g.Admit(Request{Bucket: BucketRESTSearch, Class: ClassInteractive, Cost: 21})
	if !errors.As(err, &r) || !r.RetryAt.IsZero() {
		t.Errorf("oversize request: %+v", r)
	}
	// Bad input is an error, not a refusal.
	for _, req := range []Request{
		{Bucket: "nope", Class: ClassInteractive, Cost: 1},
		{Bucket: BucketGraphQL, Class: 0, Cost: 1},
		{Bucket: BucketGraphQL, Class: 6, Cost: 1},
	} {
		if err := g.Check(req); err == nil || errors.Is(err, ErrRefused) {
			t.Errorf("Check(%+v) = %v, want a plain error", req, err)
		}
	}
}

func TestSpendObserverSeesEveryRecordedCall(t *testing.T) {
	var got []SpendEvent
	g, clk := newGov(t, testConfig(), Options{OnSpend: func(e SpendEvent) { got = append(got, e) }})
	clk.Advance(time.Second)
	tk, err := g.Admit(Request{Bucket: BucketGraphQL, Class: ClassKeepAlive, Cost: 1})
	if err != nil {
		t.Fatal(err)
	}
	reading := Reading{Remaining: 3990, ResetAt: t0.Add(time.Hour)}
	if err := tk.Settle(Spend{Purpose: PurposeRefresh, Group: "summary", BatchSize: 74, Cost: 1, Reading: &reading, Outcome: "ok"}); err != nil {
		t.Fatal(err)
	}
	if err := tk.Settle(Spend{Cost: 1}); !errors.Is(err, ErrTicketDone) {
		t.Errorf("second Settle = %v", err)
	}
	// A call that already happened is recorded even past the cap.
	if err := g.Record(Spend{Bucket: BucketRESTCore, Class: ClassInteractive, Purpose: PurposePassthrough, Cost: 4000, Outcome: "error"}); err != nil {
		t.Fatal(err)
	}
	want := []SpendEvent{
		{At: t0.Add(time.Second), Bucket: BucketGraphQL, Class: ClassKeepAlive, Purpose: PurposeRefresh, Group: "summary", BatchSize: 74, Cost: 1, Remaining: 3990, Outcome: "ok"},
		{At: t0.Add(time.Second), Bucket: BucketRESTCore, Class: ClassInteractive, Purpose: PurposePassthrough, Cost: 4000, Remaining: -1, Outcome: "error"},
	}
	if len(got) != len(want) {
		t.Fatalf("events = %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("event %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if bs, _ := g.State().Bucket(BucketRESTCore); bs.Spent != 4000 || bs.SpentByPurpose[PurposePassthrough] != 4000 {
		t.Errorf("state = %+v", bs)
	}
	if err := g.Record(Spend{Bucket: "nope"}); err == nil {
		t.Error("Record accepted an unknown bucket")
	}
}

func TestInFlightReservationsCountAgainstTheCap(t *testing.T) {
	cfg := testConfig()
	cfg.GraphQL = Limits{Cap: 10}
	g, _ := newGov(t, cfg, Options{})
	a, err := g.Admit(Request{Bucket: BucketGraphQL, Class: ClassInteractive, Cost: 4})
	if err != nil {
		t.Fatal(err)
	}
	b, err := g.Admit(Request{Bucket: BucketGraphQL, Class: ClassInteractive, Cost: 4})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.Admit(Request{Bucket: BucketGraphQL, Class: ClassInteractive, Cost: 4}); kindOf(err) != RefusedCap {
		t.Fatalf("a third request admitted while two are in flight: %v", err)
	}
	if bs, _ := g.State().Bucket(BucketGraphQL); bs.InFlight != 8 || bs.Spent != 0 {
		t.Errorf("state = %+v", bs)
	}
	a.Release()
	a.Release() // idempotent
	if bs, _ := g.State().Bucket(BucketGraphQL); bs.InFlight != 4 {
		t.Errorf("after Release: %+v", bs)
	}
	// The actual cost may exceed the estimate; it is what is recorded.
	if err := b.Settle(Spend{Purpose: PurposeInteractive, Cost: 9}); err != nil {
		t.Fatal(err)
	}
	if bs, _ := g.State().Bucket(BucketGraphQL); bs.InFlight != 0 || bs.Spent != 9 {
		t.Errorf("after Settle: %+v", bs)
	}
	if _, err := g.Admit(Request{Bucket: BucketGraphQL, Class: ClassInteractive, Cost: 2}); kindOf(err) != RefusedCap {
		t.Errorf("the overshoot was not counted: %v", err)
	}
}

func TestSearchWindowIsPerMinute(t *testing.T) {
	g, clk := newGov(t, testConfig(), Options{})
	if total, _ := spendUntilRefused(t, g, BucketRESTSearch, ClassQuery, 1); total != 18 {
		t.Fatalf("background spent %d of 20 per minute, want 18", total)
	}
	clk.Advance(59*time.Second + 999*time.Millisecond)
	if err := spend(t, g, BucketRESTSearch, ClassQuery, 1); kindOf(err) != RefusedBackgroundShare {
		t.Fatalf("admitted inside the minute: %v", err)
	}
	clk.Advance(time.Millisecond)
	if total, _ := spendUntilRefused(t, g, BucketRESTSearch, ClassQuery, 1); total != 18 {
		t.Fatalf("the next minute spent %d, want 18 again", total)
	}
}

func TestConcurrentWorkersNeverPassTheCap(t *testing.T) {
	cfg := testConfig()
	cfg.GraphQL = Limits{Cap: 1000}
	g, _ := newGov(t, cfg, Options{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	var total int64
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				tk, err := g.Admit(Request{Bucket: BucketGraphQL, Class: ClassInteractive, Cost: 3})
				if err != nil {
					return
				}
				mu.Lock()
				total += 3
				mu.Unlock()
				_ = tk.Settle(Spend{Purpose: PurposeInteractive, Cost: 3})
			}
		}()
	}
	wg.Wait()
	if total > 1000 || total < 998 {
		t.Fatalf("workers spent %d against a cap of 1000 (cost 3 each, so 999 is the most that fits)", total)
	}
}

func TestConfigValidate(t *testing.T) {
	ok := testConfig()
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Config){
		"graphql cap":      func(c *Config) { c.GraphQL.Cap = 0 },
		"rest cap":         func(c *Config) { c.RESTCore.Cap = -1 },
		"search cap":       func(c *Config) { c.RESTSearch.Cap = 0 },
		"negative reserve": func(c *Config) { c.GraphQL.Reserve = -1 },
		"share zero":       func(c *Config) { c.BackgroundShare = 0 },
		"share over one":   func(c *Config) { c.BackgroundShare = 1.1 },
	} {
		cfg := testConfig()
		mutate(&cfg)
		if _, err := New(cfg, Options{}); err == nil {
			t.Errorf("%s: New accepted %+v", name, cfg)
		}
	}
	if share := testConfig(); backgroundCap(4000, share.BackgroundShare) != 3600 || backgroundCap(1500, 0.9) != 1350 || backgroundCap(3000, 0.9) != 2700 {
		t.Error("background caps of the defaults are not 3600, 1350 and 2700")
	}
}

func TestPurposeEncodingRoundTrips(t *testing.T) {
	for _, c := range []Class{ClassInteractive, ClassInvalidated, ClassQuery, ClassKeepAlive, ClassFill} {
		for _, p := range []Purpose{PurposeInteractive, PurposeWrite, PurposeQuery, PurposeRefresh, PurposeSettle, PurposeFill, PurposePassthrough, ""} {
			gc, gp := decodePurpose(encodePurpose(c, p))
			if gc != c || gp != p {
				t.Errorf("(%v, %q) round-tripped to (%v, %q)", c, p, gc, gp)
			}
		}
	}
	// A row this package did not write has no class, and counts as background.
	if c, p := decodePurpose("legacy"); c != 0 || p != "legacy" {
		t.Errorf("foreign purpose decoded to (%v, %q)", c, p)
	}
	if c, p := decodePurpose("x:c9"); c != 0 || p != "x:c9" {
		t.Errorf("out-of-range class decoded to (%v, %q)", c, p)
	}
	if !(entry{}).background() {
		t.Error("an unknown class must count as background")
	}
}
