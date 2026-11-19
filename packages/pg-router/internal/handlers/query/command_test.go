package query

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/phillipgreenii/x/cmdrun"
	"github.com/phillipgreenii/x/cmdruntest"
)

func TestOSCommanderRunEnv_UsesExactlyTheGivenEnv(t *testing.T) {
	t.Setenv("QUERY_TEST_INHERITED", "yes")
	out, err := OSCommander{}.RunEnv(context.Background(), []string{"/usr/bin/env"}, []string{"ONLY=1"})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(out)); got != "ONLY=1" {
		t.Fatalf("env = %q, want only ONLY=1", got)
	}
}

func TestOSCommanderRun_StillInheritsEnv(t *testing.T) {
	t.Setenv("QUERY_TEST_INHERITED", "yes")
	out, err := OSCommander{}.Run(context.Background(), []string{"/usr/bin/env"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "QUERY_TEST_INHERITED=yes") {
		t.Fatalf("Run must keep today's inherited env; got %q", out)
	}
}

func TestOSCommander_EmptyArgv(t *testing.T) {
	if _, err := (OSCommander{}).RunEnv(context.Background(), nil, nil); err == nil {
		t.Fatal("empty argv must error")
	}
	var _ EnvCommander = OSCommander{}
}

func fakeCommander(t *testing.T) (OSCommander, *cmdruntest.Fake) {
	t.Helper()
	f := cmdruntest.New(t)
	return OSCommander{Exec: f.Runner()}, f
}

// RunEnv hands cmdrun its own process group, a SIGTERM grace, the exact env and
// a backstop timeout: the shape that stops a non-exec wrapper orphaning its
// payload and no run from being unbounded.
func TestOSCommanderRunEnv_CallShape(t *testing.T) {
	c, f := fakeCommander(t)
	f.On(cmdruntest.Any).Return(cmdruntest.Out("hi"))
	out, err := c.RunEnv(context.Background(), []string{"tool", "a"}, []string{"K=v"})
	if err != nil || string(out) != "hi" {
		t.Fatalf("out=%q err=%v", out, err)
	}
	got := f.Calls()[0]
	if got.Name != "tool" || len(got.Args) != 1 || got.Args[0] != "a" || !got.OwnProcGroup ||
		got.TermGrace != CancelGrace || got.Timeout != commandTimeout ||
		got.EnvMode != cmdrun.EnvExact || len(got.Env) != 1 || got.Env[0] != "K=v" {
		t.Fatalf("call = %+v", got)
	}
}

func TestOSCommanderRun_BoundedAndInheritsEnv(t *testing.T) {
	c, f := fakeCommander(t)
	f.On(cmdruntest.Any).Return(cmdruntest.Out(""))
	if _, err := c.Run(context.Background(), []string{"tool"}); err != nil {
		t.Fatal(err)
	}
	if got := f.Calls()[0]; got.Timeout != commandTimeout || got.Env != nil || got.EnvMode != cmdrun.EnvAppend {
		t.Fatalf("call = %+v", got)
	}
}

func TestOSCommander_FailureShapes(t *testing.T) {
	c, f := fakeCommander(t)
	f.On(cmdruntest.MatchName("exit9")).Return(cmdruntest.Fail(9, "busy\n"))
	f.On(cmdruntest.MatchName("slow")).Return(cmdrun.Result{Code: -1, Signal: "terminated", CtxErr: context.DeadlineExceeded, TimedOut: true})
	f.On(cmdruntest.MatchName("gone")).Return(cmdrun.Result{Code: -1, StartErr: errors.New("not found")})

	_, err := c.RunEnv(context.Background(), []string{"exit9"}, nil)
	code, stderr, ok := ExitDetail(err)
	if !ok || code != 9 || string(stderr) != "busy\n" {
		t.Fatalf("exit failure: code=%d stderr=%q ok=%v err=%v", code, stderr, ok, err)
	}
	if _, err := c.RunEnv(context.Background(), []string{"slow"}, nil); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("timeout err = %v", err)
	}
	if _, err := c.Run(context.Background(), []string{"gone"}); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("start err = %v", err)
	}
	if _, _, ok := ExitDetail(errors.New("x")); ok {
		t.Fatal("ExitDetail accepted a non-exit error")
	}
}

// With files the payload keeps os/exec (cmdrun cannot pass descriptors), so the
// fake must NOT be consulted.
func TestOSCommanderRunEnvFiles_WithFilesBypassesRunner(t *testing.T) {
	c, f := fakeCommander(t)
	fh, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fh.Close() }()
	out, err := c.RunEnvFiles(context.Background(), []string{"/usr/bin/true"}, []string{}, []*os.File{fh})
	if err != nil || len(out) != 0 {
		t.Fatalf("out=%q err=%v", out, err)
	}
	if n := len(f.Calls()); n != 0 {
		t.Fatalf("runner consulted %d times for a files run", n)
	}
}
