package feed

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/query"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ratefeed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/scheduler"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// fakeRunner is the runner the Queries is built over in the logic tests.
type fakeRunner struct {
	st      *store.Store
	known   map[string]bool
	fetches int
	err     error
}

func (f *fakeRunner) Expr(name string) (schema.QueryExpr, bool) { return nil, f.known[name] }

func (f *fakeRunner) Row(ctx context.Context, key query.Key) (store.QueryRow, bool, error) {
	return f.st.GetQuery(ctx, key.String())
}

func (f *fakeRunner) Fetch(ctx context.Context, key query.Key) (store.QueryRow, error) {
	f.fetches++
	if f.err != nil {
		return store.QueryRow{}, f.err
	}
	row := store.QueryRow{
		Key: key.String(), Configured: true, IDs: []string{refA}, Complete: true,
		FetchedAt: time.Now(), FreshUntil: time.Now().Add(time.Hour), BaselineAt: time.Now(),
	}
	return row, f.st.PutQuery(ctx, row)
}

func TestQueriesKnownMembersAndRun(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	fr := &fakeRunner{st: r.st, known: map[string]bool{"mine": true}}
	q := &queries{r: fr, st: r.st, now: func() time.Time { return t0 }}

	if !q.Known("mine") || q.Known("other") {
		t.Fatal("Known must follow the runner's configured queries")
	}
	if m, err := q.Members(ctx, "mine"); err != nil || m.Baseline || len(m.Refs) != 0 {
		t.Fatalf("members of a query never run = %+v, %v", m, err)
	}

	// No stored row: the query runs.
	if err := q.Run(ctx, "mine"); err != nil || fr.fetches != 1 {
		t.Fatalf("first run: fetches=%d err=%v", fr.fetches, err)
	}
	m, err := q.Members(ctx, "mine")
	if err != nil || !m.Baseline || len(m.Refs) != 1 || m.Refs[0] != refA {
		t.Fatalf("members after the run = %+v, %v", m, err)
	}

	// A fresh row: no re-run. A stale one: re-run.
	row, _, _ := r.st.GetQuery(ctx, "mine")
	row.FreshUntil = t0.Add(time.Hour)
	row.FetchedAt = t0
	if err := r.st.PutQuery(ctx, row); err != nil {
		t.Fatal(err)
	}
	if err := q.Run(ctx, "mine"); err != nil || fr.fetches != 1 {
		t.Fatalf("run of a fresh query: fetches=%d err=%v", fr.fetches, err)
	}
	q.now = func() time.Time { return t0.Add(2 * time.Hour) }
	if err := q.Run(ctx, "mine"); err != nil || fr.fetches != 2 {
		t.Fatalf("run of a stale query: fetches=%d err=%v", fr.fetches, err)
	}

	boom := errors.New("origin failure")
	fr.err = boom
	row.FreshUntil = t0
	if err := r.st.PutQuery(ctx, row); err != nil {
		t.Fatal(err)
	}
	q.now = func() time.Time { return t0.Add(10 * time.Hour) }
	if err := q.Run(ctx, "mine"); !errors.Is(err, boom) {
		t.Fatalf("run error = %v, want the fetch's error", err)
	}
}

func TestTheFeedRunsThroughQueriesBuiltOverARunner(t *testing.T) {
	r := newRig(t)
	fr := &fakeRunner{st: r.st, known: map[string]bool{"mine": true}}
	f, err := New(Config{Store: r.st, Queries: &queries{r: fr, st: r.st, now: time.Now}, Clock: r.clk, Retention: 14 * day, ConsumerStaleAfter: 7 * day})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// The first poll runs the query (which establishes the baseline) and starts at the tail.
	res, err := f.Poll(ctx, PollRequest{Kind: KindPR, Consumer: "c", Query: "mine"})
	if err != nil || fr.fetches != 1 {
		t.Fatalf("poll = %+v, %v (fetches %d)", res, err, fr.fetches)
	}
	if _, ok := r.pos(KindPR, "c", "mine"); !ok {
		t.Fatal("no key after a poll with a baseline")
	}
	if _, err := f.Poll(ctx, PollRequest{Kind: KindPR, Consumer: "c", Query: "nope"}); !errors.Is(err, ErrQueryNotRecognized) {
		t.Fatalf("unknown query = %v", err)
	}
}

type noTracker struct{}

func (noTracker) Track(context.Context, []string) ([]string, error) { return nil, nil }

type noGH struct{}

func (noGH) Run(context.Context, ...string) ([]byte, error) {
	return nil, errors.New("no origin in this test")
}

// TestNewQueriesOverTheRealRunner pins the adapter against *query.Runner: the
// configured names are known, a stored row gives the members and the baseline,
// and a fresh stored row is not re-run (the run would need an origin, which this
// test does not have).
func TestNewQueriesOverTheRealRunner(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	cfg := config.Defaults()
	cfg.PR.Queries = map[string]schema.QueryExpr{"mine": {"author:phillipgreenii is:open"}}
	gov, err := governor.New(ratefeed.GovernorConfig(cfg.Settings), governor.Options{Clock: r.clk})
	if err != nil {
		t.Fatal(err)
	}
	sched := scheduler.New(scheduler.Options{Clock: r.clk, Gate: gov, Workers: 1})
	runner, err := query.New(query.Config{
		Settings: cfg, Store: r.st, Identity: entity.NewIdentity(r.st, r.clk), Tracker: noTracker{}, GH: noGH{},
		Governor: gov, Feed: ratefeed.New(gov, noGH{}, r.clk), Scheduler: sched, Clock: r.clk,
	})
	if err != nil {
		t.Fatal(err)
	}
	q := NewQueries(runner, r.st)
	if !q.Known("mine") || q.Known("nope") {
		t.Fatal("Known")
	}
	if m, err := q.Members(ctx, "mine"); err != nil || m.Baseline || len(m.Refs) != 0 {
		t.Fatalf("members before any run = %+v, %v", m, err)
	}
	now := time.Now()
	row := store.QueryRow{
		Key: "mine", Configured: true, IDs: []string{refA, refB}, Complete: true,
		FetchedAt: now, FreshUntil: now.Add(time.Hour), BaselineAt: now,
	}
	if err := r.st.PutQuery(ctx, row); err != nil {
		t.Fatal(err)
	}
	m, err := q.Members(ctx, "mine")
	if err != nil || !m.Baseline || len(m.Refs) != 2 || m.Refs[0] != refA || m.Refs[1] != refB {
		t.Fatalf("members = %+v, %v", m, err)
	}
	if err := q.Run(ctx, "mine"); err != nil {
		t.Fatalf("run of a fresh query = %v", err)
	}
}
