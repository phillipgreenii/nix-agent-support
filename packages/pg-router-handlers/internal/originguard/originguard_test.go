package originguard

import (
	"context"
	"reflect"
	"testing"

	"github.com/phillipgreenii/x/cmdrun"
	"github.com/phillipgreenii/x/cmdruntest"
)

func TestExecCheck_exitCodes(t *testing.T) {
	for name, tc := range map[string]struct {
		res     cmdrun.Result
		avail   bool
		reason  string
		wantErr bool
	}{
		"available":   {cmdruntest.Out("available realm=zr reason=ok\n"), true, "ok", false},
		"unavailable": {cmdrun.Result{Stdout: []byte("unavailable realm=zr reason=cert-expired\n"), Code: 3}, false, "cert-expired", false},
		"fault":       {cmdruntest.Fail(1, ""), false, "", true},
		"usage":       {cmdruntest.Fail(2, ""), false, "", true},
		"config":      {cmdruntest.Fail(7, ""), false, "", true},
		"old-75":      {cmdruntest.Fail(75, ""), false, "", true},
		"signalled":   {cmdrun.Result{Code: -1, Signal: "killed"}, false, "", true},
	} {
		t.Run(name, func(t *testing.T) {
			fake := cmdruntest.New(t)
			fake.On(cmdruntest.Any).Return(tc.res)
			v, err := ExecCheckWith("pg-origin-guard", fake.Runner())(context.Background(), "/repo")
			if (err != nil) != tc.wantErr || v.Available != tc.avail || v.Reason != tc.reason {
				t.Fatalf("got %+v, %v", v, err)
			}
		})
	}
}

// TestExitCodesPinPgOriginGuardContract pins the codes this client branches on
// to pg-origin-guard's published contract (its README exit-code table): 0
// available, 3 unavailable. A drift in the guard's codes must fail here rather
// than silently turn every unavailable answer into a fail-open fault.
func TestExitCodesPinPgOriginGuardContract(t *testing.T) {
	if exitAvailable != 0 || exitUnavailable != 3 {
		t.Fatalf("available=%d unavailable=%d, want 0 and 3", exitAvailable, exitUnavailable)
	}
}

func TestExecCheck_passesRepoArgsAndBounds(t *testing.T) {
	fake := cmdruntest.New(t)
	fake.On(cmdruntest.Any).Return(cmdruntest.Out(""))
	if _, err := ExecCheckWith("", fake.Runner())(context.Background(), "/some/repo"); err != nil {
		t.Fatal(err)
	}
	calls := fake.Calls()
	if len(calls) != 1 || calls[0].Name != DefaultBin || !reflect.DeepEqual(calls[0].Args, []string{"check", "--repo", "/some/repo"}) {
		t.Fatalf("calls %+v", calls)
	}
	if calls[0].Timeout != DefaultTimeout {
		t.Fatalf("timeout %v, want %v", calls[0].Timeout, DefaultTimeout)
	}
}

// One real-process case through the real Exec path: a missing binary is a
// tool fault, not "unavailable".
func TestExecCheck_missingBinaryIsFault(t *testing.T) {
	if _, err := ExecCheck("/nonexistent/pg-origin-guard")(context.Background(), "/r"); err == nil {
		t.Fatal("want fault")
	}
}
