package engine

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/registry"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

func TestNewFailsForAPrimaryGroupOtherThanTheStoreOne(t *testing.T) {
	s := testSpec()
	s.PrimaryGroup = "main"
	s.Groups = []string{"main", groupAlpha}
	_, err := New(Config{Spec: s, Classifier: fake{}, SettleTimeout: time.Second, BodyCap: 1})
	if err == nil || !strings.Contains(err.Error(), store.SummaryGroupName) {
		t.Fatalf("err = %v, want one naming the store's primary group", err)
	}
}

func TestNewRejectsAnInvalidSpecANilClassifierAndNonPositiveSettleOrBodyCap(t *testing.T) {
	good := Config{Spec: testSpec(), Classifier: fake{}, SettleTimeout: time.Second, BodyCap: 1}
	if _, err := New(good); err != nil {
		t.Fatalf("a good config was rejected: %v", err)
	}
	cases := map[string]func(*Config){
		"an empty entity kind":      func(c *Config) { c.Spec.EntityKind = "" },
		"a role outside the kinds":  func(c *Config) { c.Spec.Roles.Removed = "missing" },
		"no classifier":             func(c *Config) { c.Classifier = nil },
		"a zero settle timeout":     func(c *Config) { c.SettleTimeout = 0 },
		"a negative settle timeout": func(c *Config) { c.SettleTimeout = -time.Second },
		"a zero body cap":           func(c *Config) { c.BodyCap = 0 },
		"a negative body cap":       func(c *Config) { c.BodyCap = -1 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			c := good
			mutate(&c)
			if _, err := New(c); err == nil {
				t.Error("was accepted")
			}
		})
	}
}

func TestANilClockIsTheSystemClock(t *testing.T) {
	e, err := New(Config{Spec: testSpec(), Classifier: fake{}, SettleTimeout: time.Second, BodyCap: 1})
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Since(e.now()); d < 0 || d > time.Minute {
		t.Errorf("now is %s away from the system clock", d)
	}
}

func TestHintsAreUsedOnceAndExpire(t *testing.T) {
	r := newRig(t)
	r.e.NoteOrigin(idOne, store.SummaryGroupName, OriginWrite)
	if got := r.e.originFor(idOne, store.SummaryGroupName, governor.PurposeInteractive); got != OriginWrite {
		t.Errorf("origin with a hint = %q, want write", got)
	}
	if got := r.e.originFor(idOne, store.SummaryGroupName, governor.PurposeInteractive); got != OriginRead {
		t.Errorf("the hint was not cleared: origin = %q, want read", got)
	}
	if got := r.e.originFor(idOne, store.SummaryGroupName, governor.PurposeFill); got != OriginRefresh {
		t.Errorf("origin of a background purpose = %q, want refresh", got)
	}
	r.e.NoteOrigin(idOne, groupAlpha, OriginWrite)
	if got := r.e.originFor(idOne, store.SummaryGroupName, governor.PurposeRefresh); got != OriginRefresh {
		t.Errorf("a hint for another group leaked: %q", got)
	}
	r.clk.Advance(11 * time.Minute)
	if got := r.e.originFor(idOne, groupAlpha, governor.PurposeRefresh); got != OriginRefresh {
		t.Errorf("a hint older than ten minutes was used: %q", got)
	}
}

func TestBaselinesAddNothingAndCreateNoPendingRow(t *testing.T) {
	r := newRig(t)
	r.seed(idOne, sample())
	ctx := context.Background()
	next := sample()
	next.Stamp = "s2"
	r.must(r.inTx(func(tx *store.Tx) error {
		// A first sighting of the entity: no previous content.
		if err := r.e.DetectPrimary(ctx, tx, PrimaryChange{ID: idOne, Next: widgetJSON(t, next)}); err != nil {
			return err
		}
		// A gone content is no baseline to compare with either.
		gone := next
		gone.Gone = true
		if err := r.e.DetectPrimary(ctx, tx, PrimaryChange{ID: idOne, Prev: widgetJSON(t, sample()), Next: widgetJSON(t, gone)}); err != nil {
			return err
		}
		// A content with no entity key is skipped.
		return r.e.DetectPrimary(ctx, tx, PrimaryChange{Prev: widgetJSON(t, sample()), Next: widgetJSON(t, next)})
	}))
	// The first fetch of a group is its baseline.
	r.must(r.refresh(idOne, groupAlpha, "one", "", "", r.clk.Now()))
	wantNoChange(t, r)
	if v := r.version(idOne); v != 5 {
		t.Errorf("version = %d, want 5 (no bump)", v)
	}
}

func TestAChangeThatQueuesNothingAppendsAtOnceOnAnUnwatchedEntity(t *testing.T) {
	r := newRig(t)
	r.seed(idOne, sample())
	r.clk.Advance(time.Minute)
	r.must(r.changePrimary(idOne, func(w *widget) { w.Title = "Another title" }))
	rows := r.changes()
	if len(rows) != 1 || r.version(idOne) != 6 {
		t.Fatalf("rows = %d, version = %d; want 1 and 6", len(rows), r.version(idOne))
	}
	row := rows[0]
	wantKinds(t, row.Kinds, changeRetitled)
	if row.Kind != kindWidget || row.ID != refOne || row.Stamp() != "s1" || row.Origin != OriginRefresh || !row.At.Equal(r.clk.Now()) {
		t.Errorf("row = %+v", row)
	}
	if !slices.Equal(rowFields(row), []string{"title"}) {
		t.Errorf("fields = %v", rowFields(row))
	}
	if _, ok := r.pending(); ok {
		t.Error("a pending row was created")
	}
	want := []detectedCall{{changeRetitled, OriginRefresh, row.At.Sub(row.OriginUpdatedAt)}}
	if !slices.Equal(r.obs.detected, want) {
		t.Errorf("detected = %+v, want %+v", r.obs.detected, want)
	}
}

func TestADependentChangeWaitsForItsGroupsThenOneRowCarriesBoth(t *testing.T) {
	r := newRig(t)
	r.seed(idOne, sample())
	r.seedGroup(idOne, groupAlpha, "a1", "s1", "t1")
	r.clk.Advance(time.Minute)
	detected := r.clk.Now()

	r.must(r.changePrimary(idOne, func(w *widget) { w.Count = 1; w.Open = false }))
	if rows := r.changes(); len(rows) != 0 {
		t.Fatalf("a row was appended before gamma was refreshed: %+v", rows)
	}
	p, ok := r.pending()
	if !ok {
		t.Fatal("no pending row")
	}
	pa, pb := p.Stamps()
	if p.Kind != kindWidget || p.ID != idOne || p.Origin != OriginRefresh || !p.DetectedAt.Equal(detected) ||
		!p.Deadline.Equal(detected.Add(30*time.Second)) || pa != "s1" || pb != "t1" {
		t.Errorf("pending = %+v", p)
	}
	if got := r.awaitedGroups(); !slices.Equal(got, []string{groupGamma}) {
		t.Errorf("awaited = %v, want [gamma]", got)
	}
	if v := r.version(idOne); v != 5 {
		t.Errorf("version moved to %d before the row", v)
	}

	r.clk.Advance(time.Second)
	r.must(r.refresh(idOne, groupGamma, "g1", "", "", r.clk.Now())) // gamma's baseline satisfies the barrier
	rows := r.changes()
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1: %+v", len(rows), rows)
	}
	wantKinds(t, rows[0].Kinds, changeToggled)
	if r.version(idOne) != 6 {
		t.Errorf("version = %d, want 6 (moved once)", r.version(idOne))
	}
	if _, ok := r.pending(); ok {
		t.Error("the pending row survived the append")
	}
	if !slices.Equal(r.obs.settled, []string{ResultSettled}) {
		t.Errorf("settled = %v, want [settled]", r.obs.settled)
	}
}

func TestAStampedGroupCountsOnlyForTheStampsOfThePendingRow(t *testing.T) {
	r := newRig(t)
	r.seed(idOne, sample())
	r.seedGroup(idOne, groupAlpha, "a1", "s1", "t1")
	r.seedGroup(idOne, groupBeta, "b1", "s1", "t1")
	r.clk.Advance(time.Minute)
	r.must(r.changePrimary(idOne, func(w *widget) { w.Stamp = "s2" }))
	if got := r.awaitedGroups(); !slices.Equal(got, []string{groupAlpha, groupBeta}) {
		t.Fatalf("awaited = %v, want alpha beta", got)
	}
	r.clk.Advance(time.Second)
	r.must(r.refresh(idOne, groupBeta, "b2", "s1", "t1", r.clk.Now())) // fetched for the old stamps
	if got := r.awaitedGroups(); !slices.Equal(got, []string{groupAlpha, groupBeta}) {
		t.Errorf("a fetch for the old stamps counted: awaited = %v", got)
	}
	r.must(r.refresh(idOne, groupBeta, "b3", "s2", "t1", r.clk.Now()))
	if got := r.awaitedGroups(); !slices.Equal(got, []string{groupAlpha}) {
		t.Errorf("awaited = %v, want [alpha]", got)
	}
	// A fetch that started before the group was awaited does not count.
	r.must(r.refresh(idOne, groupAlpha, "a2", "", "", r.clk.Now().Add(-time.Hour)))
	if got := r.awaitedGroups(); !slices.Equal(got, []string{groupAlpha}) {
		t.Errorf("an old fetch counted: awaited = %v", got)
	}
	r.must(r.refresh(idOne, groupAlpha, "a3", "", "", r.clk.Now()))
	rows := r.changes()
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	for _, n := range []string{"alpha", "beta"} {
		if !slices.Contains(rowFields(rows[0]), n) {
			t.Errorf("fields = %v, missing %s", rowFields(rows[0]), n)
		}
	}
}

func TestNewlyAwaitedGroupsWithStoredContentGoStaleInTheSameTransaction(t *testing.T) {
	r := newRig(t)
	r.seed(idOne, sample())
	r.seedGroup(idOne, groupAlpha, "a1", "s1", "t1")
	r.seedGroup(idOne, groupGamma, "g1", "", "")
	r.clk.Advance(time.Minute)
	r.must(r.changePrimary(idOne, func(w *widget) { w.Stamp = "s2" }))
	for group, wantFresh := range map[string]time.Time{groupAlpha: r.clk.Now(), groupGamma: t0.Add(time.Hour)} {
		var row store.GroupRow
		r.must(r.inTx(func(tx *store.Tx) error {
			var err error
			row, _, err = tx.GetGroup(context.Background(), kindWidget, idOne, group)
			return err
		}))
		if !row.FreshUntil.Equal(wantFresh) {
			t.Errorf("%s fresh_until = %v, want %v", group, row.FreshUntil, wantFresh)
		}
		if row.Generation != 1 || len(row.Content) == 0 {
			t.Errorf("%s generation or content changed: %+v", group, row)
		}
	}
}

func TestAMergedPendingRowKeepsTheOriginOfTheChangeThatCreatedIt(t *testing.T) {
	r := newRig(t)
	r.seed(idOne, sample())
	r.seedGroup(idOne, groupGamma, "g1", "", "")
	r.purpose = governor.PurposeInteractive
	r.must(r.changePrimary(idOne, func(w *widget) { w.Count = 1 }))
	r.purpose = governor.PurposeRefresh
	r.e.NoteOrigin(idOne, groupGamma, OriginWrite)
	r.clk.Advance(time.Second)
	r.must(r.refresh(idOne, groupGamma, "g2", "", "", r.clk.Now()))
	rows := r.changes()
	if len(rows) != 1 || rows[0].Origin != OriginRead {
		t.Fatalf("rows = %+v, want the creator's origin read", rows)
	}
	// The unused hint for gamma was consumed by the refresh, not left behind.
	if got := r.e.originFor(idOne, groupGamma, governor.PurposeRefresh); got != OriginRefresh {
		t.Errorf("a hint outlived its detection: %q", got)
	}
}

func TestACompleteBumpsTheVersionOnceReportsTheWordAndIgnoresAStaleCopy(t *testing.T) {
	for _, word := range []string{ResultSettled, ResultFailed, ResultTimeout} {
		t.Run(word, func(t *testing.T) {
			r := newRig(t)
			r.seed(idOne, sample())
			r.seedGroup(idOne, groupGamma, "g1", "", "")
			r.clk.Advance(time.Minute)
			r.must(r.changePrimary(idOne, func(w *widget) { w.Count = 1; w.Title = "T2" }))
			p, ok := r.pending()
			if !ok {
				t.Fatal("no pending row")
			}
			r.clk.Advance(30 * time.Second)
			complete := func() error {
				return r.inTx(func(tx *store.Tx) error { return r.e.Complete(context.Background(), tx, p, word) })
			}
			r.must(complete())
			r.must(complete()) // the lifecycle's stale copy
			rows := r.changes()
			if len(rows) != 1 || r.version(idOne) != 6 {
				t.Fatalf("rows = %d, version = %d; want 1 and 6", len(rows), r.version(idOne))
			}
			wantKinds(t, rows[0].Kinds, changeRetitled)
			if !rows[0].At.Equal(r.clk.Now()) {
				t.Errorf("row = %+v", rows[0])
			}
			if !slices.Equal(r.obs.settled, []string{word}) {
				t.Errorf("settled = %v, want [%s]", r.obs.settled, word)
			}
		})
	}
}

func TestAPendingRowWhoseMergedResultIsEmptyAppendsNothing(t *testing.T) {
	r := newRig(t)
	r.seed(idOne, sample())
	r.seedGroup(idOne, groupGamma, "g1", "", "")
	r.must(r.changePrimary(idOne, func(w *widget) { w.Count = 1 })) // queues gamma, own result empty
	p, ok := r.pending()
	if !ok {
		t.Fatal("no pending row")
	}
	r.must(r.inTx(func(tx *store.Tx) error { return r.e.Complete(context.Background(), tx, p, ResultTimeout) }))
	wantNoChange(t, r)
	if v := r.version(idOne); v != 5 {
		t.Errorf("version = %d, want 5", v)
	}
	if !slices.Equal(r.obs.settled, []string{ResultTimeout}) {
		t.Errorf("settled = %v", r.obs.settled)
	}
}

func TestADetectionThatFailsInsideTheTransactionLeavesNothingBehind(t *testing.T) {
	boom := errors.New("boom")
	r := newRig(t)
	r.seed(idOne, sample())
	r.seedGroup(idOne, groupGamma, "g1", "", "")
	err := r.inTx(func(tx *store.Tx) error {
		next := sample()
		next.Count = 1
		if err := r.e.DetectPrimary(context.Background(), tx, PrimaryChange{ID: idOne, Prev: widgetJSON(t, sample()), Next: widgetJSON(t, next)}); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	wantNoChange(t, r)
	var fresh time.Time
	r.must(r.inTx(func(tx *store.Tx) error {
		row, _, err := tx.GetGroup(context.Background(), kindWidget, idOne, groupGamma)
		fresh = row.FreshUntil
		return err
	}))
	if !fresh.Equal(t0.Add(time.Hour)) {
		t.Errorf("gamma fresh_until = %v, want the seeded value (the early expiry rolled back)", fresh)
	}
}

func TestAGroupWhoseContentCannotBeClassifiedFailsTheDetection(t *testing.T) {
	r := newRig(t)
	r.seed(idOne, sample())
	// The classifier cannot decode the primary content, and the engine says which entity.
	err := r.inTx(func(tx *store.Tx) error {
		return r.e.DetectPrimary(context.Background(), tx, PrimaryChange{ID: idOne, Prev: widgetJSON(t, sample()), Next: []byte("not json")})
	})
	if err == nil || !strings.Contains(err.Error(), idOne) {
		t.Errorf("err = %v, want one naming the entity", err)
	}
}

func TestAGroupDetectionOfThePrimaryGroupOrAnotherKindIsIgnored(t *testing.T) {
	r := newRig(t)
	r.seed(idOne, sample())
	r.must(r.inTx(func(tx *store.Tx) error {
		ctx := context.Background()
		prev := store.GroupRow{Kind: kindWidget, ID: idOne, Group: store.SummaryGroupName, Content: []byte("x")}
		next := store.GroupRow{Kind: kindWidget, ID: idOne, Group: store.SummaryGroupName, Content: []byte("y")}
		if err := r.e.DetectGroup(ctx, tx, &prev, next, RefreshInfo{}); err != nil {
			return err
		}
		other := store.GroupRow{Kind: "other", ID: idOne, Group: groupAlpha, Content: []byte("y")}
		return r.e.DetectGroup(ctx, tx, &prev, other, RefreshInfo{})
	}))
	wantNoChange(t, r)
}

func putQueries(r *rig, qs ...store.QueryRow) {
	r.t.Helper()
	r.must(r.inTx(func(tx *store.Tx) error {
		for _, q := range qs {
			if err := tx.PutQuery(context.Background(), q); err != nil {
				return err
			}
		}
		return nil
	}))
}

// tombstone does what the seam's transaction does, then calls DetectGone.
func (r *rig) tombstone(id string, purpose governor.Purpose) error {
	return r.inTx(func(tx *store.Tx) error {
		ctx := context.Background()
		e, ok, err := tx.GetEntity(ctx, kindWidget, id)
		if err != nil || !ok {
			return err
		}
		e.TombstonedAt = r.clk.Now()
		if err := tx.PutEntity(ctx, e); err != nil {
			return err
		}
		return r.e.DetectGone(ctx, tx, id, purpose)
	})
}

func TestATombstoneAppendsOneRemovedRoleRowNamingEveryQueryThatListedTheEntity(t *testing.T) {
	r := newRig(t)
	r.seed(idOne, sample())
	putQueries(
		r,
		store.QueryRow{Key: "mine", Configured: true, IDs: []string{"other/x#1", "EXAMPLE-ORG/thing#1"}},
		store.QueryRow{Key: "triage", Configured: true, IDs: []string{"example-org/thing#1"}},
		store.QueryRow{Key: "elsewhere", Configured: true, IDs: []string{"example-org/thing#10", "example-org/thing#8"}},
		store.QueryRow{Key: "adhoc", Configured: false, IDs: []string{"example-org/thing#1"}},
	)
	r.clk.Advance(time.Minute)
	r.must(r.tombstone(idOne, governor.PurposeRefresh))

	rows := r.changes()
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	row := rows[0]
	wantKinds(t, row.Kinds, changeGone)
	if !slices.Equal(rowFields(row), []string{"query:mine", "query:triage"}) {
		t.Errorf("fields = %v, want query:mine and query:triage", rowFields(row))
	}
	fs, err := DecodeFields(row.Fields)
	if err != nil || string(fs[0].Before) != `"mine"` || len(fs[0].After) != 0 {
		t.Errorf("query:mine = %+v, %v", fs[0], err)
	}
	if row.ID != refOne || row.Version != 6 || row.Stamp() != "s1" || row.Origin != OriginRefresh {
		t.Errorf("row = %+v", row)
	}

	// A repeat for the same tombstone appends nothing and bumps nothing.
	r.must(r.inTx(func(tx *store.Tx) error {
		return r.e.DetectGone(context.Background(), tx, idOne, governor.PurposeRefresh)
	}))
	if len(r.changes()) != 1 || r.version(idOne) != 6 {
		t.Errorf("a repeat appended: %d rows, version %d", len(r.changes()), r.version(idOne))
	}

	// A later tombstone (the entity came back and went again) is a new removal.
	r.clk.Advance(time.Hour)
	r.must(r.tombstone(idOne, governor.PurposeInteractive))
	rows = r.changes()
	if len(rows) != 2 || rows[1].Origin != OriginRead || r.version(idOne) != 7 {
		t.Errorf("second tombstone: %d rows, version %d", len(rows), r.version(idOne))
	}
}

func TestATombstoneForAnEntityWithNoStoredContentAppendsNothingAndDropsItsPendingRow(t *testing.T) {
	r := newRig(t)
	r.must(r.inTx(func(tx *store.Tx) error {
		ctx := context.Background()
		if err := tx.EnsureEntity(ctx, kindWidget, idOne); err != nil {
			return err
		}
		return tx.PutPending(ctx, store.PendingChange{Kind: kindWidget, ID: idOne, Awaited: []byte("[]"), DetectedAt: t0, Deadline: t0})
	}))
	r.must(r.inTx(func(tx *store.Tx) error {
		return r.e.DetectGone(context.Background(), tx, idOne, governor.PurposeRefresh)
	}))
	wantNoChange(t, r)
}

func TestATombstoneMergesIntoAndEndsThePendingRow(t *testing.T) {
	r := newRig(t)
	r.seed(idOne, sample())
	r.seedGroup(idOne, groupGamma, "g1", "", "")
	putQueries(r, store.QueryRow{Key: "mine", Configured: true, IDs: []string{refOne}})
	r.clk.Advance(time.Minute)
	r.must(r.changePrimary(idOne, func(w *widget) { w.Count = 1; w.Open = false }))
	if _, ok := r.pending(); !ok {
		t.Fatal("no pending row")
	}
	r.clk.Advance(time.Second)
	r.must(r.tombstone(idOne, governor.PurposeRefresh))
	rows := r.changes()
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1 (the pending row merged into the removal)", len(rows))
	}
	wantKinds(t, rows[0].Kinds, changeToggled, changeGone)
	if !slices.Equal(rowFields(rows[0]), []string{"open", "query:mine"}) {
		t.Errorf("fields = %v", rowFields(rows[0]))
	}
	if _, ok := r.pending(); ok {
		t.Error("the pending row survived the tombstone")
	}
	if r.version(idOne) != 6 {
		t.Errorf("version = %d, want 6", r.version(idOne))
	}
}

func (r *rig) membership(m Membership) error {
	return r.inTx(func(tx *store.Tx) error { return r.e.AppendMembership(context.Background(), tx, m) })
}

func TestAMembershipRowForAnEntityTheStoreHasNotSeenCreatesItAtVersionOne(t *testing.T) {
	r := newRig(t)
	r.clk.Advance(time.Minute)
	r.must(r.membership(Membership{Kind: changeJoined, Query: "mine", ID: idOne, Ref: refOne}))
	rows := r.changes()
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	row := rows[0]
	wantKinds(t, row.Kinds, changeJoined)
	if row.Version != 1 || row.ID != refOne || row.Kind != kindWidget || row.Origin != OriginQuery || row.Stamp() != "" {
		t.Errorf("row = %+v", row)
	}
	fs, err := DecodeFields(row.Fields)
	if err != nil || len(fs) != 1 || fs[0].Name != QueryFieldPrefix+"mine" || string(fs[0].After) != `"mine"` {
		t.Errorf("fields = %+v, %v", fs, err)
	}
	if _, ok := r.pending(); ok {
		t.Error("membership created a pending row")
	}
	if len(r.obs.detected) != 0 {
		t.Errorf("Detected fired with an unknown origin time: %+v", r.obs.detected)
	}
}

func TestAMembershipRowCarriesTheStoredStampAndTheExtraFields(t *testing.T) {
	r := newRig(t)
	r.seed(idOne, sample())
	r.must(r.membership(Membership{
		Kind: changeLeft, Query: "mine", ID: idOne, Ref: refOne,
		Extra: []Field{{Name: "open", Before: json.RawMessage("true"), After: json.RawMessage("false")}},
	}))
	rows := r.changes()
	if len(rows) != 1 || rows[0].Version != 6 || rows[0].Stamp() != "s1" {
		t.Fatalf("rows = %+v", rows)
	}
	wantKinds(t, rows[0].Kinds, changeLeft)
	if !slices.Equal(rowFields(rows[0]), []string{"open", "query:mine"}) {
		t.Errorf("fields = %v", rowFields(rows[0]))
	}
	if len(r.obs.detected) != 1 || r.obs.detected[0].Origin != OriginQuery || r.obs.detected[0].Kind != changeLeft {
		t.Errorf("detected = %+v", r.obs.detected)
	}
}

func TestAMembershipRowMergesIntoAPendingRowAndLeavesItPending(t *testing.T) {
	r := newRig(t)
	r.seed(idOne, sample())
	r.seedGroup(idOne, groupGamma, "g1", "", "")
	r.clk.Advance(time.Minute)
	r.must(r.changePrimary(idOne, func(w *widget) { w.Count = 1; w.Open = false }))
	before, _ := r.pending()

	r.clk.Advance(time.Second)
	r.must(r.membership(Membership{Kind: changeJoined, Query: "mine", ID: idOne, Ref: refOne}))
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
	if !slices.Equal(after.Kinds, []string{changeToggled, changeJoined}) {
		t.Errorf("kinds = %v", after.Kinds)
	}
	r.must(r.refresh(idOne, groupGamma, "g2", "", "", r.clk.Now()))
	rows := r.changes()
	if len(rows) != 1 || r.version(idOne) != 6 {
		t.Fatalf("rows = %d, version = %d; want one row at version 6", len(rows), r.version(idOne))
	}
	if !slices.Equal(rowFields(rows[0]), []string{"gamma", "open", "query:mine"}) {
		t.Errorf("fields = %v", rowFields(rows[0]))
	}
}

func TestAMembershipNeedsAnIDARefAndAMembershipKind(t *testing.T) {
	r := newRig(t)
	for _, m := range []Membership{
		{Kind: changeJoined, Query: "q", Ref: refOne},
		{Kind: changeJoined, Query: "q", ID: idOne},
		{Kind: changeGone, Query: "q", ID: idOne, Ref: refOne},
	} {
		if err := r.membership(m); err == nil {
			t.Errorf("%+v was accepted", m)
		}
	}
	wantNoChange(t, r)
}

func TestAwaitAddsRenewsAndOrdersByTheSpecGroups(t *testing.T) {
	e := newEngine(t, governor.NewManualClock(t0))
	later := t0.Add(time.Minute)
	got := e.await([]Awaiting{{Group: groupGamma, Since: t0}, {Group: groupBeta, Since: t0}}, []string{groupAlpha, groupBeta}, later)
	var names []string
	for _, a := range got {
		names = append(names, a.Group)
	}
	if !slices.Equal(names, []string{groupAlpha, groupBeta, groupGamma}) {
		t.Fatalf("order = %v, want the Spec.Groups order", names)
	}
	want := map[string]time.Time{groupAlpha: later, groupBeta: later, groupGamma: t0}
	for _, a := range got {
		if !a.Since.Equal(want[a.Group]) {
			t.Errorf("%s Since = %v, want %v", a.Group, a.Since, want[a.Group])
		}
	}
}

func TestDecodeAwaitedReadsEmptyNullAndEncodedForms(t *testing.T) {
	for _, raw := range []json.RawMessage{nil, json.RawMessage(""), json.RawMessage("null"), json.RawMessage("[]")} {
		aw, err := DecodeAwaited(raw)
		if err != nil || len(aw) != 0 {
			t.Errorf("DecodeAwaited(%q) = %v, %v; want none", raw, aw, err)
		}
	}
	in := []Awaiting{{Group: groupGamma, Since: t0}, {Group: groupAlpha, Since: t0.Add(time.Second)}}
	enc, err := encodeAwaited(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := DecodeAwaited(enc)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || out[0].Group != groupGamma || !out[0].Since.Equal(t0) || !out[1].Since.Equal(t0.Add(time.Second)) {
		t.Errorf("round trip = %+v", out)
	}
	if _, err := DecodeAwaited(json.RawMessage(`{"x":1}`)); err == nil {
		t.Error("a document that is not a list was accepted")
	}
	enc, err = encodeAwaited(nil)
	if err != nil || string(enc) != "[]" {
		t.Errorf("encodeAwaited(nil) = %q, %v; want []", enc, err)
	}
}

func TestTheEngineIsAnOriginHintsReceiverAndTheSpecIsStillValid(t *testing.T) {
	var hints interface {
		NoteOrigin(id, group, origin string)
	} = (*Engine)(nil)
	_ = hints
	var _ Observer = (*recorder)(nil)
	var _ registry.Spec = testSpec()
	if err := testSpec().Validate(); err != nil {
		t.Error(err)
	}
}
