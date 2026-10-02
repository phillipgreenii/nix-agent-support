package session

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/phillipgreenii/ccpool/internal/store"
)

// tokenCall is one recordSessionOutputTokens call.
type tokenCall struct {
	tokens int64
	attrs  []attribute.KeyValue
}

func spyTokens(t *testing.T) *[]tokenCall {
	t.Helper()
	orig := recordSessionOutputTokens
	t.Cleanup(func() { recordSessionOutputTokens = orig })
	var calls []tokenCall
	recordSessionOutputTokens = func(n int64, attrs []attribute.KeyValue) {
		calls = append(calls, tokenCall{n, attrs})
	}
	return &calls
}

// tokenSvc is a reaper Service whose transcript reports total output tokens
// (and a read error when err is non-nil); PoolPath names the pool attribute.
func tokenSvc(st Store, tm Tmux, total int64, err error) *Service {
	return New(Deps{
		Tmux: tm, Trust: &fakeTrust{}, Store: st, Prefix: "cc-", Exister: fakeExister{ok: true},
		Transcript: fakeTranscript{tokens: total, tokensErr: err},
		PoolPath:   "/pools/pg-router-ccpool-review",
		Now:        func() time.Time { return time.Unix(10_000, 0) },
	})
}

func endRunByHook(t *testing.T, st *store.Store, endedAt int64) {
	t.Helper()
	ctx := context.Background()
	id, err := st.OpenRun(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.FinalizeRun(ctx, id, "exited", store.RunEndHook, endedAt); err != nil {
		t.Fatal(err)
	}
}

func sweep(t *testing.T, svc *Service) {
	t.Helper()
	if err := svc.Reap(context.Background(), 6, time.Hour); err != nil {
		t.Fatal(err)
	}
}

func idleTmux() *reapTmux { return &reapTmux{live: map[string]bool{}, closed: map[string]bool{}} }

// A run the hook ended is attributed its transcript total by the next sweep,
// with the pool attribute, exactly once.
func TestRunTokens_sweepAttributesHookEndedRunOnce(t *testing.T) {
	calls := spyTokens(t)
	st := newMemStore(t)
	seedIdleRow(t, st, 50)
	endRunByHook(t, st, 200)
	svc := tokenSvc(st, idleTmux(), 500, nil)
	sweep(t, svc)
	if len(*calls) != 1 || (*calls)[0].tokens != 500 {
		t.Fatalf("calls = %+v, want exactly one with 500 tokens", *calls)
	}
	set := attribute.NewSet((*calls)[0].attrs...)
	if v, ok := set.Value("pool"); !ok || v.AsString() != "pg-router-ccpool-review" {
		t.Errorf("pool attr = %q ok=%v, want the pool basename", v.AsString(), ok)
	}
	sweep(t, svc)
	if len(*calls) != 1 {
		t.Errorf("second sweep re-attributed tokens: %+v", *calls)
	}
}

// A resumed session appends to the same transcript: the second run is
// attributed only the tokens produced since the first run was snapshotted.
func TestRunTokens_secondRunGetsOnlyTheDelta(t *testing.T) {
	calls := spyTokens(t)
	st := newMemStore(t)
	seedIdleRow(t, st, 50)
	endRunByHook(t, st, 200)
	sweep(t, tokenSvc(st, idleTmux(), 500, nil))
	endRunByHook(t, st, 300)
	sweep(t, tokenSvc(st, idleTmux(), 800, nil))
	if len(*calls) != 2 || (*calls)[0].tokens != 500 || (*calls)[1].tokens != 300 {
		t.Fatalf("calls = %+v, want 500 then 300", *calls)
	}
}

// An unreadable transcript attributes nothing, but the run's lifecycle metrics
// still emit and a LATER run's delta catches the tokens up (the snapshot did
// not advance).
func TestRunTokens_transcriptErrorLosesNothingForLaterRun(t *testing.T) {
	calls := spyTokens(t)
	closed := spyClosed(t)
	st := newMemStore(t)
	seedIdleRow(t, st, 50)
	endRunByHook(t, st, 200)
	sweep(t, tokenSvc(st, idleTmux(), 0, errors.New("read failed")))
	if len(*calls) != 0 {
		t.Fatalf("attributed tokens despite a read error: %+v", *calls)
	}
	if len(*closed) != 1 {
		t.Fatalf("lifecycle metrics not emitted on a token read error: %+v", *closed)
	}
	endRunByHook(t, st, 300)
	sweep(t, tokenSvc(st, idleTmux(), 900, nil))
	if len(*calls) != 1 || (*calls)[0].tokens != 900 {
		t.Fatalf("calls = %+v, want one catch-up of 900", *calls)
	}
}

// No Transcript dep, an empty transcript path, or a zero delta record nothing.
func TestRunTokens_nothingToRecordRecordsNothing(t *testing.T) {
	calls := spyTokens(t)
	st := newMemStore(t)
	seedIdleRow(t, st, 50)
	endRunByHook(t, st, 200)
	sweep(t, reap0Service(st, idleTmux(), time.Unix(10_000, 0), true)) // nil Transcript
	if len(*calls) != 0 {
		t.Errorf("nil Transcript recorded %+v", *calls)
	}

	st2 := newMemStore(t)
	if err := st2.Insert(context.Background(), store.Session{
		ExternalID: "a", ClaudeSessionID: "csid-a", State: store.Idle, TmuxSession: "cc-a", LastActivityAt: 50,
	}); err != nil {
		t.Fatal(err)
	}
	endRunByHook(t, st2, 200)
	sweep(t, tokenSvc(st2, idleTmux(), 500, nil)) // row has no transcript path
	if len(*calls) != 0 {
		t.Errorf("empty transcript path recorded %+v", *calls)
	}

	st3 := newMemStore(t)
	seedIdleRow(t, st3, 50)
	endRunByHook(t, st3, 200)
	sweep(t, tokenSvc(st3, idleTmux(), 0, nil)) // transcript has no output yet
	if len(*calls) != 0 {
		t.Errorf("zero delta recorded %+v", *calls)
	}
}

// A purge close reads the transcript and attributes the tokens BEFORE the row
// (and its metadata) is deleted, with the labels resolved first.
func TestRunTokens_purgeCloseAttributesBeforeDelete(t *testing.T) {
	calls := spyTokens(t)
	ctx := context.Background()
	st := newMemStore(t)
	seedIdleRow(t, st, 50)
	_, _ = st.OpenRun(ctx, "a")
	svc := New(Deps{
		Tmux: &closeTmux{live: false}, Trust: &fakeTrust{}, Store: st, Prefix: "cc-", Exister: fakeExister{ok: true},
		Transcript: fakeTranscript{tokens: 321},
		PoolPath:   "/pools/p1",
		Now:        func() time.Time { return time.Unix(300, 0) },
	})
	if err := svc.CloseReason(ctx, "a", "operator", true); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 || (*calls)[0].tokens != 321 {
		t.Fatalf("calls = %+v, want one with 321 tokens", *calls)
	}
	if _, ok, _ := st.GetByExternalID(ctx, "a"); ok {
		t.Error("row not deleted")
	}
}
