package store

import (
	"context"
	"testing"
	"time"

	"github.com/phillipgreenii/ccpool/internal/clock"
)

func TestInsertAndGetByExternalID_roundTripsAllFields(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	in := Session{
		ExternalID: "ext-alpha", ClaudeSessionID: "csid-alpha", Name: "alpha-display",
		CWD: "/tmp/a", State: Starting, TmuxSession: "cc-ext-alpha",
	}
	if err := st.Insert(ctx, in); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	got, ok, err := st.GetByExternalID(ctx, "ext-alpha")
	if err != nil || !ok {
		t.Fatalf("GetByExternalID: ok=%v err=%v", ok, err)
	}
	if got.ID <= 0 {
		t.Errorf("ID = %d, want a positive surrogate id", got.ID)
	}
	if got.ExternalID != "ext-alpha" || got.ClaudeSessionID != "csid-alpha" || got.Name != "alpha-display" {
		t.Errorf("identity round-trip failed: %+v", got)
	}
	if got.State != Starting || got.CWD != "/tmp/a" || got.TmuxSession != "cc-ext-alpha" {
		t.Errorf("fields round-trip failed: %+v", got)
	}
	if got.Generation != 1 {
		t.Errorf("Generation = %d, want 1 (set on insert)", got.Generation)
	}
	if got.CreatedAt != 1000 || got.LastActivityAt != 1000 {
		t.Errorf("timestamps from fake clock not applied: %+v", got)
	}

	_, ok, err = st.GetByExternalID(ctx, "missing")
	if err != nil || ok {
		t.Fatalf("GetByExternalID(missing): ok=%v err=%v, want ok=false err=nil", ok, err)
	}
}

func TestInsert_requiresExternalID(t *testing.T) {
	st := newTestStore(t)
	if err := st.Insert(context.Background(), Session{State: Starting}); err == nil {
		t.Fatal("Insert without ExternalID must error")
	}
}

func TestGetByClaudeSessionID_findsRow(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	mustInsert(t, st, "ext-a", "csid-a")

	got, ok, err := st.GetByClaudeSessionID(ctx, "csid-a")
	if err != nil || !ok {
		t.Fatalf("GetByClaudeSessionID: ok=%v err=%v", ok, err)
	}
	if got.ExternalID != "ext-a" {
		t.Errorf("ExternalID = %q, want ext-a", got.ExternalID)
	}

	_, ok, err = st.GetByClaudeSessionID(ctx, "nope")
	if err != nil || ok {
		t.Fatalf("GetByClaudeSessionID(missing): ok=%v err=%v, want ok=false err=nil", ok, err)
	}
}

func TestInsert_allowsMultipleRowsWithNoClaudeSessionID(t *testing.T) {
	// claude_session_id is nullable+unique: two rows with no csid must not collide
	// on the UNIQUE constraint (NULLs are distinct in SQLite).
	st := newTestStore(t)
	ctx := context.Background()
	if err := st.Insert(ctx, Session{ExternalID: "ext-1", State: Starting}); err != nil {
		t.Fatalf("Insert ext-1: %v", err)
	}
	if err := st.Insert(ctx, Session{ExternalID: "ext-2", State: Starting}); err != nil {
		t.Fatalf("Insert ext-2 (no csid) must not collide: %v", err)
	}
}

func TestTransition_byExternalID_bumpsGenerationAndReturnsPrior(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	mustInsert(t, st, "a", "csid-a") // Starting, generation 1
	bumpClock(t, st, 5)

	prior, err := st.Transition(ctx, "a", Idle, "csid-a", "/p/a.jsonl")
	if err != nil {
		t.Fatalf("Transition: %v", err)
	}
	if prior != Starting {
		t.Errorf("prior = %q, want starting", prior)
	}
	got, _, _ := st.GetByExternalID(ctx, "a")
	if got.State != Idle {
		t.Errorf("state = %q, want idle", got.State)
	}
	if got.Generation != 2 {
		t.Errorf("generation = %d, want 2", got.Generation)
	}
	if got.ClaudeSessionID != "csid-a" || got.TranscriptPath != "/p/a.jsonl" {
		t.Errorf("csid/transcript not updated: %+v", got)
	}
}

func TestDelete_byExternalID(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	mustInsert(t, st, "a", "csid-a")
	if err := st.Delete(ctx, "a"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok, _ := st.GetByExternalID(ctx, "a"); ok {
		t.Error("row still present after Delete")
	}
	if err := st.Delete(ctx, "missing"); err != nil {
		t.Errorf("Delete(missing) = %v, want nil", err)
	}
}

func TestUpsert_insertsStartingWhenAbsentNoopWhenPresent(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	if err := st.Upsert(ctx, "a", "csid-a", "display-a"); err != nil {
		t.Fatalf("Upsert(new): %v", err)
	}
	got, ok, _ := st.GetByExternalID(ctx, "a")
	if !ok || got.State != Starting || got.ClaudeSessionID != "csid-a" || got.Name != "display-a" {
		t.Fatalf("after upsert-new: ok=%v %+v", ok, got)
	}

	// Second upsert with a different csid must NOT clobber the existing row.
	if err := st.Upsert(ctx, "a", "csid-OTHER", "other"); err != nil {
		t.Fatalf("Upsert(existing): %v", err)
	}
	got2, _, _ := st.GetByExternalID(ctx, "a")
	if got2.ClaudeSessionID != "csid-a" {
		t.Errorf("upsert clobbered claude_session_id: %q, want csid-a", got2.ClaudeSessionID)
	}
}

func TestPoll_byExternalID(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	mustInsert(t, st, "a", "csid-a")

	gen, state, ok, err := st.Poll(ctx, "a")
	if err != nil || !ok {
		t.Fatalf("Poll: ok=%v err=%v", ok, err)
	}
	if gen != 1 || state != Starting {
		t.Errorf("Poll = (gen %d, state %q), want (1, starting)", gen, state)
	}

	_, _, ok, err = st.Poll(ctx, "missing")
	if err != nil || ok {
		t.Fatalf("Poll(missing): ok=%v err=%v, want ok=false err=nil", ok, err)
	}
}

func TestList_orderedByLastActivityDesc(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	mustInsert(t, st, "a", "csid-a")
	bumpClock(t, st, 10)
	mustInsert(t, st, "b", "csid-b")

	got, err := st.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 || got[0].ExternalID != "b" || got[1].ExternalID != "a" {
		t.Fatalf("List order = %v, want [b a]", externalIDs(got))
	}
}

// test helpers
func mustInsert(t *testing.T, st *Store, externalID, csid string) {
	t.Helper()
	if err := st.Insert(context.Background(), Session{ExternalID: externalID, ClaudeSessionID: csid, State: Starting}); err != nil {
		t.Fatalf("Insert %s: %v", externalID, err)
	}
}

func externalIDs(ss []Session) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = s.ExternalID
	}
	return out
}

func bumpClock(t *testing.T, st *Store, secs int) {
	t.Helper()
	f, ok := st.clock.(*clock.Fake)
	if !ok {
		t.Fatal("test store must use *clock.Fake")
	}
	f.Advance(time.Duration(secs) * time.Second)
}
