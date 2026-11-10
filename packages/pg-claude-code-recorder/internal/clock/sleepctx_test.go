package clock

import (
	"context"
	"testing"
	"time"
)

func TestSleepCtxWakesOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	res := make(chan bool, 1)
	go func() { res <- SleepCtx(ctx, Real{}, time.Hour) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case ok := <-res:
		if ok {
			t.Fatal("SleepCtx reported a full sleep after cancel")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SleepCtx did not wake on cancel")
	}
}

func TestSleepCtxCompletesAndRefusesDoneContext(t *testing.T) {
	if !SleepCtx(context.Background(), Real{}, time.Millisecond) {
		t.Fatal("a short sleep on a live context must report true")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if SleepCtx(ctx, Real{}, 0) {
		t.Fatal("a done context must report false")
	}
}
