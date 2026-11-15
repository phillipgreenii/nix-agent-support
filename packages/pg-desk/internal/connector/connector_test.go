package connector

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/phillipgreenii/x/cmdrun"
	"github.com/phillipgreenii/x/cmdruntest"
)

func TestRunBoundsEveryCallBeforeTheRunnerSeesIt(t *testing.T) {
	fake := cmdruntest.New(t)
	fake.On(cmdruntest.Any).Return(cmdruntest.Out("ok"))
	defer Use(fake.Runner())()

	res := Run(context.Background(), cmdrun.Call{Name: "pg-connector"})
	if string(res.Stdout) != "ok" {
		t.Fatalf("stdout = %q, want ok", res.Stdout)
	}
	got := fake.Calls()[0]
	if !got.OwnProcGroup || got.TermGrace != TermGrace || got.Timeout != DefaultTimeout {
		t.Errorf("call = %+v, want own process group, TermGrace %v, Timeout %v", got, TermGrace, DefaultTimeout)
	}
}

func TestRunKeepsACallersOwnTimeout(t *testing.T) {
	fake := cmdruntest.New(t)
	fake.On(cmdruntest.Any).Return(cmdruntest.Out(""))
	defer Use(fake.Runner())()

	Run(context.Background(), cmdrun.Call{Name: "pg-connector", Timeout: 3 * time.Second})
	if got := fake.Calls()[0].Timeout; got != 3*time.Second {
		t.Errorf("Timeout = %v, want the caller's 3s", got)
	}
}

func TestResultError(t *testing.T) {
	start := errors.New("not found")
	cases := []struct {
		name string
		res  cmdrun.Result
		want string
	}{
		{"clean", cmdrun.Result{}, ""},
		{"exit", cmdrun.Result{Code: 3}, "exit status 3"},
		{"start", cmdrun.Result{Code: -1, StartErr: start}, "not found"},
		{"timeout", cmdrun.Result{Code: -1, TimedOut: true, CtxErr: context.DeadlineExceeded}, "timed out: context deadline exceeded"},
		{"cancel", cmdrun.Result{Code: -1, CtxErr: context.Canceled}, "context canceled"},
		{"signal", cmdrun.Result{Code: -1, Signal: "killed"}, "signal: killed"},
	}
	for _, tc := range cases {
		err := ResultError(tc.res)
		got := ""
		if err != nil {
			got = err.Error()
		}
		if got != tc.want {
			t.Errorf("%s: err = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestOutputAndCombined(t *testing.T) {
	fake := cmdruntest.New(t)
	fake.On(cmdruntest.Any).Return(cmdrun.Result{Stdout: []byte("out"), Stderr: []byte("err"), Code: 1})
	defer Use(fake.Runner())()

	out, err := Output(context.Background(), cmdrun.Call{Name: "x"})
	if string(out) != "out" || err == nil || err.Error() != "exit status 1" {
		t.Errorf("Output = %q, %v", out, err)
	}
	both, err := Combined(context.Background(), cmdrun.Call{Name: "x"})
	if string(both) != "outerr" || err == nil {
		t.Errorf("Combined = %q, %v", both, err)
	}
}
