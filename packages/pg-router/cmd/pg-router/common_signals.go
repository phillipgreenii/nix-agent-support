package main

import (
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
