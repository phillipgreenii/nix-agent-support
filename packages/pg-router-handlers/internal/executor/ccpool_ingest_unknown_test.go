package executor

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-router-handlers/internal/ccpool"
	"github.com/phillipgreenii/pg-router-handlers/internal/dtest"
	"github.com/phillipgreenii/pg-router-handlers/internal/item"
)

// Regression guards for the "prompt not ingested" false negative: the recorder
// lags or is unavailable under load, ccpool reply --confirm-ingest cannot tell,
// and the handler used to close the working session and unclaim the bead. Only a
// definitive not-ingested result (the recorder answered) may do that; an unknown
// one leaves the session and the claim alone. Driven against a REAL repository
// (the harness of ccpool_abandoned_worktree_realgit_test.go), so the worktree
// verdicts come from git itself.

func (h *realGitHarness) dispatchOnce(cc *dtest.FakeCC, bd *dtest.ScriptBD) error {
	h.t.Helper()
	h.run.deps.CC = cc
	h.run.deps.BD = bd
	_, err := h.run.run(h.t.Context(), DispatchContext{Role: h.role, Item: item.Item{ID: realGitBead}})
	return err
}

// captureSlog routes slog to a buffer for the test and returns it.
func captureSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func liveWorker() [][]ccpool.Session {
	return [][]ccpool.Session{{{ExternalID: realGitExt, Live: true, State: ccpool.StateWorking}}}
}

func TestDispatch_ingestUnknown_leavesSessionAndClaimIntact(t *testing.T) {
	h := newRealGitHarness(t)
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{realGitBead: {"in_progress", "closed"}}}
	cc := &dtest.FakeCC{SendErr: fmt.Errorf("%w: %s", ccpool.ErrIngestUnknown, realGitExt), ListSeq: liveWorker()}
	if err := h.dispatchOnce(cc, bd); err != nil {
		t.Fatalf("an unknown ingestion must not fail the dispatch: %v", err)
	}
	if len(cc.Closed) != 0 {
		t.Errorf("the session must not be closed on an unknown ingestion; closed=%v", cc.Closed)
	}
	// Every release goes through beads.UnclaimHeld, whose update line starts
	// "update <id> --status=open --assignee=" and carries the guard flags; match by
	// substring so a guarded release is caught (an exact-line match is vacuous).
	for _, u := range bd.Updates {
		if strings.Contains(u, "--status=open") || strings.Contains(u, "--assignee=") {
			t.Errorf("the bead must stay claimed on an unknown ingestion; update=%q", u)
		}
	}
	if len(cc.Sent) != 1 {
		t.Errorf("the nudge must be sent exactly once; sent=%v", cc.Sent)
	}
}

func TestDispatch_definitiveNotIngested_stillClosesAndUnclaims(t *testing.T) {
	h := newRealGitHarness(t)
	bd := &dtest.ScriptBD{}
	cc := &dtest.FakeCC{SendErr: fmt.Errorf("%w: %s", ccpool.ErrPromptNotIngested, realGitExt), ListSeq: liveWorker()}
	err := h.dispatchOnce(cc, bd)
	if err == nil || !ccpool.IsNotIngested(err) {
		t.Fatalf("a definitive not-ingested must fail the dispatch with it; got %v", err)
	}
	if len(cc.Closed) != 1 || cc.Closed[0] != realGitExt {
		t.Errorf("the session must be closed; closed=%v", cc.Closed)
	}
	if !dtest.HasUpdate(bd, "update "+realGitBead+" --status=open --assignee=") {
		t.Errorf("the bead must be unclaimed; updates=%v", bd.Updates)
	}
}

func TestDispatch_notIngestedCloseError_loggedAndRetriedOnce(t *testing.T) {
	logs := captureSlog(t)
	h := newRealGitHarness(t)
	bd := &dtest.ScriptBD{}
	closeErr := errors.New("recorder unavailable")
	cc := &dtest.FakeCC{SendErr: ccpool.ErrPromptNotIngested, CloseErr: closeErr, ListSeq: liveWorker()}
	if err := h.dispatchOnce(cc, bd); err == nil {
		t.Fatal("a definitive not-ingested must still fail the dispatch")
	}
	if len(cc.Closed) != 2 {
		t.Errorf("a failed close must be retried exactly once; close calls=%v", cc.Closed)
	}
	out := logs.String()
	for _, want := range []string{"retrying once", "failed after retry", "recorder unavailable", realGitExt} {
		if !strings.Contains(out, want) {
			t.Errorf("log must mention %q; got:\n%s", want, out)
		}
	}
	// The bead is still handed back even though the close failed.
	if !dtest.HasUpdate(bd, "update "+realGitBead+" --status=open --assignee=") {
		t.Errorf("the bead must be unclaimed; updates=%v", bd.Updates)
	}
}
