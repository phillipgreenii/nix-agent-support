package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/phillipgreenii/x/otelsetup"
	"go.opentelemetry.io/otel/metric"
)

// processStart is the process start time reported as process.start_time.
var processStart = time.Now()

// registerCommonSignals registers the shared daemon signals (process start
// time, build info, shutdown duration and overruns) on mp. A failure is logged
// and yields nil, which ObserveShutdown accepts: telemetry never stops a run.
func registerCommonSignals(mp metric.MeterProvider) *otelsetup.CommonSignals {
	sig, err := otelsetup.RegisterCommonSignals(mp.Meter("github.com/phillipgreenii/pg-router"), version, processStart)
	if err != nil {
		slog.Warn("run: common signals not registered", "err", err)
		return nil
	}
	return sig
}

// observeShutdown records one finished shutdown on sig: the time since the
// clock's context ended, marked as an overrun when it passed the deadline. It
// does nothing while the context has not ended.
func observeShutdown(ctx context.Context, sig *otelsetup.CommonSignals, clk *shutdownClock) {
	if d, ok := clk.Elapsed(); ok {
		sig.ObserveShutdown(ctx, d, d > clk.deadline)
	}
}
