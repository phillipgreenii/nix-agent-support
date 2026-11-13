package session

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/phillipgreenii/ccpool/internal/store"
	"github.com/phillipgreenii/ccpool/internal/tmux"
)

// Batch-read support for the test doubles. A double with a live map answers
// ListPanes from it, so capacity and the reap scan exercise the batched path;
// a double without one errors, which makes the scan fall back to per-row
// HasSession (the documented degradation).

var errNoBatch = errors.New("test double has no batched read")

func panesFromLive(live map[string]bool) map[string]tmux.PaneInfo {
	out := make(map[string]tmux.PaneInfo, len(live))
	for name, ok := range live {
		if ok {
			out[name] = tmux.PaneInfo{Live: true}
		}
	}
	return out
}

func (f *fakeTmux) ListPanes() (map[string]tmux.PaneInfo, error) { return panesFromLive(f.live), nil }

func (r *reapTmux) ListPanes() (map[string]tmux.PaneInfo, error) { return panesFromLive(r.live), nil }

// ListPanes fires every pending probe hook first: the flip mutations model "the
// row changes after List and before the locked re-read", and the batch read is
// where the scan now looks at tmux.
func (f *flipTmux) ListPanes() (map[string]tmux.PaneInfo, error) {
	for name, fn := range f.onProbe {
		delete(f.onProbe, name)
		fn()
	}
	return f.reapTmux.ListPanes()
}

func (c *closeTmux) ListPanes() (map[string]tmux.PaneInfo, error)  { return nil, errNoBatch }
func (s *sendTmux) ListPanes() (map[string]tmux.PaneInfo, error)   { return nil, errNoBatch }
func (c *envCapTmux) ListPanes() (map[string]tmux.PaneInfo, error) { return nil, errNoBatch }

// snapshotTmux reports every session dead in the batch snapshot while its fresh
// per-session HasSession answers come from hasSeq (the n-th call, 1-based).
type snapshotTmux struct {
	*reapTmux
	hasSeq   func(n int) bool
	hasCalls int
	batches  int
}

func (s *snapshotTmux) ListPanes() (map[string]tmux.PaneInfo, error) {
	s.batches++
	return map[string]tmux.PaneInfo{}, nil
}

func (s *snapshotTmux) HasSession(string) bool {
	s.hasCalls++
	return s.hasSeq(s.hasCalls)
}

func snapshotFixture(t *testing.T, now time.Time, hasSeq func(int) bool) (*Service, *snapshotTmux, *store.Store) {
	t.Helper()
	clk := &mutClock{t: now}
	st, err := store.Open(":memory:", clk)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Insert(context.Background(), store.Session{
		ExternalID: "stale", ClaudeSessionID: "csid-stale", TranscriptPath: "/p/stale.jsonl",
		State: store.NeedsInput, TmuxSession: "cc-stale", LastActivityAt: now.Unix() - sec(deadTTL) - 60,
	}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	tm := &snapshotTmux{reapTmux: &reapTmux{live: map[string]bool{}, closed: map[string]bool{}}, hasSeq: hasSeq}
	s := New(Deps{Tmux: tm, Trust: &fakeTrust{}, Store: st, Prefix: "cc-", Exister: fakeExister{ok: true}, Now: clk.Now})
	return s, tm, st
}

// The reap scan reads liveness with one batched call, but the two re-checks made
// under the per-row lock MUST ask tmux afresh: a snapshot that says dead must not
// decide a close for a session that has come back since.

// The first fresh check (the dead-row classification) sees the session dead and
// the second (the dead-needs_input backstop) sees it live again: the row must
// NOT be closed, which only holds if the backstop did not reuse the snapshot.
func TestReap_backstopRecheckIsFreshNotTheBatchSnapshot(t *testing.T) {
	now := time.Unix(10_000_000, 0)
	s, tm, st := snapshotFixture(t, now, func(n int) bool { return n >= 2 })
	if err := s.Reap(context.Background(), 6, time.Hour, WithDeadNeedsInputTTL(deadTTL)); err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if tm.batches != 1 {
		t.Errorf("batched reads = %d, want exactly 1 for the scan", tm.batches)
	}
	if tm.hasCalls < 2 {
		t.Errorf("fresh HasSession calls = %d, want at least 2 (classification re-check and backstop re-check)", tm.hasCalls)
	}
	r, ok, err := st.GetByExternalID(context.Background(), "stale")
	if err != nil || !ok {
		t.Fatalf("GetByExternalID: ok=%v err=%v", ok, err)
	}
	if r.CloseReason != "" {
		t.Errorf("close_reason = %q; the backstop closed a row whose fresh check said live", r.CloseReason)
	}
}

// The dead-row classification re-check sees the session live: the row joins the
// live set instead of being treated as dead, so it is never closed either.
func TestReap_classificationRecheckIsFreshNotTheBatchSnapshot(t *testing.T) {
	now := time.Unix(10_000_000, 0)
	s, tm, st := snapshotFixture(t, now, func(int) bool { return true })
	if err := s.Reap(context.Background(), 6, time.Hour, WithDeadNeedsInputTTL(deadTTL)); err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if tm.hasCalls < 1 {
		t.Errorf("fresh HasSession calls = %d, want at least 1", tm.hasCalls)
	}
	r, _, _ := st.GetByExternalID(context.Background(), "stale")
	if r.CloseReason != "" {
		t.Errorf("close_reason = %q; a live row was closed off the stale snapshot", r.CloseReason)
	}
}

// Capacity counts from one batched read and no per-row call.
func TestCapacity_usesOneBatchedRead(t *testing.T) {
	now := time.Unix(10_000_000, 0)
	s, tm, _ := snapshotFixture(t, now, func(int) bool { return false })
	if _, err := s.Capacity(context.Background(), 6); err != nil {
		t.Fatalf("Capacity: %v", err)
	}
	if tm.batches != 1 || tm.hasCalls != 0 {
		t.Errorf("batches=%d hasCalls=%d, want 1 and 0", tm.batches, tm.hasCalls)
	}
}
