package feed

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/classify"
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
	r.poll(KindPR, "stale", "mine") // at the empty tail, position 0
	r.plain(refA)
	r.plain(refA)
	r.plain(refA)
	r.poll(KindPR, "current", "mine")
	r.clk.Advance(15 * day)
	r.plain(refA) // seq 4, newer than the retention
	rows, _, err := r.f.Prune(context.Background())
	if err != nil || rows != 3 {
		t.Fatalf("prune = %d, %v", rows, err)
	}

	_, err = r.f.Poll(context.Background(), PollRequest{Kind: KindPR, Consumer: "stale", Query: "mine"})
	wantExpired(t, err)
	// It stays expired, and an ack does not hide it.
	_, err = r.f.Poll(context.Background(), PollRequest{Kind: KindPR, Consumer: "stale", Query: "mine"})
	wantExpired(t, err)

	// A key acknowledged at the last pruned row (the oldest retained minus one) is served.
	if res := r.poll(KindPR, "current", "mine"); res.Position != 3 || len(res.Rows) != 1 {
		t.Fatalf("current key = %+v", res)
	}

	res, err := r.f.Poll(context.Background(), PollRequest{Kind: KindPR, Consumer: "stale", Query: "mine", Reset: true})
	if err != nil || res.Position != r.head() {
		t.Fatalf("reset = %+v, %v", res, err)
	}
	if res := r.poll(KindPR, "stale", "mine"); len(res.Rows) != 0 {
		t.Fatalf("poll after reset = %+v", res)
	}
}

func TestAKeyBelowTheHeadOfAnEmptyLogExpiresAndOneAtTheHeadNever(t *testing.T) {
	r := newRig(t, func(c *Config) { c.ConsumerStaleAfter = 60 * day })
	r.poll(KindPR, "behind", "mine")
	r.plain(refA)
	r.poll(KindPR, "caught-up", "mine")
	r.clk.Advance(15 * day)
	if rows, _, err := r.f.Prune(context.Background()); err != nil || rows != 1 {
		t.Fatalf("prune = %d, %v", rows, err)
	}
	_, err := r.f.Poll(context.Background(), PollRequest{Kind: KindPR, Consumer: "behind", Query: "mine"})
	wantExpired(t, err)
	if res := r.poll(KindPR, "caught-up", "mine"); len(res.Rows) != 0 {
		t.Fatalf("caught-up key = %+v", res)
	}
	if res := r.poll(KindPR, "caught-up", "mine"); res.Position != 1 {
		t.Fatalf("caught-up key moved: %+v", res)
	}
}

func TestAfterMovedAsideTheFirstPollOfEveryKeyIsCursorExpiredUntilReset(t *testing.T) {
	r := newRig(t)
	r.poll(KindPR, "old", "mine") // a key that predates the marker
	r.clk.Advance(time.Minute)
	if err := r.f.NoteMovedAside(context.Background(), r.clk.Now()); err != nil {
		t.Fatal(err)
	}
	r.clk.Advance(time.Second)
	ctx := context.Background()

	_, err := r.f.Poll(ctx, PollRequest{Kind: KindPR, Consumer: "old", Query: "mine"})
	wantExpired(t, err)
	_, err = r.f.Poll(ctx, PollRequest{Kind: KindPR, Consumer: "brand-new", Query: "mine"})
	wantExpired(t, err)
	wantExpired(t, r.f.Ack(ctx, KindPR, "brand-new", "mine", 0))
	if _, ok := r.pos(KindPR, "brand-new", "mine"); ok {
		t.Fatal("an expired poll or ack created a key")
	}
	if n := r.q.runCount("mine"); n != 1 {
		t.Fatalf("an expired answer ran the query: %d runs", n)
	}

	for _, c := range []string{"old", "brand-new"} {
		if _, err := r.f.Poll(ctx, PollRequest{Kind: KindPR, Consumer: c, Query: "mine", Reset: true}); err != nil {
			t.Fatalf("reset of %s: %v", c, err)
		}
		r.plain(refA)
		if res := r.poll(KindPR, c, "mine"); len(res.Rows) != 1 {
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
	_, err := r.f.Poll(ctx, PollRequest{Kind: KindPR, Consumer: "c", Query: "mine"})
	wantExpired(t, err)

	r.clk.Advance(7 * day) // consumer_stale_after
	head := r.plain(refA)
	res := r.poll(KindPR, "c", "mine")
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
		if _, err := r.f.Poll(ctx, PollRequest{Kind: KindPR, Consumer: name, Query: "mine"}); err != nil {
			t.Errorf("%s marker: %v", name, err)
		}
	}
	if _, err := r.f.Poll(ctx, PollRequest{Kind: KindPR, Consumer: "none", Query: "mine"}); err != nil {
		t.Errorf("no marker: %v", err)
	}
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

func TestOneConsumerCannotHoldBothTheCIAndThePRKeyOfOneQuery(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.poll(KindPR, "c", "mine")
	_, err := r.f.Poll(ctx, PollRequest{Kind: KindCI, Consumer: "c", Query: "mine"})
	wantConflict(t, err, "(c, pr, mine)")
	_, err = r.f.Poll(ctx, PollRequest{Kind: KindCI, Consumer: "c", Query: "mine", Reset: true})
	wantConflict(t, err, "(c, pr, mine)")
	wantConflict(t, r.f.Ack(ctx, KindCI, "c", "mine", 0), "(c, pr, mine)")
	if _, ok := r.pos(KindCI, "c", "mine"); ok {
		t.Fatal("a refused creation left a key")
	}

	// The reverse, a different consumer and a different query are allowed.
	r.poll(KindCI, "d", "mine")
	_, err = r.f.Poll(ctx, PollRequest{Kind: KindPR, Consumer: "d", Query: "mine"})
	wantConflict(t, err, "(d, ci, mine)")
	r.poll(KindPR, "e", "mine")
	r.poll(KindCI, "e", "team")
	r.poll(KindCI, "c2", "mine")

	// An existing key keeps working: the rule is on creation.
	r.plain(refA)
	if res := r.poll(KindPR, "c", "mine"); len(res.Rows) != 1 {
		t.Fatalf("existing key = %+v", res)
	}
}

func TestTheCIKeyDeliversOnlyCIChangesAndTheMembershipRowsNamingItsQuery(t *testing.T) {
	r := newRig(t)
	r.poll(KindCI, "notifier", "mine") // mine = {refA}
	r.plain(refA)
	ci := r.add(refA, []string{classify.KindCIChanged})
	r.add(refB, []string{classify.KindCIChanged}) // not a member
	entered := r.add(refA, []string{classify.KindEnteredQuery}, enteredField("mine"))
	r.add(refA, []string{classify.KindEnteredQuery}, enteredField("team")) // another query
	left := r.add(refB, []string{classify.KindLeftQuery}, leftField("mine"))
	removed := r.add(refOther, []string{classify.KindRemoved}, leftField("mine"))
	r.add(refA, []string{classify.KindReviewChanged})

	res := r.poll(KindCI, "notifier", "mine")
	eqSeqs(t, res.Rows, ci, entered, left, removed)

	// The CI key has its own cursor and ack, independent of the PR key's.
	r.ack(KindCI, "notifier", "mine", res.NextSeq)
	if p, _ := r.pos(KindCI, "notifier", "mine"); p != r.head() {
		t.Fatalf("ci position = %d", p)
	}
	if _, ok := r.pos(KindPR, "notifier", "mine"); ok {
		t.Fatal("an ack of the ci key created a pr key")
	}
}

func TestKeyLocksDoNotGrow(t *testing.T) {
	var l keyLocks
	for i := range 100 {
		unlock := l.lock(keyID{"c", "pr", string(rune('a' + i%26))})
		unlock()
	}
	if n := l.size(); n != 0 {
		t.Fatalf("%d locks retained", n)
	}
}
