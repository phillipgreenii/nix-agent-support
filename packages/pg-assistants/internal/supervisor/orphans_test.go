package supervisor

import (
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-assistants/internal/config"
)

func TestOrphanSessionGetsDisabledLadder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, exampleA)
		const orphan = "example-gone"
		h.w.addSession(orphan)
		h.start()
		defer h.stop()

		ev := h.eventsFor("orphan", orphan)
		if len(ev) != 1 || ev[0]["note"] != "not in config" {
			t.Errorf("orphan events = %v, want one noting not in config", ev)
		}
		if got := h.snapshot().Orphans; !slices.Equal(got, []string{orphan}) {
			t.Errorf("status orphans = %q, want [%s]", got, orphan)
		}
		if sub := h.w.callsOf("submit", orphan); len(sub) != 1 || !strings.Contains(sub[0].text, "pg-assistants exit") {
			t.Errorf("orphan nudge = %+v, want one nudge naming pg-assistants exit", sub)
		}

		h.until(9 * time.Minute)
		wantActions(t, h, orphan, "submit@0s", "interrupt@5m0s", "submit@5m0s", "signal@7m0s", "kill@8m0s")
		if n := h.group.endedCount("orphan:" + orphan); n != 1 {
			t.Errorf("orphan loop returned %d times, want once the session is gone", n)
		}
		if got := h.snapshot().Orphans; len(got) != 0 {
			t.Errorf("status orphans after the kill = %q, want none", got)
		}
		if n := len(h.eventsFor("orphan", orphan)); n != 1 {
			t.Errorf("orphan events = %d, want 1 (logged once)", n)
		}
		wantBudgets(t, h)
	})
}

func TestProblemAssistantSessionGetsDisabledLadder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, exampleA, "example-bad")
		h.a("example-bad").Problems = []config.FieldError{{Field: "assistant[example-bad].effort", Msg: "must be one of low, medium, high, xhigh, max"}}
		h.w.addSession("example-bad")
		h.start()
		defer h.stop()

		if !slices.Contains(h.group.names(), "orphan:example-bad") {
			t.Errorf("loops = %q, want orphan:example-bad", h.group.names())
		}
		if ev := h.eventsFor("orphan", "example-bad"); len(ev) != 1 || ev[0]["note"] != "config error" {
			t.Errorf("orphan events = %v, want one noting the config error", ev)
		}
		wantActions(t, h, "example-bad", "submit@0s")
		if got := h.snapshot().Orphans; !slices.Equal(got, []string{"example-bad"}) {
			t.Errorf("status orphans = %q, want [example-bad]", got)
		}
		if st := h.statusOf("example-bad"); st.ConfigError == "" {
			t.Error("the assistant's status lost its config error")
		}
	})
}

func TestOrphanHonoursExitRequest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, exampleA)
		const orphan = "example-gone"
		h.w.addSession(orphan)
		h.requestExit(orphan)
		h.start()
		defer h.stop()
		h.until(time.Minute)
		// The stop leaves a dead pane, which the next tick removes.
		wantActions(t, h, orphan, "stop@0s", "kill@1m0s")
		if h.exitRequested(orphan) {
			t.Error("the orphan's exit request is still on file after the session ended")
		}
		if n := h.group.endedCount("orphan:" + orphan); n != 1 {
			t.Errorf("orphan loop returned %d times, want 1", n)
		}
	})
}

func TestLoopsAreNamed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, exampleA)
		h.start()
		defer h.stop()
		got := h.group.names()
		slices.Sort(got)
		if want := []string{"assistant:example-a", "heartbeat", "orphans"}; !slices.Equal(got, want) {
			t.Errorf("loops = %q, want %q", got, want)
		}

		h.until(90 * time.Second)
		h.w.addSession("example-gone")
		h.until(3 * time.Minute)
		n := 0
		for _, name := range h.group.names() {
			if name == "orphan:example-gone" {
				n++
			}
		}
		if n != 1 {
			t.Errorf("orphan:example-gone started %d times, want once", n)
		}
	})
}
