// testmain_test.go: the reentrant test-helper-process wire double for
// BOTH ccpool and pg-connector — this packet's own "tests MAY fake the
// pg-connector subprocess" allowance [Binding decisions: "'Nothing new'
// rule" closing paragraph], extended to ccpool's own list subprocess too.
// Mirrors packages/pg-router-probe/cmd/pg-router-probe/testmain_test.go's
// pattern exactly (itself mirroring packages/pg-connector's own
// pkg/scriptout/exec_test.go): the test binary re-execs itself with
// GO_WANT_HELPER_PROCESS=1 set; TestHelperProcess recognizes that and
// runs helperMain instead of the real test suite; helperMain inspects
// the real CLI args this probe passed it (findChildArgs) and the
// GO_HELPER_BEHAVIOR env var to pick a canned wire response/exit code.
//
// One shared helperMain switch covers both subprocesses' fake behaviors
// (ccpoolExecCmdFactory in ccpoolexec.go and execCmdFactory in
// connector.go are independent variables, so a test swaps only the one
// it needs for a given call).
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	defer os.Exit(0)
	helperMain()
}

// findChildArgs recovers the real CLI args this probe passed to
// ccpoolExecCmdFactory/execCmdFactory, out of the re-exec'd test binary's
// own os.Args (helperCmdFactory below inserts "--" then the impersonated
// binary name then those args).
func findChildArgs() []string {
	for i, a := range os.Args {
		if a == "--" {
			if i+2 <= len(os.Args) {
				return os.Args[i+2:]
			}
			return nil
		}
	}
	return nil
}

// recordArgsIfRequested writes childArgs (space-joined) to the path named
// by GO_HELPER_ARGS_RECORD_FILE, when set — lets a test assert on the
// EXACT argv this probe passed ccpool/pg-connector (e.g. that
// --backend pg-connector-issue-beads is really present) rather than
// trusting a code read of the hardcoded constant that builds it.
func recordArgsIfRequested(childArgs []string) {
	file := os.Getenv("GO_HELPER_ARGS_RECORD_FILE")
	if file == "" {
		return
	}
	_ = os.WriteFile(file, []byte(fmt.Sprint(childArgs)), 0o600)
}

func helperMain() {
	childArgs := findChildArgs()
	recordArgsIfRequested(childArgs)
	behavior := os.Getenv("GO_HELPER_BEHAVIOR")

	switch behavior {
	// pg-connector behaviors.
	case "create_ok":
		_, _ = fmt.Fprint(os.Stdout, `{"result":{"id":"zr-123","labels":["escalated"],"metadata":{"pg_router_escalation_fingerprint":"fp1"}}}`)
		os.Exit(0)
	case "create_fail":
		_, _ = fmt.Fprintln(os.Stderr, "pg-connector: issue create: backend rejected: title required")
		os.Exit(1)
	case "update_ok":
		_, _ = fmt.Fprint(os.Stdout, `{"result":{"id":"zr-123"}}`)
		os.Exit(0)
	case "update_fail":
		_, _ = fmt.Fprintln(os.Stderr, "pg-connector: issue update: not found")
		os.Exit(4)
	case "comment_ok":
		_, _ = fmt.Fprint(os.Stdout, `{"result":null}`)
		os.Exit(0)
	case "comment_fail":
		_, _ = fmt.Fprintln(os.Stderr, "pg-connector: issue comment: not found")
		os.Exit(4)
	case "list_ok_with_match":
		_, _ = fmt.Fprint(os.Stdout, `{"entities":[`+
			`{"id":"zr-1","labels":["escalated"],"metadata":{"pg_router_escalation_fingerprint":"fp1","pg_router_escalation_state":"needs_input"}},`+
			`{"id":"zr-2","labels":["escalated"],"metadata":{"pg_router_escalation_fingerprint":"fp2"}}`+
			`],"present_ids":["zr-1","zr-2"],"sources":[{"source":"pg-connector-issue-beads","status":"succeeded","count":2}]}`)
		os.Exit(0)
	case "list_degraded_empty":
		_, _ = fmt.Fprint(os.Stdout, `{"entities":[],"present_ids":[],"sources":[{"source":"pg-connector-issue-beads","status":"degraded","reason":"timeout"}]}`)
		os.Exit(2)
	case "list_total_failure":
		_, _ = fmt.Fprintln(os.Stderr, "pg-connector: total failure: no backend succeeded")
		os.Exit(3)

	// ccpool behaviors.
	case "ccpool_list_empty":
		_, _ = fmt.Fprint(os.Stdout, `[]`)
		os.Exit(0)
	case "ccpool_list_needs_input_one":
		_, _ = fmt.Fprint(os.Stdout, `[{"external_id":"sess-1","name":"worker","state":"needs_input","cwd":"/tmp/w1","meta":{"pgrouter.pool":"pg-router"}}]`)
		os.Exit(0)
	case "ccpool_list_mixed_states":
		_, _ = fmt.Fprint(os.Stdout, `[`+
			`{"external_id":"sess-1","name":"a","state":"working","cwd":"/tmp/a"},`+
			`{"external_id":"sess-2","name":"b","state":"errored","cwd":"/tmp/b"},`+
			`{"external_id":"sess-3","name":"c","state":"idle","cwd":"/tmp/c"}`+
			`]`)
		os.Exit(0)
	case "ccpool_list_fail":
		_, _ = fmt.Fprintln(os.Stderr, "ccpool: list: store open failed")
		os.Exit(1)

	case "slow":
		// Deliberately outlives any short test timeout -- exercises
		// "every external call in run MUST carry an explicit timeout" for
		// both subprocess sides.
		time.Sleep(10 * time.Second)
		os.Exit(0)
	default:
		_, _ = fmt.Fprintln(os.Stderr, "unknown GO_HELPER_BEHAVIOR: "+behavior+" (childArgs="+fmt.Sprint(childArgs)+")")
		os.Exit(99)
	}
}

func helperCmdFactory(behavior string) func(ctx context.Context, name string, args ...string) *exec.Cmd {
	return func(ctx context.Context, name string, args ...string) *exec.Cmd {
		cs := append([]string{"-test.run=^TestHelperProcess$", "--", name}, args...)
		cmd := exec.CommandContext(ctx, os.Args[0], cs...)
		cmd.Env = append(
			os.Environ(),
			"GO_WANT_HELPER_PROCESS=1",
			"GO_HELPER_BEHAVIOR="+behavior,
		)
		return cmd
	}
}

// withFactory swaps pg-connector's own execCmdFactory (connector.go).
func withFactory(t *testing.T, behavior string) {
	t.Helper()
	orig := execCmdFactory
	execCmdFactory = helperCmdFactory(behavior)
	t.Cleanup(func() { execCmdFactory = orig })
}

// withCcpoolFactory swaps ccpool's own ccpoolExecCmdFactory (ccpoolexec.go)
// — a SEPARATE variable from withFactory's execCmdFactory, so a single
// test can fake both subprocesses independently in the same call.
func withCcpoolFactory(t *testing.T, behavior string) {
	t.Helper()
	orig := ccpoolExecCmdFactory
	ccpoolExecCmdFactory = helperCmdFactory(behavior)
	t.Cleanup(func() { ccpoolExecCmdFactory = orig })
}
