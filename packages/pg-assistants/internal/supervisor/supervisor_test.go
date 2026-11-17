package supervisor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-assistants/internal/blocks"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-assistants/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-assistants/internal/control"
	"github.com/phillipgreenii/x/tmux"
)

const permissionHint = "did not exit after the nudge (a permission prompt?)"

func controlHint(id string) string {
	return "control mod not answering (shell startup waiting? trust dialog? plugin disabled?): pg-assistants attach " + id
}

func TestStartsEnabledAssistant(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, exampleA)
		h.cfg.BlankEnv = []string{"EXAMPLE_GUARD"}
		h.start()
		defer h.stop()
		h.until(3 * time.Minute)

		starts := h.w.callsOf("new-session", exampleA)
		if len(starts) == 1 && !slices.Contains(starts[0].env, "EXAMPLE_GUARD=") {
			t.Errorf("env = %q, want the config's blank_env entry EXAMPLE_GUARD=", starts[0].env)
		}
		if len(starts) != 1 {
			t.Fatalf("new-session calls = %d, want 1", len(starts))
		}
		a, c := *h.a(exampleA), starts[0]
		if c.at != 0 {
			t.Errorf("started at %s, want on the first tick", c.at)
		}
		if c.cwd != LaunchCwd(a) {
			t.Errorf("cwd = %q, want %q", c.cwd, LaunchCwd(a))
		}
		if want := LaunchEnv(a, h.cfg.Paths.StateDir, h.cfg.BlankEnv); !slices.Equal(c.env, want) {
			t.Errorf("env = %q, want %q", c.env, want)
		}
		if want := LaunchArgv(h.cfg.Paths, a, "uuid-1"); !slices.Equal(c.argv, want) {
			t.Errorf("argv = %q, want %q", c.argv, want)
		}
		ev := h.eventsFor("start", exampleA)
		if len(ev) != 1 || ev[0]["session_id"] != "uuid-1" || num(ev[0]["pid"]) != firstPID {
			t.Errorf("start events = %v, want one with session uuid-1 and pid %d", ev, firstPID)
		}
		st := h.statusOf(exampleA)
		if !st.Running || st.PID != firstPID || !st.StartedAt.Equal(h.t0) || !st.LastTick.Equal(h.t0.Add(3*time.Minute)) {
			t.Errorf("status = %+v, want running pid %d started at the bubble start, last tick at 3m", st, firstPID)
		}
		wantBudgets(t, h)
	})
}

func TestDoesNotStartDisabled(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, exampleA)
		h.a(exampleA).Enabled = false
		h.start()
		defer h.stop()
		h.until(3 * time.Minute)
		if n := len(h.w.callsOf("new-session", "")); n != 0 {
			t.Errorf("new-session calls = %d, want 0", n)
		}
		if st := h.statusOf(exampleA); st.Enabled || st.Running || st.WaitReason != "" {
			t.Errorf("status = %+v, want disabled, not running, not waiting", st)
		}
	})
}

func TestAdoptsRunningSessionOnStart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, exampleA)
		pid := h.w.addSession(exampleA)
		h.w.usage = func(_ context.Context, _ string, _ int, _ time.Time) (control.Usage, error) {
			return control.Usage{SessionID: "sess-adopted", HaveContext: true, ContextTokens: 1000}, nil
		}
		h.start()
		defer h.stop()
		h.until(time.Minute)

		if n := len(h.w.callsOf("new-session", "")); n != 0 {
			t.Errorf("new-session calls = %d, want 0", n)
		}
		ev := h.eventsFor("adopt", exampleA)
		if len(ev) != 1 || num(ev[0]["pid"]) != float64(pid) {
			t.Errorf("adopt events = %v, want one with pid %d", ev, pid)
		}
		wantTimes(t, "pane-pid", h.w.times("pane-pid", exampleA), minutes(0))
		usage := h.w.callsOf("usage", exampleA)
		if len(usage) == 0 {
			t.Fatal("no usage call")
		}
		for _, c := range usage {
			if c.pid != pid {
				t.Errorf("usage at %s targets pid %d, want %d", c.at, c.pid, pid)
			}
		}
		if st := h.statusOf(exampleA); st.SessionID != "sess-adopted" || st.PID != pid || !st.Running || !st.StartedAt.Equal(h.t0) {
			t.Errorf("status = %+v, want session sess-adopted, pid %d, started now", st, pid)
		}
	})
}

func TestContextLadderSequence(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, exampleA)
		tokens := []int64{160000, 195000, 200000}
		h.w.usage = func(_ context.Context, name string, n int, _ time.Time) (control.Usage, error) {
			return control.Usage{SessionID: "sess-" + name, HaveContext: true, ContextTokens: tokens[min(n, len(tokens))-1]}, nil
		}
		h.w.kill = func(string) error { return nil } // the session survives every kill
		h.start()
		defer h.stop()
		h.until(5 * time.Minute)

		wantActions(t, h, exampleA,
			"new-session@0s", "submit@1m0s", "interrupt@2m0s", "submit@2m0s", "signal@3m0s", "kill@4m0s", "kill@5m0s")
		submits := h.w.callsOf("submit", exampleA)
		if len(submits) == 2 {
			if want := NudgeText("context at 80% (160000 of 200000 tokens)"); submits[0].text != want {
				t.Errorf("nudge text = %q, want %q", submits[0].text, want)
			}
			if want := StopText("context at 97% (195000 of 200000 tokens)"); submits[1].text != want {
				t.Errorf("stop text = %q, want %q", submits[1].text, want)
			}
		}
		for _, ev := range []string{"nudge", "interrupt", "kill"} {
			if n := len(h.eventsFor(ev, exampleA)); n != 1 {
				t.Errorf("%s events = %d, want 1", ev, n)
			}
		}
		if n := len(h.eventsFor("force_kill", exampleA)); n != 2 {
			t.Errorf("force_kill events = %d, want 2", n)
		}
		st := h.statusOf(exampleA)
		if st.Ladder != "meter" || st.Rung != "force-kill" || st.Hint != "" {
			t.Errorf("status ladder %q rung %q hint %q, want meter, force-kill and no hint", st.Ladder, st.Rung, st.Hint)
		}
		wantBudgets(t, h)
	})
}

func TestNudgedIdleSessionIsRecycled(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, exampleA)
		h.w.usage = usageTokens(165000)
		h.start()
		defer h.stop()

		h.until(3 * time.Minute)
		st := h.statusOf(exampleA)
		if st.Rung != "nudge" || st.Reason != "context at 82% (165000 of 200000 tokens)" || st.Hint != "" {
			t.Errorf("after the nudge: rung %q reason %q hint %q; want nudge, the nudge's reason kept, no hint",
				st.Rung, st.Reason, st.Hint)
		}
		if st.ContextTokens != 165000 || st.ContextFraction != 0.825 || !st.ContextAt.Equal(h.t0.Add(3*time.Minute)) {
			t.Errorf("context %d %v at %v, want 165000 0.825 at 3m", st.ContextTokens, st.ContextFraction, st.ContextAt)
		}

		h.until(8 * time.Minute)
		wantActions(t, h, exampleA, "new-session@0s", "submit@1m0s", "interrupt@6m0s", "submit@6m0s", "signal@8m0s")
		if st := h.statusOf(exampleA); st.Hint != permissionHint {
			t.Errorf("hint = %q, want %q", st.Hint, permissionHint)
		}
	})
}

func TestTimeFallbackWhileUsageFails(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, exampleA)
		h.w.usage = func(_ context.Context, name string, n int, _ time.Time) (control.Usage, error) {
			if n == 1 {
				return control.Usage{SessionID: "sess-" + name, HaveContext: true, ContextTokens: 165000}, nil
			}
			return control.Usage{}, fmt.Errorf("pg-claude-control usage: %w: timed out", control.ErrNoReply)
		}
		h.start()
		defer h.stop()
		h.until(8 * time.Minute)
		wantActions(t, h, exampleA, "new-session@0s", "submit@1m0s", "interrupt@6m0s", "submit@6m0s", "signal@8m0s")
		if n := len(h.eventsFor("usage_failed", exampleA)); n != 1 {
			t.Errorf("usage_failed events = %d, want 1 (logged once while the error stays the same)", n)
		}
		if st := h.statusOf(exampleA); !strings.Contains(st.LastError, "no reply") {
			t.Errorf("last error = %q, want the usage failure", st.LastError)
		}
	})
}

func TestBlockLadderAndHold(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, exampleA, exampleB)
		reset := h.t0.Add(3*time.Hour + 30*time.Second)
		var observed sync.Map // tick time -> true, for every reading's ObservedAt
		h.w.usage = func(_ context.Context, name string, n int, now time.Time) (control.Usage, error) {
			frac := 0.98
			if n == 1 {
				frac = 0.95
			}
			observed.Store(now, true)
			return control.Usage{
				SessionID: "sess-" + name, HaveContext: true, ContextTokens: 1000,
				Limits: []control.Limit{{Kind: control.KindFiveHour, Fraction: frac, ResetsAt: reset, ObservedAt: now}},
			}, nil
		}
		h.start()
		defer h.stop()

		h.until(2 * time.Minute)
		for _, id := range []string{exampleA, exampleB} {
			wantActions(t, h, id, "new-session@0s", "submit@1m0s", "interrupt@2m0s", "submit@2m0s")
			if sub := h.w.callsOf("submit", id); len(sub) > 0 && !strings.Contains(sub[0].text, "five_hour at 95%") {
				t.Errorf("%s nudge = %q, want it to name five_hour at 95%%", id, sub[0].text)
			}
		}
		observed.Range(func(k, _ any) bool {
			if at := k.(time.Time).Sub(h.t0); at%time.Minute != 0 {
				t.Errorf("a reading observed at %s, want the tick time", at)
			}
			return true
		})

		// Both sessions end; no new start until the block resets.
		h.until(2*time.Minute + 30*time.Second)
		h.w.die(exampleA, tmux.DeadInfo{})
		h.w.die(exampleB, tmux.DeadInfo{})
		h.until(3 * time.Hour)
		for _, id := range []string{exampleA, exampleB} {
			st := h.statusOf(id)
			if st.Running || st.WaitReason != "block_hold" || !st.WaitUntil.Equal(reset) {
				t.Errorf("%s at 3h: running %v wait %q until %v, want block_hold until the reset", id, st.Running, st.WaitReason, st.WaitUntil)
			}
			wantTimes(t, id+" new-session", h.w.times("new-session", id), minutes(0))
		}
		if b := h.snapshot().Blocks; len(b) != 1 || b[0].Fraction != 0.98 {
			t.Errorf("status blocks = %+v, want the five_hour reading at 0.98", b)
		}

		h.until(3*time.Hour + time.Minute)
		for _, id := range []string{exampleA, exampleB} {
			wantTimes(t, id+" new-session", h.w.times("new-session", id), minutes(0, 181))
		}
	})
}

func TestHoldSurvivesDaemonRestart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, exampleA)
		reset := h.t0.Add(time.Hour + 30*time.Second)
		h.blocks = blocks.NewTracker(h.cfg.Blocks)
		h.blocks.Seed([]control.Limit{{Kind: control.KindFiveHour, Fraction: 0.96, ResetsAt: reset, ObservedAt: h.t0.Add(-10 * time.Minute)}}, h.t0)
		h.start()
		defer h.stop()

		h.until(time.Hour)
		if n := len(h.w.callsOf("new-session", "")); n != 0 {
			t.Errorf("new-session calls before the reset = %d, want 0", n)
		}
		if st := h.statusOf(exampleA); st.WaitReason != "block_hold" || !st.WaitUntil.Equal(reset) {
			t.Errorf("wait %q until %v, want block_hold until %v", st.WaitReason, st.WaitUntil, reset)
		}
		h.until(time.Hour + time.Minute)
		wantTimes(t, "new-session", h.w.times("new-session", exampleA), minutes(61))
	})
}

func TestDisabledLadderWithoutControlMod(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, exampleA)
		h.a(exampleA).Enabled = false
		h.w.addSession(exampleA)
		noMod := fmt.Errorf("pg-claude-control: %w: no inbox", control.ErrNoControl)
		h.w.usage = func(context.Context, string, int, time.Time) (control.Usage, error) { return control.Usage{}, noMod }
		h.w.submit = func(string, string) error { return noMod }
		h.w.interrupt = func(string) error { return noMod }
		h.w.signal = func(string) error { return noMod }
		h.start()
		defer h.stop()

		h.until(5 * time.Minute)
		if st := h.statusOf(exampleA); !strings.Contains(st.Hint, permissionHint) || !strings.Contains(st.Hint, controlHint(exampleA)) {
			t.Errorf("hint = %q, want both the permission-prompt and the control-mod hints", st.Hint)
		}
		h.until(9 * time.Minute)
		wantActions(t, h, exampleA, "submit@0s", "interrupt@5m0s", "signal@7m0s", "kill@8m0s")
		var failed []string
		for _, ev := range h.eventsFor("action_failed", exampleA) {
			failed = append(failed, fmt.Sprint(ev["action"]))
			if !strings.Contains(fmt.Sprint(ev["error"]), "no control mod") {
				t.Errorf("action_failed error = %v, want the control failure", ev["error"])
			}
		}
		if want := []string{"nudge", "interrupt", "kill"}; !slices.Equal(failed, want) {
			t.Errorf("action_failed actions = %q, want %q", failed, want)
		}
		if n := len(h.eventsFor("force_kill", exampleA)); n != 1 {
			t.Errorf("force_kill events = %d, want 1", n)
		}
		if st := h.statusOf(exampleA); st.Running {
			t.Error("still running after the kill")
		}
	})
}

func TestHintOnNoControlProcessOrBusy(t *testing.T) {
	for _, sentinel := range []error{control.ErrNoControl, control.ErrNoProcess, control.ErrBusy} {
		t.Run(sentinel.Error(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := newHarness(t, exampleA)
				h.w.usage = func(_ context.Context, name string, n int, _ time.Time) (control.Usage, error) {
					if n <= 3 {
						return control.Usage{}, fmt.Errorf("pg-claude-control usage: %w: x", sentinel)
					}
					return control.Usage{SessionID: "sess-" + name, HaveContext: true, ContextTokens: 1000}, nil
				}
				h.start()
				defer h.stop()
				h.until(2 * time.Minute)
				if st := h.statusOf(exampleA); st.Hint != "" {
					t.Errorf("hint after two failures = %q, want none", st.Hint)
				}
				h.until(3 * time.Minute)
				if st := h.statusOf(exampleA); st.Hint != controlHint(exampleA) {
					t.Errorf("hint after three failures = %q, want %q", st.Hint, controlHint(exampleA))
				}
				h.until(4 * time.Minute)
				if st := h.statusOf(exampleA); st.Hint != "" || st.LastError != "" {
					t.Errorf("after an answer: hint %q, last error %q; want both cleared", st.Hint, st.LastError)
				}
			})
		})
	}
}

func TestStartStallIsKilledAndBacksOff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, exampleA)
		h.w.usage = func(context.Context, string, int, time.Time) (control.Usage, error) {
			return control.Usage{}, fmt.Errorf("pg-claude-control usage: %w: waited 5s", control.ErrNoReply)
		}
		h.start()
		defer h.stop()

		h.until(7 * time.Minute)
		st := h.statusOf(exampleA)
		if st.Running || st.WaitReason != "backoff" || !st.WaitUntil.Equal(h.t0.Add(8*time.Minute)) {
			t.Errorf("after the stall: running %v wait %q until %v, want backoff until 8m", st.Running, st.WaitReason, st.WaitUntil)
		}
		if !strings.Contains(st.LastError, "stalled") || !strings.Contains(st.LastError, "no reply") {
			t.Errorf("last error = %q, want the stall and the last usage error", st.LastError)
		}

		h.until(18 * time.Minute)
		wantTimes(t, "new-session", h.w.times("new-session", exampleA), minutes(0, 8, 17))
		wantTimes(t, "signal", h.w.times("signal", exampleA), minutes(5, 13))
		wantTimes(t, "kill", h.w.times("kill", exampleA), minutes(6, 14))
		ev := h.eventsFor("start_stalled", exampleA)
		if len(ev) != 2 {
			t.Fatalf("start_stalled events = %d, want 2", len(ev))
		}
		if !strings.Contains(fmt.Sprint(ev[0]["error"]), "no reply") {
			t.Errorf("start_stalled error = %v, want the last usage error", ev[0]["error"])
		}
	})
}

func TestExitRequestStopsGracefully(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, exampleA)
		h.a(exampleA).Tick = 30 * time.Second
		h.start()
		defer h.stop()

		h.until(15 * time.Second)
		h.requestExit(exampleA)
		h.until(30 * time.Second)
		stops := h.w.callsOf("stop", exampleA)
		if len(stops) != 1 || stops[0].pid != firstPID || stops[0].escalate != 2*time.Minute {
			t.Fatalf("stop calls = %+v, want one for pid %d escalating after 2m", stops, firstPID)
		}
		if !h.exitRequested(exampleA) {
			t.Error("the exit request was cleared while the session still runs")
		}
		if n := len(h.eventsFor("graceful_stop", exampleA)); n != 1 {
			t.Errorf("graceful_stop events = %d, want 1", n)
		}

		h.until(time.Minute)
		if h.exitRequested(exampleA) {
			t.Error("the exit request is still on file after the session ended")
		}
		// Seen gone after 30 s, under min_uptime, yet it restarts at once:
		// a requested exit never counts as a crash.
		wantTimes(t, "new-session", h.w.times("new-session", exampleA), []time.Duration{0, time.Minute})
		st := h.statusOf(exampleA)
		if st.LastExit == nil || st.LastExit.UptimeS != 30 || st.WaitReason != "" {
			t.Errorf("status last exit %+v wait %q, want uptime 30 s and no wait", st.LastExit, st.WaitReason)
		}
		ev := h.eventsFor("exited", exampleA)
		if len(ev) != 1 || ev[0]["exit_requested"] != true {
			t.Errorf("exited events = %v, want one with exit_requested", ev)
		}
		wantBudgets(t, h)
	})
}

func TestExitRequestSurvivesCancelledStop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, exampleA)
		h.w.addSession(exampleA)
		h.requestExit(exampleA)
		var stopErr error
		h.w.stop = func(ctx context.Context, _ string) error {
			<-ctx.Done()
			stopErr = ctx.Err()
			return ctx.Err()
		}
		h.start()
		if n := len(h.w.callsOf("stop", exampleA)); n != 1 {
			t.Fatalf("stop calls = %d, want 1 in flight", n)
		}
		h.stop()
		if !errors.Is(stopErr, context.Canceled) {
			t.Errorf("stop saw %v, want context.Canceled", stopErr)
		}
		if !h.exitRequested(exampleA) {
			t.Error("the exit request was removed by the cancelled stop")
		}
	})
}

func TestAdoptedSessionWithPendingExitRequestIsStopped(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, exampleA)
		pid := h.w.addSession(exampleA)
		h.requestExit(exampleA)
		h.start()
		defer h.stop()
		stops := h.w.callsOf("stop", exampleA)
		if len(stops) != 1 || stops[0].at != 0 || stops[0].pid != pid {
			t.Errorf("stop calls = %+v, want one on the first tick for pid %d", stops, pid)
		}
		if n := len(h.w.callsOf("new-session", "")); n != 0 {
			t.Errorf("new-session calls = %d, want 0", n)
		}
	})
}

func TestExitRequestWithoutModForceKills(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, exampleA)
		h.w.addSession(exampleA)
		h.requestExit(exampleA)
		h.w.stop = func(context.Context, string) error {
			return fmt.Errorf("pg-claude-control stop: %w: no inbox", control.ErrNoControl)
		}
		h.start()
		defer h.stop()
		h.until(time.Minute)
		wantActions(t, h, exampleA, "stop@0s", "kill@1m0s")
		ev := h.eventsFor("action_failed", exampleA)
		if len(ev) != 1 || ev[0]["action"] != "graceful_stop" {
			t.Errorf("action_failed events = %v, want one for graceful_stop", ev)
		}
	})
}

func TestLeftoverExitRequestClearedBeforeStart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, exampleA)
		h.requestExit(exampleA)
		h.start()
		defer h.stop()
		if h.exitRequested(exampleA) {
			t.Error("the leftover exit request is still on file after the start")
		}
		h.until(2 * time.Minute)
		wantActions(t, h, exampleA, "new-session@0s")
	})
}

func TestManualKillResetsLadder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, exampleA)
		h.w.usage = func(_ context.Context, name string, n int, _ time.Time) (control.Usage, error) {
			tokens := int64(195000)
			if n > 2 {
				tokens = 100000
			}
			return control.Usage{SessionID: "sess-" + name, HaveContext: true, ContextTokens: tokens}, nil
		}
		h.start()
		defer h.stop()
		h.until(2 * time.Minute)
		if st := h.statusOf(exampleA); st.Rung != "interrupt" {
			t.Fatalf("rung = %q, want interrupt", st.Rung)
		}
		h.until(2*time.Minute + 30*time.Second)
		h.w.remove(exampleA) // killed by hand
		h.until(10 * time.Minute)
		// Seen alive for 2m, so it restarts at once; the new session starts
		// at the bottom, so the old interrupt's kill step never fires.
		wantActions(t, h, exampleA, "new-session@0s", "submit@1m0s", "interrupt@2m0s", "submit@2m0s", "new-session@3m0s")
		if st := h.statusOf(exampleA); st.Rung != "" || st.Ladder != "meter" || st.PID != firstPID+1 {
			t.Errorf("new session: ladder %q rung %q pid %d, want meter, no rung, pid %d", st.Ladder, st.Rung, st.PID, firstPID+1)
		}
		if ev := h.eventsFor("exited", exampleA); len(ev) != 1 || num(ev[0]["uptime_s"]) != 120 {
			t.Errorf("exited events = %v, want one with uptime 120 s", ev)
		}
	})
}

func TestCrashBeforeFirstTickBacksOff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, exampleA)
		var starts int
		h.w.newSession = func(name string) error {
			starts++
			h.w.addSession(name)
			if starts != 4 { // every session but the fourth crashes before the next tick
				h.w.die(name, tmux.DeadInfo{Status: 1})
			}
			return nil
		}
		h.start()
		defer h.stop()

		h.until(time.Minute)
		if st := h.statusOf(exampleA); st.WaitReason != "backoff" || !st.WaitUntil.Equal(h.t0.Add(2*time.Minute)) {
			t.Errorf("after the first crash: wait %q until %v, want backoff until 2m", st.WaitReason, st.WaitUntil)
		}
		h.until(20*time.Minute + 30*time.Second)
		h.w.die(exampleA, tmux.DeadInfo{}) // the fourth session ends after 10m alive
		h.until(120 * time.Minute)
		// 1m, 2m, 4m; a 10m session resets it; then 1m, 2m, ... capped at 30m.
		wantTimes(t, "new-session", h.w.times("new-session", exampleA),
			minutes(0, 2, 5, 10, 21, 23, 26, 31, 40, 57, 88, 119))
	})
}

func TestStartFailureBacksOff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, exampleA)
		h.w.newSession = func(string) error { return errors.New("tmux: exit 1: duplicate session") }
		h.start()
		defer h.stop()
		h.until(7 * time.Minute)
		wantTimes(t, "new-session", h.w.times("new-session", exampleA), minutes(0, 1, 3, 7))
		ev := h.eventsFor("start_failed", exampleA)
		if len(ev) != 4 || ev[0]["step"] != "new-session" {
			t.Errorf("start_failed events = %v, want four for new-session", ev)
		}
	})
}

func TestDeadPaneRecordsLastExit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, exampleA)
		var starts int
		h.w.newSession = func(name string) error {
			starts++
			h.w.addSession(name)
			if starts == 1 {
				h.w.die(name, tmux.DeadInfo{Status: 3, Tail: "boom\n"})
			}
			return nil
		}
		h.start()
		defer h.stop()
		h.until(2 * time.Minute)

		dead := h.w.callsOf("dead", exampleA)
		if len(dead) != 1 || dead[0].tail != 40 {
			t.Errorf("dead calls = %+v, want one reading 40 lines", dead)
		}
		wantActions(t, h, exampleA, "new-session@0s", "kill@1m0s", "new-session@2m0s")
		body, err := os.ReadFile(filepath.Join(h.cfg.Paths.StateDir, "last-exit", exampleA+".txt"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), "status: 3") || !strings.Contains(string(body), "boom") {
			t.Errorf("last-exit file = %q, want status 3 and the tail", body)
		}
		ev := h.eventsFor("exited", exampleA)
		if len(ev) != 1 || num(ev[0]["status"]) != 3 {
			t.Errorf("exited events = %v, want one with status 3", ev)
		}
		if st := h.statusOf(exampleA); st.LastExit == nil || st.LastExit.Status != 3 {
			t.Errorf("status last exit = %+v, want status 3", st.LastExit)
		}
	})
}

func TestMissingCwdRetriesEveryTick(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, exampleA, exampleB)
		cwd := h.a(exampleA).Cwd
		if err := os.Remove(cwd); err != nil {
			t.Fatal(err)
		}
		var mu sync.Mutex
		var stats []time.Duration
		h.stat = func(p string) (os.FileInfo, error) {
			if p == cwd {
				mu.Lock()
				stats = append(stats, h.now())
				mu.Unlock()
			}
			return os.Stat(p)
		}
		h.start()
		defer h.stop()

		h.until(3 * time.Minute)
		st := h.statusOf(exampleA)
		if st.WaitReason != "missing_cwd" || !st.WaitUntil.IsZero() || st.Running {
			t.Errorf("status wait %q until %v running %v, want missing_cwd, no wait time, not running", st.WaitReason, st.WaitUntil, st.Running)
		}
		mu.Lock()
		wantTimes(t, "stat", stats, minutes(0, 1, 2, 3))
		mu.Unlock()
		wantTimes(t, exampleB+" new-session", h.w.times("new-session", exampleB), minutes(0))

		h.until(3*time.Minute + 30*time.Second)
		if err := os.Mkdir(cwd, 0o700); err != nil {
			t.Fatal(err)
		}
		h.until(4 * time.Minute)
		wantTimes(t, exampleA+" new-session", h.w.times("new-session", exampleA), minutes(4))
		if ev := h.eventsFor("start_failed", exampleA); len(ev) != 1 || ev[0]["step"] != "cwd" {
			t.Errorf("start_failed events = %v, want one for the cwd", ev)
		}
	})
}

// Trust is read and written under the resolved directory, the key Claude
// looks up; the existence check and tmux get the configured path.
func TestTrustUsesTheResolvedCwd(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, exampleA)
		realDir := h.a(exampleA).Cwd
		link := filepath.Join(h.root, "work", "example-a-link")
		if err := os.Symlink(realDir, link); err != nil {
			t.Fatal(err)
		}
		resolved, err := filepath.EvalSymlinks(realDir)
		if err != nil {
			t.Fatal(err)
		}
		h.a(exampleA).Cwd = link
		h.w.untrust[resolved] = true
		h.start()
		defer h.stop()
		for _, op := range []string{"is-trusted", "ensure"} {
			if cs := h.w.callsOf(op, exampleA); len(cs) != 1 || cs[0].cwd != resolved {
				t.Errorf("%s calls = %+v, want one for the resolved %s", op, cs, resolved)
			}
		}
		if starts := h.w.callsOf("new-session", exampleA); len(starts) != 1 || starts[0].cwd != LaunchCwd(*h.a(exampleA)) {
			t.Errorf("new-session calls = %+v, want one in the configured %s", starts, link)
		}
	})
}

func TestUntrustedCwdIsTrustedBeforeStart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, exampleA)
		// trust is keyed by the resolved directory (the temporary directory
		// may itself sit under a symlink)
		cwd := config.ResolvePath(h.a(exampleA).Cwd)
		h.w.untrust[cwd] = true
		h.start()
		defer h.stop()
		var order []string
		for _, c := range h.w.callsOf("", exampleA) {
			if c.op == "is-trusted" || c.op == "ensure" || c.op == "new-session" {
				order = append(order, c.op)
			}
		}
		if want := []string{"is-trusted", "ensure", "new-session"}; !slices.Equal(order, want) {
			t.Errorf("calls = %q, want %q", order, want)
		}
		if ens := h.w.callsOf("ensure", exampleA); len(ens) != 1 || ens[0].cwd != cwd {
			t.Errorf("ensure calls = %+v, want one for %s", ens, cwd)
		}
	})
}

func TestAssistantWithProblemsIsNotStarted(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, exampleA, "example-bad")
		h.a("example-bad").Problems = []config.FieldError{{Field: "assistant[example-bad].model", Msg: "unknown model"}}
		h.start()
		defer h.stop()
		h.until(time.Minute)

		if slices.Contains(h.group.names(), "assistant:example-bad") {
			t.Error("the assistant with problems got a loop")
		}
		if n := len(h.w.callsOf("new-session", "example-bad")); n != 0 {
			t.Errorf("new-session calls for the bad assistant = %d, want 0", n)
		}
		if st := h.statusOf("example-bad"); st.ConfigError != "assistant[example-bad].model: unknown model" || st.Running {
			t.Errorf("status = %+v, want the config error", st)
		}
		if ev := h.eventsFor("config_problem", "example-bad"); len(ev) != 1 || ev[0]["field"] != "assistant[example-bad].model" {
			t.Errorf("config_problem events = %v, want one naming the field", ev)
		}
		wantTimes(t, exampleA+" new-session", h.w.times("new-session", exampleA), minutes(0))
	})
}

func TestSlowAssistantDoesNotDelayOthers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, exampleA, exampleB)
		var mu sync.Mutex
		var slowErrs []error
		h.w.usage = func(ctx context.Context, name string, _ int, _ time.Time) (control.Usage, error) {
			if name == exampleA {
				<-ctx.Done()
				mu.Lock()
				slowErrs = append(slowErrs, ctx.Err())
				mu.Unlock()
				return control.Usage{}, fmt.Errorf("pg-claude-control usage: %w", ctx.Err())
			}
			return control.Usage{SessionID: "sess-" + name, HaveContext: true, ContextTokens: 1000}, nil
		}
		h.start()
		defer h.stop()
		h.until(5 * time.Minute)

		wantTimes(t, exampleB+" usage", h.w.times("usage", exampleB), minutes(1, 2, 3, 4, 5))
		wantTimes(t, exampleB+" state", h.w.times("state", exampleB), minutes(0, 1, 2, 3, 4, 5))
		slow := h.w.callsOf("usage", exampleA)
		if len(slow) == 0 {
			t.Fatal("no usage call for the slow assistant")
		}
		for _, c := range slow {
			if c.budget != 15*time.Second {
				t.Errorf("slow usage at %s had a %s budget, want 15s", c.at, c.budget)
			}
		}
		mu.Lock()
		defer mu.Unlock()
		for _, err := range slowErrs {
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("slow usage ended with %v, want a deadline error", err)
			}
		}
	})
}

func TestHeartbeatFlushesWhileLoopBlockedInStop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, exampleA, exampleB)
		h.w.stop = func(ctx context.Context, name string) error {
			select {
			case <-time.After(2 * time.Minute):
				h.w.die(name, tmux.DeadInfo{})
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		h.start()
		defer h.stop()
		h.until(30 * time.Second)
		h.requestExit(exampleA)
		h.until(time.Minute)
		if n := len(h.w.callsOf("stop", exampleA)); n != 1 {
			t.Fatalf("stop calls = %d, want 1 in flight", n)
		}
		for at := time.Minute + 15*time.Second; at <= 3*time.Minute; at += 15 * time.Second {
			h.until(at)
			s, err := ReadStatus(h.statusPath())
			if err != nil {
				t.Fatal(err)
			}
			if want := h.t0.Add(at); !s.WrittenAt.Equal(want) {
				t.Errorf("at %s written_at = %s, want %s", at, s.WrittenAt.Sub(h.t0), at)
			}
		}
		wantTimes(t, exampleB+" state", h.w.times("state", exampleB), minutes(0, 1, 2, 3))
		if st := h.statusOf(exampleB); !st.LastTick.Equal(h.t0.Add(3 * time.Minute)) {
			t.Errorf("%s last tick = %s, want 3m", exampleB, st.LastTick.Sub(h.t0))
		}
	})
}

// Only the heartbeat writes the status file: a write that hangs holds up the
// heartbeat alone, never an assistant's loop.
func TestBlockedStatusWriteDoesNotStallLoops(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, exampleA, exampleB)
		release := make(chan struct{})
		h.startWith(func(b *StatusBoard) {
			b.write = func(string, Status) error {
				<-release
				return nil
			}
		})
		defer h.stop()
		defer close(release) // runs first: lets the hung write return before the stop
		h.until(3 * time.Minute)
		for _, id := range []string{exampleA, exampleB} {
			wantTimes(t, id+" state", h.w.times("state", id), minutes(0, 1, 2, 3))
		}
	})
}

func TestPanicInOneLoopIsContained(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, exampleA, exampleB)
		var mu sync.Mutex
		var panics int
		h.w.state = func(name string) {
			if name != exampleA {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if panics < 2 {
				panics++
				panic("boom")
			}
		}
		h.start()
		defer h.stop()

		ev := h.eventsNamed("loop_panic")
		if len(ev) != 1 || ev[0]["loop"] != "assistant:example-a" || !strings.Contains(fmt.Sprint(ev[0]["error"]), "boom") ||
			!strings.Contains(fmt.Sprint(ev[0]["stack"]), "goroutine") || num(ev[0]["restart_in_s"]) != 60 {
			t.Errorf("loop_panic events = %v, want one for assistant:example-a naming boom, with a stack, restarting in 60 s", ev)
		}
		h.until(2 * time.Minute)
		ev = h.eventsNamed("loop_panic")
		if len(ev) != 2 || num(ev[1]["restart_in_s"]) != 120 {
			t.Errorf("loop_panic events = %v, want a second one restarting in 120 s", ev)
		}
		if st := h.statusOf(exampleA); !strings.Contains(st.LastError, "panic: boom") {
			t.Errorf("last error = %q, want it to name the panic", st.LastError)
		}
		h.until(3 * time.Minute)
		wantTimes(t, exampleA+" state", h.w.times("state", exampleA), minutes(0, 1, 3))
		wantTimes(t, exampleA+" new-session", h.w.times("new-session", exampleA), minutes(3))
		wantTimes(t, exampleB+" state", h.w.times("state", exampleB), minutes(0, 1, 2, 3))
		wantTimes(t, exampleB+" new-session", h.w.times("new-session", exampleB), minutes(0))
	})
}

func TestCancelStopsInFlightCalls(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, exampleA, exampleB)
		h.w.addSession(exampleA)
		h.requestExit(exampleA)
		var stopErr error
		h.w.stop = func(ctx context.Context, _ string) error {
			<-ctx.Done()
			stopErr = ctx.Err()
			return ctx.Err()
		}
		h.start()
		if n := len(h.w.callsOf("stop", exampleA)); n != 1 {
			t.Fatalf("stop calls = %d, want 1 in flight", n)
		}

		h.cancel()
		done := make(chan struct{})
		go func() {
			h.stop()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("Run and its loops did not return within 1s of the cancel")
		}
		if !errors.Is(stopErr, context.Canceled) {
			t.Errorf("stop saw %v, want context.Canceled", stopErr)
		}
		if n := len(h.w.callsOf("kill", "")); n != 0 {
			t.Errorf("kill calls = %d, want 0: shutdown never touches sessions", n)
		}
		for _, name := range h.group.names() {
			if h.group.endedCount(name) != 1 {
				t.Errorf("loop %s did not return", name)
			}
		}
	})
}

func TestPerAssistantTicks(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, exampleA, exampleB)
		h.a(exampleA).Tick = 30 * time.Second
		h.start()
		defer h.stop()
		h.until(2 * time.Minute)
		count := func(id string) int {
			n := 0
			for _, at := range h.w.times("state", id) {
				if at > 0 {
					n++
				}
			}
			return n
		}
		if a, b := count(exampleA), count(exampleB); a != 4 || b != 2 {
			t.Errorf("ticks in 2m: %s %d, %s %d; want 4 and 2", exampleA, a, exampleB, b)
		}
	})
}

func TestWindowMismatchLogged(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, exampleA)
		h.a(exampleA).Context.Window = 200000
		h.w.usage = func(_ context.Context, name string, _ int, _ time.Time) (control.Usage, error) {
			return control.Usage{SessionID: "sess-" + name, HaveContext: true, ContextTokens: 1000, Window: 1000000}, nil
		}
		h.start()
		defer h.stop()
		h.until(3 * time.Minute)
		ev := h.eventsFor("window_mismatch", exampleA)
		if len(ev) != 1 || num(ev[0]["window"]) != 1000000 || num(ev[0]["configured_window"]) != 200000 {
			t.Errorf("window_mismatch events = %v, want one naming 1000000 against 200000", ev)
		}
	})
}

func TestSignalOnGoneProcessIsNotAFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, exampleA)
		h.a(exampleA).Enabled = false
		h.w.addSession(exampleA)
		h.w.signal = func(string) error {
			return fmt.Errorf("pg-claude-control stop: %w: pid:1000", control.ErrNoProcess)
		}
		h.start()
		defer h.stop()
		h.until(7 * time.Minute)
		wantActions(t, h, exampleA, "submit@0s", "interrupt@5m0s", "submit@5m0s", "signal@7m0s")
		if ev := h.eventsFor("action_failed", exampleA); len(ev) != 0 {
			t.Errorf("action_failed events = %v, want none: the process was already gone", ev)
		}
		if ev := h.eventsFor("kill", exampleA); len(ev) != 1 || ev[0]["already_gone"] != true {
			t.Errorf("kill events = %v, want one marked already_gone", ev)
		}
	})
}

func TestEventShape(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, exampleA)
		h.start()
		defer h.stop()
		h.until(90 * time.Second)
		h.w.die(exampleA, tmux.DeadInfo{Status: 3})
		h.until(2 * time.Minute)
		ev := h.eventsFor("exited", exampleA)
		if len(ev) != 1 {
			t.Fatalf("exited events = %v, want one", ev)
		}
		e := ev[0]
		for _, k := range []string{"ts", "event", "assistant", "session_id", "pid", "uptime_s", "status"} {
			if _, ok := e[k]; !ok {
				t.Errorf("exited event %v has no %q", e, k)
			}
		}
		if e["session_id"] != "sess-example-a" || num(e["pid"]) != firstPID || num(e["uptime_s"]) != 60 || num(e["status"]) != 3 {
			t.Errorf("exited event = %v, want session sess-example-a, pid %d, uptime 60, status 3", e, firstPID)
		}
		if _, ok := e["msg"]; ok {
			t.Errorf("exited event %v carries msg; the event name is the event key", e)
		}
	})
}

func TestNudgeTextNamesExitAndReason(t *testing.T) {
	reason := "context at 81% (162000 of 200000 tokens)"
	n := NudgeText(reason)
	for _, want := range []string{reason, "finish your current step", "notes", "`pg-assistants exit`", "on its own", "not chained", "end your turn", "Never wait for input"} {
		if !strings.Contains(n, want) {
			t.Errorf("NudgeText = %q, want it to contain %q", n, want)
		}
	}
	s := StopText(reason)
	for _, want := range []string{reason, "Stop now", "notes", "`pg-assistants exit`", "on its own", "end your turn", "Never wait for input"} {
		if !strings.Contains(s, want) {
			t.Errorf("StopText = %q, want it to contain %q", s, want)
		}
	}
}

func TestMessagesNeverStartWithSlash(t *testing.T) {
	for _, reason := range []string{"", "/exit", "context at 100%", " /clear"} {
		for _, text := range []string{NudgeText(reason), StopText(reason)} {
			if strings.HasPrefix(strings.TrimSpace(text), "/") || text == "" {
				t.Errorf("message %q starts with a slash or is empty", text)
			}
			if strings.Contains(text, "\n") {
				t.Errorf("message %q spans lines", text)
			}
		}
	}
}

func TestTickTimeSnapsToTheGrid(t *testing.T) {
	start := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		now  time.Time
		want time.Time
	}{
		{"on time", start.Add(time.Minute), start.Add(time.Minute)},
		{"a little late", start.Add(time.Minute + 3*time.Millisecond), start.Add(time.Minute)},
		{"a little early", start.Add(2*time.Minute - 3*time.Millisecond), start.Add(2 * time.Minute)},
		{"late, off the grid", start.Add(time.Minute + 20*time.Second), start.Add(time.Minute + 20*time.Second)},
		{"the first tick", start, start},
	}
	for _, c := range cases {
		if got := tickTime(start, c.now, time.Minute); !got.Equal(c.want) {
			t.Errorf("%s: tickTime = %s, want %s", c.name, got, c.want)
		}
	}
}
