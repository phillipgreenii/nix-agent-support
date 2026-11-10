package settle

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
)

// The slow-wait line names the holder the waiter queued behind and how long that
// holder kept the lock.
func TestSlowRunLockWaitNamesTheHolder(t *testing.T) {
	var buf bytes.Buffer
	var mu sync.Mutex
	h := slog.NewJSONHandler(writerFunc(func(p []byte) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		return buf.Write(p)
	}), nil)
	e := newEnvWith(t, false, func(e *env) {
		e.extra = func(c *Config) { c.Obs = obs.New(slog.New(h)) }
	})
	old := slowRunLockWait
	slowRunLockWait = 10 * time.Millisecond
	t.Cleanup(func() { slowRunLockWait = old })

	unlock, _ := e.sett.lockRunWait("remove")
	done := make(chan struct{})
	go func() {
		defer close(done)
		u, _ := e.sett.lockRunWait("take_dirty")
		u()
	}()
	time.Sleep(60 * time.Millisecond)
	unlock()
	<-done

	mu.Lock()
	defer mu.Unlock()
	out := buf.String()
	for _, want := range []string{`"msg":"settle_run_lock_wait_slow"`, `"op":"take_dirty"`, `"holder_op":"remove"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("log line lacks %s:\n%s", want, out)
		}
	}
	if !strings.Contains(out, `"holder_held_ms":`) || strings.Contains(out, `"holder_held_ms":0,`) || strings.Contains(out, `"holder_held_ms":0}`) {
		t.Fatalf("holder_held_ms missing or zero:\n%s", out)
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// A dry run over many Conversations, each slow to read, yields the run lock at a
// bounded interval: a queued caller waits about one slice plus one Conversation,
// not the whole pass.
func TestDryRunRemoveYieldsRunLockInSlices(t *testing.T) {
	const (
		perSession = 40 * time.Millisecond
		sessions   = 6
		slice      = 20 * time.Millisecond
	)
	e := newEnvWith(t, false, func(e *env) {
		e.removeOn = true
		e.extra = func(c *Config) {
			c.Mtime = func(string) (time.Time, error) {
				time.Sleep(perSession)
				return t0, nil
			}
		}
	})
	e.seed(3, removeAge+time.Hour)
	for i := 1; i < sessions; i++ {
		e.seedAs(fmt.Sprintf("0a1b2c3d-0000-4000-8000-00000000010%d", i), removeAge+time.Hour)
	}
	e.tickSettleOnly()
	e.sett.yieldSlice = slice

	start := time.Now()
	var rep RemoveReport
	var rerr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		rep, rerr = e.sett.Remove(RemoveOptions{DryRun: true})
	}()
	time.Sleep(perSession) // inside the pass
	var maxWait time.Duration
	probes := 0
	for {
		select {
		case <-done:
			goto finished
		default:
		}
		u, w := e.sett.lockRunWait("probe")
		u()
		probes++
		if w > maxWait {
			maxWait = w
		}
		time.Sleep(5 * time.Millisecond)
	}
finished:
	total := time.Since(start)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if len(rep.Candidates)+len(rep.Skipped) < sessions {
		t.Fatalf("the dry run did not see every Conversation: %+v", rep)
	}
	if total < sessions*perSession/2 {
		t.Fatalf("pass took %v: the slow reads did not run", total)
	}
	// One Conversation (perSession) plus slack for the hand-off and the scheduler.
	if limit := slice + 2*perSession; maxWait > limit {
		t.Fatalf("a queued caller waited %v (probes %d, pass %v), want at most %v", maxWait, probes, total, limit)
	}
	if probes < 3 {
		t.Fatalf("only %d probes got the lock during a %v pass", probes, total)
	}
}

// yieldRun does nothing when nobody queues or the slice has not passed.
func TestYieldRunOnlyForQueuedCallersAfterTheSlice(t *testing.T) {
	e := newEnv(t, false)
	e.sett.yieldSlice = 10 * time.Millisecond
	unlock, _ := e.sett.lockRunWait("remove")
	defer unlock()
	time.Sleep(15 * time.Millisecond)
	if e.sett.yieldRun() {
		t.Fatal("yielded with nobody queued")
	}
	e.sett.yieldSlice = time.Hour
	e.sett.waiters.Add(1) // a queued caller
	defer e.sett.waiters.Add(-1)
	if e.sett.yieldRun() {
		t.Fatal("yielded before the slice passed")
	}
}
