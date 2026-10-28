package feed

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/engine"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

func TestNewRequiresItsParts(t *testing.T) {
	r := newRig(t)
	for name, cfg := range map[string]Config{
		"store":     {Spec: testSpec(), Queries: r.q, Retention: day, ConsumerStaleAfter: day},
		"queries":   {Spec: testSpec(), Store: r.st, Retention: day, ConsumerStaleAfter: day},
		"retention": {Spec: testSpec(), Store: r.st, Queries: r.q, ConsumerStaleAfter: day},
		"spec":      {Store: r.st, Queries: r.q, Retention: day, ConsumerStaleAfter: day},
		"stale":     {Spec: testSpec(), Store: r.st, Queries: r.q, Retention: day},
	} {
		if _, err := New(cfg); err == nil {
			t.Errorf("New without %s succeeded", name)
		}
	}
	if _, err := New(Config{Spec: testSpec(), Store: r.st, Queries: r.q, Retention: day, ConsumerStaleAfter: day}); err != nil {
		t.Errorf("New with a nil clock and observer: %v", err)
	}
}

func TestFirstPollStartsAtTheTailAndDeliversNoOlderRow(t *testing.T) {
	r := newRig(t)
	r.plain(refA)
	r.plain(refA)
	third := r.plain(refA)

	res := r.poll(viewAll, "pg-router", "mine")
	if len(res.Rows) != 0 || res.NextSeq != third || res.Position != third {
		t.Fatalf("first poll = %+v, want no rows at the tail %d", res, third)
	}
	if p, ok := r.pos(viewAll, "pg-router", "mine"); !ok || p != third {
		t.Fatalf("key = %d, %v; want created at the tail %d", p, ok, third)
	}
	fourth := r.plain(refA)
	res = r.poll(viewAll, "pg-router", "mine")
	eqSeqs(t, res.Rows, fourth)
	if res.NextSeq != fourth || res.Position != third {
		t.Fatalf("second poll = next %d position %d, want %d and %d", res.NextSeq, res.Position, fourth, third)
	}
}

func TestMissingBaselineCreatesNoKeyAndAckCreatesItAtNextSeq(t *testing.T) {
	r := newRig(t)
	r.q.set("fresh", false)
	r.plain(refA)
	head := r.plain(refA)

	res := r.poll(viewAll, "c", "fresh")
	if len(res.Rows) != 0 || res.NextSeq != head || res.Position != 0 {
		t.Fatalf("poll before baseline = %+v", res)
	}
	if _, ok := r.pos(viewAll, "c", "fresh"); ok {
		t.Fatal("a poll before the baseline created a key")
	}
	r.ack(viewAll, "c", "fresh", res.NextSeq)
	if p, ok := r.pos(viewAll, "c", "fresh"); !ok || p != head {
		t.Fatalf("key after Ack = %d, %v; want %d", p, ok, head)
	}

	// The baseline appears and a row follows: the key delivers it.
	r.q.set("fresh", true, refA)
	next := r.plain(refA)
	eqSeqs(t, r.poll(viewAll, "c", "fresh").Rows, next)
}

func TestCachedWithoutBaselineAnswersEmptyAtHeadAndCreatesNoKey(t *testing.T) {
	r := newRig(t)
	r.q.set("fresh", false)
	head := r.plain(refA)
	res, err := r.f.Poll(context.Background(), PollRequest{View: viewAll, Consumer: "c", Scope: "fresh", Cached: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 0 || res.NextSeq != head {
		t.Fatalf("cached poll = %+v", res)
	}
	if _, ok := r.pos(viewAll, "c", "fresh"); ok {
		t.Fatal("a cached poll before the baseline created a key")
	}
}

func TestDeliveryIsAtLeastOnce(t *testing.T) {
	r := newRig(t)
	r.poll(viewAll, "c", "mine") // creates the key at the empty tail
	s1 := r.plain(refA)
	s2 := r.plain(refA)

	first := r.poll(viewAll, "c", "mine")
	eqSeqs(t, first.Rows, s1, s2)
	if calls, _ := r.obs.total(); calls != 0 {
		t.Fatalf("redelivery reported on a first delivery: %d", calls)
	}

	again := r.poll(viewAll, "c", "mine")
	eqSeqs(t, again.Rows, s1, s2)
	if calls, rows := r.obs.total(); calls != 1 || rows != 2 {
		t.Fatalf("redelivery calls=%d rows=%d, want 1 and 2", calls, rows)
	}
	if got := r.obs.calls[0]; got != "c/mine" {
		t.Fatalf("redelivery key = %q", got)
	}

	r.ack(viewAll, "c", "mine", again.NextSeq)
	if res := r.poll(viewAll, "c", "mine"); len(res.Rows) != 0 || res.Position != s2 {
		t.Fatalf("poll after the ack = %+v", res)
	}
	s3 := r.plain(refA)
	eqSeqs(t, r.poll(viewAll, "c", "mine").Rows, s3)
	if calls, _ := r.obs.total(); calls != 1 {
		t.Fatalf("a poll after the ack reported a redelivery: %d calls", calls)
	}

	// A new row arriving between two unacknowledged polls is delivered, and only
	// the rows already handed count as redelivered.
	s4 := r.plain(refA)
	res := r.poll(viewAll, "c", "mine")
	eqSeqs(t, res.Rows, s3, s4)
	if calls, rows := r.obs.total(); calls != 2 || rows != 3 {
		t.Fatalf("redelivery calls=%d rows=%d, want 2 and 3", calls, rows)
	}
}

func TestAPollWithoutAnObserverIsFine(t *testing.T) {
	r := newRig(t, func(c *Config) { c.Observer = nil })
	r.poll(viewAll, "c", "mine")
	r.plain(refA)
	r.poll(viewAll, "c", "mine")
	if res := r.poll(viewAll, "c", "mine"); len(res.Rows) != 1 {
		t.Fatalf("rows = %d", len(res.Rows))
	}
}

func TestAckOnlyMovesForwardAndRefusesBeyondTheHead(t *testing.T) {
	r := newRig(t)
	r.poll(viewAll, "c", "mine")
	s1 := r.plain(refA)
	s2 := r.plain(refA)
	r.ack(viewAll, "c", "mine", s2)
	r.ack(viewAll, "c", "mine", s1) // a stale ack changes nothing
	if p, _ := r.pos(viewAll, "c", "mine"); p != s2 {
		t.Fatalf("position = %d after a stale ack, want %d", p, s2)
	}
	err := ackErr(r.f, context.Background(), viewAll, "c", "mine", s2+1)
	if !errors.Is(err, ErrAckBeyondHead) || !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("ack beyond the head = %v", err)
	}
	if p, _ := r.pos(viewAll, "c", "mine"); p != s2 {
		t.Fatalf("position = %d after a refused ack", p)
	}
	if err := ackErr(r.f, context.Background(), viewAll, "c", "mine", -1); !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("negative ack = %v", err)
	}
}

func TestResetReplaysTheMembersAndMovesTheKeyToTheHead(t *testing.T) {
	r := newRig(t)
	r.poll(viewAll, "c", "team")
	r.plain(refA)
	r.plain(refB)
	head := r.plain(refA)

	res, err := r.f.Poll(context.Background(), PollRequest{View: viewAll, Consumer: "c", Scope: "team", Reset: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 0 || res.NextSeq != head || res.Position != head {
		t.Fatalf("reset = %+v", res)
	}
	if len(res.Replay) != 2 || res.Replay[0] != refA || res.Replay[1] != refB {
		t.Fatalf("replay = %v, want the members in the stored order", res.Replay)
	}
	if p, _ := r.pos(viewAll, "c", "team"); p != head {
		t.Fatalf("position = %d, want the head %d", p, head)
	}
	if r.q.runCount("team") != 2 {
		t.Fatalf("a reset ran the query %d times in all, want once more", r.q.runCount("team"))
	}

	// A reset of a key that does not exist creates it, and a normal poll has no Replay.
	if _, err := r.f.Poll(context.Background(), PollRequest{View: viewAll, Consumer: "new", Scope: "team", Reset: true}); err != nil {
		t.Fatal(err)
	}
	if p, ok := r.pos(viewAll, "new", "team"); !ok || p != head {
		t.Fatalf("created key = %d, %v", p, ok)
	}
	if res := r.poll(viewAll, "new", "team"); res.Replay != nil {
		t.Fatalf("a normal poll carries Replay %v", res.Replay)
	}
}

func TestCachedNeverRunsTheQueryAndItsAbsenceRunsItOnce(t *testing.T) {
	r := newRig(t)
	r.poll(viewAll, "c", "mine")
	if n := r.q.runCount("mine"); n != 1 {
		t.Fatalf("runs = %d after one poll, want 1", n)
	}
	for range 3 {
		if _, err := r.f.Poll(context.Background(), PollRequest{View: viewAll, Consumer: "c", Scope: "mine", Cached: true}); err != nil {
			t.Fatal(err)
		}
	}
	if n := r.q.runCount("mine"); n != 1 {
		t.Fatalf("runs = %d after cached polls, want 1", n)
	}
	if _, err := r.f.Poll(context.Background(), PollRequest{View: viewAll, Consumer: "c", Scope: "mine", Cached: true, Reset: true}); err != nil {
		t.Fatal(err)
	}
	if n := r.q.runCount("mine"); n != 1 {
		t.Fatalf("a cached reset ran the query: %d", n)
	}
}

func TestAFailedRunIsReturnedAsItIsAndServesNoRows(t *testing.T) {
	r := newRig(t)
	r.poll(viewAll, "c", "mine")
	r.plain(refA)
	boom := errors.New("origin failure")
	r.q.runErr = boom
	if _, err := r.f.Poll(context.Background(), PollRequest{View: viewAll, Consumer: "c", Scope: "mine"}); !errors.Is(err, boom) {
		t.Fatalf("poll = %v, want the run's error", err)
	}
	res, err := r.f.Poll(context.Background(), PollRequest{View: viewAll, Consumer: "c", Scope: "mine", Cached: true})
	if err != nil || len(res.Rows) != 1 {
		t.Fatalf("cached poll after a failed run = %+v, %v", res, err)
	}
}

func TestAnUnknownQueryAndABadRequestAreRefused(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if _, err := r.f.Poll(ctx, PollRequest{View: viewAll, Consumer: "c", Scope: "nope"}); !errors.Is(err, ErrQueryNotRecognized) {
		t.Fatalf("poll of an unknown query = %v", err)
	}
	if err := ackErr(r.f, ctx, viewAll, "c", "nope", 0); !errors.Is(err, ErrQueryNotRecognized) {
		t.Fatalf("ack of an unknown query = %v", err)
	}
	if n := r.q.runCount("nope"); n != 0 {
		t.Fatalf("an unknown query was run %d times", n)
	}
	for name, req := range map[string]PollRequest{
		"kind":     {View: "nope", Consumer: "c", Scope: "mine"},
		"consumer": {View: viewAll, Scope: "mine"},
	} {
		if _, err := r.f.Poll(ctx, req); !errors.Is(err, scriptout.ErrInvalidArgument) {
			t.Errorf("poll with a bad %s = %v", name, err)
		}
	}
}

func TestAQuietQueryAdvancesPastOtherEntitiesRows(t *testing.T) {
	r := newRig(t)
	r.poll(viewAll, "c", "mine") // mine = {refA}
	r.plain(refOther)
	r.plain(refB)
	head := r.plain(refOther)

	res := r.poll(viewAll, "c", "mine")
	if len(res.Rows) != 0 || res.NextSeq != head {
		t.Fatalf("poll = %+v, want no rows and NextSeq at the head %d", res, head)
	}
	r.ack(viewAll, "c", "mine", res.NextSeq)
	ps, err := r.f.Positions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 1 || ps[0].AckedSeq != head || ps[0].LagRows != 0 || !ps[0].OldestUnacked.IsZero() {
		t.Fatalf("positions = %+v", ps)
	}
}

func TestLimitStopsTheScanAndNextSeqFollowsTheLastRow(t *testing.T) {
	r := newRig(t)
	r.poll(viewAll, "c", "mine")
	var want []int64
	for range 5 {
		r.plain(refOther) // skipped
		want = append(want, r.plain(refA))
	}
	var got []int64
	for range 6 {
		res, err := r.f.Poll(context.Background(), PollRequest{View: viewAll, Consumer: "c", Scope: "mine", Limit: 2})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Rows) > 2 {
			t.Fatalf("limit exceeded: %d rows", len(res.Rows))
		}
		got = append(got, seqs(res.Rows)...)
		r.ack(viewAll, "c", "mine", res.NextSeq)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("delivered %v, want %v", got, want)
	}
}

func TestRowsOfOneEntityAreDeliveredInSeqOrderAcrossGroups(t *testing.T) {
	r := newRig(t)
	r.poll(viewAll, "c", "mine")
	a := r.add(refA, []string{kindContent}, engine.Field{Name: "content", Before: mustJSON("a"), After: mustJSON("b")})
	b := r.add(refA, []string{kindOther}, engine.Field{Name: "other:1", After: mustJSON("x")})
	c := r.add(refA, []string{kindFlag}, engine.Field{Name: "run:1", After: mustJSON("x")})
	eqSeqs(t, r.poll(viewAll, "c", "mine").Rows, a, b, c)
}

func TestTheFilteredFeedDeliversMembersAndDepartures(t *testing.T) {
	r := newRig(t)
	r.poll(viewAll, "c", "mine") // mine = {refA}
	member := r.plain(refA)
	r.plain(refB) // not a member
	left := r.add(refB, []string{kindLeft}, leftField("mine"))
	r.add(refOther, []string{kindLeft}, leftField("team")) // another query
	removed := r.add(refOther, []string{kindRemoved}, leftField("mine"), leftField("team"))
	// An entered_query naming ANOTHER query is not this key's added, even for a member.
	otherEntered := r.add(refA, []string{kindEntered}, enteredField("team"))
	entered := r.add(refA, []string{kindEntered}, enteredField("mine"))

	res := r.poll(viewAll, "c", "mine")
	eqSeqs(t, res.Rows, member, left, removed, otherEntered, entered)
	changes := map[int64]string{}
	for _, row := range res.Rows {
		_, ch := Deliver(testSpec(), testSpec().Views[0], row, "mine", row.ID == refA)
		changes[row.Seq] = ch
	}
	want := map[int64]string{member: "changed", left: "removed", removed: "removed", otherEntered: "changed", entered: "added"}
	if !maps.Equal(changes, want) {
		t.Fatalf("changes = %v, want %v", changes, want)
	}
}

func TestCallsForOneKeyAreSerialisedAndDifferentKeysRunInParallel(t *testing.T) {
	r := newRig(t)
	inRun := make(chan string, 8)
	release := make(chan struct{})
	r.q.onRun = func(name string) {
		inRun <- name
		<-release
	}
	ctx := context.Background()
	var wg sync.WaitGroup
	poll := func(consumer, query string) {
		defer wg.Done()
		if _, err := r.f.Poll(ctx, PollRequest{View: viewAll, Consumer: consumer, Scope: query}); err != nil {
			t.Errorf("poll: %v", err)
		}
	}
	wg.Add(2)
	go poll("c", "mine")
	waitFor(t, inRun) // the first poll of consumer c is inside Run, holding its lock

	go poll("c", "mine") // the same consumer: must wait for the lock, never reaching Run
	select {
	case <-inRun:
		t.Fatal("a second call for the same key entered Run while the first held the key")
	case <-time.After(150 * time.Millisecond):
	}

	wg.Add(1)
	go poll("d", "team") // a different consumer runs at once
	waitFor(t, inRun)

	close(release)
	wg.Wait()
	if n := r.f.locks.size(); n != 0 {
		t.Fatalf("%d key locks left behind", n)
	}
}

func waitFor(t *testing.T, ch <-chan string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a call to enter Run")
	}
}

func TestConcurrentPollsAndAcksKeepThePositionMonotonic(t *testing.T) {
	r := newRig(t)
	r.poll(viewAll, "c", "mine")
	ctx := context.Background()
	var wg sync.WaitGroup
	for w := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			consumer := fmt.Sprintf("w%d", w%2)
			for range 25 {
				r.plain(refA)
				res, err := r.f.Poll(ctx, PollRequest{View: viewAll, Consumer: consumer, Scope: "mine"})
				if err != nil {
					t.Errorf("poll: %v", err)
					return
				}
				if err := ackErr(r.f, ctx, viewAll, consumer, "mine", res.NextSeq); err != nil {
					t.Errorf("ack: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
	if n := r.f.locks.size(); n != 0 {
		t.Fatalf("%d key locks left behind", n)
	}
	for _, c := range []string{"w0", "w1"} {
		res := r.poll(viewAll, c, "mine")
		r.ack(viewAll, c, "mine", res.NextSeq)
		if p, _ := r.pos(viewAll, c, "mine"); p != r.head() {
			t.Fatalf("%s position = %d, want the head %d", c, p, r.head())
		}
	}
}
