package query

import (
	"context"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

func TestMembersAreTheIdsOfStoredConfiguredRows(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	for _, q := range []store.QueryRow{
		{Key: "mine", Configured: true, IDs: []string{"a-1", "B-2"}},
		{Key: "team", Configured: true, IDs: []string{"B-2", "C-3"}},
		{Key: "mine?x=1", Configured: false, IDs: []string{"Z-9"}},
		{Key: "gone", Configured: false, IDs: []string{"Y-8"}},
	} {
		if err := st.PutQuery(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	got, err := Members(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || !got["A-1"] || !got["B-2"] || !got["C-3"] {
		t.Errorf("Members = %v, want A-1, B-2 and C-3 only", got)
	}
}

func TestTrackerReturnsNoUnresolvedReference(t *testing.T) {
	un, err := NewTracker().Track(context.Background(), []string{"A-1", "B-2"})
	if err != nil || len(un) != 0 {
		t.Errorf("Track = %v, %v, want nothing unresolved", un, err)
	}
}
