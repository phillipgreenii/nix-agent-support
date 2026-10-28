package enginetest

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/engine"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/feed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/query"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/registry"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// The runner's marking of the members of watched queries, on the rig: the
// baseline run, the arrivals, the watched-flip burst (HT-11b), the ad-hoc watch
// option (INV-HOIST-15) and the property test of the watched delivery (HT-21).

const (
	idThree  = "id-3"
	refThree = "Example-Org/Thing#3"
)

func member(id, ref string) query.Member { return query.Member{ID: id, Ref: ref} }

// watchedQueryRig is a rig whose configured query mine is marked watched, with
// the emission mode and ad-hoc watching chosen by the options.
func watchedQueryRig(t *testing.T, opts ...Option) *Rig {
	t.Helper()
	r := NewRig(t, WithRegistration(opts...))
	r.Settings.Define(queryMine, "expression-one")
	r.Settings.SetWatched(queryMine, true)
	return r
}

func watchedVia(t *testing.T, r *Rig, id string) (string, bool) {
	t.Helper()
	ent, ok := entityOf(t, r, id)
	return ent.WatchedVia, ok && !ent.WatchedAt.IsZero()
}

func TestTheBaselineRunOfAWatchedQueryFlagsItsMembersAndAppendsNothing(t *testing.T) {
	r := watchedQueryRig(t, WithEmission(registry.EmitWatchedOnly))
	seedKnown(r, idOne, refOne) // a known entity, and one the store has never seen
	r.Lister.Script(queryMine, true, member(idOne, refOne), member(idTwo, refTwo))
	row := r.RunQuery(queryMine)

	if row.BaselineAt.IsZero() {
		t.Fatal("the run stored no baseline")
	}
	WantNoRows(t, r.Rows())
	for _, id := range []string{idOne, idTwo} {
		via, ok := watchedVia(t, r, id)
		if !ok || via != registry.ViaQuery+queryMine {
			t.Errorf("%s: watched = %v via %q, want the flag set through the query", id, ok, via)
		}
	}
}

func TestALaterRunOfAWatchedQueryAddsOneRowPerArrivalAndNoneForAWatchedMember(t *testing.T) {
	for _, storeRows := range []bool{true, false} {
		t.Run(fmt.Sprintf("store-rows-%v", storeRows), func(t *testing.T) {
			r := watchedQueryRig(t, WithEmission(registry.EmitWatchedOnly), WithStoreRows(storeRows))
			reg := r.Reg
			r.Lister.Script(queryMine, true, member(idOne, refOne))
			r.RunQuery(queryMine) // the baseline: idOne is watched

			seedKnown(r, idTwo, refTwo) // known, not watched
			r.Lister.Script(queryMine, true, member(idOne, refOne), member(idTwo, refTwo), member(idThree, refThree))
			r.RunQuery(queryMine)

			rows := r.Rows()
			want := []string{reg.Added}
			if storeRows {
				want = []string{reg.Added, reg.Entered}
			}
			WantRowKinds(t, rows, want, want)
			WantRefs(t, rows, refTwo, refThree)
			for _, row := range rows {
				if !row.Watched {
					t.Errorf("row %d lacks the watched snapshot", row.Seq)
				}
				if storeRows {
					WantFieldNames(t, row, "query:"+queryMine)
				}
			}
			for _, id := range []string{idTwo, idThree} {
				if via, ok := watchedVia(t, r, id); !ok || via != registry.ViaQuery+queryMine {
					t.Errorf("%s: watched = %v via %q", id, ok, via)
				}
			}

			// Nothing is new: a rerun appends nothing.
			r.RunQuery(queryMine)
			if got := len(r.Rows()); got != 2 {
				t.Errorf("a rerun of an unchanged query left %d rows, want 2", got)
			}
		})
	}
}

func TestTheWatchedFlipBurstEqualsTheKnownMembers(t *testing.T) {
	r := watchedQueryRig(t, WithEmission(registry.EmitWatchedOnly))
	reg := r.Reg
	members := []query.Member{member(idOne, refOne), member(idTwo, refTwo)}
	for _, m := range members {
		seedKnown(r, m.ID, m.Ref)
	}
	// An unknown member joins the query too: it gets the flag and no row, there
	// being nothing stored to report.
	members = append(members, member(idThree, refThree))

	// A query with a baseline and stored members that was not watched until now.
	refs := []string{refOne, refTwo, refThree}
	if err := r.Aliases.Put(context.Background(), idThree, refThree); err != nil {
		t.Fatal(err)
	}
	if err := r.Store.PutQuery(context.Background(), store.QueryRow{
		Key: queryMine, Configured: true, IDs: refs, Complete: true,
		BaselineAt: Start, FetchedAt: Start, FreshUntil: Start.Add(time.Hour), LastSuccessAt: Start,
	}); err != nil {
		t.Fatal(err)
	}
	r.Lister.Script(queryMine, true, members...)
	r.RunQuery(queryMine)

	rows := r.Rows()
	WantRowKinds(t, rows, []string{reg.Added}, []string{reg.Added})
	WantRefs(t, rows, refOne, refTwo)
	if _, ok := watchedVia(t, r, idThree); !ok {
		t.Error("the unknown member is not flagged")
	}

	r.RunQuery(queryMine)
	if got := len(r.Rows()); got != 2 {
		t.Errorf("the second run left %d rows, want the burst of 2 only", got)
	}
}

// INV-HOIST-15: the watch option of a fetch is refused unless the registration
// allows it.
func TestTheWatchOptionOfAFetchIsRefusedUnlessTheRegistrationAllowsIt(t *testing.T) {
	r := NewRig(t) // AdHocQueryWatch is false
	r.Settings.Define(queryMine, "expression-one")
	r.Seed(idOne, Content{Ref: refOne, Stamp: "s1", Title: "A title", At: Start})
	r.Lister.Script(queryMine, true, member(idOne, refOne))

	_, err := r.RunQueryWith(query.Key{Name: queryMine}, query.FetchOptions{Watch: true})
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("the refused fetch error = %v, want invalid_argument", err)
	}
	if calls := r.Lister.Calls(); len(calls) != 0 {
		t.Errorf("a refused fetch listed %d times, want none", len(calls))
	}
	if _, ok := watchedVia(t, r, idOne); ok {
		t.Error("a refused fetch flagged an entity")
	}
	// Without the option the same fetch runs.
	if _, err := r.RunQueryWith(query.Key{Name: queryMine}, query.FetchOptions{}); err != nil {
		t.Fatalf("a fetch without the option = %v", err)
	}

	allowed := NewRig(t, WithRegistration(WithAdHocQueryWatch(true)))
	allowed.Settings.Define(queryMine, "expression-one")
	allowed.Lister.Script(queryMine, true, member(idOne, refOne))
	if _, err := allowed.RunQueryWith(query.Key{Name: queryMine}, query.FetchOptions{Watch: true}); err != nil {
		t.Fatalf("a fetch with the option under a registration that allows it = %v", err)
	}
}

// An ad-hoc watched run marks its members late, whatever the query's own
// baseline: a known member gets its added row, an unknown one only the flag.
func TestAnAdHocWatchedRunMarksItsMembersLate(t *testing.T) {
	keys := map[string]query.Key{
		"an ad-hoc key":           {Name: queryMine, Args: map[string]string{"filter": "one"}},
		"the configured key":      {Name: queryMine},
		"the configured baseline": {Name: queryMine},
	}
	for name, key := range keys {
		t.Run(name, func(t *testing.T) {
			r := NewRig(t, WithRegistration(WithEmission(registry.EmitWatchedOnly), WithAdHocQueryWatch(true)))
			reg := r.Reg
			r.Settings.Define(queryMine, "expression-one") // not watched by its configuration
			seedKnown(r, idOne, refOne)
			r.Lister.Script(queryMine, true, member(idOne, refOne), member(idTwo, refTwo))

			if _, err := r.RunQueryWith(key, query.FetchOptions{Watch: true}); err != nil {
				t.Fatal(err)
			}
			rows := r.Rows()
			WantRowKinds(t, rows, []string{reg.Added})
			WantRefs(t, rows, refOne)
			for _, id := range []string{idOne, idTwo} {
				if via, ok := watchedVia(t, r, id); !ok || via != registry.ViaAdHoc+queryMine {
					t.Errorf("%s: watched = %v via %q, want the ad-hoc cause", id, ok, via)
				}
			}
		})
	}
}

// A fetch that carries the watch option is a task of its own: it never shares
// the flight of a plain run of the same key.
func TestAWatchFetchDoesNotJoinAPlainRunOfTheSameKey(t *testing.T) {
	r := NewRig(t, WithRegistration(WithEmission(registry.EmitWatchedOnly), WithAdHocQueryWatch(true)))
	r.Settings.Define(queryMine, "expression-one")
	r.Lister.Script(queryMine, true, member(idOne, refOne))
	key := query.Key{Name: queryMine}

	if _, err := r.RunQueryWith(key, query.FetchOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, ok := watchedVia(t, r, idOne); ok {
		t.Fatal("a plain run of an unwatched query flagged an entity")
	}
	if _, err := r.RunQueryWith(key, query.FetchOptions{Watch: true}); err != nil {
		t.Fatal(err)
	}
	if _, ok := watchedVia(t, r, idOne); !ok {
		t.Error("the watch fetch did not mark the member")
	}
	if got := len(r.Lister.Calls()); got != 2 {
		t.Errorf("origin lists = %d, want one per fetch", got)
	}
}

// HT-20 on the rig: under DeliverByWatched the closed row of a watched entity
// that left its query reaches an unscoped consumer and a scoped one that it names
// the scope for, a row raw-inserted with the snapshot unset reaches nobody, and a
// row appended before the entity was watched is never delivered.
func TestDeliverByWatchedOnTheRig(t *testing.T) {
	r := NewRig(t)
	reg := r.Reg
	r.Settings.Define(queryMine, "expression-one")
	seedKnown(r, idOne, refOne)
	seedKnown(r, idTwo, refTwo)
	r.Lister.Script(queryMine, true, member(idOne, refOne), member(idTwo, refTwo))
	r.RunQuery(queryMine)
	r.Poll(reg.ViewAll, consumer, queryMine) // the cursor starts at the tail

	// A raw row of an entity before it was watched, with the snapshot unset (the
	// engine appends none for an unwatched entity).
	if err := r.InTx(func(ctx context.Context, tx *store.Tx) error {
		_, err := tx.AppendChange(ctx, store.ChangeRow{Kind: reg.Entity, ID: refOne, Kinds: []string{reg.Retitled}, At: r.Clock.Now()})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	mustWatch(t, r, engine.WatchRequest{ID: idOne, Ref: refOne, Via: registry.ViaDirect, Mode: engine.WatchBaseline})
	retitle(t, r, idOne, "after the watch")
	// A raw row of a never watched entity.
	if err := r.InTx(func(ctx context.Context, tx *store.Tx) error {
		_, err := tx.AppendChange(ctx, store.ChangeRow{Kind: reg.Entity, ID: refTwo, Kinds: []string{reg.Retitled}, At: r.Clock.Now()})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// While the entity is a member the scoped consumer gets the row of the watch
	// and none of the other two.
	res := r.Poll(reg.ViewAll, consumer, queryMine)
	WantRowKinds(t, res.Rows, []string{reg.Retitled})
	WantRefs(t, res.Rows, refOne)
	r.Ack(reg.ViewAll, consumer, queryMine, res.NextSeq)

	// The watched entity leaves the query, then changes once more: the departure
	// names the scope, the later closed row names none and the entity is no longer
	// a member, so the scoped consumer does not get it.
	r.Confirmer.Script(idOne, query.Confirmation{State: "closed"})
	r.Lister.Script(queryMine, true, member(idTwo, refTwo))
	r.RunQuery(queryMine)
	retitle(t, r, idOne, "after it left")
	res = r.Poll(reg.ViewAll, consumer, queryMine)
	WantRowKinds(t, res.Rows, []string{reg.Left})
	WantRefs(t, res.Rows, refOne)

	// An unscoped consumer gets every row whose snapshot is set, the closed row of
	// the entity that left included, and no row whose snapshot is not.
	var unscoped []string
	for _, row := range r.Rows() {
		if ok, _ := feed.Deliver(reg.Spec, registry.View{Name: reg.ViewAll}, row, "", false); ok {
			unscoped = append(unscoped, fmt.Sprintf("%d:%v", row.Seq, row.Watched))
			if !row.Watched {
				t.Errorf("row %d reached an unscoped consumer with the snapshot unset", row.Seq)
			}
		}
	}
	if len(unscoped) != 3 {
		t.Errorf("an unscoped consumer got %v, want the three watched rows", unscoped)
	}
}

// HT-21: over generated sequences of watch, unwatched write, poll and ack, a row
// is delivered if and only if its snapshot is set, and an entity that was never
// watched appears in no answer.
func TestPropertyAWatchedRowIsDeliveredIffItsSnapshotIsSetAndAnUnwatchedEntityNeverAppears(t *testing.T) {
	type entity struct{ id, ref string }
	ents := []entity{{"id-p1", "Example-Org/Prop#1"}, {"id-p2", "Example-Org/Prop#2"}, {"id-p3", "Example-Org/Prop#3"}, {"id-p4", "Example-Org/Prop#4"}}
	for seed := uint64(1); seed <= 6; seed++ {
		t.Run(fmt.Sprintf("seed-%d", seed), func(t *testing.T) {
			rng := rand.New(rand.NewPCG(seed, seed*7919))
			r := NewRig(t, WithRegistration(WithEmission(registry.EmitWatchedOnly), WithDelivery(registry.DeliverByWatched)))
			reg := r.Reg
			// The scope's query is not watched and lists every entity, so each is
			// a member and membership never decides a delivery.
			r.Settings.Define(queryMine, "expression-one")
			var listed []query.Member
			refOf := map[string]string{}
			for _, e := range ents {
				seedKnown(r, e.id, e.ref)
				listed = append(listed, member(e.id, e.ref))
				refOf[e.ref] = e.id
			}
			r.Lister.Script(queryMine, true, listed...)
			r.RunQuery(queryMine)
			r.Poll(reg.ViewAll, consumer, queryMine) // the cursor starts at the tail

			watched := map[string]bool{}
			acked := int64(0)
			var lastNext int64
			titles := 0
			for step := 0; step < 80; step++ {
				e := ents[rng.IntN(len(ents))]
				switch rng.IntN(4) {
				case 0: // watch
					mustWatch(t, r, watchRequest(e.id, e.ref))
					watched[e.id] = true
				case 1: // a write, watched or not
					before := len(r.Rows())
					titles++
					retitle(t, r, e.id, fmt.Sprintf("title %d", titles))
					if got, want := len(r.Rows())-before, map[bool]int{true: 1, false: 0}[watched[e.id]]; got != want {
						t.Fatalf("step %d: a write of %s (watched %v) appended %d rows, want %d", step, e.id, watched[e.id], got, want)
					}
				case 2: // poll
					res := r.Poll(reg.ViewAll, consumer, queryMine)
					var want []store.ChangeRow
					for _, row := range r.Rows() {
						if row.Seq > acked && row.Watched {
							want = append(want, row)
						}
					}
					if !slices.Equal(Seqs(res.Rows), Seqs(want)) {
						t.Fatalf("step %d: delivered %v, want exactly the rows with the snapshot set %v", step, Seqs(res.Rows), Seqs(want))
					}
					for _, row := range res.Rows {
						if !row.Watched || !watched[refOf[row.ID]] {
							t.Fatalf("step %d: row %d of %s reached the consumer: snapshot %v, entity watched %v", step, row.Seq, row.ID, row.Watched, watched[refOf[row.ID]])
						}
					}
					lastNext = res.NextSeq
				case 3: // ack what the last poll returned
					if lastNext > acked {
						r.Ack(reg.ViewAll, consumer, queryMine, lastNext)
						acked = lastNext
					}
				}
				r.Clock.Advance(time.Second)
			}
			for _, row := range r.Rows() {
				if !watched[refOf[row.ID]] {
					t.Errorf("row %d of the never-watched %s is in the log", row.Seq, row.ID)
				}
			}
		})
	}
}

func watchRequest(id, ref string) engine.WatchRequest {
	return engine.WatchRequest{ID: id, Ref: ref, Via: registry.ViaDirect, Mode: engine.WatchLate}
}
