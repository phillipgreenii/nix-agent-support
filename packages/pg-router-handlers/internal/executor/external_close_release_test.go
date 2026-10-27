package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-router-handlers/internal/beads"
	"github.com/phillipgreenii/pg-router-handlers/internal/ccpool"
	"github.com/phillipgreenii/pg-router-handlers/internal/dtest"
	"github.com/phillipgreenii/pg-router-handlers/internal/item"
	"github.com/phillipgreenii/pg-router-handlers/internal/report"
)

// External close (ccpool closed the session: idle_ttl, cap_eviction, operator)
// releases the claim of a session that is gone. The claim is usually under
// another actor's identity, so bd refuses an unguarded release (pg2-qyc3h); the
// release is therefore guarded by the observed holder, and a release that did
// not land is surfaced instead of reported as an unclaim (pg2-ybkav).

// externalCloseRun drives waitDone's external-close branch (close reason
// idle_ttl) for a worker whose bead is held by dead-actor, with bd as given.
func externalCloseRun(t *testing.T, wrap func(*dtest.ScriptBD) beads.Runner) (*ccpoolRun, *dtest.ScriptBD, error, string) {
	t.Helper()
	cfg := fastCfg()
	script := &dtest.ScriptBD{
		StatusSeq:   map[string][]string{"zr-w": {"in_progress", "in_progress", "in_progress"}},
		AssigneeSeq: map[string][]string{"zr-w": {"dead-actor"}},
	}
	row := ccpool.Session{ExternalID: "pg-router-worker-zr-w", Live: false, State: ccpool.StateWorking, CloseReason: "idle_ttl"}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{row}, {row}, {row}}}
	logPath := filepath.Join(t.TempDir(), "events.jsonl")
	e := newExecWithLog(cc, script, cfg, logPath)
	e.deps.BD = wrap(script)
	d := DispatchContext{Role: workerRole(cfg), Item: item.Item{ID: "zr-w"}}
	return e, script, e.waitDone(context.Background(), nil, d, "pg-router-worker-zr-w"), logPath
}

// A landed release is the guarded update, and the dispatch reports the unclaim.
func TestWaitDone_externalCloseReleaseIsGuardedByTheObservedHolder(t *testing.T) {
	e, script, err, _ := externalCloseRun(t, func(s *dtest.ScriptBD) beads.Runner { return s })
	if !errors.Is(err, ErrExternallyClosed) {
		t.Fatalf("want an external close, got %v", err)
	}
	if !dtest.HasUpdate(script, "update zr-w --status=open --assignee= --if-assignee dead-actor --if-status in_progress") {
		t.Errorf("the release must be guarded by the observed holder; updates=%v", script.Updates)
	}
	if v := verbsOf(e.waitFailureResult(workerRole(fastCfg()).CCPool, "zr-w", err)); len(v) != 1 || v[0] != report.Unclaimed {
		t.Errorf("a landed release is reported as the unclaim verb; got %v", v)
	}
}

// A release bd refuses with a non-guard error is not reported as an unclaim:
// the dispatch result carries no Unclaimed verb, the failure is logged, and one
// external_close_release_failed eventlog record names the bead and bd's refusal.
func TestWaitDone_externalCloseReleaseFailureIsSurfaced(t *testing.T) {
	e, _, err, logPath := externalCloseRun(t, func(s *dtest.ScriptBD) beads.Runner { return refuseUpdateBD{s} })
	if !errors.Is(err, ErrExternallyClosed) {
		t.Fatalf("want an external close, got %v", err)
	}
	var ec *externallyClosed
	if !errors.As(err, &ec) || ec.releaseErr == nil {
		t.Fatalf("the failed release must ride on the error; got %+v", ec)
	}
	res := e.waitFailureResult(workerRole(fastCfg()).CCPool, "zr-w", err)
	if len(res.Actions) != 0 {
		t.Errorf("a release that did not land must not report a verb; got %v", verbsOf(res))
	}
	raw, rerr := os.ReadFile(logPath)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if n := strings.Count(string(raw), `"kind":"`+externalReleaseKind+`"`); n != 1 {
		t.Errorf("want exactly one %s record, got %d in:\n%s", externalReleaseKind, n, raw)
	}
	if !strings.Contains(string(raw), `"bead":"zr-w"`) || !strings.Contains(string(raw), "cannot reassign") {
		t.Errorf("the record must name the bead and carry bd's refusal; log:\n%s", raw)
	}
}
