package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/ccpool/internal/clock"
	"github.com/phillipgreenii/ccpool/internal/store"
)

func openTestStore(t *testing.T) (*store.Store, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "store.db")
	st, err := store.Open(dbPath, &clock.Fake{T: time.Unix(2000, 0).UTC()})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st, dbPath
}

const startPayload = `{"session_id":"u-x","transcript_path":"/p/u-x.jsonl","cwd":"/tmp/x","hook_event_name":"SessionStart","source":"startup"}`
const stopPayload = `{"session_id":"u-x","transcript_path":"/p/u-x.jsonl","hook_event_name":"Stop","stop_hook_active":false}`

func TestHook_start_upsertsThenReady(t *testing.T) {
	st, _ := openTestStore(t)
	ctx := context.Background()
	// No pre-existing row; only CCPOOL_NAME in env (the race case, spec §9/§18).
	if err := handleHook("start", strings.NewReader(startPayload), st, "alpha"); err != nil {
		t.Fatalf("handleHook start: %v", err)
	}
	got, ok, _ := st.GetByName(ctx, "alpha")
	if !ok {
		t.Fatal("row not upserted")
	}
	if got.State != store.Ready {
		t.Errorf("state = %q, want ready", got.State)
	}
	if got.UUID != "u-x" || got.TranscriptPath != "/p/u-x.jsonl" {
		t.Errorf("reconcile failed: %+v", got)
	}
}

func TestHook_stop_resolvesByUUID_setsDone(t *testing.T) {
	st, _ := openTestStore(t)
	ctx := context.Background()
	if err := st.Insert(ctx, store.Session{Name: "alpha", UUID: "u-x", State: store.Working}); err != nil {
		t.Fatal(err)
	}
	// No CCPOOL_NAME — must resolve by session_id.
	if err := handleHook("stop", strings.NewReader(stopPayload), st, ""); err != nil {
		t.Fatalf("handleHook stop: %v", err)
	}
	got, _, _ := st.GetByName(ctx, "alpha")
	if got.State != store.Done {
		t.Errorf("state = %q, want done", got.State)
	}
}

func TestHook_unresolvable_isNoErrorNoRow(t *testing.T) {
	st, _ := openTestStore(t)
	// session_id matches nothing and no CCPOOL_NAME → log + succeed (exit 0).
	if err := handleHook("stop", strings.NewReader(stopPayload), st, ""); err != nil {
		t.Fatalf("unresolvable hook returned error, want nil: %v", err)
	}
	list, _ := st.List(context.Background())
	if len(list) != 0 {
		t.Errorf("rows = %d, want 0", len(list))
	}
}
