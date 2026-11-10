package main

import (
	"testing"

	"github.com/phillipgreenii/x/cmdrun"
	"github.com/phillipgreenii/x/cmdruntest"
)

func TestLaunchctlStateVia(t *testing.T) {
	isList := func(c cmdrun.Call) bool {
		return c.Name == "launchctl" && len(c.Args) == 2 && c.Args[0] == "list" && c.Args[1] == "lbl"
	}
	cases := []struct {
		name string
		goos string
		res  cmdrun.Result
		want string
	}{
		{"running", "darwin", cmdrun.Result{Stdout: []byte("{\n\t\"PID\" = 42;\n}")}, "loaded, running"},
		{"loaded not running", "darwin", cmdrun.Result{Stdout: []byte("{\n\t\"Label\" = \"lbl\";\n}")}, "loaded, not running"},
		{"not loaded", "darwin", cmdrun.Result{Code: 113}, "not loaded"},
		{"never started", "darwin", cmdrun.Result{Code: -1, StartErr: startErr{}}, "not loaded"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := cmdruntest.New(t)
			f.On(isList).Return(tc.res)
			if got := launchctlStateVia(f.Runner(), tc.goos, "lbl"); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
	t.Run("non darwin never runs launchctl", func(t *testing.T) {
		f := cmdruntest.New(t)
		got := launchctlStateVia(f.Runner(), "linux", "lbl")
		if got != "unknown (no launchctl on linux)" || len(f.Calls()) != 0 {
			t.Fatalf("got %q, calls %v", got, f.Calls())
		}
	})
}

type startErr struct{}

func (startErr) Error() string { return "no such program" }
