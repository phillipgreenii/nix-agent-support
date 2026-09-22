// testmain_test.go: the reentrant test-helper-process wire double for
// pg-connector — this packet's own "tests MAY fake the pg-connector
// subprocess" allowance [Binding decisions: "'Nothing new' rule" closing
// paragraph], implemented as a stubbed exec. Mirrors
// packages/pg-router-source-pg-connector/cmd/pg-router-source-pg-connector/testmain_test.go's
// pattern exactly (itself mirroring packages/pg-connector's own
// pkg/scriptout/exec_test.go): the test binary re-execs itself with
// GO_WANT_HELPER_PROCESS=1 set; TestHelperProcess recognizes that and
// runs helperMain instead of the real test suite; helperMain inspects
// the real CLI args this probe passed it (findChildArgs) and the
// GO_HELPER_BEHAVIOR env var to pick a canned wire response/exit code.
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"testing"
)

func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	defer os.Exit(0)
	helperMain()
}

// findChildArgs recovers the real pg-connector CLI args this probe
// passed to execCmdFactory, out of the re-exec'd test binary's own
// os.Args (helperCmdFactory below inserts "--" then the impersonated
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

func helperMain() {
	childArgs := findChildArgs()
	behavior := os.Getenv("GO_HELPER_BEHAVIOR")

	switch behavior {
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
			`{"id":"zr-1","labels":["escalated"],"metadata":{"pg_router_escalation_fingerprint":"fp1","pg_router_escalation_state":"active"}},`+
			`{"id":"zr-2","labels":["escalated"],"metadata":{"pg_router_escalation_fingerprint":"fp2"}}`+
			`],"present_ids":["zr-1","zr-2"],"sources":[{"source":"pg-connector-issue-beads","status":"succeeded","count":2}]}`)
		os.Exit(0)
	case "list_degraded_empty":
		_, _ = fmt.Fprint(os.Stdout, `{"entities":[],"present_ids":[],"sources":[{"source":"pg-connector-issue-beads","status":"degraded","reason":"timeout"}]}`)
		os.Exit(2)
	case "list_total_failure":
		_, _ = fmt.Fprintln(os.Stderr, "pg-connector: total failure: no backend succeeded")
		os.Exit(3)
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

func withFactory(t *testing.T, behavior string) {
	t.Helper()
	orig := execCmdFactory
	execCmdFactory = helperCmdFactory(behavior)
	t.Cleanup(func() { execCmdFactory = orig })
}
