package serve

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/shutdown"
)

// Close logs shutdown_started, one shutdown_phase per step in order, then
// shutdown_done. The daemon is Open-only (no Start) with one goLoop running.
func TestShutdownPhasesLoggedInOrder(t *testing.T) {
	e := newEnv(t, false)
	d := e.open(t)
	d.goLoop("probe", func() { <-d.stop })
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, line := range strings.Split(e.log.String(), "\n") {
		var rec map[string]any
		if json.Unmarshal([]byte(line), &rec) != nil {
			continue
		}
		switch rec["event"] {
		case obs.EvShutdownStarted:
			got = append(got, "started:"+rec["reason"].(string))
		case obs.EvShutdownPhase:
			got = append(got, rec["phase"].(string))
		case obs.EvShutdownDone:
			got = append(got, "done")
		}
	}
	want := "started:close,cancel,server_exporter,watcher,loops_wait,seen_flush,tracker,checkpoint_flush,store,done"
	if strings.Join(got, ",") != want {
		t.Fatalf("shutdown events %v, want %s", got, want)
	}
}

// Close is idempotent, and works on a daemon that never started.
func TestCloseTwiceAndWithoutStart(t *testing.T) {
	e := newEnv(t, false)
	d := e.open(t)
	err1 := d.Close()
	err2 := d.Close()
	if err1 != nil || err2 != nil {
		t.Fatalf("Close errors %v, %v", err1, err2)
	}
	if n := strings.Count(e.log.String(), `"event":"`+obs.EvShutdownStarted+`"`); n != 1 {
		t.Fatalf("%d shutdown_started events, want 1", n)
	}
}

// Open registers the watchdog providers, and a finishing Close disarms it.
func TestOpenRegistersHooksAndCloseDisarms(t *testing.T) {
	e := newEnv(t, false)
	hooks := &shutdown.Hooks{}
	ctx, cancel := context.WithCancel(context.Background())
	exited := make(chan int, 1)
	wd := shutdown.Watch(ctx, shutdown.Options{Deadline: 20 * time.Millisecond, InFlight: hooks.InFlight, Phase: hooks.Phase, Exit: func(c int) { exited <- c }})
	d, err := Open(Options{Config: e.cfg, Logger: NewLogger(e.log), NoEvents: true, Version: "test", Procs: e.procs, Hooks: hooks, Watchdog: wd})
	if err != nil {
		t.Fatal(err)
	}
	d.goLoop("probe", func() { <-d.stop })
	time.Sleep(2 * time.Millisecond)
	if fl := hooks.InFlight(); len(fl) != 1 || fl[0].Name != "probe" {
		t.Fatalf("in flight %v", fl)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if got := hooks.Phase().Name; got != "done" {
		t.Fatalf("phase %q after Close", got)
	}
	cancel()
	select {
	case <-exited:
		t.Fatal("watchdog fired after a finished Close")
	case <-time.After(80 * time.Millisecond):
	}
}
