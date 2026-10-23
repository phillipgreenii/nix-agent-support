package enginetest

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/engine"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/query"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/registry"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// watchedOnlyRig is a rig whose engine appends rows for watched entities only.
func watchedOnlyRig(t *testing.T, opts ...Option) *Rig {
	t.Helper()
	return NewRig(t, WithRegistration(append([]Option{WithEmission(registry.EmitWatchedOnly)}, opts...)...))
}

// doWatch runs Engine.Watch in a transaction of its own.
func doWatch(t *testing.T, r *Rig, w engine.WatchRequest) (engine.WatchResult, error) {
	t.Helper()
	var res engine.WatchResult
	err := r.InTx(func(ctx context.Context, tx *store.Tx) error {
		var err error
		res, err = r.Engine.Watch(ctx, tx, w)
		return err
	})
	return res, err
}

func mustWatch(t *testing.T, r *Rig, w engine.WatchRequest) engine.WatchResult {
	t.Helper()
	res, err := doWatch(t, r, w)
	if err != nil {
		t.Fatalf("Watch(%+v) = %v", w, err)
	}
	return res
}

func entityOf(t *testing.T, r *Rig, id string) (store.EntityRow, bool) {
	t.Helper()
	row, ok, err := r.Store.GetEntity(context.Background(), r.Reg.Entity, id)
	if err != nil {
		t.Fatal(err)
	}
	return row, ok
}

func pendingIDs(t *testing.T, r *Rig) []string {
	t.Helper()
	rows, err := r.Store.ListPending(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, p := range rows {
		ids = append(ids, p.ID)
	}
	return ids
}

func membershipOf(t *testing.T, r *Rig, m engine.Membership) error {
	t.Helper()
	return r.InTx(func(ctx context.Context, tx *store.Tx) error { return r.Engine.AppendMembership(ctx, tx, m) })
}

func goneOf(t *testing.T, r *Rig, id string) error {
	t.Helper()
	return r.InTx(func(ctx context.Context, tx *store.Tx) error {
		return r.Engine.DetectGone(ctx, tx, id, governor.PurposeRefresh)
	})
}

// seedKnown seeds the entity with a baseline and moves the clock on, so a change
// that follows is a change of a known entity.
func seedKnown(r *Rig, id, ref string) {
	r.Seed(id, Content{Ref: ref, Stamp: "s1", Title: "A title", At: Start})
	r.Clock.Advance(time.Minute)
}

func retitle(t *testing.T, r *Rig, id, title string) {
	t.Helper()
	if err := r.ChangePrimary(id, governor.PurposeRefresh, func(c *Content) { c.Title = title }); err != nil {
		t.Fatal(err)
	}
}

func versionOf(t *testing.T, r *Rig, id string) int64 {
	t.Helper()
	row, ok := entityOf(t, r, id)
	if !ok {
		t.Fatalf("no entity row for %s", id)
	}
	return row.Version
}

// HT-10b: the Emission matrix.
func TestEmitWatchedOnlyAppendsNothingForAnUnwatchedEntity(t *testing.T) {
	r := watchedOnlyRig(t)
	reg := r.Reg
	seedKnown(r, idOne, refOne)
	r.SeedGroup(idOne, reg.Extra, "one")

	// A primary change that asks for a group: no row, no pending row, no bump.
	if err := r.ChangePrimary(idOne, governor.PurposeRefresh, func(c *Content) {
		c.Title, c.Await = "Another title", []string{reg.Extra}
	}); err != nil {
		t.Fatal(err)
	}
	// A group change, a tombstone and an arrival.
	if err := r.RefreshGroup(idOne, reg.Extra, "two", r.Clock.Now()); err != nil {
		t.Fatal(err)
	}
	if err := goneOf(t, r, idOne); err != nil {
		t.Fatal(err)
	}
	if err := membershipOf(t, r, engine.Membership{Kind: reg.Entered, Query: queryMine, ID: idOne, Ref: refOne}); err != nil {
		t.Fatal(err)
	}
	if err := membershipOf(t, r, engine.Membership{Kind: reg.Entered, Query: queryMine, ID: idTwo, Ref: refTwo}); err != nil {
		t.Fatal(err)
	}

	WantNoRows(t, r.Rows())
	if got := pendingIDs(t, r); len(got) != 0 {
		t.Errorf("pending rows = %v, want none", got)
	}
	if got := versionOf(t, r, idOne); got != 1 {
		t.Errorf("version = %d, want 1 (no bump)", got)
	}
	if _, ok := entityOf(t, r, idTwo); ok {
		t.Error("an arrival of an unwatched entity created its entity row")
	}
	if _, ok, err := r.Store.GetMeta(context.Background(), "changes.removed."+idOne); err != nil || ok {
		t.Errorf("the tombstone was recorded: ok=%v err=%v", ok, err)
	}
}

func TestEmitWatchedOnlyAppendsAndDeliversForAWatchedEntity(t *testing.T) {
	r := watchedOnlyRig(t)
	reg := r.Reg
	r.Settings.Define(queryMine, "expression-one")
	seedKnown(r, idOne, refOne)
	r.Lister.Script(queryMine, true, query.Member{ID: idOne, Ref: refOne})
	r.RunQuery(queryMine)
	WantNoRows(t, r.Rows())
	WantNoRows(t, r.Poll(reg.ViewAll, consumer, queryMine).Rows)

	res := mustWatch(t, r, engine.WatchRequest{ID: idOne, Ref: refOne, Via: registry.ViaQuery + queryMine, Mode: engine.WatchBaseline})
	if !res.Newly || res.Appended {
		t.Fatalf("baseline watch = %+v, want newly and nothing appended", res)
	}
	retitle(t, r, idOne, "Another title")

	rows := r.Rows()
	WantRowKinds(t, rows, []string{reg.Retitled})
	if !rows[0].Watched || rows[0].Version != 2 {
		t.Errorf("row = %+v, want watched at version 2", rows[0])
	}
	WantRowKinds(t, r.Poll(reg.ViewAll, consumer, queryMine).Rows, []string{reg.Retitled})
}

func TestAPendingRowOfAnEntityWatchedBeforeItSettlesAppends(t *testing.T) {
	r := watchedOnlyRig(t)
	reg := r.Reg
	seedKnown(r, idOne, refOne)
	r.SeedGroup(idOne, reg.Extra, "one")
	mustWatch(t, r, engine.WatchRequest{ID: idOne, Via: registry.ViaDirect, Mode: engine.WatchBaseline})

	if err := r.ChangePrimary(idOne, governor.PurposeRefresh, func(c *Content) {
		c.Title, c.Await = "Another title", []string{reg.Extra}
	}); err != nil {
		t.Fatal(err)
	}
	WantNoRows(t, r.Rows())
	if got := pendingIDs(t, r); !slices.Equal(got, []string{idOne}) {
		t.Fatalf("pending rows = %v, want the watched entity's", got)
	}
	if err := r.RefreshGroup(idOne, reg.Extra, "two", r.Clock.Now()); err != nil {
		t.Fatal(err)
	}
	rows := r.Rows()
	WantRowKinds(t, rows, []string{reg.Retitled, reg.Tweaked})
	if !rows[0].Watched {
		t.Error("the settled row lost its watched snapshot")
	}
	if got := pendingIDs(t, r); len(got) != 0 {
		t.Errorf("pending rows after settling = %v", got)
	}
}

func TestEmitAllAppendsForAnUnwatchedEntityAsBefore(t *testing.T) {
	r := NewRig(t)
	reg := r.Reg
	seedKnown(r, idOne, refOne)
	retitle(t, r, idOne, "Another title")
	rows := r.Rows()
	WantRowKinds(t, rows, []string{reg.Retitled})
	if rows[0].Watched {
		t.Error("a row of an unwatched entity carries the watched snapshot")
	}
}

// INV-HOIST-13 and HT-9: watch rules.
func TestWatchAppendsOneAddedRowForAKnownEntityOnlyUnderWatchedOnlyEmission(t *testing.T) {
	t.Run("EmitWatchedOnly, a known entity", func(t *testing.T) {
		r := watchedOnlyRig(t)
		seedKnown(r, idOne, refOne)
		res := mustWatch(t, r, engine.WatchRequest{ID: idOne, Ref: refOne, Via: registry.ViaDirect, Mode: engine.WatchLate})
		if !res.Newly || !res.Appended {
			t.Fatalf("Watch = %+v, want newly and appended", res)
		}
		rows := r.Rows()
		WantRowKinds(t, rows, []string{r.Reg.Added})
		WantRefs(t, rows, refOne)
		WantFieldNames(t, rows[0])
		if !rows[0].Watched || rows[0].Version != 2 {
			t.Errorf("row = %+v, want watched at version 2", rows[0])
		}
		ent, _ := entityOf(t, r, idOne)
		if ent.WatchedVia != registry.ViaDirect || ent.WatchedAt.IsZero() {
			t.Errorf("entity = %+v", ent)
		}

		// A watch of an already watched entity changes and appends nothing,
		// whatever its cause.
		before := versionOf(t, r, idOne)
		again := mustWatch(t, r, engine.WatchRequest{ID: idOne, Ref: refOne, Via: registry.ViaQuery + queryMine, Mode: engine.WatchLate})
		if again.Newly || again.Appended {
			t.Errorf("second Watch = %+v", again)
		}
		after, _ := entityOf(t, r, idOne)
		if after != ent || after.Version != before {
			t.Errorf("entity changed: %+v then %+v", ent, after)
		}
		WantSeqs(t, r.Rows(), rows[0].Seq)
	})
	t.Run("EmitAll appends none", func(t *testing.T) {
		r := NewRig(t)
		seedKnown(r, idOne, refOne)
		res := mustWatch(t, r, engine.WatchRequest{ID: idOne, Ref: refOne, Via: registry.ViaDirect, Mode: engine.WatchLate})
		if !res.Newly || res.Appended {
			t.Fatalf("Watch = %+v, want newly and nothing appended", res)
		}
		WantNoRows(t, r.Rows())
		if got := versionOf(t, r, idOne); got != 1 {
			t.Errorf("version = %d, want 1", got)
		}
		// The flag is stored and snapshotted: the next row carries it.
		retitle(t, r, idOne, "Another title")
		if rows := r.Rows(); len(rows) != 1 || !rows[0].Watched {
			t.Errorf("rows = %+v, want one watched row", rows)
		}
	})
	t.Run("a baseline watch appends none", func(t *testing.T) {
		r := watchedOnlyRig(t)
		seedKnown(r, idOne, refOne)
		res := mustWatch(t, r, engine.WatchRequest{ID: idOne, Ref: refOne, Via: registry.ViaQuery + queryMine, Mode: engine.WatchBaseline})
		if !res.Newly || res.Appended {
			t.Fatalf("Watch = %+v", res)
		}
		WantNoRows(t, r.Rows())
	})
	t.Run("a never-seen entity watched directly appends none", func(t *testing.T) {
		r := watchedOnlyRig(t)
		res := mustWatch(t, r, engine.WatchRequest{ID: idOne, Ref: refOne, Via: registry.ViaDirect, Mode: engine.WatchLate})
		if !res.Newly || res.Appended {
			t.Fatalf("Watch = %+v", res)
		}
		WantNoRows(t, r.Rows())
		if ent, ok := entityOf(t, r, idOne); !ok || ent.WatchedAt.IsZero() {
			t.Errorf("entity = %+v, %v; want a watched row created by the watch", ent, ok)
		}
	})
}

func TestWatchRefusesARequestItCannotAnswer(t *testing.T) {
	r := watchedOnlyRig(t)
	cases := map[string]engine.WatchRequest{
		"no mode":   {ID: idOne, Ref: refOne, Via: registry.ViaDirect},
		"bad mode":  {ID: idOne, Ref: refOne, Via: registry.ViaDirect, Mode: engine.WatchLate + 1},
		"no id":     {Ref: refOne, Via: registry.ViaDirect, Mode: engine.WatchLate},
		"no cause":  {ID: idOne, Ref: refOne, Mode: engine.WatchLate},
		"neither a": {},
	}
	for name, w := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := doWatch(t, r, w); err == nil {
				t.Error("Watch accepted the request")
			}
		})
	}
	if _, ok := entityOf(t, r, idOne); ok {
		t.Error("a refused watch created the entity")
	}
	if _, err := r.Engine.Watch(context.Background(), nil, engine.WatchRequest{ID: idOne, Via: registry.ViaDirect, Mode: engine.WatchLate}); err == nil {
		t.Error("Watch accepted a nil transaction")
	}
	if _, err := r.Engine.WatchOne(context.Background(), nil, engine.WatchRequest{ID: idOne, Via: registry.ViaDirect, Mode: engine.WatchLate}); err == nil {
		t.Error("WatchOne accepted a nil store")
	}
}

// HT-33: WatchOne before a read.
func TestWatchOneBeforeAReadMakesTheChangeTheReadFindsARowOfAWatchedEntity(t *testing.T) {
	ctx := context.Background()
	w := engine.WatchRequest{ID: idOne, Ref: refOne, Via: registry.ViaDirect, Mode: engine.WatchLate}

	t.Run("watch then read finds the change", func(t *testing.T) {
		r := watchedOnlyRig(t)
		seedKnown(r, idOne, refOne)
		res, err := r.Engine.WatchOne(ctx, r.Store, w)
		if err != nil || !res.Newly || !res.Appended {
			t.Fatalf("WatchOne = %+v, %v", res, err)
		}
		retitle(t, r, idOne, "Another title") // the read's refresh finds a change
		rows := r.Rows()
		WantRowKinds(t, rows, []string{r.Reg.Added}, []string{r.Reg.Retitled})
		if !rows[1].Watched {
			t.Error("the change is not a row of the watched entity")
		}
	})
	t.Run("read then watch loses it", func(t *testing.T) {
		r := watchedOnlyRig(t)
		seedKnown(r, idOne, refOne)
		retitle(t, r, idOne, "Another title") // the change lands in an unwatched entity's skipped row
		res, err := r.Engine.WatchOne(ctx, r.Store, w)
		if err != nil || !res.Newly || !res.Appended {
			t.Fatalf("WatchOne = %+v, %v", res, err)
		}
		WantRowKinds(t, r.Rows(), []string{r.Reg.Added}) // the retitle is nowhere
	})
	t.Run("an already watched entity changes nothing", func(t *testing.T) {
		r := watchedOnlyRig(t)
		seedKnown(r, idOne, refOne)
		if _, err := r.Engine.WatchOne(ctx, r.Store, w); err != nil {
			t.Fatal(err)
		}
		ent, _ := entityOf(t, r, idOne)
		rows := r.Rows()
		res, err := r.Engine.WatchOne(ctx, r.Store, engine.WatchRequest{ID: idOne, Ref: refOne, Via: registry.ViaAdHoc + queryMine, Mode: engine.WatchLate})
		if err != nil || res.Newly || res.Appended {
			t.Fatalf("WatchOne = %+v, %v", res, err)
		}
		if after, _ := entityOf(t, r, idOne); after != ent {
			t.Errorf("entity changed: %+v then %+v", ent, after)
		}
		WantSeqs(t, r.Rows(), rows[0].Seq)
	})
	t.Run("a failed store call is an error", func(t *testing.T) {
		r := watchedOnlyRig(t)
		seedKnown(r, idOne, refOne)
		if err := r.Store.Close(); err != nil {
			t.Fatal(err)
		}
		if res, err := r.Engine.WatchOne(ctx, r.Store, w); err == nil || res != (engine.WatchResult{}) {
			t.Errorf("WatchOne on a closed store = %+v, %v", res, err)
		}
	})
}

// The arrival of an entity into a watched query is one row.
func TestAnArrivalIsOneRowTheUnionOfItsParts(t *testing.T) {
	type arrival struct {
		emission  registry.Emission
		storeRows bool
		watch     engine.WatchMode
		// preWatched: the entity is already watched, by another cause.
		preWatched bool
		// known: the entity has stored primary content.
		known bool
	}
	cases := []struct {
		name string
		in   arrival
		// kinds is the row's kind list in order (names of the registration),
		// nil for no row; flagged is whether the flag is set afterwards.
		kinds   func(Registration) []string
		flagged bool
	}{
		{
			"watch-only, rows stored, late: the entered and added kinds in canonical order",
			arrival{emission: registry.EmitWatchedOnly, storeRows: true, watch: engine.WatchLate},
			func(g Registration) []string { return engine.OrderKinds(g.Spec.Changes, []string{g.Entered, g.Added}) }, true,
		},
		{
			"watch-only, rows stored, late, a known entity: still one row",
			arrival{emission: registry.EmitWatchedOnly, storeRows: true, watch: engine.WatchLate, known: true},
			func(g Registration) []string { return engine.OrderKinds(g.Spec.Changes, []string{g.Entered, g.Added}) }, true,
		},
		{
			"watch-only, no rows stored, late: the added kind alone",
			arrival{emission: registry.EmitWatchedOnly, watch: engine.WatchLate},
			func(g Registration) []string { return []string{g.Added} }, true,
		},
		{
			"watch-only, rows stored, late, already watched: the entered kind alone",
			arrival{emission: registry.EmitWatchedOnly, storeRows: true, watch: engine.WatchLate, preWatched: true},
			func(g Registration) []string { return []string{g.Entered} }, true,
		},
		{
			"watch-only, no rows stored, late, already watched: nothing",
			arrival{emission: registry.EmitWatchedOnly, watch: engine.WatchLate, preWatched: true},
			nil, true,
		},
		{
			"emit-all, rows stored, late: the entered kind alone, the flag set",
			arrival{emission: registry.EmitAll, storeRows: true, watch: engine.WatchLate},
			func(g Registration) []string { return []string{g.Entered} }, true,
		},
		{
			"emit-all, no rows stored, late: nothing, the flag set",
			arrival{emission: registry.EmitAll, watch: engine.WatchLate},
			nil, true,
		},
		{
			"watch-only, rows stored, baseline: the flag and no row",
			arrival{emission: registry.EmitWatchedOnly, storeRows: true, watch: engine.WatchBaseline},
			nil, true,
		},
		{
			"watch-only, no rows stored, baseline: the flag and no row",
			arrival{emission: registry.EmitWatchedOnly, watch: engine.WatchBaseline},
			nil, true,
		},
		{
			"emit-all, rows stored, baseline: the flag and no row",
			arrival{emission: registry.EmitAll, storeRows: true, watch: engine.WatchBaseline},
			nil, true,
		},
		{
			"emit-all, no rows stored, baseline: the flag and no row",
			arrival{emission: registry.EmitAll, watch: engine.WatchBaseline},
			nil, true,
		},
		{
			"watch-only, rows stored, no watch, unwatched: nothing",
			arrival{emission: registry.EmitWatchedOnly, storeRows: true},
			nil, false,
		},
		{
			"watch-only, rows stored, no watch, already watched: the entered kind",
			arrival{emission: registry.EmitWatchedOnly, storeRows: true, preWatched: true},
			func(g Registration) []string { return []string{g.Entered} }, true,
		},
		{
			"emit-all, rows stored, no watch: the entered kind as before",
			arrival{emission: registry.EmitAll, storeRows: true},
			func(g Registration) []string { return []string{g.Entered} }, false,
		},
		{
			"emit-all, no rows stored, no watch: nothing",
			arrival{emission: registry.EmitAll},
			nil, false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := NewRig(t, WithRegistration(WithEmission(tc.in.emission), WithStoreRows(tc.in.storeRows)))
			reg := r.Reg
			if tc.in.known {
				seedKnown(r, idOne, refOne)
			}
			if tc.in.preWatched {
				mustWatch(t, r, engine.WatchRequest{ID: idOne, Ref: refOne, Via: registry.ViaDirect, Mode: engine.WatchBaseline})
			}
			m := engine.Membership{Kind: reg.Entered, Query: queryMine, ID: idOne, Ref: refOne}
			if tc.in.watch != 0 {
				m.Watch, m.Via = tc.in.watch, registry.ViaQuery+queryMine
			}
			if err := membershipOf(t, r, m); err != nil {
				t.Fatal(err)
			}
			rows := r.Rows()
			if tc.kinds == nil {
				WantNoRows(t, rows)
			} else {
				WantRowKinds(t, rows, tc.kinds(reg))
				if rows[0].Watched != tc.flagged {
					t.Errorf("row watched = %v", rows[0].Watched)
				}
				WantRefs(t, rows, refOne)
				// The query field is written only with the membership kind.
				wantFields := []string{engine.QueryFieldPrefix + queryMine}
				if !tc.in.storeRows {
					wantFields = nil
				}
				if got := FieldNames(t, rows[0]); !slices.Equal(got, wantFields) {
					t.Errorf("fields = %v, want %v", got, wantFields)
				}
			}
			ent, ok := entityOf(t, r, idOne)
			if flagged := ok && !ent.WatchedAt.IsZero(); flagged != tc.flagged {
				t.Errorf("flag set = %v, want %v", flagged, tc.flagged)
			}
		})
	}
}

func TestADepartureAppendsOnlyWhenRowsAreStoredAndTheEntityIsWatchedUnderWatchedOnly(t *testing.T) {
	for _, storeRows := range []bool{true, false} {
		r := watchedOnlyRig(t, WithStoreRows(storeRows))
		reg := r.Reg
		seedKnown(r, idOne, refOne)
		leave := engine.Membership{Kind: reg.Left, Query: queryMine, ID: idOne, Ref: refOne}
		if err := membershipOf(t, r, leave); err != nil {
			t.Fatal(err)
		}
		WantNoRows(t, r.Rows()) // unwatched
		mustWatch(t, r, engine.WatchRequest{ID: idOne, Via: registry.ViaDirect, Mode: engine.WatchBaseline})
		if err := membershipOf(t, r, leave); err != nil {
			t.Fatal(err)
		}
		if storeRows {
			WantRowKinds(t, r.Rows(), []string{reg.Left})
		} else {
			WantNoRows(t, r.Rows())
		}
	}
}

func TestAnArrivalMergesIntoThePendingRowOfAWatchedEntityAndLeavesItPending(t *testing.T) {
	r := watchedOnlyRig(t)
	reg := r.Reg
	seedKnown(r, idOne, refOne)
	r.SeedGroup(idOne, reg.Extra, "one")
	mustWatch(t, r, engine.WatchRequest{ID: idOne, Via: registry.ViaDirect, Mode: engine.WatchBaseline})
	if err := r.ChangePrimary(idOne, governor.PurposeRefresh, func(c *Content) {
		c.Title, c.Await = "Another title", []string{reg.Extra}
	}); err != nil {
		t.Fatal(err)
	}
	if err := membershipOf(t, r, engine.Membership{Kind: reg.Entered, Query: queryMine, ID: idOne, Ref: refOne}); err != nil {
		t.Fatal(err)
	}
	WantNoRows(t, r.Rows())
	if got := pendingIDs(t, r); !slices.Equal(got, []string{idOne}) {
		t.Fatalf("pending rows = %v", got)
	}
	if err := r.RefreshGroup(idOne, reg.Extra, "two", r.Clock.Now()); err != nil {
		t.Fatal(err)
	}
	WantRowKinds(t, r.Rows(), engine.OrderKinds(reg.Spec.Changes, []string{reg.Entered, reg.Retitled, reg.Tweaked}))
}

func TestAMembershipThatWatchesNeedsAValidModeAndACause(t *testing.T) {
	r := watchedOnlyRig(t)
	base := engine.Membership{Kind: r.Reg.Entered, Query: queryMine, ID: idOne, Ref: refOne}
	noVia := base
	noVia.Watch = engine.WatchLate
	badMode := base
	badMode.Watch, badMode.Via = engine.WatchLate+1, registry.ViaDirect
	for name, m := range map[string]engine.Membership{"no cause": noVia, "unknown mode": badMode} {
		if err := membershipOf(t, r, m); err == nil {
			t.Errorf("%s: AppendMembership accepted it", name)
		}
	}
	if _, ok := entityOf(t, r, idOne); ok {
		t.Error("a refused membership created the entity")
	}
}

// HT-35 on the rig: the membership kinds are required in Spec.Changes only when
// membership rows are stored, and the engine accepts both shapes.
func TestMembershipKindsAreRequiredInTheCatalogOnlyWhenRowsAreStored(t *testing.T) {
	for _, storeRows := range []bool{true, false} {
		reg := NewRegistration(WithStoreRows(storeRows))
		if got := slices.Contains(reg.Spec.Changes, reg.Entered) && slices.Contains(reg.Spec.Changes, reg.Left); got != storeRows {
			t.Errorf("StoreRows %v: catalog lists the membership kinds = %v", storeRows, got)
		}
		if err := reg.Spec.Validate(); err != nil {
			t.Errorf("StoreRows %v: Validate = %v", storeRows, err)
		}
		// Only a catalog that lists the kinds is valid once rows are stored.
		bad := NewRegistration(WithStoreRows(false))
		bad.Spec.Membership.StoreRows = true
		if bad.Spec.Validate() == nil {
			t.Error("a spec that stores rows without the membership kinds is valid")
		}
		NewRig(t, WithRegistration(WithStoreRows(storeRows)))
	}
}

// The watched snapshot of every row the engine appends: Complete, DetectGone,
// AppendMembership and Watch, under both emission modes.
func TestEveryRowTheEngineAppendsCarriesTheWatchedSnapshot(t *testing.T) {
	for _, emission := range []registry.Emission{registry.EmitAll, registry.EmitWatchedOnly} {
		t.Run(map[registry.Emission]string{registry.EmitAll: "emit-all", registry.EmitWatchedOnly: "watch-only"}[emission], func(t *testing.T) {
			r := NewRig(t, WithRegistration(WithEmission(emission)))
			reg := r.Reg
			watchedFlag := func(id string) bool {
				ent, _ := entityOf(t, r, id)
				return !ent.WatchedAt.IsZero()
			}

			// Complete: a settled pending row.
			settle := func(id, ref string) {
				seedKnown(r, id, ref)
				r.SeedGroup(id, reg.Extra, "one")
				if err := r.ChangePrimary(id, governor.PurposeRefresh, func(c *Content) {
					c.Title, c.Await = "Another title", []string{reg.Extra}
				}); err != nil {
					t.Fatal(err)
				}
				if err := r.RefreshGroup(id, reg.Extra, "two", r.Clock.Now()); err != nil {
					t.Fatal(err)
				}
			}
			// DetectGone: a tombstone.
			// AppendMembership: an arrival.
			var unwatchedRows []store.ChangeRow
			if emission == registry.EmitAll {
				settle(idOne, refOne)
				if err := goneOf(t, r, idOne); err != nil {
					t.Fatal(err)
				}
				if err := membershipOf(t, r, engine.Membership{Kind: reg.Entered, Query: queryMine, ID: idOne, Ref: refOne}); err != nil {
					t.Fatal(err)
				}
				unwatchedRows = r.Rows()
				WantRowKinds(t, unwatchedRows,
					[]string{reg.Retitled, reg.Tweaked}, []string{reg.Removed}, []string{reg.Entered})
				for _, row := range unwatchedRows {
					if row.Watched {
						t.Errorf("row %d of an unwatched entity has the snapshot set", row.Seq)
					}
				}
				if watchedFlag(idOne) {
					t.Fatal("the entity is watched")
				}
			}

			// The same three, for an entity whose flag is set before.
			start := len(unwatchedRows)
			mustWatch(t, r, engine.WatchRequest{ID: idTwo, Ref: refTwo, Via: registry.ViaDirect, Mode: engine.WatchBaseline})
			settle(idTwo, refTwo)
			if err := goneOf(t, r, idTwo); err != nil {
				t.Fatal(err)
			}
			if err := membershipOf(t, r, engine.Membership{Kind: reg.Entered, Query: queryMine, ID: idTwo, Ref: refTwo}); err != nil {
				t.Fatal(err)
			}
			rows := r.Rows()[start:]
			WantRowKinds(t, rows, []string{reg.Retitled, reg.Tweaked}, []string{reg.Removed}, []string{reg.Entered})
			for _, row := range rows {
				if !row.Watched {
					t.Errorf("row %d of a watched entity lacks the snapshot", row.Seq)
				}
			}

			// A flag set by the very write that appends the row is in its snapshot.
			if err := membershipOf(t, r, engine.Membership{
				Kind: reg.Entered, Query: queryMine, ID: "id-3", Ref: "Example-Org/Thing#3",
				Watch: engine.WatchLate, Via: registry.ViaQuery + queryMine,
			}); err != nil {
				t.Fatal(err)
			}
			last := r.Rows()
			if row := last[len(last)-1]; row.ID != "Example-Org/Thing#3" || !row.Watched {
				t.Errorf("last row = %+v, want the arrival's row with the flag set in the same write", row)
			}
		})
	}
}

// HT-9 on the rig: a settle row appends once and bumps the version once, a
// tombstone appends one removed row once, and baselines append nothing.
func TestTheRigSettlesOnceAndRecordsATombstoneOnce(t *testing.T) {
	r := NewRig(t)
	reg := r.Reg
	seedKnown(r, idOne, refOne)
	r.SeedGroup(idOne, reg.Extra, "one")

	// The baseline of a group appends nothing.
	if err := r.RefreshGroup(idOne, "unlisted-group", "x", r.Clock.Now()); err != nil {
		t.Fatal(err)
	}
	WantNoRows(t, r.Rows())

	if err := r.ChangePrimary(idOne, governor.PurposeRefresh, func(c *Content) {
		c.Title, c.Await = "Another title", []string{reg.Extra}
	}); err != nil {
		t.Fatal(err)
	}
	WantNoRows(t, r.Rows())
	if err := r.RefreshGroup(idOne, reg.Extra, "two", r.Clock.Now()); err != nil {
		t.Fatal(err)
	}
	WantRowKinds(t, r.Rows(), []string{reg.Retitled, reg.Tweaked})
	if got := versionOf(t, r, idOne); got != 2 {
		t.Errorf("version = %d, want 2 (bumped once)", got)
	}

	for range 2 {
		if err := goneOf(t, r, idOne); err != nil {
			t.Fatal(err)
		}
	}
	WantRowKinds(t, r.Rows(), []string{reg.Retitled, reg.Tweaked}, []string{reg.Removed})
}
