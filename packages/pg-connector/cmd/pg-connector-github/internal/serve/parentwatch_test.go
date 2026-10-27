package serve

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestExitWithParent_CancelsWhenTheParentChanges(t *testing.T) {
	var ppid atomic.Int64
	ppid.Store(4242)
	ctx, cancel := ExitWithParent(context.Background(), 5*time.Millisecond, func() int { return int(ppid.Load()) })
	defer cancel()

	select {
	case <-ctx.Done():
		t.Fatal("cancelled while the parent was unchanged")
	case <-time.After(50 * time.Millisecond):
	}
	ppid.Store(1) // re-parented to init: the parent died
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("not cancelled after the parent changed")
	}
}

func TestExitWithParent_DisabledIsInert(t *testing.T) {
	calls := 0
	parent := context.Background()
	ctx, cancel := ExitWithParent(parent, 0, func() int { calls++; return 1 })
	defer cancel()
	if ctx != parent {
		t.Error("a zero interval must return the context unchanged")
	}
	if calls != 0 {
		t.Errorf("a zero interval must not read the parent pid, read it %d times", calls)
	}
}

func TestExitWithParent_FollowsTheParentContext(t *testing.T) {
	parent, stop := context.WithCancel(context.Background())
	ctx, cancel := ExitWithParent(parent, 5*time.Millisecond, func() int { return 7 })
	defer cancel()
	stop()
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the derived context must end with its parent context")
	}
}
