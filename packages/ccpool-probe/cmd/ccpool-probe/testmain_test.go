// testmain_test.go: the in-process wire double for BOTH ccpool and
// pg-connector — this packet's own "tests MAY fake the pg-connector
// subprocess" allowance [Binding decisions: "'Nothing new' rule" closing
// paragraph], extended to ccpool's own list subprocess too.
//
// The double is a cmdruntest.Fake (github.com/phillipgreenii/x) installed on
// the package's Runner seams (runner.go): it answers a Call in process and
// keeps the ordered call log, so a test can assert on the EXACT argv this
// probe passed ccpool/pg-connector. It used to re-execute the race-built test
// binary once per call, and a race-built child sleeps 1 s at exit
// (atexit_sleep_ms); nothing here starts a process. The behavior
// names (GO_HELPER_BEHAVIOR values) are kept so call sites in the tests did
// not change. The real exec path is cmdrun.Exec, covered by the shared
// cmdruntest.RunContract suite in x and, through this adapter, by
// runner_test.go.
//
// One shared helperRun switch covers both subprocesses' fake behaviors
// (ccpoolRunner in ccpoolexec.go and connectorRunner in connector.go are
// independent variables, so a test swaps only the one it needs for a given
// call).
package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/x/cmdrun"
	"github.com/phillipgreenii/x/cmdruntest"
)

// helperRun answers one Call with the canned wire response/exit code for
// behavior. The "slow" behavior is not answered here: see helperRunner.
func helperRun(behavior string, c cmdrun.Call) cmdrun.Result {
	ok := cmdruntest.Out
	fail := cmdruntest.Fail

	switch behavior {
	// pg-connector behaviors.
	case "create_ok":
		return ok(`{"result":{"id":"zr-123","labels":["escalated"],"metadata":{"pg_router_escalation_fingerprint":"fp1"}}}`)
	case "create_fail":
		return fail(1, "pg-connector: issue create: backend rejected: title required\n")
	case "update_ok":
		return ok(`{"result":{"id":"zr-123"}}`)
	case "update_fail":
		return fail(4, "pg-connector: issue update: not found\n")
	case "comment_ok":
		return ok(`{"result":null}`)
	case "comment_fail":
		return fail(4, "pg-connector: issue comment: not found\n")
	case "list_ok_with_match":
		return ok(`{"entities":[` +
			`{"id":"zr-1","labels":["escalated"],"metadata":{"pg_router_escalation_fingerprint":"fp1","pg_router_escalation_state":"needs_input"}},` +
			`{"id":"zr-2","labels":["escalated"],"metadata":{"pg_router_escalation_fingerprint":"fp2"}}` +
			`],"present_ids":["zr-1","zr-2"],"sources":[{"source":"pg-connector-issue-beads","status":"succeeded","count":2}]}`)
	case "list_degraded_empty":
		return cmdrun.Result{Code: 2, Stdout: []byte(`{"entities":[],"present_ids":[],"sources":[{"source":"pg-connector-issue-beads","status":"degraded","reason":"timeout"}]}`)}
	case "list_total_failure":
		return fail(3, "pg-connector: total failure: no backend succeeded\n")

	// ccpool behaviors.
	case "ccpool_list_empty":
		return ok(`[]`)
	case "ccpool_list_needs_input_one":
		return ok(`[{"external_id":"sess-1","name":"worker","state":"needs_input","cwd":"/tmp/w1","meta":{"pgrouter.pool":"pg-router"}}]`)
	case "ccpool_list_mixed_states":
		return ok(`[` +
			`{"external_id":"sess-1","name":"a","state":"working","cwd":"/tmp/a"},` +
			`{"external_id":"sess-2","name":"b","state":"errored","cwd":"/tmp/b"},` +
			`{"external_id":"sess-3","name":"c","state":"idle","cwd":"/tmp/c"}` +
			`]`)
	case "ccpool_list_by_pool":
		// Answers according to the CCPOOL_POOL the probe exported on the
		// child (bead pg2-bkzrc): only the review pool holds a stuck
		// session; every other pool (and the ambient one, CCPOOL_POOL
		// unset) is empty. A real child sees the LAST CCPOOL_POOL entry of
		// its environment, so the last one in Call.Env wins here.
		pool := ""
		for _, e := range c.Env {
			if v, found := strings.CutPrefix(e, "CCPOOL_POOL="); found {
				pool = v
			}
		}
		if filepath.Base(pool) == "pg-router-ccpool-review" {
			return ok(`[{"external_id":"sess-r1","name":"reviewer","state":"needs_input","live":true,"cwd":"/tmp/r1"}]`)
		}
		return ok(`[]`)
	case "ccpool_list_fail":
		return fail(1, "ccpool: list: store open failed\n")

	default:
		return fail(99, "unknown GO_HELPER_BEHAVIOR: "+behavior+" (childArgs="+fmt.Sprint(c.Args)+")\n")
	}
}

// lastFake is the cmdruntest.Fake the most recent withFactory or
// withCcpoolFactory installed; recordedArgs reads its call log.
var lastFake *cmdruntest.Fake

// helperRunner returns a Runner answering every Call with behavior; the call
// is recorded in lastFake's ordered log.
func helperRunner(t *testing.T, behavior string) cmdrun.Runner {
	t.Helper()
	f := cmdruntest.New(t)
	lastFake = f
	rule := f.On(cmdruntest.Any)
	if behavior != "slow" {
		rule.Respond(func(c cmdrun.Call) cmdrun.Result { return helperRun(behavior, c) })
		return f.Runner()
	}
	// "slow" outlives any short test timeout -- exercises "every external
	// call in run MUST carry an explicit timeout" for both subprocess sides.
	// The Fake does not see the context, so this wrapper does: a child
	// killed by its context deadline reports Code -1, a signal and the
	// context error, exactly as cmdrun.Exec does.
	inner := f.Runner()
	return func(ctx context.Context, c cmdrun.Call) cmdrun.Result {
		inner(ctx, c)
		select {
		case <-ctx.Done():
			return cmdrun.Result{Code: -1, Signal: "killed", CtxErr: ctx.Err()}
		case <-time.After(10 * time.Second):
			return cmdrun.Result{}
		}
	}
}

// withFactory swaps pg-connector's own connectorRunner (connector.go).
func withFactory(t *testing.T, behavior string) {
	t.Helper()
	orig := connectorRunner
	connectorRunner = helperRunner(t, behavior)
	t.Cleanup(func() { connectorRunner = orig })
}

// withCcpoolFactory swaps ccpool's own ccpoolRunner (ccpoolexec.go) — a
// SEPARATE variable from withFactory's connectorRunner, so a single test
// can fake both subprocesses independently in the same call.
func withCcpoolFactory(t *testing.T, behavior string) {
	t.Helper()
	orig := ccpoolRunner
	ccpoolRunner = helperRunner(t, behavior)
	t.Cleanup(func() { ccpoolRunner = orig })
}
