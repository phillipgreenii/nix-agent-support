package governor

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

func openStore(t *testing.T, path string) *store.Store {
	t.Helper()
	s, _, err := store.Open(path, store.Options{Synchronous: "OFF"})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	return s
}

func storePath(t *testing.T) string { return filepath.Join(t.TempDir(), "store.sqlite") }

// busyHour records the spend of a busy hour on the GraphQL bucket: 3000
// points of background work (purpose query) spread over ten minutes, then 200
// of interactive work that carries a reading.
func busyHour(t *testing.T, g *Governor, clk *ManualClock) {
	t.Helper()
	for i := 0; i < 30; i++ {
		if err := spend(t, g, BucketGraphQL, ClassQuery, 100); err != nil {
			t.Fatal(err)
		}
		clk.Advance(20 * time.Second)
	}
	tk, err := g.Admit(Request{Bucket: BucketGraphQL, Class: ClassInteractive, Cost: 200})
	if err != nil {
		t.Fatal(err)
	}
	reading := Reading{Remaining: 2000, Limit: 5000, ResetAt: t0.Add(50 * time.Minute)}
	if err := tk.Settle(Spend{Purpose: PurposeInteractive, Group: "summary", BatchSize: 1, Cost: 200, Reading: &reading, Outcome: "ok"}); err != nil {
		t.Fatal(err)
	}
}

func TestSpendIsRecordedPerBucketAndPurposeAndSurvivesAStoreReopen(t *testing.T) {
	path := storePath(t)
	clk := NewManualClock(t0)
	s1 := openStore(t, path)
	g1, _ := newGov(t, testConfig(), Options{Clock: clk, Store: s1})
	busyHour(t, g1, clk)
	// A REST call on another bucket.
	if err := spend(t, g1, BucketRESTCore, ClassKeepAlive, 1); err != nil {
		t.Fatal(err)
	}
	before := g1.State()
	if err := s1.Close(); err != nil {
		t.Fatal(err)
	}

	// The budget table holds one row per call, with the purpose and the class.
	s2 := openStore(t, path)
	defer func() { _ = s2.Close() }()
	rows, err := s2.BudgetSince(context.Background(), string(BucketGraphQL), t0)
	if err != nil || len(rows) != 31 {
		t.Fatalf("graphql rows = %d, err %v, want 31", len(rows), err)
	}
	byPurpose := map[Purpose]int64{}
	for _, r := range rows {
		_, p := decodePurpose(r.Purpose)
		byPurpose[p] += r.Spent
	}
	if byPurpose[PurposeQuery] != 3000 || byPurpose[PurposeInteractive] != 200 {
		t.Errorf("spend by purpose in the table = %v", byPurpose)
	}
	if rows[0].Purpose != "query:c3" || rows[30].Remaining != 2000 || !rows[30].ResetAt.Equal(t0.Add(50*time.Minute)) {
		t.Errorf("rows[0] = %+v, rows[30] = %+v", rows[0], rows[30])
	}
	if rows[0].Remaining != unknownRemaining {
		t.Errorf("a call without a reading stored remaining %d", rows[0].Remaining)
	}

	// Five minutes later the daemon restarts: it still knows the hour so far.
	clk.Advance(5 * time.Minute)
	g2, _ := newGov(t, testConfig(), Options{Clock: clk, Store: s2})
	after := g2.State()
	for _, b := range []Bucket{BucketGraphQL, BucketRESTCore, BucketRESTSearch} {
		x, _ := before.Bucket(b)
		y, _ := after.Bucket(b)
		if x.Spent != y.Spent || x.BackgroundSpent != y.BackgroundSpent || len(x.SpentByPurpose) != len(y.SpentByPurpose) {
			t.Errorf("%s: before %+v, after reopen %+v", b, x, y)
		}
		for p, v := range x.SpentByPurpose {
			if y.SpentByPurpose[p] != v {
				t.Errorf("%s purpose %s: %d before, %d after", b, p, v, y.SpentByPurpose[p])
			}
		}
	}
	gq, _ := after.Bucket(BucketGraphQL)
	if gq.Spent != 3200 || gq.BackgroundSpent != 3000 || gq.Remaining != 2000 || !gq.ResetAt.Equal(t0.Add(50*time.Minute)) {
		t.Fatalf("graphql after reopen = %+v", gq)
	}

	// The share and the cap hold across the restart: 600 of background room and
	// 800 of total room are left, not 3600 and 4000.
	if err := g2.Check(Request{Bucket: BucketGraphQL, Class: ClassQuery, Cost: 600}); err != nil {
		t.Errorf("the last 600 of the background share refused: %v", err)
	}
	if k := kindOf(g2.Check(Request{Bucket: BucketGraphQL, Class: ClassQuery, Cost: 601})); k != RefusedBackgroundShare {
		t.Errorf("a restart forgot the background spend: %v", k)
	}
	if err := g2.Check(Request{Bucket: BucketGraphQL, Class: ClassInteractive, Cost: 800}); err != nil {
		t.Errorf("the last 800 of the cap refused: %v", err)
	}
	if k := kindOf(g2.Check(Request{Bucket: BucketGraphQL, Class: ClassInteractive, Cost: 801})); k != RefusedCap {
		t.Errorf("a restart forgot the spend against the cap: %v", k)
	}
}

func TestAReadingSurvivesARestartSoTheReserveStillHolds(t *testing.T) {
	path := storePath(t)
	clk := NewManualClock(t0)
	s1 := openStore(t, path)
	g1, _ := newGov(t, testConfig(), Options{Clock: clk, Store: s1})
	tk, err := g1.Admit(Request{Bucket: BucketGraphQL, Class: ClassInteractive, Cost: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := tk.Settle(Spend{Purpose: PurposeInteractive, Cost: 1, Reading: &Reading{Remaining: 1005, ResetAt: t0.Add(30 * time.Minute)}}); err != nil {
		t.Fatal(err)
	}
	// A reading fed on its own is persisted too.
	if err := g1.Observe(Reading{Bucket: BucketRESTSearch, Remaining: 7, ResetAt: t0.Add(30 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	_ = s1.Close()

	clk.Advance(10 * time.Second)
	s2 := openStore(t, path)
	defer func() { _ = s2.Close() }()
	g2, _ := newGov(t, testConfig(), Options{Clock: clk, Store: s2})
	if err := g2.Check(Request{Bucket: BucketGraphQL, Class: ClassInteractive, Cost: 5}); err != nil {
		t.Errorf("a spend that leaves exactly the reserve refused: %v", err)
	}
	if k := kindOf(g2.Check(Request{Bucket: BucketGraphQL, Class: ClassInteractive, Cost: 6})); k != RefusedReserve {
		t.Errorf("a restart forgot the reading: %v", k)
	}
	if bs, _ := g2.State().Bucket(BucketRESTSearch); bs.Remaining != 7 {
		t.Errorf("search reading after restart = %+v", bs)
	}

	// Once the origin's window has reset (its row is still inside the hour), the
	// stored reading no longer binds.
	clk.Advance(31 * time.Minute)
	g3, _ := newGov(t, testConfig(), Options{Clock: clk, Store: s2})
	if err := g3.Check(Request{Bucket: BucketGraphQL, Class: ClassInteractive, Cost: 1000}); err != nil {
		t.Errorf("an expired reading bound after a restart: %v", err)
	}
}

func TestCatchUpAfterALongDowntimeStaysInsideTheHourlyCap(t *testing.T) {
	path := storePath(t)
	clk := NewManualClock(t0)
	s1 := openStore(t, path)
	g1, _ := newGov(t, testConfig(), Options{Clock: clk, Store: s1})
	busyHour(t, g1, clk)
	_ = s1.Close()

	// Three days of outage: the window has long passed, so nothing is remembered.
	clk.Advance(72 * time.Hour)
	s2 := openStore(t, path)
	defer func() { _ = s2.Close() }()
	var events []SpendEvent
	g2, _ := newGov(t, testConfig(), Options{Clock: clk, Store: s2, OnSpend: func(e SpendEvent) { events = append(events, e) }})
	if bs, _ := g2.State().Bucket(BucketGraphQL); bs.Spent != 0 || bs.Remaining != -1 {
		t.Fatalf("state after the outage = %+v", bs)
	}

	// The scheduler, with every entity overdue, spends as much background work
	// as it is allowed, every simulated minute for three hours.
	for minute := 0; minute < 180; minute++ {
		for spend(t, g2, BucketGraphQL, ClassKeepAlive, 50) == nil {
		}
		if minute == 130 {
			// The table agrees with the governor on the hour in progress.
			rows, err := s2.BudgetSince(context.Background(), string(BucketGraphQL), clk.Now().Add(-time.Hour+time.Millisecond))
			if err != nil {
				t.Fatal(err)
			}
			var stored int64
			for _, r := range rows {
				stored += r.Spent
			}
			if bs, _ := g2.State().Bucket(BucketGraphQL); stored != bs.Spent || bs.Spent != 3600 {
				t.Errorf("minute 130: stored %d, governor %d, want both 3600", stored, bs.Spent)
			}
		}
		clk.Advance(time.Minute)
	}
	const bgCap = 3600
	var total, worst int64
	for i, e := range events {
		total += e.Cost
		var inHour int64
		for _, f := range events[:i+1] {
			if f.At.After(e.At.Add(-time.Hour)) {
				inHour += f.Cost
			}
		}
		worst = max(worst, inHour)
	}
	if worst > bgCap {
		t.Fatalf("an hour held %d points of background spend, more than the share %d", worst, bgCap)
	}
	// It does use what it is allowed: a full share in each of the three hours,
	// nothing more.
	if worst != bgCap || total != 3*bgCap {
		t.Errorf("worst hour %d, total %d; want %d and %d", worst, total, bgCap, 3*bgCap)
	}
}

func TestBudgetRowsPastRetentionArePrunedAtStart(t *testing.T) {
	path := storePath(t)
	clk := NewManualClock(t0)
	s1 := openStore(t, path)
	g1, _ := newGov(t, testConfig(), Options{Clock: clk, Store: s1})
	if err := spend(t, g1, BucketGraphQL, ClassInteractive, 10); err != nil {
		t.Fatal(err)
	}
	clk.Advance(20 * time.Hour)
	if err := spend(t, g1, BucketGraphQL, ClassInteractive, 20); err != nil {
		t.Fatal(err)
	}
	_ = s1.Close()

	clk.Advance(5 * time.Hour) // the first row is 25h old, the second 5h
	s2 := openStore(t, path)
	defer func() { _ = s2.Close() }()
	newGov(t, testConfig(), Options{Clock: clk, Store: s2})
	rows, err := s2.BudgetSince(context.Background(), string(BucketGraphQL), t0)
	if err != nil || len(rows) != 1 || rows[0].Spent != 20 {
		t.Fatalf("rows after the default retention of %v = %+v, err %v", DefaultRetention, rows, err)
	}
}

type failingStore struct{ Store }

func (failingStore) AppendBudget(context.Context, store.BudgetSample) error {
	return errors.New("disk full")
}

func TestAPersistenceFailureIsReportedButTheSpendStillCounts(t *testing.T) {
	var logged []string
	log := loggerFunc(func(change string, kv ...any) { logged = append(logged, change) })
	g, _ := newGov(t, testConfig(), Options{Store: failingStore{openStore(t, storePath(t))}, Logger: log})
	tk, err := g.Admit(Request{Bucket: BucketRESTCore, Class: ClassInteractive, Cost: 1})
	if err != nil {
		t.Fatal(err)
	}
	err = tk.Settle(Spend{Purpose: PurposeInteractive, Cost: 1})
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("Settle = %v, want the persistence failure", err)
	}
	if bs, _ := g.State().Bucket(BucketRESTCore); bs.Spent != 1 || bs.InFlight != 0 {
		t.Errorf("state = %+v: the call happened, so it counts", bs)
	}
	if len(logged) != 1 || logged[0] != "budget_persist_failed" {
		t.Errorf("logged = %v", logged)
	}
}

type loggerFunc func(change string, kv ...any)

func (f loggerFunc) Log(change string, kv ...any) { f(change, kv...) }
