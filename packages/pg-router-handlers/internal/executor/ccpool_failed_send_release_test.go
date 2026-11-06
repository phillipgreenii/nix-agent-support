package executor

import (
	"errors"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-router-handlers/internal/beads"
	"github.com/phillipgreenii/pg-router-handlers/internal/ccpool"
	"github.com/phillipgreenii/pg-router-handlers/internal/config"
	"github.com/phillipgreenii/pg-router-handlers/internal/dtest"
	"github.com/phillipgreenii/pg-router-handlers/internal/item"
	"github.com/phillipgreenii/pg-router-handlers/internal/report"
	"github.com/phillipgreenii/pg-router-handlers/internal/roles"
)

// A failed nudge send releases the bead's claim. The session may already have
// claimed the bead under its own actor (a send can report "not ingested" while
// the model is working), and bd refuses an unguarded release of another
// actor's in_progress claim, so the release is guarded by the observed holder
// and a release that did not land is logged and not reported as an unclaim.

const heldByActor = "pg-router-worker-zr-w"

func heldBD() *dtest.ScriptBD {
	return &dtest.ScriptBD{
		StatusSeq:   map[string][]string{"zr-w": {"in_progress"}},
		AssigneeSeq: map[string][]string{"zr-w": {heldByActor}},
	}
}

const guardedRelease = "update zr-w --status=open --assignee= --if-assignee " + heldByActor + " --if-status in_progress"

func notIngestedCC() *dtest.FakeCC {
	return &dtest.FakeCC{SendErr: ccpool.ErrPromptNotIngested, ListSeq: [][]ccpool.Session{{{ExternalID: heldByActor, Live: true, State: ccpool.StateReady}}}}
}

func TestDispatch_notIngested_releaseIsGuardedByTheObservedHolder(t *testing.T) {
	cfg := fastCfg()
	bd := heldBD()
	res, err := dispatchWorker(t, notIngestedCC(), bd, cfg, heldByActor)
	if err == nil {
		t.Fatal("a not-ingested nudge must fail the dispatch")
	}
	if !dtest.HasUpdate(bd, guardedRelease) {
		t.Errorf("the release must carry the observed holder's guard; updates=%v", bd.Updates)
	}
	if v := verbOf(res); v != report.Unclaimed {
		t.Errorf("a landed release is reported as Unclaimed; got %q", v)
	}
}

func TestDispatch_notIngested_refusedReleaseIsLoggedAndNotReported(t *testing.T) {
	logs := captureLog(t)
	cfg := fastCfg()
	bd := heldBD()

	res, err := dispatchWorkerWithBD(t, notIngestedCC(), refuseUpdateBD{bd}, bd, cfg, workerRole(cfg), heldByActor)
	if err == nil {
		t.Fatal("a not-ingested nudge must fail the dispatch")
	}
	if !dtest.HasUpdate(bd, guardedRelease) {
		t.Errorf("the guarded release must still be attempted; updates=%v", bd.Updates)
	}
	if len(res.Actions) != 0 {
		t.Errorf("a release that did not land must not be reported as unclaimed; got %v", verbsOf(res))
	}
	out := logs.String()
	if !strings.Contains(out, "release after a failed nudge send did not land") || !strings.Contains(out, "zr-w") {
		t.Errorf("the refused release must be logged naming the bead; log:\n%s", out)
	}
}

func TestDispatch_dispatchUnclaim_releaseIsGuardedAndRefusalLogged(t *testing.T) {
	cfg := fastCfg()
	role := workerRole(cfg)
	role.CCPool.OnDispatchFail = roles.DispatchUnclaim
	cc := &dtest.FakeCC{SendErr: errors.New("ccpool send: boom"), ListSeq: [][]ccpool.Session{{{ExternalID: heldByActor, Live: true, State: ccpool.StateReady}}}}

	bd := heldBD()
	res, err := dispatchWorkerWithBD(t, cc, bd, bd, cfg, role, heldByActor)
	if err == nil {
		t.Fatal("a failed send must fail the dispatch")
	}
	if !dtest.HasUpdate(bd, guardedRelease) {
		t.Errorf("the on_dispatch_fail release must carry the holder's guard; updates=%v", bd.Updates)
	}
	if v := verbOf(res); v != report.Unclaimed {
		t.Errorf("a landed release is reported as Unclaimed; got %q", v)
	}

	logs := captureLog(t)
	bd2 := heldBD()
	cc2 := &dtest.FakeCC{SendErr: errors.New("ccpool send: boom"), ListSeq: [][]ccpool.Session{{{ExternalID: heldByActor, Live: true, State: ccpool.StateReady}}}}
	res2, _ := dispatchWorkerWithBD(t, cc2, refuseUpdateBD{bd2}, bd2, cfg, role, heldByActor)
	if len(res2.Actions) != 0 {
		t.Errorf("a refused release must not be reported as unclaimed; got %v", verbsOf(res2))
	}
	if !strings.Contains(logs.String(), "release after a failed nudge send did not land") {
		t.Errorf("the refused release must be logged; log:\n%s", logs.String())
	}
}

// dispatchWorkerWithBD is dispatchWorkerRole with the bd runner the executor
// sees (run) separate from the ScriptBD that records it (rec).
func dispatchWorkerWithBD(t *testing.T, cc *dtest.FakeCC, run beads.Runner, rec *dtest.ScriptBD, cfg config.Config, role roles.Role, ext string) (report.Result, error) {
	t.Helper()
	cfg.WorktreeDir = t.TempDir()
	d := DispatchContext{Role: role, Item: item.Item{ID: "zr-w"}}
	deps := newExec(cc, rec, cfg).deps
	deps.BD = run
	deps.ExternalID = ext
	deps.Git = &dtest.NoopGit{}
	deps.GitOpener = (&dtest.NoopGitOpener{}).Open
	return ccpoolExecutor{}.Dispatch(t.Context(), d, deps)
}
