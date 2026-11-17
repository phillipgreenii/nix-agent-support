package control_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-assistants/internal/control"
	"github.com/phillipgreenii/x/cmdrun"
	"github.com/phillipgreenii/x/cmdruntest"
)

const bin = "/example/bin/pg-claude-control"

var now = time.Date(2026, 11, 16, 18, 30, 0, 0, time.UTC)

func newClient(t *testing.T) (control.Client, *cmdruntest.Fake) {
	t.Helper()
	fake := cmdruntest.New(t)
	return control.Client{Bin: bin, Run: fake.Runner()}, fake
}

// only returns the one call the Fake saw, failing the test otherwise.
func only(t *testing.T, fake *cmdruntest.Fake) cmdrun.Call {
	t.Helper()
	calls := fake.Calls()
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1: %+v", len(calls), calls)
	}
	return calls[0]
}

func checkCall(t *testing.T, c cmdrun.Call, timeout time.Duration, args ...string) {
	t.Helper()
	if c.Name != bin {
		t.Errorf("name = %q, want %q", c.Name, bin)
	}
	if !slices.Equal(c.Args, args) {
		t.Errorf("args = %q, want %q", c.Args, args)
	}
	if c.Timeout != timeout {
		t.Errorf("timeout = %v, want %v", c.Timeout, timeout)
	}
	if c.EnvMode != cmdrun.EnvAppend || len(c.Env) != 0 {
		t.Errorf("env = %q (mode %v), want the daemon's own environment", c.Env, c.EnvMode)
	}
}

var usageArgv = []string{"usage", "pid:42", "--json", "--timeout", "5s"}

func TestUsageParsesResult(t *testing.T) {
	c, fake := newClient(t)
	fake.On(cmdruntest.MatchName(bin)).Return(cmdruntest.Out(`{"sent":true,"session":"s1","result":{"context":{"tokens":83000,"percent":41.5,"window":200000},` +
		`"rateLimits":[{"kind":"five_hour","percentUsed":62,"resetsAt":"2026-11-16T20:00:00.123Z"},{"kind":"spend","percentUsed":5},` +
		`{"kind":"seven_day","percentUsed":1.7e9},{"kind":"seven_day","percentUsed":-3}]}}` + "\n"))

	u, err := c.Usage(t.Context(), 42, now)
	if err != nil {
		t.Fatal(err)
	}
	checkCall(t, only(t, fake), control.CallTimeout, usageArgv...)
	if u.SessionID != "s1" || !u.HaveContext || u.ContextTokens != 83000 || u.ContextPct != 41.5 || u.Window != 200000 {
		t.Errorf("usage = %+v", u)
	}
	want := control.Limit{
		Kind:       "five_hour",
		Fraction:   0.62,
		ResetsAt:   time.Date(2026, 11, 16, 20, 0, 0, 0, time.UTC), // to the second
		ObservedAt: now,
	}
	if len(u.Limits) != 1 {
		t.Fatalf("limits = %+v, want only five_hour", u.Limits)
	}
	got := u.Limits[0]
	if got.Kind != want.Kind || got.Fraction != want.Fraction || !got.ResetsAt.Equal(want.ResetsAt) || !got.ObservedAt.Equal(want.ObservedAt) {
		t.Errorf("limit = %+v, want %+v", got, want)
	}
}

func TestUsageNoContextYet(t *testing.T) {
	for name, stdout := range map[string]string{
		"no context":         `{"sent":true,"session":"s1","result":{"rateLimits":[]}}`,
		"percent only":       `{"sent":true,"session":"s1","result":{"context":{"percent":3},"rateLimits":[]}}`,
		"no result":          `{"sent":true,"session":"s1"}`,
		"negative tokens":    `{"sent":true,"session":"s1","result":{"context":{"tokens":-1}}}`,
		"window without any": `{"sent":true,"session":"s1","result":{"context":{"window":200000}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			c, fake := newClient(t)
			fake.On(cmdruntest.MatchName(bin)).Return(cmdruntest.Out(stdout + "\n"))
			u, err := c.Usage(t.Context(), 42, now)
			if err != nil {
				t.Fatal(err)
			}
			if u.SessionID != "s1" || u.HaveContext || u.ContextTokens != 0 || len(u.Limits) != 0 {
				t.Errorf("usage = %+v, want session s1 and no context", u)
			}
		})
	}
}

func TestUsageLimitEdges(t *testing.T) {
	c, fake := newClient(t)
	fake.On(cmdruntest.MatchName(bin)).Return(cmdruntest.Out(`{"session":"s1","result":{"rateLimits":[` +
		`{"kind":"five_hour","percentUsed":0,"resetsAt":"2026-11-16T20:00:00Z"},` +
		`{"kind":"seven_day","percentUsed":100,"resetsAt":"2026-11-16T21:00:00+01:00"},` +
		`{"kind":"five_hour","percentUsed":50,"resetsAt":"next tuesday"},` +
		`{"kind":"five_hour","resetsAt":"2026-11-16T20:00:00Z"},` +
		`{"percentUsed":40}]}}`))
	u, err := c.Usage(t.Context(), 42, now)
	if err != nil {
		t.Fatal(err)
	}
	type lim struct {
		kind  string
		frac  float64
		reset time.Time
	}
	var got []lim
	for _, l := range u.Limits {
		if !l.ObservedAt.Equal(now) {
			t.Errorf("%s observed at %v, want %v", l.Kind, l.ObservedAt, now)
		}
		got = append(got, lim{l.Kind, l.Fraction, l.ResetsAt.UTC()})
	}
	want := []lim{
		{"five_hour", 0, time.Date(2026, 11, 16, 20, 0, 0, 0, time.UTC)},
		{"seven_day", 1, time.Date(2026, 11, 16, 20, 0, 0, 0, time.UTC)},
		{"five_hour", 0.5, time.Time{}}, // unparseable reset time: zero, reading kept
	}
	if !slices.Equal(got, want) {
		t.Errorf("limits = %+v, want %+v (a reading without percentUsed or kind is dropped)", got, want)
	}
}

// Sessions report the same block's reset time with different fractions of a
// second. Truncated to the second they are one block, so the tracker keeps
// the higher reading instead of a stale lower one whose reset time happens to
// be a few milliseconds later.
func TestUsageResetTimeIsWholeSeconds(t *testing.T) {
	resets := func(stamp string) time.Time {
		t.Helper()
		c, fake := newClient(t)
		fake.On(cmdruntest.MatchName(bin)).Return(cmdruntest.Out(
			`{"session":"s1","result":{"rateLimits":[{"kind":"five_hour","percentUsed":40,"resetsAt":"` + stamp + `"}]}}`,
		))
		u, err := c.Usage(t.Context(), 42, now)
		if err != nil || len(u.Limits) != 1 {
			t.Fatalf("usage = %+v, %v; want one limit", u, err)
		}
		return u.Limits[0].ResetsAt
	}
	a, b := resets("2026-11-16T20:00:00.001Z"), resets("2026-11-16T20:00:00.999Z")
	if want := time.Date(2026, 11, 16, 20, 0, 0, 0, time.UTC); !a.Equal(want) || !b.Equal(want) {
		t.Errorf("resets = %v and %v, want both %v", a, b, want)
	}
}

func TestUsageBadReport(t *testing.T) {
	c, fake := newClient(t)
	fake.On(cmdruntest.MatchName(bin)).Return(cmdruntest.Out("not json\n"))
	if _, err := c.Usage(t.Context(), 42, now); err == nil {
		t.Fatal("want an error for an unparseable report")
	}
}

func TestUsageMapsExitCodes(t *testing.T) {
	cases := []struct {
		code int
		want error
	}{
		{3, control.ErrNoControl},
		{4, control.ErrNoProcess},
		{6, control.ErrNoReply},
		{9, control.ErrBusy},
		{21, control.ErrRefused},
		{22, control.ErrAmbiguous},
	}
	sentinels := []error{control.ErrNoControl, control.ErrNoProcess, control.ErrNoReply, control.ErrBusy, control.ErrRefused, control.ErrAmbiguous}
	for _, tc := range cases {
		t.Run(tc.want.Error(), func(t *testing.T) {
			c, fake := newClient(t)
			fake.On(cmdruntest.MatchName(bin)).Return(cmdruntest.Fail(tc.code, `{"error":"example","exit":0,"message":"example-detail"}`+"\n"))
			_, err := c.Usage(t.Context(), 42, now)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			for _, s := range sentinels {
				if s != tc.want && errors.Is(err, s) {
					t.Errorf("err = %v also matches %v", err, s)
				}
			}
			if !strings.Contains(err.Error(), "example-detail") {
				t.Errorf("err = %q, want the stderr text in it", err)
			}
		})
	}
	t.Run("other exit", func(t *testing.T) {
		c, fake := newClient(t)
		fake.On(cmdruntest.MatchName(bin)).Return(cmdruntest.Fail(2, "error: usage\nexample-usage-text\n"))
		_, err := c.Usage(t.Context(), 42, now)
		if err == nil || !strings.Contains(err.Error(), "exit 2") || !strings.Contains(err.Error(), "example-usage-text") {
			t.Fatalf("err = %v, want exit 2 with the stderr text", err)
		}
		for _, s := range sentinels {
			if errors.Is(err, s) {
				t.Errorf("err = %v matches %v", err, s)
			}
		}
	})
	t.Run("never started", func(t *testing.T) {
		c, fake := newClient(t)
		boom := errors.New("example: no such file")
		fake.On(cmdruntest.MatchName(bin)).Return(cmdrun.Result{Code: -1, StartErr: boom})
		if _, err := c.Usage(t.Context(), 42, now); !errors.Is(err, boom) {
			t.Fatalf("err = %v, want it to wrap the start error", err)
		}
	})
}

func TestSubmitSendsStdin(t *testing.T) {
	c, fake := newClient(t)
	fake.On(cmdruntest.MatchName(bin)).Return(cmdruntest.Out("sent; effect not confirmed\n"))
	if err := c.Submit(t.Context(), 42, "context is at 81%: please wrap up"); err != nil {
		t.Fatal(err)
	}
	call := only(t, fake)
	checkCall(t, call, control.CallTimeout, "submit", "pid:42")
	if string(call.Stdin) != "context is at 81%: please wrap up" {
		t.Errorf("stdin = %q", call.Stdin)
	}
}

func TestSubmitMapsExitCodes(t *testing.T) {
	c, fake := newClient(t)
	fake.On(cmdruntest.MatchName(bin)).Return(cmdruntest.Fail(4, "error: no_such_process\npid 42 is not a running Claude\n"))
	if err := c.Submit(t.Context(), 42, "hello"); !errors.Is(err, control.ErrNoProcess) {
		t.Fatalf("err = %v, want ErrNoProcess", err)
	}
}

func TestInterruptArgv(t *testing.T) {
	c, fake := newClient(t)
	fake.On(cmdruntest.MatchName(bin)).Return(cmdruntest.Out("sent; confirmed by the mod\n"))
	if err := c.Interrupt(t.Context(), 42); err != nil {
		t.Fatal(err)
	}
	checkCall(t, only(t, fake), control.CallTimeout, "interrupt", "pid:42", "--reply")
}

func TestInterruptNoTurnIsSuccess(t *testing.T) {
	c, fake := newClient(t)
	fake.On(cmdruntest.MatchName(bin)).Return(cmdruntest.Fail(21, "error: refused_by_mod\nno turn\n"))
	if err := c.Interrupt(t.Context(), 42); err != nil {
		t.Fatalf("err = %v, want nil for a no-turn refusal", err)
	}

	c, fake = newClient(t)
	fake.On(cmdruntest.MatchName(bin)).Return(cmdruntest.Fail(21, "error: refused_by_mod\nexample-other-reason\n"))
	if err := c.Interrupt(t.Context(), 42); !errors.Is(err, control.ErrRefused) {
		t.Fatalf("err = %v, want ErrRefused for another refusal", err)
	}

	c, fake = newClient(t)
	fake.On(cmdruntest.MatchName(bin)).Return(cmdruntest.Fail(3, "error: no_control_mod\nno turn\n"))
	if err := c.Interrupt(t.Context(), 42); !errors.Is(err, control.ErrNoControl) {
		t.Fatalf("err = %v, want ErrNoControl: only a refusal can be a no-turn success", err)
	}
}

func TestStopArgv(t *testing.T) {
	c, fake := newClient(t)
	fake.On(cmdruntest.MatchName(bin)).Return(cmdruntest.Out("pid 42 stopped\n"))
	if err := c.Stop(t.Context(), 42, 2*time.Minute); err != nil {
		t.Fatal(err)
	}
	checkCall(t, only(t, fake), 2*time.Minute+15*time.Second, "stop", "pid:42", "--escalate", "2m0s")
}

func TestStopRejectsNonPositiveEscalate(t *testing.T) {
	c, fake := newClient(t)
	for _, d := range []time.Duration{0, -time.Second} {
		if err := c.Stop(t.Context(), 42, d); err == nil {
			t.Errorf("Stop(escalate %v) = nil, want an error", d)
		}
	}
	if n := len(fake.Calls()); n != 0 {
		t.Errorf("calls = %d, want none", n)
	}
}

func TestSignalArgv(t *testing.T) {
	c, fake := newClient(t)
	fake.On(cmdruntest.MatchName(bin)).Return(cmdruntest.Out("sent SIGTERM to pid 42\n"))
	if err := c.Signal(t.Context(), 42); err != nil {
		t.Fatal(err)
	}
	checkCall(t, only(t, fake), control.CallTimeout, "stop", "pid:42", "--signal")
}

// TestControlCancelledContext scripts the Result a cancelled or timed-out run
// produces (the Fake ignores a done context) and checks every call wraps it.
func TestControlCancelledContext(t *testing.T) {
	calls := map[string]func(control.Client, context.Context) error{
		"usage": func(c control.Client, ctx context.Context) error {
			_, err := c.Usage(ctx, 42, now)
			return err
		},
		"submit":    func(c control.Client, ctx context.Context) error { return c.Submit(ctx, 42, "hello") },
		"interrupt": func(c control.Client, ctx context.Context) error { return c.Interrupt(ctx, 42) },
		"stop":      func(c control.Client, ctx context.Context) error { return c.Stop(ctx, 42, time.Minute) },
		"signal":    func(c control.Client, ctx context.Context) error { return c.Signal(ctx, 42) },
	}
	results := map[string]struct {
		res  cmdrun.Result
		want error
	}{
		"cancelled": {cmdrun.Result{Code: -1, Signal: "killed", CtxErr: context.Canceled}, context.Canceled},
		"timed out": {cmdrun.Result{Code: -1, Signal: "killed", CtxErr: context.DeadlineExceeded, TimedOut: true}, context.DeadlineExceeded},
		// A refusal text on stderr must not turn a cancelled interrupt into success.
		"cancelled refusal": {cmdrun.Result{Code: 21, Stderr: []byte("no turn"), CtxErr: context.Canceled}, context.Canceled},
	}
	for name, call := range calls {
		for rname, r := range results {
			t.Run(name+"/"+rname, func(t *testing.T) {
				c, fake := newClient(t)
				fake.On(cmdruntest.MatchName(bin)).Return(r.res)
				if err := call(c, t.Context()); !errors.Is(err, r.want) {
					t.Fatalf("err = %v, want it to wrap %v", err, r.want)
				}
			})
		}
	}
}
