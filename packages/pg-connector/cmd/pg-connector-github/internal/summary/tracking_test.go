package summary

import (
	"context"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// watch sets the watched flag of id through the store's one writer.
func (r *rig) watch(t *testing.T, id, via string) {
	t.Helper()
	err := r.st.InTx(context.Background(), func(tx *store.Tx) error {
		_, err := tx.SetWatched(context.Background(), entity.KindPR, id, via, t0)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestClassesCountsMembersOtherOpenClosedGraceAndWatchedApart(t *testing.T) {
	r := newRig(t, rigOpts{})
	ctx := context.Background()
	r.putQuery(t, "mine", true, "member-open", "member-closed", "member-watched")
	for _, id := range []string{"member-open", "member-closed", "member-watched", "read-open", "read-no-summary", "read-watched", "merged", "closed", "tombstoned"} {
		r.putEntity(t, id, t0)
	}
	r.putSummaryOf(t, "member-open", "OPEN", t0)
	r.putSummaryOf(t, "member-closed", "CLOSED", t0) // a member stays a member whatever its state
	r.putSummaryOf(t, "read-open", "OPEN", t0)
	r.putSummaryOf(t, "merged", "MERGED", t0)
	r.putSummaryOf(t, "closed", "CLOSED", t0)
	if err := r.st.PutEntity(ctx, store.EntityRow{Kind: entity.KindPR, ID: "tombstoned", LastAccess: t0, TombstonedAt: t0}); err != nil {
		t.Fatal(err)
	}
	r.watch(t, "member-watched", "query")
	r.watch(t, "read-watched", "direct")
	r.watch(t, "tombstoned", "direct") // the watched count is Store.CountWatched's: the flag alone

	got, err := r.svc.Classes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := Counts{Members: 3, OtherOpen: 3, ClosedGrace: 2, Watched: 3}
	if got != want {
		t.Errorf("Classes = %+v, want %+v", got, want)
	}
	if got.Tracked() != 6 {
		t.Errorf("Tracked = %d, want 6: members and other open, never the closed or the tombstoned", got.Tracked())
	}
	if n, err := r.st.CountWatched(ctx, entity.KindPR); err != nil || n != got.Watched {
		t.Errorf("CountWatched = %d %v, want the same as Classes.Watched (%d)", n, err, got.Watched)
	}
}

func TestClassesOfAnEmptyStoreIsZero(t *testing.T) {
	r := newRig(t, rigOpts{})
	got, err := r.svc.Classes(context.Background())
	if err != nil || got != (Counts{}) {
		t.Errorf("Classes of an empty store = %+v %v, want zero", got, err)
	}
}

func TestInKeepAliveSetAnswersEachKnownIDByTheKeepAliveRuleAndSkipsUnknownOnes(t *testing.T) {
	r := newRig(t, rigOpts{})
	ctx := context.Background()
	r.putQuery(t, "mine", true, "member")
	for _, id := range []string{"member", "read-open", "merged"} {
		r.putEntity(t, id, t0)
	}
	r.putSummaryOf(t, "read-open", "OPEN", t0)
	r.putSummaryOf(t, "merged", "MERGED", t0)
	got, err := r.svc.InKeepAliveSet(ctx, []string{"member", "read-open", "merged", "never-seen"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"member": true, "read-open": true, "merged": false}
	if len(got) != len(want) {
		t.Fatalf("InKeepAliveSet = %v, want %v (an id with no entity is absent)", got, want)
	}
	for id, in := range want {
		if v, ok := got[id]; !ok || v != in {
			t.Errorf("InKeepAliveSet[%s] = %v (present %v), want %v", id, v, ok, in)
		}
		if single, err := r.svc.InKeepAlive(ctx, id); err != nil || single != in {
			t.Errorf("InKeepAlive(%s) = %v %v disagrees with the set's %v", id, single, err, in)
		}
	}
}
