package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router-handlers/internal/ccpool"
)

// A run's gauges leave in one send, in emit order, and the buffer empties.
func TestGaugeBatch_FlushSendsOnceAndClears(t *testing.T) {
	var sends [][]ccpool.GaugePoint
	b := &gaugeBatch{send: func(_ context.Context, p []ccpool.GaugePoint) error {
		sends = append(sends, p)
		return nil
	}}
	ctx := context.Background()
	_ = b.Emit(ctx, "a", 1, nil)
	_ = b.Emit(ctx, "b", 2, map[string]string{"k": "v"})
	if err := b.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if len(sends) != 1 || len(sends[0]) != 2 || sends[0][0].Name != "a" || sends[0][1].Labels["k"] != "v" {
		t.Fatalf("sends = %+v", sends)
	}
	if err := b.Flush(ctx); err != nil || len(sends) != 1 {
		t.Fatalf("second flush must be a no-op: err=%v sends=%d", err, len(sends))
	}
}

// The flush is bounded and survives a cancelled run context.
func TestGaugeBatch_FlushDetachedAndBounded(t *testing.T) {
	var dl time.Time
	var cerr error
	b := &gaugeBatch{send: func(ctx context.Context, _ []ccpool.GaugePoint) error {
		dl, _ = ctx.Deadline()
		cerr = ctx.Err()
		return errors.New("boom")
	}}
	ctx, cancel := context.WithCancel(context.Background())
	_ = b.Emit(ctx, "a", 1, nil)
	cancel()
	if err := b.Flush(ctx); err == nil {
		t.Fatal("send error must be returned")
	}
	if cerr != nil || dl.IsZero() || time.Until(dl) > gaugeBatchTimeout {
		t.Fatalf("flush ctx: err=%v deadline=%v", cerr, dl)
	}
}

func TestFlushGauges_NilAndErrorAreHarmless(t *testing.T) {
	flushGauges(context.Background(), nil)
	flushGauges(context.Background(), func(context.Context) error { return errors.New("x") })
}
