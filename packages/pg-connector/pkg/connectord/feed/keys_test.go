package feed

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

func wantExpired(t *testing.T, err error) {
	t.Helper()
	var ce *CursorExpiredError
	if !errors.As(err, &ce) {
		t.Fatalf("err = %v, want *CursorExpiredError", err)
	}
	if !strings.HasPrefix(ce.Message, "cursor_expired:") || !strings.Contains(ce.Message, "--reset") {
		t.Fatalf("message = %q", ce.Message)
	}
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatal("a cursor_expired answer is an invalid_argument")
	}
	if got := scriptout.CodeForError(err); got != "invalid_argument" {
		t.Fatalf("wire code = %q", got)
	}
}

func TestAKeyPastRetentionAnswersCursorExpiredUntilReset(t *testing.T) {
	r := newRig(t, func(c *Config) { c.ConsumerStaleAfter = 60 * day })
	r.poll(viewAll, "stale", "mine") // at the empty tail, position 0
	r.plain(refA)
	r.plain(refA)
	r.plain(refA)
	r.poll(viewAll, "current", "mine")
	r.clk.Advance(15 * day)
	r.plain(refA) // seq 4, newer than the retention
	rows, _, err := r.f.Prune(context.Background())
	if err != nil || rows != 3 {
		t.Fatalf("prune = %d, %v", rows, err)
	}

	_, err = r.f.Poll(context.Background(), PollRequest{View: viewAll, Consumer: "stale", Scope: "mine"})
	wantExpired(t, err)
	// It stays expired, and an ack does not hide it.
	_, err = r.f.Poll(context.Background(), PollRequest{View: viewAll, Consumer: "stale", Scope: "mine"})
	wantExpired(t, err)

	// A key acknowledged at the last pruned row (the oldest retained minus one) is served.
	if res := r.poll(viewAll, "current", "mine"); res.Position != 3 || len(res.Rows) != 1 {
		t.Fatalf("current key = %+v", res)
	}

	res, err := r.f.Poll(context.Background(), PollRequest{View: viewAll, Consumer: "stale", Scope: "mine", Reset: true})
	if err != nil || res.Position != r.head() {
		t.Fatalf("reset = %+v, %v", res, err)
	}
	if res := r.poll(viewAll, "stale", "mine"); len(res.Rows) != 0 {
		t.Fatalf("poll after reset = %+v", res)
	}
}

func TestAKeyBelowTheHeadOfAnEmptyLogExpiresAndOneAtTheHeadNever(t *testing.T) {
	r := newRig(t, func(c *Config) { c.ConsumerStaleAfter = 60 * day })
	r.poll(viewAll, "behind", "mine")
	r.plain(refA)
	r.poll(viewAll, "caught-up", "mine")
	r.clk.Advance(15 * day)
	if rows, _, err := r.f.Prune(context.Background()); err != nil || rows != 1 {
		t.Fatalf("prune = %d, %v", rows, err)
	}
	_, err := r.f.Poll(context.Background(), PollRequest{View: viewAll, Consumer: "behind", Scope: "mine"})
	wantExpired(t, err)
	if res := r.poll(viewAll, "caught-up", "mine"); len(res.Rows) != 0 {
		t.Fatalf("caught-up key = %+v", res)
	}
	if res := r.poll(viewAll, "caught-up", "mine"); res.Position != 1 {
		t.Fatalf("caught-up key moved: %+v", res)
	}
}

func TestAfterMovedAsideTheFirstPollOfEveryKeyIsCursorExpiredUntilReset(t *testing.T) {
	r := newRig(t)
	r.poll(viewAll, "old", "mine") // a key that predates the marker
	r.clk.Advance(time.Minute)
	if err := r.f.NoteMovedAside(context.Background(), r.clk.Now()); err != nil {
		t.Fatal(err)
	}
	r.clk.Advance(time.Second)
	ctx := context.Background()

	_, err := r.f.Poll(ctx, PollRequest{View: viewAll, Consumer: "old", Scope: "mine"})
	wantExpired(t, err)
	_, err = r.f.Poll(ctx, PollRequest{View: viewAll, Consumer: "brand-new", Scope: "mine"})
	wantExpired(t, err)
	wantExpired(t, ackErr(r.f, ctx, viewAll, "brand-new", "mine", 0))
	if _, ok := r.pos(viewAll, "brand-new", "mine"); ok {
		t.Fatal("an expired poll or ack created a key")
	}
	if n := r.q.runCount("mine"); n != 1 {
		t.Fatalf("an expired answer ran the query: %d runs", n)
	}

	for _, c := range []string{"old", "brand-new"} {
		if _, err := r.f.Poll(ctx, PollRequest{View: viewAll, Consumer: c, Scope: "mine", Reset: true}); err != nil {
			t.Fatalf("reset of %s: %v", c, err)
		}
		r.plain(refA)
		if res := r.poll(viewAll, c, "mine"); len(res.Rows) != 1 {
			t.Fatalf("%s after its reset: %+v", c, res)
		}
	}
}

func TestTheMovedAsideMarkerLapsesAfterConsumerStaleAfter(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if err := r.f.NoteMovedAside(ctx, r.clk.Now()); err != nil {
		t.Fatal(err)
	}
	r.plain(refA)
	_, err := r.f.Poll(ctx, PollRequest{View: viewAll, Consumer: "c", Scope: "mine"})
	wantExpired(t, err)

	r.clk.Advance(7 * day) // consumer_stale_after
	head := r.plain(refA)
	res := r.poll(viewAll, "c", "mine")
	if len(res.Rows) != 0 || res.Position != head {
		t.Fatalf("a key created after the marker's lifetime = %+v, want it at the tail %d", res, head)
	}
}

func TestAMissingUnparsableOrFutureMarkerCountsAsAbsent(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	for name, v := range map[string]string{
		"unparsable": "yesterday",
		"future":     r.clk.Now().Add(time.Hour).Format(time.RFC3339Nano),
	} {
		if err := r.st.PutMeta(ctx, MovedAsideKey, v); err != nil {
			t.Fatal(err)
		}
		if _, err := r.f.Poll(ctx, PollRequest{View: viewAll, Consumer: name, Scope: "mine"}); err != nil {
			t.Errorf("%s marker: %v", name, err)
		}
	}
	if _, err := r.f.Poll(ctx, PollRequest{View: viewAll, Consumer: "none", Scope: "mine"}); err != nil {
		t.Errorf("no marker: %v", err)
	}
}

// bound is the binding a conflict names: "consumer X is bound to view V, scope S".
func bound(consumer, view, scope string) string {
	return "consumer " + consumer + " is bound to view " + view + ", scope " + scope
}

func wantConflict(t *testing.T, err error, existing string) {
	t.Helper()
	var ke *KeyConflictError
	if !errors.As(err, &ke) {
		t.Fatalf("err = %v, want *KeyConflictError", err)
	}
	if ke.Existing != existing || !strings.Contains(ke.Message, existing) {
		t.Fatalf("conflict = %+v, want it to name %s", ke, existing)
	}
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatal("a key conflict is an invalid_argument")
	}
}

func TestOneConsumerCannotHoldTheCursorsOfTwoViewsOfOneScope(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.poll(viewAll, "c", "mine")
	_, err := r.f.Poll(ctx, PollRequest{View: viewFlagged, Consumer: "c", Scope: "mine"})
	wantConflict(t, err, bound("c", viewAll, "mine"))
	_, err = r.f.Poll(ctx, PollRequest{View: viewFlagged, Consumer: "c", Scope: "mine", Reset: true})
	wantConflict(t, err, bound("c", viewAll, "mine"))
	wantConflict(t, ackErr(r.f, ctx, viewFlagged, "c", "mine", 0), bound("c", viewAll, "mine"))
	if _, ok := r.pos(viewFlagged, "c", "mine"); ok {
		t.Fatal("a refused creation left a key")
	}

	// The reverse, a different consumer and a different query are allowed.
	r.poll(viewFlagged, "d", "mine")
	_, err = r.f.Poll(ctx, PollRequest{View: viewAll, Consumer: "d", Scope: "mine"})
	wantConflict(t, err, bound("d", viewFlagged, "mine"))
	r.poll(viewAll, "e", "mine")
	r.poll(viewFlagged, "e2", "team")
	r.poll(viewFlagged, "c2", "mine")

	// An existing key keeps working: the rule is on the call that names another view.
	r.plain(refA)
	if res := r.poll(viewAll, "c", "mine"); len(res.Rows) != 1 {
		t.Fatalf("existing key = %+v", res)
	}
}

func TestTheFlaggedViewDeliversOnlyFlaggedChangesAndTheMembershipRowsNamingItsScope(t *testing.T) {
	r := newRig(t)
	r.poll(viewFlagged, "notifier", "mine") // mine = {refA}
	r.plain(refA)
	flagged := r.add(refA, []string{kindFlag})
	r.add(refB, []string{kindFlag}) // not a member
	entered := r.add(refA, []string{kindEntered}, enteredField("mine"))
	r.add(refA, []string{kindEntered}, enteredField("team")) // another query
	left := r.add(refB, []string{kindLeft}, leftField("mine"))
	removed := r.add(refOther, []string{kindRemoved}, leftField("mine"))
	r.add(refA, []string{kindOther})

	res := r.poll(viewFlagged, "notifier", "mine")
	eqSeqs(t, res.Rows, flagged, entered, left, removed)

	// The flagged cursor has its own position and ack, independent of the all cursor's.
	r.ack(viewFlagged, "notifier", "mine", res.NextSeq)
	if p, _ := r.pos(viewFlagged, "notifier", "mine"); p != r.head() {
		t.Fatalf("flagged position = %d", p)
	}
	if _, ok := r.pos(viewAll, "notifier", "mine"); ok {
		t.Fatal("an ack of the flagged view created an all-view cursor")
	}
}

func TestKeyLocksDoNotGrow(t *testing.T) {
	var l keyLocks
	for i := range 100 {
		unlock := l.lock(string(rune('a' + i%26)))
		unlock()
	}
	if n := l.size(); n != 0 {
		t.Fatalf("%d locks retained", n)
	}
}

func TestAnEmptyViewIsTheDefaultViewAndAnUnregisteredOneIsInvalid(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if _, err := r.f.Poll(ctx, PollRequest{Consumer: "c", Scope: "mine"}); err != nil {
		t.Fatalf("poll of the default view: %v", err)
	}
	if _, ok := r.pos(viewAll, "c", "mine"); !ok {
		t.Fatal("the default view is the first registered view")
	}
	if _, err := r.f.Poll(ctx, PollRequest{View: "unregistered", Consumer: "d", Scope: "mine"}); !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("poll of an unregistered view = %v", err)
	}
	if err := ackErr(r.f, ctx, "unregistered", "d", "mine", 0); !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("ack of an unregistered view = %v", err)
	}
}

func TestTheConflictMessageEndsWithTheReasonOfTheConfig(t *testing.T) {
	r := newRig(t, func(c *Config) { c.ConflictReason = "example reason" })
	r.poll(viewAll, "c", "mine")
	_, err := r.f.Poll(context.Background(), PollRequest{View: viewFlagged, Consumer: "c", Scope: "mine"})
	var ke *KeyConflictError
	if !errors.As(err, &ke) || !strings.HasSuffix(ke.Message, ": example reason") {
		t.Fatalf("conflict = %v", err)
	}
	d := newRig(t)
	d.poll(viewAll, "c", "mine")
	_, err = d.f.Poll(context.Background(), PollRequest{View: viewFlagged, Consumer: "c", Scope: "mine"})
	if !errors.As(err, &ke) || !strings.Contains(ke.Message, "one consumer") {
		t.Fatalf("default conflict = %v", err)
	}
}

func TestAckAnswersTheStoredPosition(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.poll(viewAll, "c", "mine")
	r.plain(refA)
	second := r.plain(refA)
	res, err := r.f.Ack(ctx, AckRequest{View: viewAll, Consumer: "c", Scope: "mine", NextSeq: second})
	if err != nil || res.AckedSeq != second {
		t.Fatalf("ack = %+v, %v", res, err)
	}
	res, err = r.f.Ack(ctx, AckRequest{View: viewAll, Consumer: "c", Scope: "mine", NextSeq: 1})
	if err != nil || res.AckedSeq != second {
		t.Fatalf("a backward ack answers the stored position: %+v, %v", res, err)
	}
	res, err = r.f.Ack(ctx, AckRequest{View: viewAll, Consumer: "new", Scope: "mine", NextSeq: 1})
	if err != nil || res.AckedSeq != 1 {
		t.Fatalf("a creating ack = %+v, %v", res, err)
	}
}

func TestANewFeedRefusesAnInvalidSpec(t *testing.T) {
	r := newRig(t)
	bad := testSpec()
	bad.Views = nil
	if _, err := New(Config{Spec: bad, Store: r.st, Queries: r.q, Retention: day, ConsumerStaleAfter: day}); err == nil {
		t.Fatal("a spec without a view was accepted")
	}
}
