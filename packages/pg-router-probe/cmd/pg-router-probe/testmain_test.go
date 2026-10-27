// testmain_test.go: the in-process wire double for pg-connector — this
// packet's own "tests MAY fake the pg-connector subprocess" allowance
// [Binding decisions: "'Nothing new' rule" closing paragraph], implemented
// as a stubbed exec. Mirrors
// packages/pg-router-source-pg-connector/cmd/pg-router-source-pg-connector/testmain_test.go's
// pattern (itself mirroring packages/pg-connector's own
// pkg/scriptout/exec_test.go).
//
// The double answers a Call through the package's Runner seam (runner.go) in
// process. It used to re-execute the race-built test binary once per call
// (GO_WANT_HELPER_PROCESS), and a race-built child sleeps 1 s at exit
// (atexit_sleep_ms), so every fake call cost about 1 s under -race. The
// behavior names (GO_HELPER_BEHAVIOR values) are kept so call sites in the
// tests did not change. The real exec path (ExecRunner) is covered by
// runner_test.go with sh -c.
package main

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

var (
	recordedMu    sync.Mutex
	recordedCalls []Call
)

// recordCall remembers the Call a double answered, so a test can assert on
// the EXACT argv this probe passed pg-connector (e.g. that --backend
// pg-connector-issue-beads is really present [design: Contract's
// "pg-connector's issue capability" bullet]) rather than trusting a code
// read of the hardcoded constant that builds it.
func recordCall(c Call) {
	recordedMu.Lock()
	defer recordedMu.Unlock()
	recordedCalls = append(recordedCalls, c)
}

// helperRun answers one Call with the canned wire response/exit code for
// behavior. ctx is honored by the "slow" behavior only.
func helperRun(ctx context.Context, behavior string, c Call) Result {
	ok := func(stdout string) Result { return Result{Stdout: stdout} }
	fail := func(code int, stderr string) Result { return Result{Code: code, Stderr: stderr} }

	switch behavior {
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
			`{"id":"zr-1","labels":["escalated"],"metadata":{"pg_router_escalation_fingerprint":"fp1","pg_router_escalation_state":"active"}},` +
			`{"id":"zr-2","labels":["escalated"],"metadata":{"pg_router_escalation_fingerprint":"fp2"}}` +
			`],"present_ids":["zr-1","zr-2"],"sources":[{"source":"pg-connector-issue-beads","status":"succeeded","count":2}]}`)
	case "list_degraded_empty":
		return Result{Code: 2, Stdout: `{"entities":[],"present_ids":[],"sources":[{"source":"pg-connector-issue-beads","status":"degraded","reason":"timeout"}]}`}
	case "list_total_failure":
		return fail(3, "pg-connector: total failure: no backend succeeded\n")
	case "slow":
		// Outlives any short test timeout -- exercises "every external call
		// in run MUST carry an explicit timeout" for the pg-connector
		// subprocess side (grafana_test.go's
		// TestFiringAlertsHonorsExplicitTimeout is the Grafana-HTTP-side
		// counterpart). A child killed by its context deadline reports
		// Code -1 and Signaled, exactly as ExecRunner does.
		select {
		case <-ctx.Done():
			return Result{Code: -1, Signaled: true}
		case <-time.After(10 * time.Second):
			return Result{}
		}
	default:
		return fail(99, "unknown GO_HELPER_BEHAVIOR: "+behavior+" (childArgs="+fmt.Sprint(c.Args)+")\n")
	}
}

// helperRunner returns a Runner that answers every Call with behavior and
// records it (recordCall).
func helperRunner(behavior string) Runner {
	return func(ctx context.Context, c Call) Result {
		recordCall(c)
		return helperRun(ctx, behavior, c)
	}
}

// withFactory swaps the package's runner (connector.go) for the in-process
// double; it serves both pg-connector (connector.go) and `pg-router status`
// (status.go).
func withFactory(t *testing.T, behavior string) {
	t.Helper()
	orig := runner
	runner = helperRunner(behavior)
	t.Cleanup(func() { runner = orig })
}
