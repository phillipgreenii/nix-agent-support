package session

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/phillipgreenii/ccpool/internal/store"
)

// TestEnsure_finalFactReadFailureLeavesNothingBehind: when the resumability read
// still fails after its retries, Ensure returns the error before any tmux
// session is created and without changing the row.
func TestEnsure_finalFactReadFailureLeavesNothingBehind(t *testing.T) {
	ctx := context.Background()
	st := newMemStore(t)
	if err := st.Insert(ctx, store.Session{ExternalID: "ext-alpha", ClaudeSessionID: "csid-1", CWD: "/tmp/proj", TranscriptPath: "/p/ext-alpha.jsonl", State: store.Idle, TmuxSession: "cc-ext-alpha"}); err != nil {
		t.Fatalf("seed row: %v", err)
	}
	before, _, _ := st.GetByExternalID(ctx, "ext-alpha")
	ft := &fakeTmux{live: map[string]bool{}}
	offline := errors.New("the recorder is not available")
	s := New(Deps{
		Tmux: ft, Trust: &fakeTrust{}, Store: st, Exister: checkedExister{err: offline},
		Wait:   waitFunc(nil),
		Socket: "ccpool", Prefix: "cc-", PluginDir: "/plugin", ClaudeBin: "claude",
		NewUUID: func() string { return "must-not-be-used" },
		Now:     func() time.Time { return time.Unix(100, 0) },
	})
	_, err := s.Ensure(ctx, "ext-alpha", "/tmp/proj", "", EnsureOpts{})
	if !errors.Is(err, offline) {
		t.Fatalf("Ensure err = %v; want the fact-read error", err)
	}
	if len(ft.newCalls) != 0 {
		t.Errorf("tmux sessions created = %d; want none", len(ft.newCalls))
	}
	after, ok, _ := st.GetByExternalID(ctx, "ext-alpha")
	if !ok || after != before {
		t.Errorf("row changed: before %+v, after %+v, ok %v", before, after, ok)
	}
}
