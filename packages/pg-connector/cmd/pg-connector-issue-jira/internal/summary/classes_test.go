package summary

import (
	"context"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

func TestClassesCountsMembersOtherOpenClosedGraceAndWatched(t *testing.T) {
	p, st, clk := newPersister(t)
	ctx := context.Background()
	persist(t, p, issue("M-1", "indeterminate", "member"))
	persist(t, p, issue("M-2", "new", "member"))
	persist(t, p, issue("O-1", "new", "other open"))
	persist(t, p, issue("C-1", "done", "closed in grace"))
	persist(t, p, issue("G-1", "new", "gone"))
	if err := p.MarkGone(ctx, "G-1", governor.PurposeQuery); err != nil {
		t.Fatal(err)
	}
	persist(t, p, issue("C-2", "done", "closed long ago"))
	for _, id := range []string{"M-1", "O-1", "C-1"} {
		if err := st.InTx(ctx, func(tx *store.Tx) error {
			_, err := tx.SetWatched(ctx, groups.EntityKind, id, "read", t0)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	clk.Advance(30 * time.Hour)
	// C-1 and C-2 closed at t0, 30 hours ago: out of grace; C-3 closes now.
	persist(t, p, issue("C-3", "done", "closed just now"))

	got, err := Classes(ctx, st, map[string]bool{"M-1": true, "M-2": true}, clk.Now(), 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	want := Counts{Members: 2, OtherOpen: 1, ClosedGrace: 1, Watched: 3}
	if got != want {
		t.Errorf("Classes = %+v, want %+v", got, want)
	}
}
