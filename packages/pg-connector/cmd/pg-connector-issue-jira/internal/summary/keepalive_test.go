package summary

import (
	"context"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
)

func retain(t *testing.T, k KeepAlive, id string) bool {
	t.Helper()
	ok, err := k.Retain(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

func TestRetainOpenIssueHoweverOldTheRead(t *testing.T) {
	p, st, clk := newPersister(t)
	k := KeepAlive{Store: st, Clock: clk, TerminalTTL: 24 * time.Hour}
	ctx := context.Background()
	if retain(t, k, key) {
		t.Error("Retain is true for an entity the store has no row for")
	}
	if err := k.Access(ctx, key); err != nil {
		t.Fatal(err)
	}
	persist(t, p, issue(key, "indeterminate", "one"))
	clk.Advance(365 * 24 * time.Hour)
	if !retain(t, k, "abc-1") {
		t.Error("Retain is false for an open issue read a year ago")
	}
}

func TestRetainUntilTwentyFourHoursAfterClosing(t *testing.T) {
	p, st, clk := newPersister(t)
	k := KeepAlive{Store: st, Clock: clk, TerminalTTL: 24 * time.Hour}
	persist(t, p, issue(key, "done", "one")) // closed_at = t0
	for _, c := range []struct {
		after time.Duration
		want  bool
	}{
		{0, true},
		{23*time.Hour + 59*time.Minute, true},
		{24 * time.Hour, false},
		{48 * time.Hour, false},
	} {
		clk.Advance(c.after - clk.Now().Sub(t0))
		if got := retain(t, k, key); got != c.want {
			t.Errorf("Retain %v after closing = %v, want %v", c.after, got, c.want)
		}
	}
}

func TestRetainIsFalseForATombstonedEntity(t *testing.T) {
	p, st, clk := newPersister(t)
	k := KeepAlive{Store: st, Clock: clk, TerminalTTL: 24 * time.Hour}
	persist(t, p, issue(key, "new", "one"))
	if err := p.MarkGone(context.Background(), key, governor.PurposeQuery); err != nil {
		t.Fatal(err)
	}
	if retain(t, k, key) {
		t.Error("Retain is true for a tombstoned entity")
	}
}

func TestAccessTouchesTheEntity(t *testing.T) {
	_, st, clk := newPersister(t)
	k := KeepAlive{Store: st, Clock: clk, TerminalTTL: 24 * time.Hour}
	ctx := context.Background()
	if err := k.Access(ctx, "abc-1"); err != nil {
		t.Fatal(err)
	}
	e := entity(t, st, key)
	if !e.KeepAlive || !e.LastAccess.Equal(t0) {
		t.Errorf("entity = %+v, want keep_alive and last_access %v", e, t0)
	}
	clk.Advance(time.Hour)
	if err := k.Access(ctx, key); err != nil {
		t.Fatal(err)
	}
	if got := entity(t, st, key).LastAccess; !got.Equal(t0.Add(time.Hour)) {
		t.Errorf("last_access = %v", got)
	}
	if _, ok, _ := st.GetEntity(ctx, groups.EntityKind, "abc-1"); ok {
		t.Error("Access stored an unfolded id")
	}
}
