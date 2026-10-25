package summary

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/engine"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

var (
	t0       = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	settings = groups.Settings{SummaryTTL: time.Minute, DetailTTL: 10 * time.Minute, DetailMaxAge: time.Hour, TerminalTTL: 24 * time.Hour}
)

const key = "ABC-1"

func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, _, err := store.Open(filepath.Join(t.TempDir(), "store.db"), store.Options{Synchronous: "OFF"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func issue(id, category, title string) groups.SummaryContent {
	return groups.SummaryContent{
		Issue:   schema.Issue{ID: id, Title: title, StatusCategory: category},
		Updated: "2026-10-05T10:11:12.000+0000",
	}
}

func newPersister(t *testing.T) (*Persister, *store.Store, *governor.ManualClock) {
	t.Helper()
	st := openStore(t)
	clk := governor.NewManualClock(t0)
	return &Persister{Store: st, Rules: groups.Rules(settings), Clock: clk}, st, clk
}

func persist(t *testing.T, p *Persister, c groups.SummaryContent) Stored {
	t.Helper()
	ctx := context.Background()
	tok, err := p.Begin(ctx, c.Issue.ID)
	if err != nil {
		t.Fatal(err)
	}
	s, err := p.Persist(ctx, tok, c, engine.RefreshInfo{})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func entity(t *testing.T, st *store.Store, id string) store.EntityRow {
	t.Helper()
	e, ok, err := st.GetEntity(context.Background(), groups.EntityKind, id)
	if err != nil || !ok {
		t.Fatalf("entity %s: ok=%v err=%v", id, ok, err)
	}
	return e
}

func group(t *testing.T, st *store.Store, id string) store.GroupRow {
	t.Helper()
	g, ok, err := st.GetGroup(context.Background(), groups.EntityKind, id, groups.Summary)
	if err != nil || !ok {
		t.Fatalf("group %s: ok=%v err=%v", id, ok, err)
	}
	return g
}

func TestPersistFirstOpenStoresRowsAndLeavesClosedAtZero(t *testing.T) {
	p, st, _ := newPersister(t)
	var hookCalls int
	var hookFirst bool
	var hookPrev *groups.SummaryContent
	p.Hook = func(_ context.Context, _ *store.Tx, prev *groups.SummaryContent, _ groups.SummaryContent, first bool, _ engine.RefreshInfo) error {
		hookCalls++
		hookFirst, hookPrev = first, prev
		return nil
	}
	got := persist(t, p, issue("abc-1", "indeterminate", "one"))
	if !got.First || got.Prev != nil {
		t.Errorf("Stored = %+v, want First with nil Prev", got)
	}
	if hookCalls != 1 || !hookFirst || hookPrev != nil {
		t.Errorf("hook calls=%d first=%v prev=%v", hookCalls, hookFirst, hookPrev)
	}
	e := entity(t, st, key)
	if !e.ClosedAt.IsZero() {
		t.Errorf("closed_at = %v, want zero", e.ClosedAt)
	}
	g := group(t, st, key)
	if g.Fingerprint != groups.Fingerprint(issue("abc-1", "indeterminate", "one")) {
		t.Errorf("fingerprint %q", g.Fingerprint)
	}
	if want := t0.Add(time.Minute); !g.FreshUntil.Equal(want) || !got.FreshUntil.Equal(want) {
		t.Errorf("fresh_until = %v / %v, want %v", g.FreshUntil, got.FreshUntil, want)
	}
	if g.Generation != 1 {
		t.Errorf("generation = %d, want 1", g.Generation)
	}
}

func TestPersistSecondCarriesPrev(t *testing.T) {
	p, _, _ := newPersister(t)
	persist(t, p, issue(key, "new", "one"))
	got := persist(t, p, issue(key, "new", "two"))
	if got.First || got.Prev == nil || got.Prev.Issue.Title != "one" {
		t.Errorf("Stored = %+v, want Prev title one", got)
	}
}

func TestPersistStampsClosedAtOnceAndReopenClears(t *testing.T) {
	p, st, clk := newPersister(t)
	persist(t, p, issue(key, "indeterminate", "one"))

	clk.Advance(time.Hour)
	stamp := clk.Now()
	persist(t, p, issue(key, "done", "one"))
	if got := entity(t, st, key).ClosedAt; !got.Equal(stamp) {
		t.Fatalf("closed_at = %v, want %v", got, stamp)
	}

	clk.Advance(time.Hour)
	persist(t, p, issue(key, "done", "one"))
	if got := entity(t, st, key).ClosedAt; !got.Equal(stamp) {
		t.Errorf("a second closed Persist moved closed_at to %v, want %v", got, stamp)
	}

	clk.Advance(time.Hour)
	persist(t, p, issue(key, "indeterminate", "one"))
	if got := entity(t, st, key).ClosedAt; !got.IsZero() {
		t.Errorf("closed_at after a reopen = %v, want zero", got)
	}
}

func TestPersistClosedForLessThanTerminalTTLKeepsNormalSchedule(t *testing.T) {
	p, st, clk := newPersister(t)
	persist(t, p, issue(key, "indeterminate", "one"))
	clk.Advance(time.Minute)

	got := persist(t, p, issue(key, "done", "one")) // sees it closed now
	if want := clk.Now().Add(time.Minute); !got.FreshUntil.Equal(want) {
		t.Errorf("fresh_until on the closing persist = %v, want the normal %v", got.FreshUntil, want)
	}

	clk.Advance(time.Hour) // closed for 1 hour
	got = persist(t, p, issue(key, "done", "one"))
	if want := clk.Now().Add(time.Minute); !got.FreshUntil.Equal(want) {
		t.Errorf("fresh_until 1 hour after closing = %v, want the normal %v", got.FreshUntil, want)
	}
	if g := group(t, st, key); !g.FreshUntil.Equal(got.FreshUntil) {
		t.Errorf("stored fresh_until %v != returned %v", g.FreshUntil, got.FreshUntil)
	}

	clk.Advance(24 * time.Hour) // closed for 25 hours: terminal
	got = persist(t, p, issue(key, "done", "one"))
	if want := clk.Now().Add(24 * time.Hour); !got.FreshUntil.Equal(want) {
		t.Errorf("fresh_until 25 hours after closing = %v, want terminal %v", got.FreshUntil, want)
	}
}

func TestPersistKeepsLastAccessAndKeepAliveAndRevivesTombstone(t *testing.T) {
	p, st, clk := newPersister(t)
	ctx := context.Background()
	if err := st.TouchEntity(ctx, groups.EntityKind, key, t0.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	persist(t, p, issue(key, "new", "one"))
	e := entity(t, st, key)
	if !e.KeepAlive || !e.LastAccess.Equal(t0.Add(-time.Hour)) {
		t.Errorf("entity = %+v, want keep_alive and last_access kept", e)
	}

	clk.Advance(time.Minute)
	if err := p.MarkGone(ctx, key, governor.PurposeQuery); err != nil {
		t.Fatal(err)
	}
	if entity(t, st, key).TombstonedAt.IsZero() {
		t.Fatal("MarkGone did not tombstone")
	}
	persist(t, p, issue(key, "new", "one"))
	if e := entity(t, st, key); !e.TombstonedAt.IsZero() || !e.KeepAlive {
		t.Errorf("entity after reappearing = %+v, want revived", e)
	}
}

func TestPersistDoesNotClearWatchedFlag(t *testing.T) {
	p, st, _ := newPersister(t)
	ctx := context.Background()
	if err := st.InTx(ctx, func(tx *store.Tx) error {
		_, err := tx.SetWatched(ctx, groups.EntityKind, key, "query", t0)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	persist(t, p, issue(key, "new", "one"))
	if entity(t, st, key).WatchedAt.IsZero() {
		t.Error("Persist cleared the watched flag")
	}
}

// raceStore wraps the store so that the commit's compare-and-swap first loses a
// write: a writer bumps the group's generation after the refresh took its token
// and before it committed.
type raceStore struct {
	*store.Store
	bumped bool
}

func (r *raceStore) PutGroupIfThen(ctx context.Context, g store.GroupRow, expect int64, then func(tx *store.Tx) error) (bool, error) {
	if !r.bumped {
		r.bumped = true
		if _, err := r.Store.BumpGroup(ctx, g.Kind, g.ID, g.Group, time.Time{}); err != nil {
			return false, err
		}
	}
	return r.Store.PutGroupIfThen(ctx, g, expect, then)
}

func TestPersistLosingTheGenerationRaceStoresNothingAndCallsNoHook(t *testing.T) {
	p, st, _ := newPersister(t)
	persist(t, p, issue(key, "new", "one")) // the row exists, so the bump is real

	var hooks, after int
	p.Hook = func(context.Context, *store.Tx, *groups.SummaryContent, groups.SummaryContent, bool, engine.RefreshInfo) error {
		hooks++
		return nil
	}
	p.AfterCommit = func(context.Context, *groups.SummaryContent, groups.SummaryContent, Stored) { after++ }
	p.Store = &raceStore{Store: st}

	before := group(t, st, key)
	got := persist(t, p, issue(key, "done", "two"))
	if got != (Stored{}) {
		t.Errorf("Stored = %+v, want the zero value", got)
	}
	if hooks != 0 || after != 0 {
		t.Errorf("hook calls = %d, after-commit calls = %d, want 0", hooks, after)
	}
	g := group(t, st, key)
	if string(g.Content) != string(before.Content) {
		t.Errorf("content changed: %s", g.Content)
	}
	if !entity(t, st, key).ClosedAt.IsZero() {
		t.Error("a losing refresh stamped closed_at")
	}
}

func TestPersistTokenTakenBeforeAWriteLosesEvenAfterTheWriteCompleted(t *testing.T) {
	p, st, _ := newPersister(t)
	ctx := context.Background()
	persist(t, p, issue(key, "new", "one"))
	tok, err := p.Begin(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.BumpGroup(ctx, groups.EntityKind, key, groups.Summary, time.Time{}); err != nil {
		t.Fatal(err)
	}
	got, err := p.Persist(ctx, tok, issue(key, "new", "two"), engine.RefreshInfo{})
	if err != nil || got != (Stored{}) {
		t.Fatalf("Persist = %+v, %v, want zero and nil", got, err)
	}
}

func TestPersistHookErrorRollsBack(t *testing.T) {
	p, st, _ := newPersister(t)
	boom := errors.New("boom")
	p.Hook = func(context.Context, *store.Tx, *groups.SummaryContent, groups.SummaryContent, bool, engine.RefreshInfo) error {
		return boom
	}
	ctx := context.Background()
	tok, err := p.Begin(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.Persist(ctx, tok, issue(key, "new", "one"), engine.RefreshInfo{})
	if !errors.Is(err, boom) || got != (Stored{}) {
		t.Fatalf("Persist = %+v, %v, want the hook error", got, err)
	}
	if _, ok, _ := st.GetGroup(ctx, groups.EntityKind, key, groups.Summary); ok {
		t.Error("the group row survived a hook failure")
	}
	if _, ok, _ := st.GetEntity(ctx, groups.EntityKind, key); ok {
		t.Error("the entity row survived a hook failure")
	}
}

func TestPersistAfterCommitRunsOnlyWhenStored(t *testing.T) {
	p, _, _ := newPersister(t)
	var seen []Stored
	var nexts []string
	p.AfterCommit = func(_ context.Context, _ *groups.SummaryContent, next groups.SummaryContent, st Stored) {
		seen = append(seen, st)
		nexts = append(nexts, next.Issue.Title)
	}
	persist(t, p, issue(key, "new", "one"))
	if len(seen) != 1 || !seen[0].First || nexts[0] != "one" {
		t.Errorf("after-commit calls = %+v %v", seen, nexts)
	}
}

func TestPersistRejectsAMismatchedToken(t *testing.T) {
	p, _, _ := newPersister(t)
	ctx := context.Background()
	tok, err := p.Begin(ctx, "XYZ-9")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Persist(ctx, tok, issue(key, "new", "one"), engine.RefreshInfo{}); err == nil {
		t.Error("Persist accepted a token for another issue")
	}
	if _, err := p.Persist(ctx, tok, issue("", "new", "one"), engine.RefreshInfo{}); err == nil {
		t.Error("Persist accepted an issue with no id")
	}
}

func TestMarkGoneTombstonesAndCallsOnGoneOnlyWhenVisible(t *testing.T) {
	p, st, _ := newPersister(t)
	ctx := context.Background()
	var gone []string
	var purposes []governor.Purpose
	p.OnGone = func(_ context.Context, _ *store.Tx, id string, purpose governor.Purpose) error {
		gone = append(gone, id)
		purposes = append(purposes, purpose)
		return nil
	}
	if err := p.MarkGone(ctx, key, governor.PurposeQuery); err != nil { // no entity
		t.Fatal(err)
	}
	if len(gone) != 0 {
		t.Fatalf("OnGone called for an absent entity: %v", gone)
	}
	if _, ok, _ := st.GetEntity(ctx, groups.EntityKind, key); ok {
		t.Fatal("MarkGone created an entity")
	}
	persist(t, p, issue(key, "new", "one"))
	if err := p.MarkGone(ctx, "abc-1", governor.PurposeInteractive); err != nil {
		t.Fatal(err)
	}
	if len(gone) != 1 || gone[0] != key || purposes[0] != governor.PurposeInteractive {
		t.Fatalf("OnGone calls = %v %v", gone, purposes)
	}
	if err := p.MarkGone(ctx, key, governor.PurposeQuery); err != nil { // already tombstoned
		t.Fatal(err)
	}
	if len(gone) != 1 {
		t.Errorf("OnGone called again for a tombstoned entity: %v", gone)
	}

	// an OnGone failure rolls the tombstone back
	persist(t, p, issue(key, "new", "one"))
	boom := errors.New("boom")
	p.OnGone = func(context.Context, *store.Tx, string, governor.Purpose) error { return boom }
	if err := p.MarkGone(ctx, key, governor.PurposeQuery); !errors.Is(err, boom) {
		t.Fatalf("MarkGone error = %v", err)
	}
	if !entity(t, st, key).TombstonedAt.IsZero() {
		t.Error("the tombstone survived an OnGone failure")
	}
}

func TestNilHooksAreAllowed(t *testing.T) {
	p, _, _ := newPersister(t)
	persist(t, p, issue(key, "new", "one"))
	if err := p.MarkGone(context.Background(), key, governor.PurposeQuery); err != nil {
		t.Fatal(err)
	}
}
