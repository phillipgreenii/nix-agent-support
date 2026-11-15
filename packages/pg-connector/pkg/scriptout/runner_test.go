// runner_test.go: runInvoke through the real cmdrun.Exec against real
// processes, the V-3 regression guard behind the in-process double of
// exec_test.go. Each test starts one /bin/sh, never the race-built test
// binary, so none pays the race runtime's 1 s exit sleep. The runner's own
// semantics (exit codes, kill, WaitDelay, process groups) are tested in
// x/cmdrun; the cancel-kills-the-group case is in runner_integration_test.go.
package scriptout

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestRunInvokeRealProcessRoundTrip runs the whole runInvoke path (request on
// stdin, response on stdout, nil runner meaning cmdrun.Exec) against a real
// shell standing in for a backend binary.
func TestRunInvokeRealProcessRoundTrip(t *testing.T) {
	prev := execRunner
	execRunner = nil
	t.Cleanup(func() { execRunner = prev })

	tgt := Target{Name: "real-sh", Command: []string{"sh", "-c", `cat >/dev/null; echo '{"protocolVersion":` + protocolVersionText() + `,"result":{"state":"OK"}}'`}}
	resp, err := InvokeTarget(context.Background(), tgt, OpAuthStatus, nil, nil)
	if err != nil {
		t.Fatalf("InvokeTarget: %v", err)
	}
	if !strings.Contains(string(resp.Result), `"OK"`) {
		t.Fatalf("result = %s", resp.Result)
	}
}

// TestRunInvokeRealProcessFailureNamesExitStatusAndStderr pins the error
// text a real failing backend produces: exit status plus folded stderr.
func TestRunInvokeRealProcessFailureNamesExitStatusAndStderr(t *testing.T) {
	prev := execRunner
	execRunner = nil
	t.Cleanup(func() { execRunner = prev })

	tgt := Target{Name: "real-sh", Command: []string{"sh", "-c", "echo boom >&2; exit 2"}}
	_, err := InvokeTarget(context.Background(), tgt, "x", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "exit status 2") || !strings.Contains(err.Error(), "stderr: boom") {
		t.Fatalf("got %v, want the exit status and folded stderr", err)
	}
}

func protocolVersionText() string {
	b, _ := json.Marshal(ProtocolVersion)
	return string(b)
}
