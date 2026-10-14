package changes

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/classify"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/summary"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

func (r *rig) membership(m Membership) error {
	return r.inTx(func(tx *store.Tx) error { return r.e.AppendMembership(context.Background(), tx, m) })
}

func TestAMembershipRowForAPRTheStoreHasNotSeenCreatesItsEntityAtVersionOne(t *testing.T) {
	r := newRig(t)
	r.clk.Advance(time.Minute)
	r.must(r.membership(Membership{Kind: classify.KindEnteredQuery, Query: "mine", Node: node, Ref: ref}))
	rows := r.changes()
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	row := rows[0]
	wantKinds(t, row.Kinds, classify.KindEnteredQuery)
	if row.Version != 1 || row.ID != ref || row.Kind != LogKindPR || row.Origin != "query" || row.HeadSHA != "" || !row.At.Equal(r.clk.Now()) {
		t.Errorf("row = %+v", row)
	}
	if f := fieldOf(t, row, "query:mine"); string(f.After) != `"mine"` || len(f.Before) != 0 {
		t.Errorf("query:mine = %s -> %s", f.Before, f.After)
	}
	if r.version() != 1 {
		t.Errorf("entity version = %d, want 1", r.version())
	}
	if _, ok := r.pending(); ok {
		t.Error("membership created a pending row")
	}
	if len(r.obs.detected) != 0 {
		t.Errorf("Detected fired with an unknown origin time: %+v", r.obs.detected)
	}
}

func TestAMembershipRowCarriesTheStoredHeadTheExtraFieldsAndNeverWaits(t *testing.T) {
	r := newRig(t)
	r.seed(sample())
	r.must(r.membership(Membership{
		Kind: classify.KindLeftQuery, Query: "mine", Node: node, Ref: ref,
		Extra: []classify.Field{{Name: "state", Before: json.RawMessage(`"OPEN"`), After: json.RawMessage(`"CLOSED"`)}},
	}))
	rows := r.changes()
	if len(rows) != 1 || rows[0].Version != 6 || rows[0].HeadSHA != "h1" {
		t.Fatalf("rows = %+v", rows)
	}
	wantKinds(t, rows[0].Kinds, classify.KindLeftQuery)
	if !slices.Equal(fieldNames(rows[0]), []string{"query:mine", "state"}) {
		t.Errorf("fields = %v", fieldNames(rows[0]))
	}
	if len(r.obs.detected) != 1 || r.obs.detected[0].Origin != "query" || r.obs.detected[0].Kind != classify.KindLeftQuery {
		t.Errorf("detected = %+v", r.obs.detected)
	}
}

func TestAMembershipRowMergesIntoAPendingRowAndLeavesItPending(t *testing.T) {
	r := newRig(t)
	r.seed(sample())
	r.seedGroup(groups.Runs, runsContent("success"), "h1", "b1")
	r.clk.Advance(time.Minute)
	r.must(r.changeSummary(func(s *summary.Summary) { s.ChecksState = "FAILURE" }, nil))
	before, _ := r.pending()

	r.clk.Advance(time.Second)
	r.must(r.membership(Membership{Kind: classify.KindEnteredQuery, Query: "mine", Node: node, Ref: ref}))
	if rows := r.changes(); len(rows) != 0 {
		t.Fatalf("a row was appended past the barrier: %+v", rows)
	}
	after, ok := r.pending()
	if !ok {
		t.Fatal("the pending row was ended by a membership change")
	}
	if !after.Deadline.Equal(before.Deadline) || string(after.Awaited) != string(before.Awaited) {
		t.Errorf("deadline or awaited moved: %+v -> %+v", before, after)
	}
	if !slices.Equal(after.Kinds, []string{classify.KindCIChanged, classify.KindEnteredQuery}) {
		t.Errorf("kinds = %v", after.Kinds)
	}

	r.must(r.refresh(groups.Runs, runsContent("failure"), "h1", "b1", r.clk.Now()))
	rows := r.changes()
	if len(rows) != 1 || r.version() != 6 {
		t.Fatalf("rows = %d, version = %d; want one row at version 6", len(rows), r.version())
	}
	if !slices.Equal(fieldNames(rows[0]), []string{"checks_state", "query:mine", "run:1"}) {
		t.Errorf("fields = %v", fieldNames(rows[0]))
	}
}

func TestAMembershipNeedsANodeARefAndAMembershipKind(t *testing.T) {
	r := newRig(t)
	for _, m := range []Membership{
		{Kind: classify.KindEnteredQuery, Query: "q", Ref: ref},
		{Kind: classify.KindEnteredQuery, Query: "q", Node: node},
		{Kind: classify.KindRemoved, Query: "q", Node: node, Ref: ref},
	} {
		if err := r.membership(m); err == nil {
			t.Errorf("%+v was accepted", m)
		}
	}
	wantNoChange(t, r)
}
