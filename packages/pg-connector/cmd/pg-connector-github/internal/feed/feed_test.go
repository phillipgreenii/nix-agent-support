package feed

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/classify"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

func TestNewRequiresItsParts(t *testing.T) {
	r := newRig(t)
	for name, cfg := range map[string]Config{
		"store":     {Queries: r.q, Retention: day, ConsumerStaleAfter: day},
		"queries":   {Store: r.st, Retention: day, ConsumerStaleAfter: day},
		"retention": {Store: r.st, Queries: r.q, ConsumerStaleAfter: day},
		"stale":     {Store: r.st, Queries: r.q, Retention: day},
	} {
		if _, err := New(cfg); err == nil {
			t.Errorf("New without %s succeeded", name)
		}
	}
	if _, err := New(Config{Store: r.st, Queries: r.q, Retention: day, ConsumerStaleAfter: day}); err != nil {
		t.Errorf("New with a nil clock and observer: %v", err)
	}
}

func TestFirstPollStartsAtTheTailAndDeliversNoOlderRow(t *testing.T) {
	r := newRig(t)
	r.plain(refA)
	r.plain(refA)
	third := r.plain(refA)

	res := r.poll(KindPR, "pg-router", "mine")
	if len(res.Rows) != 0 || res.NextSeq != third || res.Position != third {
		t.Fatalf("first poll = %+v, want no rows at the tail %d", res, third)
	}
	if p, ok := r.pos(KindPR, "pg-router", "mine"); !ok || p != third {
		t.Fatalf("key = %d, %v; want created at the tail %d", p, ok, third)
	}
	fourth := r.plain(refA)
	res = r.poll(KindPR, "pg-router", "mine")
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

	res := r.poll(KindPR, "c", "fresh")
	if len(res.Rows) != 0 || res.NextSeq != head || res.Position != 0 {
		t.Fatalf("poll before baseline = %+v", res)
	}
	if _, ok := r.pos(KindPR, "c", "fresh"); ok {
		t.Fatal("a poll before the baseline created a key")
	}
	r.ack(KindPR, "c", "fresh", res.NextSeq)
	if p, ok := r.pos(KindPR, "c", "fresh"); !ok || p != head {
		t.Fatalf("key after Ack = %d, %v; want %d", p, ok, head)
	}

	// The baseline appears and a row follows: the key delivers it.
	r.q.set("fresh", true, refA)
	next := r.plain(refA)
	eqSeqs(t, r.poll(KindPR, "c", "fresh").Rows, next)
}

func TestCachedWithoutBaselineAnswersEmptyAtHeadAndCreatesNoKey(t *testing.T) {
	r := newRig(t)
	r.q.set("fresh", false)
	head := r.plain(refA)
	res, err := r.f.Poll(context.Background(), PollRequest{Kind: KindPR, Consumer: "c", Query: "fresh", Cached: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 0 || res.NextSeq != head {
		t.Fatalf("cached poll = %+v", res)
	}
	if _, ok := r.pos(KindPR, "c", "fresh"); ok {
		t.Fatal("a cached poll before the baseline created a key")
	}
}

func TestDeliveryIsAtLeastOnce(t *testing.T) {
	r := newRig(t)
	r.poll(KindPR, "c", "mine") // creates the key at the empty tail
	s1 := r.plain(refA)
	s2 := r.plain(refA)

	first := r.poll(KindPR, "c", "mine")
	eqSeqs(t, first.Rows, s1, s2)
	if calls, _ := r.obs.total(); calls != 0 {
		t.Fatalf("redelivery reported on a first delivery: %d", calls)
	}

	again := r.poll(KindPR, "c", "mine")
	eqSeqs(t, again.Rows, s1, s2)
	if calls, rows := r.obs.total(); calls != 1 || rows != 2 {
		t.Fatalf("redelivery calls=%d rows=%d, want 1 and 2", calls, rows)
	}
	if got := r.obs.calls[0]; got != "c/mine" {
		t.Fatalf("redelivery key = %q", got)
	}

	r.ack(KindPR, "c", "mine", again.NextSeq)
	if res := r.poll(KindPR, "c", "mine"); len(res.Rows) != 0 || res.Position != s2 {
		t.Fatalf("poll after the ack = %+v", res)
	}
	s3 := r.plain(refA)
	eqSeqs(t, r.poll(KindPR, "c", "mine").Rows, s3)
	if calls, _ := r.obs.total(); calls != 1 {
		t.Fatalf("a poll after the ack reported a redelivery: %d calls", calls)
	}

	// A new row arriving between two unacknowledged polls is delivered, and only
	// the rows already handed count as redelivered.
	s4 := r.plain(refA)
	res := r.poll(KindPR, "c", "mine")
	eqSeqs(t, res.Rows, s3, s4)
	if calls, rows := r.obs.total(); calls != 2 || rows != 3 {
		t.Fatalf("redelivery calls=%d rows=%d, want 2 and 3", calls, rows)
	}
}

func TestAPollWithoutAnObserverIsFine(t *testing.T) {
	r := newRig(t, func(c *Config) { c.Observer = nil })
	r.poll(KindPR, "c", "mine")
	r.plain(refA)
	r.poll(KindPR, "c", "mine")
	if res := r.poll(KindPR, "c", "mine"); len(res.Rows) != 1 {
		t.Fatalf("rows = %d", len(res.Rows))
	}
}

func TestAckOnlyMovesForwardAndRefusesBeyondTheHead(t *testing.T) {
	r := newRig(t)
	r.poll(KindPR, "c", "mine")
	s1 := r.plain(refA)
	s2 := r.plain(refA)
	r.ack(KindPR, "c", "mine", s2)
	r.ack(KindPR, "c", "mine", s1) // a stale ack changes nothing
	if p, _ := r.pos(KindPR, "c", "mine"); p != s2 {
		t.Fatalf("position = %d after a stale ack, want %d", p, s2)
	}
	err := r.f.Ack(context.Background(), KindPR, "c", "mine", s2+1)
	if !errors.Is(err, ErrAckBeyondHead) || !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("ack beyond the head = %v", err)
	}
	if p, _ := r.pos(KindPR, "c", "mine"); p != s2 {
		t.Fatalf("position = %d after a refused ack", p)
	}
	if err := r.f.Ack(context.Background(), KindPR, "c", "mine", -1); !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("negative ack = %v", err)
	}
}

func TestResetReplaysTheMembersAndMovesTheKeyToTheHead(t *testing.T) {
	r := newRig(t)
	r.poll(KindPR, "c", "team")
	r.plain(refA)
	r.plain(refB)
	head := r.plain(refA)

	res, err := r.f.Poll(context.Background(), PollRequest{Kind: KindPR, Consumer: "c", Query: "team", Reset: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 0 || res.NextSeq != head || res.Position != head {
		t.Fatalf("reset = %+v", res)
	}
	if len(res.Replay) != 2 || res.Replay[0] != refA || res.Replay[1] != refB {
		t.Fatalf("replay = %v, want the members in the stored order", res.Replay)
	}
	if p, _ := r.pos(KindPR, "c", "team"); p != head {
		t.Fatalf("position = %d, want the head %d", p, head)
	}
	if r.q.runCount("team") != 2 {
		t.Fatalf("a reset ran the query %d times in all, want once more", r.q.runCount("team"))
	}

	// A reset of a key that does not exist creates it, and a normal poll has no Replay.
	if _, err := r.f.Poll(context.Background(), PollRequest{Kind: KindPR, Consumer: "new", Query: "team", Reset: true}); err != nil {
		t.Fatal(err)
	}
	if p, ok := r.pos(KindPR, "new", "team"); !ok || p != head {
		t.Fatalf("created key = %d, %v", p, ok)
	}
	if res := r.poll(KindPR, "new", "team"); res.Replay != nil {
		t.Fatalf("a normal poll carries Replay %v", res.Replay)
	}
}

func TestCachedNeverRunsTheQueryAndItsAbsenceRunsItOnce(t *testing.T) {
	r := newRig(t)
	r.poll(KindPR, "c", "mine")
	if n := r.q.runCount("mine"); n != 1 {
		t.Fatalf("runs = %d after one poll, want 1", n)
	}
	for range 3 {
		if _, err := r.f.Poll(context.Background(), PollRequest{Kind: KindPR, Consumer: "c", Query: "mine", Cached: true}); err != nil {
			t.Fatal(err)
		}
	}
	if n := r.q.runCount("mine"); n != 1 {
		t.Fatalf("runs = %d after cached polls, want 1", n)
	}
	if _, err := r.f.Poll(context.Background(), PollRequest{Kind: KindPR, Consumer: "c", Query: "mine", Cached: true, Reset: true}); err != nil {
		t.Fatal(err)
	}
	if n := r.q.runCount("mine"); n != 1 {
		t.Fatalf("a cached reset ran the query: %d", n)
	}
}

func TestAFailedRunIsReturnedAsItIsAndServesNoRows(t *testing.T) {
	r := newRig(t)
	r.poll(KindPR, "c", "mine")
	r.plain(refA)
	boom := errors.New("origin failure")
	r.q.runErr = boom
	if _, err := r.f.Poll(context.Background(), PollRequest{Kind: KindPR, Consumer: "c", Query: "mine"}); !errors.Is(err, boom) {
		t.Fatalf("poll = %v, want the run's error", err)
	}
	res, err := r.f.Poll(context.Background(), PollRequest{Kind: KindPR, Consumer: "c", Query: "mine", Cached: true})
	if err != nil || len(res.Rows) != 1 {
		t.Fatalf("cached poll after a failed run = %+v, %v", res, err)
	}
}

func TestAnUnknownQueryAndABadRequestAreRefused(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if _, err := r.f.Poll(ctx, PollRequest{Kind: KindPR, Consumer: "c", Query: "nope"}); !errors.Is(err, ErrQueryNotRecognized) {
		t.Fatalf("poll of an unknown query = %v", err)
	}
	if err := r.f.Ack(ctx, KindPR, "c", "nope", 0); !errors.Is(err, ErrQueryNotRecognized) {
		t.Fatalf("ack of an unknown query = %v", err)
	}
	if n := r.q.runCount("nope"); n != 0 {
		t.Fatalf("an unknown query was run %d times", n)
	}
	for name, req := range map[string]PollRequest{
		"kind":     {Kind: "issue", Consumer: "c", Query: "mine"},
		"consumer": {Kind: KindPR, Query: "mine"},
		"query":    {Kind: KindPR, Consumer: "c"},
	} {
		if _, err := r.f.Poll(ctx, req); !errors.Is(err, scriptout.ErrInvalidArgument) {
			t.Errorf("poll with a bad %s = %v", name, err)
		}
	}
}

func TestAQuietQueryAdvancesPastOtherEntitiesRows(t *testing.T) {
	r := newRig(t)
	r.poll(KindPR, "c", "mine") // mine = {refA}
	r.plain(refOther)
	r.plain(refB)
	head := r.plain(refOther)

	res := r.poll(KindPR, "c", "mine")
	if len(res.Rows) != 0 || res.NextSeq != head {
		t.Fatalf("poll = %+v, want no rows and NextSeq at the head %d", res, head)
	}
	r.ack(KindPR, "c", "mine", res.NextSeq)
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
	r.poll(KindPR, "c", "mine")
	var want []int64
	for range 5 {
		r.plain(refOther) // skipped
		want = append(want, r.plain(refA))
	}
	var got []int64
	for range 6 {
		res, err := r.f.Poll(context.Background(), PollRequest{Kind: KindPR, Consumer: "c", Query: "mine", Limit: 2})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Rows) > 2 {
			t.Fatalf("limit exceeded: %d rows", len(res.Rows))
		}
		got = append(got, seqs(res.Rows)...)
		r.ack(KindPR, "c", "mine", res.NextSeq)
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("delivered %v, want %v", got, want)
	}
}

func TestRowsOfOneEntityAreDeliveredInSeqOrderAcrossGroups(t *testing.T) {
	r := newRig(t)
	r.poll(KindPR, "c", "mine")
	a := r.add(refA, []string{classify.KindHeadChanged}, classify.Field{Name: "head_sha", Before: mustJSON("a"), After: mustJSON("b")})
	b := r.add(refA, []string{classify.KindReviewChanged}, classify.Field{Name: "review:1", After: mustJSON("APPROVED")})
	c := r.add(refA, []string{classify.KindCIChanged}, classify.Field{Name: "run:1", After: mustJSON("x")})
	eqSeqs(t, r.poll(KindPR, "c", "mine").Rows, a, b, c)
}

func TestTheFilteredFeedDeliversMembersAndDepartures(t *testing.T) {
	r := newRig(t)
	r.poll(KindPR, "c", "mine") // mine = {refA}
	member := r.plain(refA)
	r.plain(refB) // not a member
	left := r.add(refB, []string{classify.KindLeftQuery}, leftField("mine"))
	r.add(refOther, []string{classify.KindLeftQuery}, leftField("team")) // another query
	removed := r.add(refOther, []string{classify.KindRemoved}, leftField("mine"), leftField("team"))
	// An entered_query naming ANOTHER query is not this key's added, even for a member.
	otherEntered := r.add(refA, []string{classify.KindEnteredQuery}, enteredField("team"))
	entered := r.add(refA, []string{classify.KindEnteredQuery}, enteredField("mine"))

	res := r.poll(KindPR, "c", "mine")
	eqSeqs(t, res.Rows, member, left, removed, otherEntered, entered)
	changes := map[int64]string{}
	for _, row := range res.Rows {
		_, ch := DeliveryFor(row, "mine", map[string]bool{refA: true}[row.ID])
		changes[row.Seq] = ch
	}
	want := map[int64]string{member: "changed", left: "removed", removed: "removed", otherEntered: "changed", entered: "added"}
	if fmt.Sprint(changes) != fmt.Sprint(want) {
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
		if _, err := r.f.Poll(ctx, PollRequest{Kind: KindPR, Consumer: consumer, Query: query}); err != nil {
			t.Errorf("poll: %v", err)
		}
	}
	wg.Add(2)
	go poll("c", "mine")
	waitFor(t, inRun) // the first poll of key c/mine is inside Run, holding its lock

	go poll("c", "mine") // the same key: must wait for the lock, never reaching Run
	select {
	case <-inRun:
		t.Fatal("a second call for the same key entered Run while the first held the key")
	case <-time.After(150 * time.Millisecond):
	}

	wg.Add(1)
	go poll("c", "team") // a different key runs at once
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
	r.poll(KindPR, "c", "mine")
	ctx := context.Background()
	var wg sync.WaitGroup
	for w := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			consumer := fmt.Sprintf("w%d", w%2)
			for range 25 {
				r.plain(refA)
				res, err := r.f.Poll(ctx, PollRequest{Kind: KindPR, Consumer: consumer, Query: "mine"})
				if err != nil {
					t.Errorf("poll: %v", err)
					return
				}
				if err := r.f.Ack(ctx, KindPR, consumer, "mine", res.NextSeq); err != nil {
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
		res := r.poll(KindPR, c, "mine")
		r.ack(KindPR, c, "mine", res.NextSeq)
		if p, _ := r.pos(KindPR, c, "mine"); p != r.head() {
			t.Fatalf("%s position = %d, want the head %d", c, p, r.head())
		}
	}
}
