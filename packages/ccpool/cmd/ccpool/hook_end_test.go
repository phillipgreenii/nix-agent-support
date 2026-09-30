package main

import (
	"context"
	"strings"
	"testing"

	"github.com/phillipgreenii/ccpool/internal/store"
)

const (
	endOther = `{"session_id":"csid-x","hook_event_name":"SessionEnd","reason":"other"}`
	endClear = `{"session_id":"csid-x","hook_event_name":"SessionEnd","reason":"clear"}`
	endResum = `{"session_id":"csid-x","hook_event_name":"SessionEnd","reason":"resume"}`
)

func endFixture(t *testing.T) *store.Store {
	t.Helper()
	st, _ := openTestStore(t)
	ctx := context.Background()
	if err := st.Insert(ctx, store.Session{ExternalID: "ext", ClaudeSessionID: "csid-x", State: store.Idle}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.OpenRun(ctx, "ext"); err != nil {
		t.Fatal(err)
	}
	return st
}

func onlyRun(t *testing.T, st *store.Store) store.Run {
	t.Helper()
	runs, err := st.RunsFor(context.Background(), "ext")
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs=%v err=%v", runs, err)
	}
	return runs[0]
}

func TestHookEnd_naturalExitRecordsExitedFromHook(t *testing.T) {
	st := endFixture(t)
	if err := handleHook("end", strings.NewReader(endOther), st, ""); err != nil {
		t.Fatal(err)
	}
	r := onlyRun(t, st)
	if r.EndReason != "exited" || r.EndSource != store.RunEndHook || r.EndedAt != 2000 {
		t.Errorf("run = %+v, want exited/hook/2000 (hook's own timestamp)", r)
	}
}

func TestHookEnd_clearAndResumeDoNotEndRun(t *testing.T) {
	for _, payload := range []string{endClear, endResum} {
		st := endFixture(t)
		if err := handleHook("end", strings.NewReader(payload), st, ""); err != nil {
			t.Fatal(err)
		}
		if r := onlyRun(t, st); !r.Open() || r.EndReason == "exited" {
			t.Errorf("payload %s ended the run: %+v", payload, r)
		}
	}
}

func TestHookEnd_keepsPendingCloseReason(t *testing.T) {
	st := endFixture(t)
	ctx := context.Background()
	_ = st.SetRunPendingReason(ctx, "ext", "operator")
	if err := handleHook("end", strings.NewReader(endOther), st, ""); err != nil {
		t.Fatal(err)
	}
	r := onlyRun(t, st)
	if r.EndReason != "operator" || r.EndSource != store.RunEndHook {
		t.Errorf("run = %+v, want pending reason operator kept, source hook", r)
	}
}

// A stale sessions.close_reason from an EARLIER run must not turn this run's
// natural exit into a ccpool-initiated close: the pending reason lives on the run.
func TestHookEnd_staleSessionCloseReasonStillExited(t *testing.T) {
	st := endFixture(t)
	if err := st.SetCloseReason(context.Background(), "ext", "idle_ttl"); err != nil {
		t.Fatal(err)
	}
	if err := handleHook("end", strings.NewReader(endOther), st, ""); err != nil {
		t.Fatal(err)
	}
	if r := onlyRun(t, st); r.EndReason != "exited" {
		t.Errorf("run = %+v, want exited despite stale close_reason", r)
	}
}

func TestHookEnd_noOpenRunIsNoOp(t *testing.T) {
	st := endFixture(t)
	_ = handleHook("end", strings.NewReader(endOther), st, "")
	if err := handleHook("end", strings.NewReader(endOther), st, ""); err != nil {
		t.Fatal(err)
	}
	if r := onlyRun(t, st); r.EndedAt != 2000 {
		t.Errorf("second hook altered the run: %+v", r)
	}
}
