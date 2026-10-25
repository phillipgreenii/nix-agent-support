package feed

import (
	"context"
	"errors"
	"slices"
	"sort"
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

func (f *fakeRunner) Names() []string {
	var names []string
	for name := range f.known {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (f *fakeRunner) Watched(string) bool { return false }

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
	res, err := f.Poll(ctx, PollRequest{View: KindPR, Consumer: "c", Scope: "mine"})
	if err != nil || fr.fetches != 1 {
		t.Fatalf("poll = %+v, %v (fetches %d)", res, err, fr.fetches)
	}
	if _, ok := r.pos(KindPR, "c", "mine"); !ok {
		t.Fatal("no key after a poll with a baseline")
	}
	if _, err := f.Poll(ctx, PollRequest{View: KindPR, Consumer: "c", Scope: "nope"}); !errors.Is(err, ErrQueryNotRecognized) {
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
	cfg.PR.Queries = map[string]schema.QueryDef{"mine": {Expr: schema.QueryExpr{"author:phillipgreenii is:open"}}}
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
	q := NewQueries(runner, r.st, entity.NewIdentity(r.st, r.clk))
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

// TestNewQueriesTakesNamesAndWatchedFromTheDaemonConfigAndListsWatchedRefs pins
// the three methods the shared interface adds to the legacy three: the names and
// the watched flag come from query.Settings, and WatchedRefs lists the watched
// entities the registration delivers by.
func TestNewQueriesTakesNamesAndWatchedFromTheDaemonConfigAndListsWatchedRefs(t *testing.T) {
	r := newRig(t)
	cfg := config.Defaults()
	cfg.PR.Queries = map[string]schema.QueryDef{
		"zeta":  {Expr: schema.QueryExpr{"is:open"}},
		"alpha": {Expr: schema.QueryExpr{"is:open author:example-author"}, Watched: true},
	}
	gov, err := governor.New(ratefeed.GovernorConfig(cfg.Settings), governor.Options{Clock: r.clk})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := query.New(query.Config{
		Settings: cfg, Store: r.st, Identity: entity.NewIdentity(r.st, r.clk), Tracker: noTracker{}, GH: noGH{},
		Governor: gov, Feed: ratefeed.New(gov, noGH{}, r.clk),
		Scheduler: scheduler.New(scheduler.Options{Clock: r.clk, Gate: gov, Workers: 1}), Clock: r.clk,
	})
	if err != nil {
		t.Fatal(err)
	}
	q := NewQueries(runner, r.st, entity.NewIdentity(r.st, r.clk))
	if got := q.Names(); !slices.Equal(got, []string{"alpha", "zeta"}) {
		t.Fatalf("Names = %v, want the configured names sorted", got)
	}
	if !q.Watched("alpha") || q.Watched("zeta") || q.Watched("nope") {
		t.Fatalf("Watched = alpha %v, zeta %v, nope %v", q.Watched("alpha"), q.Watched("zeta"), q.Watched("nope"))
	}
	ctx := context.Background()
	if refs, err := q.WatchedRefs(ctx); err != nil || len(refs) != 0 {
		t.Fatalf("WatchedRefs with nothing watched = %v, %v, want none", refs, err)
	}

	// Three watched PRs (the flag set at distinct times), one unwatched, one
	// watched without an alias row and one watched but tombstoned: only the three
	// with a recorded reference and no tombstone are listed, in the order the flag
	// was first set.
	ids := entity.NewIdentity(r.st, r.clk)
	put := func(id, ref string) {
		t.Helper()
		parsed, err := entity.ParseRef(ref)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ids.Put(ctx, id, parsed); err != nil {
			t.Fatal(err)
		}
	}
	watch := func(id string, at time.Time) {
		t.Helper()
		err := r.st.InTx(ctx, func(tx *store.Tx) error {
			_, err := tx.SetWatched(ctx, "pr", id, "query", at)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	put("N2", "acme-org/widget#2")
	put("N1", "acme-org/widget#1")
	put("N3", "acme-org/Gadget#3")
	put("N4", "acme-org/widget#4")
	put("GONE", "acme-org/widget#9")
	watch("N2", t0)
	watch("N1", t0.Add(time.Minute))
	watch("NOALIAS", t0.Add(2*time.Minute))
	watch("N3", t0.Add(3*time.Minute))
	watch("GONE", t0.Add(4*time.Minute))
	if err := r.st.InTx(ctx, func(tx *store.Tx) error { return tx.EnsureEntity(ctx, "pr", "N4") }); err != nil {
		t.Fatal(err)
	}
	gone, ok, err := r.st.GetEntity(ctx, "pr", "GONE")
	if err != nil || !ok {
		t.Fatalf("entity GONE = %v, %v", ok, err)
	}
	gone.TombstonedAt = t0
	if err := r.st.PutEntity(ctx, gone); err != nil {
		t.Fatal(err)
	}
	want := []string{"acme-org/widget#2", "acme-org/widget#1", "acme-org/Gadget#3"}
	if refs, err := q.WatchedRefs(ctx); err != nil || !slices.Equal(refs, want) {
		t.Fatalf("WatchedRefs = %v, %v, want %v", refs, err, want)
	}
}
