// testmain_test.go: the in-process wire double for pg-connector — this
// packet's own "tests MAY fake the pg-connector subprocess" allowance
// [Binding decisions: "'Nothing new' rule" closing paragraph], implemented
// as a cmdruntest.Fake behind the package's Runner seam. Mirrors
// packages/pg-router-source-pg-connector/cmd/pg-router-source-pg-connector/testmain_test.go's
// pattern (itself mirroring packages/pg-connector's own
// pkg/scriptout/exec_test.go).
//
// The double answers a Call through the Runner seam (runner.go) in process.
// It used to re-execute the race-built test binary once per call, and a
// race-built child sleeps 1 s at exit (atexit_sleep_ms), so every fake call
// cost about 1 s under -race. The behavior names (the withFactory behavior
// argument) are kept so call sites in the tests did not change. The real exec
// path is cmdrun.Exec, covered by the shared cmdruntest.RunContract suite in
// x and, through this probe's seam, by runner_test.go.
package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/phillipgreenii/x/cmdrun"
	"github.com/phillipgreenii/x/cmdruntest"
)

// currentFake is the Fake the latest withFactory installed; recordedArgs
// reads the Calls it recorded, so a test can assert on the EXACT argv this
// probe passed pg-connector (e.g. that --backend pg-connector-issue-beads is
// really present [design: Contract's "pg-connector's issue capability"
// bullet]) rather than trusting a code read of the hardcoded constant that
// builds it. Tests in this package do not run in parallel.
var currentFake *cmdruntest.Fake

// helperRun answers one Call with the canned wire response/exit code for
// behavior.
func helperRun(behavior string, c cmdrun.Call) cmdrun.Result {
	switch behavior {
	case "create_ok":
		return cmdruntest.Out(`{"result":{"id":"zr-123","labels":["escalated"],"metadata":{"pg_router_escalation_fingerprint":"fp1"}}}`)
	case "create_fail":
		return cmdruntest.Fail(1, "pg-connector: issue create: backend rejected: title required\n")
	case "update_ok":
		return cmdruntest.Out(`{"result":{"id":"zr-123"}}`)
	case "update_fail":
		return cmdruntest.Fail(4, "pg-connector: issue update: not found\n")
	case "comment_ok":
		return cmdruntest.Out(`{"result":null}`)
	case "comment_fail":
		return cmdruntest.Fail(4, "pg-connector: issue comment: not found\n")
	case "list_ok_with_match":
		return cmdruntest.Out(`{"entities":[` +
			`{"id":"zr-1","labels":["escalated"],"metadata":{"pg_router_escalation_fingerprint":"fp1","pg_router_escalation_state":"active"}},` +
			`{"id":"zr-2","labels":["escalated"],"metadata":{"pg_router_escalation_fingerprint":"fp2"}}` +
			`],"present_ids":["zr-1","zr-2"],"sources":[{"source":"pg-connector-issue-beads","status":"succeeded","count":2}]}`)
	case "list_degraded_empty":
		return cmdrun.Result{Code: 2, Stdout: []byte(`{"entities":[],"present_ids":[],"sources":[{"source":"pg-connector-issue-beads","status":"degraded","reason":"timeout"}]}`)}
	case "list_total_failure":
		return cmdruntest.Fail(3, "pg-connector: total failure: no backend succeeded\n")
	case "slow":
		// Placeholder; withFactory's slow wrapper waits for the context
		// first and answers like a child killed by it.
		return cmdrun.Result{}
	default:
		return cmdruntest.Fail(99, "unknown withFactory behavior: "+behavior+" (childArgs="+fmt.Sprint(c.Args)+")\n")
	}
}

// withFactory swaps the package's runner (runner.go) for an in-process
// cmdruntest.Fake answering every Call with behavior; it serves both
// pg-connector (connector.go) and `pg-router status` (status.go).
func withFactory(t *testing.T, behavior string) {
	t.Helper()
	f := cmdruntest.New(t)
	f.On(cmdruntest.Any).Respond(func(c cmdrun.Call) cmdrun.Result { return helperRun(behavior, c) })
	r := f.Runner()
	if behavior == "slow" {
		// Outlives any short test timeout -- exercises "every external call
		// in run MUST carry an explicit timeout" for the pg-connector
		// subprocess side (grafana_test.go's
		// TestFiringAlertsHonorsExplicitTimeout is the Grafana-HTTP-side
		// counterpart). A child killed by its context deadline reports
		// Code -1, Signal and CtxErr, exactly as cmdrun.Exec does.
		inner := r
		r = func(ctx context.Context, c cmdrun.Call) cmdrun.Result {
			res := inner(ctx, c)
			select {
			case <-ctx.Done():
				return cmdrun.Result{Code: -1, Signal: "killed", CtxErr: ctx.Err()}
			case <-time.After(10 * time.Second):
				return res
			}
		}
	}
	origRunner, origFake := runner, currentFake
	runner, currentFake = r, f
	t.Cleanup(func() { runner, currentFake = origRunner, origFake })
}
