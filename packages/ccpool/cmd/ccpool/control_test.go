package main

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/phillipgreenii/ccpool/internal/session"
)

type ctlCall struct {
	args  []string
	stdin string
}

func fakeControl(exit int, err error, calls *[]ctlCall) *controlClient {
	return &controlClient{run: func(_ context.Context, args []string, stdin string) (int, string, error) {
		*calls = append(*calls, ctlCall{args, stdin})
		return exit, "boom", err
	}}
}

func TestControlClientCommandShapes(t *testing.T) {
	var calls []ctlCall
	c := fakeControl(0, nil, &calls)
	ctx := context.Background()
	for _, f := range []func() error{
		func() error { return c.Submit(ctx, "sid", "hello there") },
		func() error { return c.Interrupt(ctx, "sid") },
		func() error { return c.Run(ctx, "sid", "clear") },
		func() error { return c.Stop(ctx, "sid") },
	} {
		if err := f(); err != nil {
			t.Fatal(err)
		}
	}
	want := []ctlCall{
		{[]string{"submit", "session:sid"}, "hello there"},
		{[]string{"interrupt", "session:sid"}, ""},
		{[]string{"run", "session:sid", "clear"}, ""},
		{[]string{"stop", "session:sid"}, ""},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %+v; want %+v", calls, want)
	}
}

func TestControlClientErrorMapping(t *testing.T) {
	ctx := context.Background()
	var calls []ctlCall
	for _, exit := range []int{ctlNoSuchProcess, ctlNoControlMod, ctlNotAccepting} {
		if err := fakeControl(exit, nil, &calls).Interrupt(ctx, "s"); !errors.Is(err, session.ErrNoControl) {
			t.Errorf("exit %d: err = %v; want ErrNoControl", exit, err)
		}
	}
	if err := fakeControl(2, nil, &calls).Interrupt(ctx, "s"); err == nil || errors.Is(err, session.ErrNoControl) {
		t.Errorf("usage error must be a real failure, got %v", err)
	}
	if err := fakeControl(-1, errors.New("not found"), &calls).Stop(ctx, "s"); !errors.Is(err, session.ErrNoControl) {
		t.Errorf("missing command: err = %v; want ErrNoControl", err)
	}
}

func TestControlClientSessionStayedMapping(t *testing.T) {
	ctx := context.Background()
	var calls []ctlCall
	err := fakeControl(ctlSessionStayed, nil, &calls).Stop(ctx, "s")
	if !errors.Is(err, session.ErrSessionStayed) {
		t.Fatalf("exit 20: err = %v; want ErrSessionStayed", err)
	}
	if errors.Is(err, session.ErrNoControl) {
		t.Errorf("exit 20 must not map to ErrNoControl: %v", err)
	}
	for _, exit := range []int{ctlNoSuchProcess, ctlNoControlMod, ctlNotAccepting} {
		err := fakeControl(exit, nil, &calls).Stop(ctx, "s")
		if !errors.Is(err, session.ErrNoControl) || errors.Is(err, session.ErrSessionStayed) {
			t.Errorf("exit %d: err = %v; want ErrNoControl only", exit, err)
		}
	}
}

// The pg-claude-control exit codes are a contract with that command: pin the literals.
func TestControlExitCodeValues(t *testing.T) {
	for name, c := range map[string][2]int{
		"no_such_process":   {ctlNoSuchProcess, 4},
		"no_control_mod":    {ctlNoControlMod, 3},
		"mod_not_accepting": {ctlNotAccepting, 9},
		"session_stayed":    {ctlSessionStayed, 20},
	} {
		if c[0] != c[1] {
			t.Errorf("%s = %d; want %d", name, c[0], c[1])
		}
	}
}
