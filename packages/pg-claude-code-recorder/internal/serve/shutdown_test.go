package serve

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	xlifecycle "github.com/phillipgreenii/x/lifecycle"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
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

// With a run group, every goLoop is listed by the group, each shutdown step is
// its phase, and a Close that finishes before the deadline never fires the
// watchdog.
func TestGroupSeesLoopsAndCloseFinishesBeforeDeadline(t *testing.T) {
	e := newEnv(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	exited := make(chan int, 1)
	var phases []string
	var mu sync.Mutex
	grp := xlifecycle.New(ctx, xlifecycle.Options{
		Deadline: time.Minute,
		Exit:     func(c int) { exited <- c },
		OnEvent: func(ev xlifecycle.Event) {
			if ev.Name == xlifecycle.EvPhase {
				mu.Lock()
				phases = append(phases, ev.Phase)
				mu.Unlock()
			}
		},
		Notify: func(parent context.Context, _ ...os.Signal) (context.Context, context.CancelFunc) {
			return context.WithCancel(parent)
		},
	})
	d, err := Open(Options{Config: e.cfg, Logger: NewLogger(e.log), NoEvents: true, Version: "test", Procs: e.procs, Group: grp})
	if err != nil {
		t.Fatal(err)
	}
	d.goLoop("probe", func() { <-d.stop })
	grp.Go("daemon", func(ctx context.Context) error { <-ctx.Done(); return d.Close() })
	cancel()
	if err := grp.Run(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	got := append([]string(nil), phases...)
	mu.Unlock()
	if len(got) == 0 || got[len(got)-1] != "done" || got[0] != "cancel" {
		t.Fatalf("phases %v, want cancel ... done", got)
	}
	if grp.Fired() {
		t.Fatal("watchdog fired after a finished Close")
	}
	select {
	case <-exited:
		t.Fatal("hard exit after a finished Close")
	default:
	}
}
