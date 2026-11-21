package settle

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
)

// A where or remove request whose deadline has passed returns ErrRunBusy while
// a tick holds the run lock, the abandoned wait logs the holder, and the lock
// is usable again once the holder releases. No sleeps: the deadline is already
// past, so only the lock can be what the call waits on.
func TestWhereAndRemoveReturnBusyWhileTheRunLockIsHeld(t *testing.T) {
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
	slowRunLockWait = 0 // every wait counts as slow, so the give-up is logged
	t.Cleanup(func() { slowRunLockWait = old })

	unlockTick, _ := e.sett.lockRunWait("tick")
	ctx, cancel := context.WithDeadline(context.Background(), time.Unix(1, 0))
	defer cancel()

	done := make(chan error, 2)
	go func() { _, err := e.sett.WhereCtx(ctx, sess); done <- err }()
	go func() { _, err := e.sett.RemoveCtx(ctx, RemoveOptions{DryRun: true}); done <- err }()
	for i := 0; i < 2; i++ {
		if err := <-done; !errors.Is(err, ErrRunBusy) {
			t.Fatalf("call %d: %v, want ErrRunBusy", i, err)
		}
	}

	for _, op := range []string{"where", "remove"} {
		if n, _ := e.sett.cfg.Obs.Reg.Counter(obs.MSettleRunLockWaits, map[string]string{"op": op}); n != 1 {
			t.Fatalf("%s waits counted %v, want 1", op, n)
		}
	}
	mu.Lock()
	out := buf.String()
	mu.Unlock()
	for _, want := range []string{`"msg":"settle_run_lock_wait_slow"`, `"op":"where"`, `"op":"remove"`, `"holder_op":"tick"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("log lacks %s:\n%s", want, out)
		}
	}

	unlockTick()
	// The abandoned waiters release the lock they win, so a caller with time gets it.
	unlock, err := e.sett.lockRunCtx(context.Background(), "after")
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	if n := e.sett.waiters.Load(); n != 0 {
		t.Fatalf("waiters %d after all calls ended, want 0", n)
	}
}
