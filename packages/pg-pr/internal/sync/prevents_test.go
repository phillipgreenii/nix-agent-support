package sync

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
)

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
