package feed

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/classify"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// The GitHub registration delivers by the watched flag: a log row carries the
// flag's snapshot and the feed drops a row whose snapshot is unset, whatever
// the query membership is. The shared feed owns the proofs of the rule; these
// tests pin it on the GitHub registration (changes.Spec) the daemon runs.

// addUnwatched appends a row whose watched snapshot is unset (a row of a PR that
// was only read).
func (r *rig) addUnwatched(ref string, kinds []string) int64 {
	r.t.Helper()
	var seq int64
	err := r.st.InTx(context.Background(), func(tx *store.Tx) error {
		var aerr error
		seq, aerr = tx.AppendChange(context.Background(), store.ChangeRow{
			Kind: "pr", ID: ref, Version: 1, Kinds: kinds, Origin: "refresh", At: r.clk.Now(),
		})
		return aerr
	})
	if err != nil {
		r.t.Fatal(err)
	}
	return seq
}

func TestAnUnwatchedRowIsNeverDeliveredToAMemberOfTheQuery(t *testing.T) {
	r := newRig(t)
	r.poll(KindPR, "c", "mine") // mine = {refA}
	r.addUnwatched(refA, []string{classify.KindHeadChanged})
	want := r.plain(refA)

	res := r.poll(KindPR, "c", "mine")
	eqSeqs(t, res.Rows, want)
}

func TestTheFeedWorksWithoutAQueryAndDeliversEveryWatchedRow(t *testing.T) {
	r := newRig(t)
	if res := r.poll(KindPR, "c", ""); len(res.Rows) != 0 {
		t.Fatalf("first unscoped poll delivered %v, want none (the key starts at the tail)", seqs(res.Rows))
	}
	a := r.plain(refA)
	r.addUnwatched(refB, []string{classify.KindHeadChanged})
	other := r.plain(refOther) // watched, in no configured query

	res := r.poll(KindPR, "c", "")
	eqSeqs(t, res.Rows, a, other)
	if pos, ok := r.pos(KindPR, "c", ""); !ok || pos != 0 {
		t.Fatalf("unscoped cursor = %d, %v, want a key at the tail it started at", pos, ok)
	}
	r.ack(KindPR, "c", "", res.NextSeq)
	if res := r.poll(KindPR, "c", ""); len(res.Rows) != 0 {
		t.Fatalf("poll after the ack delivered %v", seqs(res.Rows))
	}
}

func TestAQueryStillFiltersTheWatchedRows(t *testing.T) {
	r := newRig(t)
	r.poll(KindPR, "c", "mine") // mine = {refA}
	a := r.plain(refA)
	r.plain(refOther)

	res := r.poll(KindPR, "c", "mine")
	eqSeqs(t, res.Rows, a)
}

func TestAnUnscopedResetReplaysTheWatchedReferencesAndAScopedOneTheirIntersection(t *testing.T) {
	r := newRig(t)
	r.q.set("mine", true, refA)
	r.q.set("team", true, refA, refB)

	res, err := r.f.Poll(context.Background(), PollRequest{View: KindPR, Consumer: "u", Reset: true})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{refA, refB}; !slices.Equal(res.Replay, want) {
		t.Fatalf("unscoped replay = %v, want %v", res.Replay, want)
	}
	res, err = r.f.Poll(context.Background(), PollRequest{View: KindPR, Consumer: "s", Scope: "mine", Reset: true})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{refA}; !slices.Equal(res.Replay, want) {
		t.Fatalf("scoped replay = %v, want %v", res.Replay, want)
	}
}

func TestAnUnscopedKeyIsCursorExpiredAfterAMovedAsideStoreUntilReset(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.poll(KindPR, "u", "")
	r.clk.Advance(time.Minute)
	if err := r.f.NoteMovedAside(ctx, r.clk.Now()); err != nil {
		t.Fatal(err)
	}
	r.clk.Advance(time.Second)

	_, err := r.f.Poll(ctx, PollRequest{View: KindPR, Consumer: "u"})
	wantExpired(t, err)
	_, err = r.f.Poll(ctx, PollRequest{View: KindPR, Consumer: "brand-new"})
	wantExpired(t, err)
	if _, ok := r.pos(KindPR, "brand-new", ""); ok {
		t.Fatal("an expired unscoped poll created a key")
	}
	res, err := r.f.Poll(ctx, PollRequest{View: KindPR, Consumer: "u", Reset: true})
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if want := []string{refA, refB}; !slices.Equal(res.Replay, want) {
		t.Fatalf("replay = %v, want the watched references %v", res.Replay, want)
	}
	want := r.plain(refA)
	eqSeqs(t, r.poll(KindPR, "u", "").Rows, want)
}

func TestAnUnscopedKeyIsExpiredByPruneAndAnUnwatchedRowIsPrunedLikeAny(t *testing.T) {
	r := newRig(t, func(c *Config) { c.ConsumerStaleAfter = 60 * day })
	r.poll(KindPR, "stale", "")
	r.plain(refA)
	r.addUnwatched(refB, []string{classify.KindHeadChanged})
	r.poll(KindPR, "current", "")
	r.clk.Advance(15 * day)
	r.plain(refA) // newer than the retention
	rows, _, err := r.f.Prune(context.Background())
	if err != nil || rows != 2 {
		t.Fatalf("prune = %d, %v, want the two old rows", rows, err)
	}
	_, err = r.f.Poll(context.Background(), PollRequest{View: KindPR, Consumer: "stale"})
	wantExpired(t, err)
	if res := r.poll(KindPR, "current", ""); len(res.Rows) != 1 {
		t.Fatalf("current unscoped key = %+v, want the one retained row", res)
	}
	res, err := r.f.Poll(context.Background(), PollRequest{View: KindPR, Consumer: "stale", Reset: true})
	if err != nil || res.Position != r.head() {
		t.Fatalf("reset = %+v, %v", res, err)
	}
}
