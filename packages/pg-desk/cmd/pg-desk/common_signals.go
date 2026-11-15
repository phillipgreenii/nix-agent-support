package main

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/phillipgreenii/x/otelsetup"
	"go.opentelemetry.io/otel/metric"
)

// processStart is the process start time reported as process.start_time.
var processStart = time.Now()

// registerCommonSignals registers the shared daemon signals (process start
// time, build info, shutdown duration and overruns) on mp. A failure is logged
// and yields nil, which ObserveShutdown accepts: telemetry never stops serve.
func registerCommonSignals(mp metric.MeterProvider) *otelsetup.CommonSignals {
	sig, err := otelsetup.RegisterCommonSignals(mp.Meter("github.com/phillipgreenii/pg-desk"), Version, processStart)
	if err != nil {
		slog.Warn("serve: common signals not registered", "err", err)
		return nil
	}
	return sig
}

// shutdownTimer measures how long a shutdown has run: the time since a context
// ended, against the deadline the shutdown is meant to finish within.
type shutdownTimer struct {
	deadline time.Duration
	endedAt  atomic.Int64 // unix nanoseconds; zero until the context ends
}

// newShutdownTimer starts the timer when ctx ends.
func newShutdownTimer(ctx context.Context, deadline time.Duration) *shutdownTimer {
	t := &shutdownTimer{deadline: deadline}
	context.AfterFunc(ctx, func() { t.endedAt.Store(time.Now().UnixNano()) })
	return t
}

// Elapsed returns the time since the context ended; ok is false while it has
// not ended.
func (t *shutdownTimer) Elapsed() (d time.Duration, ok bool) {
	ended := t.endedAt.Load()
	if ended == 0 {
		return 0, false
	}
	return time.Since(time.Unix(0, ended)), true
}

// observeShutdown records one finished shutdown on sig: the time since the
// timer's context ended, marked as an overrun when it passed the deadline. It
// does nothing while the context has not ended.
func observeShutdown(ctx context.Context, sig *otelsetup.CommonSignals, t *shutdownTimer) {
	if d, ok := t.Elapsed(); ok {
		sig.ObserveShutdown(ctx, d, d > t.deadline)
	}
}

// logShutdownPhase logs that a shutdown phase finished, with the time since the
// signal context ended. It logs nothing while the context has not ended (a
// listen failure closes the same resources without a signal).
func logShutdownPhase(log *slog.Logger, t *shutdownTimer, phase string) {
	if d, ok := t.Elapsed(); ok {
		log.Info("serve: shutdown phase", "phase", phase, "elapsed", d.Round(time.Millisecond).String(), "deadline", t.deadline.String())
	}
}
