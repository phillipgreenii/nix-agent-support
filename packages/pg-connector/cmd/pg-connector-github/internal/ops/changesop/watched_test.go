package changesop

import (
	"context"
	"strconv"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/classify"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// The change feed delivers by the watched flag and works without a query: the
// query is optional on the wire and, when given, still filters.

// refOther is a PR no configured query lists.
const refOther = "acme-org/gadget#9"

// addUnwatched appends a row whose watched snapshot is unset (a PR that was only
// read).
func (r *rig) addUnwatched(ref string, version int64, kinds ...string) int64 {
	r.t.Helper()
	var seq int64
	err := r.st.InTx(context.Background(), func(tx *store.Tx) error {
		var aerr error
		seq, aerr = tx.AppendChange(context.Background(), store.ChangeRow{
			Kind: "pr", ID: ref, Version: version, Kinds: kinds, Origin: "refresh", At: r.clk.Now(),
		})
		return aerr
	})
	if err != nil {
		r.t.Fatal(err)
	}
	return seq
}

func TestThePRFeedWorksWithoutAQueryAndSkipsAnUnwatchedRow(t *testing.T) {
	r := newRig(t)
	if res, _ := r.changes(`{"consumer":"u"}`); len(res.Changes) != 0 {
		t.Fatalf("first poll = %+v, want none: the key starts at the tail", res.Changes)
	}
	if _, ok := r.pos("u", ""); !ok {
		t.Fatal("the unscoped poll created no key")
	}
	r.addUnwatched(refA, 2, classify.KindHeadChanged)
	watched := r.add(refB, 3, "head-3", []string{classify.KindHeadChanged})

	res, _ := r.changes(`{"consumer":"u"}`)
	if len(res.Changes) != 1 || res.Changes[0].Seq != watched || res.Changes[0].Change != "changed" {
		t.Fatalf("changes = %+v, want the one watched row %d as changed", res.Changes, watched)
	}
	got := r.call(OpChangesAck, `{"consumer":"u","next_seq":`+strconv.FormatInt(res.Sources[0].NextSeq, 10)+`}`)
	if got.Error != nil {
		t.Fatalf("ack without a query: %s: %s", got.Error.Code, got.Error.Message)
	}
	if p, ok := r.pos("u", ""); !ok || p != res.Sources[0].NextSeq {
		t.Errorf("position = %d, %v, want %d", p, ok, res.Sources[0].NextSeq)
	}
}

func TestAQueryStillFiltersTheWatchedRowsOnTheWire(t *testing.T) {
	r := newRig(t)
	r.changes(pollArgs("c", "mine")) // mine = {refA}
	member := r.add(refA, 2, "head-2", []string{classify.KindHeadChanged})
	r.add(refOther, 2, "head-o", []string{classify.KindHeadChanged}) // watched, not in mine

	res, _ := r.changes(pollArgs("c", "mine"))
	if len(res.Changes) != 1 || res.Changes[0].Seq != member {
		t.Fatalf("changes = %+v, want only the member row %d", res.Changes, member)
	}
}

func TestACIConsumerWithoutAQueryGetsTheWatchedCIRowsOnly(t *testing.T) {
	r := newRig(t)
	r.changesCI(`{"consumer":"ci"}`)
	r.addUnwatched(refA, 2, classify.KindCIChanged)
	watched := r.add(refB, 2, "head-2", []string{classify.KindCIChanged})
	r.add(refOther, 2, "head-o", []string{classify.KindHeadChanged}) // watched but no CI change

	res, _ := r.changesCI(`{"consumer":"ci"}`)
	if len(res.Changes) != 1 || res.Changes[0].Seq != watched {
		t.Fatalf("ci changes = %+v, want only the watched ci_changed row %d", res.Changes, watched)
	}
}
