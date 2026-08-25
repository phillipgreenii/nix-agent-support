package sync

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite" // register the "sqlite" driver for breakOutbox

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
)

// breakOutbox drops the outbox table via a SECOND connection to the store file,
// so the engine's own EnqueueEvent (an INSERT into outbox) fails on its next
// statement. SQLite reports the cross-connection schema change to the engine
// handle, which then fails to re-prepare against the now-missing table. Used to
// fault-inject a failure of the enqueue (the "second step") so the atomicity of
// UpsertPR + EnqueueEvent can be exercised.
func breakOutbox(t *testing.T, path string) {
	t.Helper()
	raw, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open raw sqlite: %v", err)
	}
	defer func() { _ = raw.Close() }()
	if _, err := raw.Exec("DROP TABLE outbox"); err != nil {
		t.Fatalf("drop outbox: %v", err)
	}
}

// drainOneEvent runs the outbox once and returns the (type, payload) of the
// last dispatched event (empty type if none).
func drainOneEvent(t *testing.T, db *store.DB) (string, store.PRPayload) {
	t.Helper()
	var typ string
	var payload store.PRPayload
	if err := db.RunOutbox(context.Background(), func(_ context.Context, ev store.Event) error {
		typ = ev.Type
		return json.Unmarshal(ev.Payload, &payload)
	}); err != nil {
		t.Fatalf("RunOutbox: %v", err)
	}
	return typ, payload
}

// TestEmitPREvent_UpsertsAuthoritativeRow proves emitPREvent itself writes the
// authoritative pull_request row (not just the event) — the state mutation and
// the pr.* event are owned by the one helper so they commit together. (#17)
func TestEmitPREvent_UpsertsAuthoritativeRow(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	e := &Engine{deps: Deps{Store: db, Now: func() time.Time { return time.Unix(0, 0).UTC() }}}
	pr := api.PR{
		Number: 9, Title: "t", Body: "the description", Branch: "b", Base: "main",
		Author: "me", URL: "u", State: "open",
	}
	if err := e.emitPREvent(ctx, store.EventPROpened, "o/r", pr, "mine"); err != nil {
		t.Fatalf("emit: %v", err)
	}

	got, err := db.GetPR(ctx, "o/r", 9)
	if err != nil {
		t.Fatalf("GetPR: %v", err)
	}
	if got == nil {
		t.Fatal("emitPREvent did not write the pull_request row")
	}
	if got.State != "open" || got.Ownership != "mine" || got.Author != "me" {
		t.Fatalf("row = %+v; want state=open ownership=mine author=me", got)
	}
	// pg2-1o1dp: prToStoreRow must carry api.PR.Body through to the store row
	// (the ingest-side half of the fix; storeRowToAPIPR/Assemble is the
	// read-side half, covered in cmd/pg-pr and internal/prview's own tests).
	if got.Body != "the description" {
		t.Fatalf("row.Body = %q, want %q", got.Body, "the description")
	}
	if typ, p := drainOneEvent(t, db); typ != store.EventPROpened || p.Number != 9 {
		t.Fatalf("event = %s %+v; want pr.opened #9", typ, p)
	}
}

// TestEmitPREvent_RollsBackRowWhenEnqueueFails proves the row upsert and the
// event enqueue are one transaction: if the enqueue fails, the row is NOT
// persisted, so the next observation re-emits it (no lost event). (#17)
func TestEmitPREvent_RollsBackRowWhenEnqueueFails(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "s.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	breakOutbox(t, path)

	e := &Engine{deps: Deps{Store: db, Now: func() time.Time { return time.Unix(0, 0).UTC() }}}
	pr := api.PR{Number: 9, Branch: "b", Base: "main", Author: "me", URL: "u", State: "open"}
	if err := e.emitPREvent(ctx, store.EventPROpened, "o/r", pr, "mine"); err == nil {
		t.Fatal("expected error when enqueue fails, got nil")
	}
	got, err := db.GetPR(ctx, "o/r", 9)
	if err != nil {
		t.Fatalf("GetPR: %v", err)
	}
	if got != nil {
		t.Fatalf("row persisted despite enqueue failure: %+v (upsert+enqueue must be atomic)", got)
	}
}

// TestEmitPRClosed_MarksRowClosed proves emitPRClosed itself marks the stored
// row closed (the close-detection state mutation) in addition to enqueuing
// pr.closed — both owned by the one helper so they commit together. (#17)
func TestEmitPRClosed_MarksRowClosed(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	row := store.PullRequest{
		Repo: "o/r", Number: 7, Ownership: "team", Author: "them",
		State: "open", Branch: "b", Base: "main", URL: "u",
	}
	if _, err := db.UpsertPR(ctx, row); err != nil {
		t.Fatalf("seed UpsertPR: %v", err)
	}

	e := &Engine{deps: Deps{Store: db, Now: func() time.Time { return time.Unix(0, 0).UTC() }}}
	if err := e.emitPRClosed(ctx, row, false); err != nil {
		t.Fatalf("emitPRClosed: %v", err)
	}

	got, err := db.GetPR(ctx, "o/r", 7)
	if err != nil {
		t.Fatalf("GetPR: %v", err)
	}
	if got == nil || got.State != "closed" {
		t.Fatalf("row = %+v; want state=closed", got)
	}
	if typ, p := drainOneEvent(t, db); typ != store.EventPRClosed || p.Number != 7 {
		t.Fatalf("event = %s %+v; want pr.closed #7", typ, p)
	}
}

// TestEmitPRClosed_RollsBackCloseWhenEnqueueFails is the load-bearing case from
// the bug: a crash/failure of the enqueue (second step) must NOT leave the row
// marked closed, because ListOpenPRs would then never re-detect it and the
// pr.closed event would be lost permanently (this path does not self-heal). (#17)
func TestEmitPRClosed_RollsBackCloseWhenEnqueueFails(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "s.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	row := store.PullRequest{
		Repo: "o/r", Number: 7, Ownership: "team", Author: "them",
		State: "open", Branch: "b", Base: "main", URL: "u",
	}
	if _, err := db.UpsertPR(ctx, row); err != nil {
		t.Fatalf("seed UpsertPR: %v", err)
	}
	breakOutbox(t, path)

	e := &Engine{deps: Deps{Store: db, Now: func() time.Time { return time.Unix(0, 0).UTC() }}}
	if err := e.emitPRClosed(ctx, row, false); err == nil {
		t.Fatal("expected error when enqueue fails, got nil")
	}
	got, err := db.GetPR(ctx, "o/r", 7)
	if err != nil {
		t.Fatalf("GetPR: %v", err)
	}
	if got == nil {
		t.Fatal("row vanished")
	}
	if got.State != "open" {
		t.Fatalf("row state = %q; want \"open\" (close must roll back when enqueue fails)", got.State)
	}
}

func TestEmitPREvent_EnqueuesPayload(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	e := &Engine{deps: Deps{
		Store: db,
		Now:   func() time.Time { return time.Unix(0, 0).UTC() },
	}}
	pr := api.PR{Number: 9, Title: "t", Branch: "b", Base: "main", Author: "me", URL: "u", Draft: true, State: "open"}
	if err := e.emitPREvent(context.Background(), store.EventPROpened, "o/r", pr, "mine"); err != nil {
		t.Fatalf("emit: %v", err)
	}
	var seen store.PRPayload
	var typ string
	_ = db.RunOutbox(context.Background(), func(_ context.Context, ev store.Event) error {
		typ = ev.Type
		return json.Unmarshal(ev.Payload, &seen)
	})
	if typ != store.EventPROpened || seen.Number != 9 || seen.Ownership != "mine" || !seen.Draft {
		t.Fatalf("bad event: type=%s payload=%+v", typ, seen)
	}
}

func TestEmitPREvent_NilStoreNoop(t *testing.T) {
	e := &Engine{deps: Deps{Now: func() time.Time { return time.Unix(0, 0).UTC() }}}
	if err := e.emitPREvent(context.Background(), store.EventPROpened, "o/r", api.PR{Number: 1}, "mine"); err != nil {
		t.Fatalf("nil-store emit should be a no-op, got %v", err)
	}
}
