package feed

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// The one-cursor-per-consumer rules of the feed (INV-HOIST-8; HT-23, HT-28,
// HT-36). A consumer holds one cursor; the view and scope of the call that
// created it are fixed.

// watchedRig is newRig under the name the watched-delivery tests use: the one
// delivery mode of the registration is by watched.
func watchedRig(t *testing.T, opts ...func(*Config)) *rig {
	t.Helper()
	return newRig(t, opts...)
}

func wantScopeConflict(t *testing.T, err error, existing string) {
	t.Helper()
	var se *ScopeConflictError
	if !errors.As(err, &se) {
		t.Fatalf("err = %v, want *ScopeConflictError", err)
	}
	if se.Existing != existing || !strings.Contains(se.Message, existing) {
		t.Fatalf("conflict = %+v, want it to name %s", se, existing)
	}
	if !errors.Is(err, scriptout.ErrInvalidArgument) || scriptout.CodeForError(err) != "invalid_argument" {
		t.Fatalf("a scope conflict is an invalid_argument, got code %q", scriptout.CodeForError(err))
	}
}

func TestAConsumerHoldsOneRowWhoseViewAndScopeAreFixedAtCreation(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.poll(viewFlagged, "c", "mine")
	r.plain(refA)

	row, ok, err := r.st.GetConsumer(ctx, "c")
	if err != nil || !ok || row.View != viewFlagged || row.Scope != "mine" {
		t.Fatalf("stored cursor = %+v ok=%v err=%v, want the view and scope of the creating call", row, ok, err)
	}

	// Another scope is a scope conflict on a poll and on an ack, naming the bound one.
	_, err = r.f.Poll(ctx, PollRequest{View: viewFlagged, Consumer: "c", Scope: "team"})
	wantScopeConflict(t, err, bound("c", viewFlagged, "mine"))
	wantScopeConflict(t, ackErr(r.f, ctx, viewFlagged, "c", "team", 0), bound("c", viewFlagged, "mine"))
	if n := r.q.runCount("team"); n != 0 {
		t.Fatalf("a refused poll ran the query %d times", n)
	}

	// A refused call changes nothing, and the consumer still has the one row.
	if list, err := r.st.ListConsumers(ctx); err != nil || len(list) != 1 || list[0] != row {
		t.Fatalf("consumers = %+v err=%v, want the unchanged %+v", list, err, row)
	}
	if res := r.poll(viewFlagged, "c", "mine"); res.Position != row.AckedSeq {
		t.Fatalf("poll of the bound cursor = %+v", res)
	}
}

func TestAResetRebindsTheCursorToTheNewScopeAtTheHead(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.poll(viewAll, "c", "mine")
	r.plain(refA)
	head := r.plain(refB)

	res, err := r.f.Poll(ctx, PollRequest{View: viewAll, Consumer: "c", Scope: "team", Reset: true})
	if err != nil {
		t.Fatalf("reset to another scope: %v", err)
	}
	if res.Position != head || res.NextSeq != head || !slices.Equal(res.Replay, []string{refA, refB}) {
		t.Fatalf("reset = %+v, want the head %d and the members of team", res, head)
	}
	row, _, _ := r.st.GetConsumer(ctx, "c")
	if row.Scope != "team" || row.View != viewAll || row.AckedSeq != head {
		t.Fatalf("stored cursor = %+v, want rebound to team at the head", row)
	}
	if list, _ := r.st.ListConsumers(ctx); len(list) != 1 {
		t.Fatalf("a rebinding left %d rows, want the one", len(list))
	}

	// The old scope is now the conflicting one.
	_, err = r.f.Poll(ctx, PollRequest{View: viewAll, Consumer: "c", Scope: "mine"})
	wantScopeConflict(t, err, bound("c", viewAll, "team"))
	next := r.plain(refA)
	eqSeqs(t, r.poll(viewAll, "c", "team").Rows, next)
}

func TestScopeIsOptionalAndWhenGivenMustBeAConfiguredQuery(t *testing.T) {
	ctx := context.Background()

	byWatched := watchedRig(t)
	if _, err := byWatched.f.Poll(ctx, PollRequest{View: viewAll, Consumer: "c"}); err != nil {
		t.Fatalf("a poll without a scope by watched: %v", err)
	}
	if p, ok := byWatched.pos(viewAll, "c", ""); !ok || p != 0 {
		t.Fatalf("unscoped cursor = %d, %v, want it created", p, ok)
	}
	if err := ackErr(byWatched.f, ctx, viewAll, "d", "", 0); err != nil {
		t.Fatalf("an ack without a scope by watched: %v", err)
	}
	// A scope that is given must be a configured query, in either mode.
	if _, err := byWatched.f.Poll(ctx, PollRequest{View: viewAll, Consumer: "e", Scope: "nope"}); !errors.Is(err, ErrQueryNotRecognized) {
		t.Fatalf("poll of an unknown scope by watched = %v", err)
	}
	if err := ackErr(byWatched.f, ctx, viewAll, "e", "nope", 0); !errors.Is(err, ErrQueryNotRecognized) {
		t.Fatalf("ack of an unknown scope by watched = %v", err)
	}
	// An unscoped consumer cannot take a scope without a Reset, and may with one.
	if _, err := byWatched.f.Poll(ctx, PollRequest{View: viewAll, Consumer: "c", Scope: "mine"}); err == nil {
		t.Fatal("an unscoped cursor took a scope without a reset")
	} else {
		wantScopeConflict(t, err, bound("c", viewAll, "(unscoped)"))
	}
	if _, err := byWatched.f.Poll(ctx, PollRequest{View: viewAll, Consumer: "c", Scope: "mine", Reset: true}); err != nil {
		t.Fatalf("reset of an unscoped cursor to a scope: %v", err)
	}
	if _, ok := byWatched.pos(viewAll, "c", "mine"); !ok {
		t.Fatal("the reset did not bind the cursor to mine")
	}
}

func TestAnUnscopedFirstPollStartsAtTheTailOnlyAfterEveryWatchedBaseline(t *testing.T) {
	r := watchedRig(t)
	ctx := context.Background()
	r.q.set("quiet", false) // configured, not watched: its missing baseline never matters
	r.q.set("mine", true, refA)
	r.q.set("fresh", false)
	r.q.setWatched("mine")
	r.q.setWatched("fresh")
	r.plain(refA)
	head := r.plain(refA)

	res, err := r.f.Poll(ctx, PollRequest{View: viewAll, Consumer: "c"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 0 || res.NextSeq != head || res.Position != 0 {
		t.Fatalf("poll before every baseline = %+v, want empty with NextSeq at the head %d", res, head)
	}
	if _, ok, _ := r.st.GetConsumer(ctx, "c"); ok {
		t.Fatal("a poll before a watched baseline created a cursor")
	}
	// The ack of that NextSeq creates it.
	r.ack(viewAll, "c", "", res.NextSeq)
	if p, ok := r.pos(viewAll, "c", ""); !ok || p != head {
		t.Fatalf("cursor after the ack = %d, %v; want %d", p, ok, head)
	}

	// Once the last watched baseline exists a new consumer starts at the tail.
	r.q.set("fresh", true)
	res = r.poll(viewAll, "d", "")
	if res.Position != head || res.NextSeq != head {
		t.Fatalf("poll after the baselines = %+v, want a cursor at the tail %d", res, head)
	}
	if p, ok := r.pos(viewAll, "d", ""); !ok || p != head {
		t.Fatalf("cursor = %d, %v", p, ok)
	}

	// A scoped first poll keeps the rule of its one query.
	r.q.set("fresh", false)
	if res := r.poll(viewAll, "e", "mine"); res.Position != head {
		t.Fatalf("a scoped first poll waited for another query's baseline: %+v", res)
	}
}

func TestAnUncachedUnscopedPollRunsEveryWatchedQueryAndACachedOneRunsNone(t *testing.T) {
	r := watchedRig(t)
	ctx := context.Background()
	r.q.setWatched("mine")
	r.q.setWatched("team")

	r.poll(viewAll, "c", "")
	for _, name := range []string{"mine", "team"} {
		if n := r.q.runCount(name); n != 1 {
			t.Fatalf("watched query %s ran %d times, want once", name, n)
		}
	}
	for range 3 {
		if _, err := r.f.Poll(ctx, PollRequest{View: viewAll, Consumer: "c", Cached: true}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.f.Poll(ctx, PollRequest{View: viewAll, Consumer: "c", Cached: true, Reset: true}); err != nil {
		t.Fatal(err)
	}
	if r.q.runCount("mine") != 1 || r.q.runCount("team") != 1 {
		t.Fatalf("a cached poll ran queries: mine %d, team %d", r.q.runCount("mine"), r.q.runCount("team"))
	}

	// A scoped poll runs its one query, and only it.
	r.poll(viewAll, "d", "mine")
	if r.q.runCount("mine") != 2 || r.q.runCount("team") != 1 {
		t.Fatalf("a scoped poll ran mine %d and team %d times in all", r.q.runCount("mine"), r.q.runCount("team"))
	}

	// A failed run is returned as it is.
	boom := errors.New("origin failure")
	r.q.runErr = boom
	if _, err := r.f.Poll(ctx, PollRequest{View: viewAll, Consumer: "c"}); !errors.Is(err, boom) {
		t.Fatalf("unscoped poll = %v, want the run's error", err)
	}
}

func TestAnUnscopedPollOfAMembershipFreeConfigurationRunsNothing(t *testing.T) {
	r := watchedRig(t) // no query is watched
	r.poll(viewAll, "c", "")
	if n := r.q.runCount("mine") + r.q.runCount("team"); n != 0 {
		t.Fatalf("an unscoped poll ran %d unwatched queries", n)
	}
}

func TestResetUnderWatchedDeliveryReplaysWatchedMembersAndAppendsNoRow(t *testing.T) {
	r := watchedRig(t)
	ctx := context.Background()
	r.q.set("team", true, refA, refB)
	// The watched references: refB spelled in another case (the registration folds
	// it), and a watched entity that is a member of no query.
	r.q.setWatchedRefs("Example-Org/widget#2", refOther)
	r.plain(refA)
	head := r.plain(refB)

	scoped, err := r.f.Poll(ctx, PollRequest{View: viewAll, Consumer: "s", Scope: "team", Reset: true})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(scoped.Replay, []string{refB}) || scoped.Position != head {
		t.Fatalf("scoped reset = %+v, want the watched member %s at the head %d", scoped, refB, head)
	}
	unscoped, err := r.f.Poll(ctx, PollRequest{View: viewAll, Consumer: "u", Reset: true})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(unscoped.Replay, []string{"Example-Org/widget#2", refOther}) || unscoped.Position != head {
		t.Fatalf("unscoped reset = %+v, want every watched reference at the head %d", unscoped, head)
	}
	if p, ok := r.pos(viewAll, "u", ""); !ok || p != head {
		t.Fatalf("unscoped cursor = %d, %v", p, ok)
	}
	if got := r.head(); got != head {
		t.Fatalf("a reset appended a row: head %d -> %d", head, got)
	}
	// Nothing watched replays an empty list, not nil.
	r.q.setWatchedRefs()
	if res, err := r.f.Poll(ctx, PollRequest{View: viewAll, Consumer: "u", Reset: true}); err != nil || res.Replay == nil || len(res.Replay) != 0 {
		t.Fatalf("reset with nothing watched = %+v, %v", res, err)
	}
}

func TestACursorPastRetentionOrMovedAsideIsExpiredForAnUnscopedConsumerToo(t *testing.T) {
	r := watchedRig(t, func(c *Config) { c.ConsumerStaleAfter = 60 * day })
	ctx := context.Background()
	r.poll(viewAll, "u", "") // an unscoped cursor at the empty tail
	r.poll(viewAll, "s", "mine")
	r.plain(refA)
	r.plain(refA)
	r.clk.Advance(15 * day)
	r.plain(refA)
	if rows, _, err := r.f.Prune(ctx); err != nil || rows != 2 {
		t.Fatalf("prune = %d, %v", rows, err)
	}
	for _, c := range []struct{ consumer, scope string }{{"u", ""}, {"s", "mine"}} {
		_, err := r.f.Poll(ctx, PollRequest{View: viewAll, Consumer: c.consumer, Scope: c.scope})
		wantExpired(t, err)
	}
	// A reset recovers each of them.
	for _, c := range []struct{ consumer, scope string }{{"u", ""}, {"s", "mine"}} {
		if _, err := r.f.Poll(ctx, PollRequest{View: viewAll, Consumer: c.consumer, Scope: c.scope, Reset: true}); err != nil {
			t.Fatalf("reset of %s: %v", c.consumer, err)
		}
		if res := r.poll(viewAll, c.consumer, c.scope); len(res.Rows) != 0 || res.Position != r.head() {
			t.Fatalf("%s after its reset = %+v", c.consumer, res)
		}
	}

	// After a move-aside every consumer, scoped or not, existing or new, is told so.
	r.clk.Advance(time.Minute)
	if err := r.f.NoteMovedAside(ctx, r.clk.Now()); err != nil {
		t.Fatal(err)
	}
	r.clk.Advance(time.Second)
	for _, c := range []struct{ consumer, scope string }{{"u", ""}, {"s", "mine"}, {"new-unscoped", ""}, {"new-scoped", "team"}} {
		_, err := r.f.Poll(ctx, PollRequest{View: viewAll, Consumer: c.consumer, Scope: c.scope})
		wantExpired(t, err)
	}
	wantExpired(t, ackErr(r.f, ctx, viewAll, "new-unscoped", "", 0))
	if _, ok, _ := r.st.GetConsumer(ctx, "new-unscoped"); ok {
		t.Fatal("an expired answer created a cursor")
	}
	for _, c := range []struct{ consumer, scope string }{{"u", ""}, {"new-unscoped", ""}} {
		if _, err := r.f.Poll(ctx, PollRequest{View: viewAll, Consumer: c.consumer, Scope: c.scope, Reset: true}); err != nil {
			t.Fatalf("reset of %s: %v", c.consumer, err)
		}
	}
}

func TestCallsAndAcksForOneConsumerAreSerialised(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.poll(viewAll, "c", "mine")
	r.plain(refA)

	inRun := make(chan string, 4)
	release := make(chan struct{})
	r.q.onRun = func(name string) {
		inRun <- name
		<-release
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if _, err := r.f.Poll(ctx, PollRequest{View: viewAll, Consumer: "c", Scope: "mine"}); err != nil {
			t.Errorf("poll: %v", err)
		}
	}()
	waitFor(t, inRun) // the poll is inside Run, holding the consumer's lock

	acked := make(chan error, 1)
	go func() { acked <- ackErr(r.f, ctx, viewAll, "c", "mine", r.head()) }()
	select {
	case err := <-acked:
		t.Fatalf("an ack of the same consumer completed while a poll held it (err %v)", err)
	case <-time.After(150 * time.Millisecond):
	}
	// Another consumer is not held up by it.
	if err := ackErr(r.f, ctx, viewAll, "other", "mine", 0); err != nil {
		t.Fatalf("an ack of another consumer: %v", err)
	}
	close(release)
	wg.Wait()
	if err := <-acked; err != nil {
		t.Fatalf("ack: %v", err)
	}
	if n := r.f.KeyLocksHeld(); n != 0 {
		t.Fatalf("%d locks left behind", n)
	}
}

func TestConcurrentPollsAndAcksOfOneConsumerKeepItsPositionMonotonic(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.poll(viewAll, "c", "mine")
	var wg sync.WaitGroup
	var mu sync.Mutex
	var seen []int64
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 20 {
				r.plain(refA)
				res, err := r.f.Poll(ctx, PollRequest{View: viewAll, Consumer: "c", Scope: "mine"})
				if err != nil {
					t.Errorf("poll: %v", err)
					return
				}
				ack, err := r.f.Ack(ctx, AckRequest{View: viewAll, Consumer: "c", Scope: "mine", NextSeq: res.NextSeq})
				if err != nil {
					t.Errorf("ack: %v", err)
					return
				}
				mu.Lock()
				seen = append(seen, ack.AckedSeq)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if n := r.f.KeyLocksHeld(); n != 0 {
		t.Fatalf("%d locks left behind", n)
	}
	if list, _ := r.st.ListConsumers(ctx); len(list) != 1 {
		t.Fatalf("concurrent calls left %d rows for one consumer", len(list))
	}
	res := r.poll(viewAll, "c", "mine")
	r.ack(viewAll, "c", "mine", res.NextSeq)
	if p, _ := r.pos(viewAll, "c", "mine"); p != r.head() {
		t.Fatalf("position = %d, want the head %d", p, r.head())
	}
	if len(seen) != 120 {
		t.Fatalf("%d acks answered, want 120", len(seen))
	}
}

// TestACollapsedConsumerGetsTheScopeConflictNamingItsBoundScope is the feed half
// of HT-36: a store whose consumer held two rows is migrated by rung 6, and a poll
// of the scope that was dropped is answered with the scope conflict naming the
// bound one; the record of the dropped row is in store_meta.
func TestACollapsedConsumerGetsTheScopeConflictNamingItsBoundScope(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "store.db")
	old, _, err := store.Open(path, store.Options{Migrations: store.BuiltinMigrations()[:5], Synchronous: "OFF"})
	if err != nil {
		t.Fatal(err)
	}
	seen := t0.UnixMilli()
	for _, row := range []struct {
		consumer, view, scope string
		acked                 int64
	}{
		{"multi", viewAll, "mine", 9},
		{"multi", viewAll, "team", 4},
		{"alone", viewFlagged, "mine", 2},
	} {
		if _, err := old.DB().Exec(`INSERT INTO consumer (consumer, kind, query, acked_seq, seen_at) VALUES (?, ?, ?, ?, ?)`,
			row.consumer, row.view, row.scope, row.acked, seen); err != nil {
			t.Fatal(err)
		}
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	st, res, err := store.Open(path, store.Options{Synchronous: "OFF"})
	if err != nil || res.Outcome != store.OutcomeMigrated {
		t.Fatalf("open = %+v, %v", res, err)
	}
	t.Cleanup(func() { _ = st.Close() })

	q := newFakeQueries()
	q.set("mine", true, refA)
	q.set("team", true, refA, refB)
	clk := governor.NewManualClock(t0)
	f, err := New(Config{Spec: testSpec(), Store: st, Queries: q, Clock: clk, Retention: 14 * day, ConsumerStaleAfter: 7 * day})
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{t: t, st: st, clk: clk, q: q, f: f}
	for range 12 {
		r.plain(refA)
	}

	// The kept row is the lowest position: team at 4.
	_, err = f.Poll(ctx, PollRequest{View: viewAll, Consumer: "multi", Scope: "mine"})
	wantScopeConflict(t, err, bound("multi", viewAll, "team"))
	if res, err := f.Poll(ctx, PollRequest{View: viewAll, Consumer: "multi", Scope: "team"}); err != nil || res.Position != 4 {
		t.Fatalf("poll of the bound scope = %+v, %v, want the kept position 4", res, err)
	}
	// A consumer of one row is untouched, view included.
	if res, err := f.Poll(ctx, PollRequest{View: viewFlagged, Consumer: "alone", Scope: "mine"}); err != nil || res.Position != 2 {
		t.Fatalf("poll of the single-row consumer = %+v, %v", res, err)
	}
	if _, err := f.Poll(ctx, PollRequest{View: viewAll, Consumer: "alone", Scope: "mine"}); err == nil {
		t.Fatal("the single-row consumer changed its view")
	}

	raw, ok, err := st.GetMeta(ctx, store.ConsumerCollapsedPrefix+"multi")
	if err != nil || !ok {
		t.Fatalf("collapse record = %q ok=%v err=%v", raw, ok, err)
	}
	var dropped []store.CollapsedRow
	if err := json.Unmarshal([]byte(raw), &dropped); err != nil || !slices.Equal(dropped, []store.CollapsedRow{{View: viewAll, Scope: "mine", AckedSeq: 9}}) {
		t.Fatalf("collapse record = %s (%v), want the dropped mine at 9", raw, err)
	}
	if _, ok, _ := st.GetMeta(ctx, store.ConsumerCollapsedPrefix+"alone"); ok {
		t.Fatal("a one-row consumer has a collapse record")
	}
}
