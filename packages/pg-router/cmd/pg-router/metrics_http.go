package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	otelprometheus "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

// startMetricsServer builds an OTel MeterProvider backed by
// go.opentelemetry.io/otel/exporters/prometheus (design decision D2:
// pg-router's metric catalog — internal/metrics/metrics.go — is already
// OTel-API-native, so bridging it through OTel's own Prometheus exporter is
// the natural fit, rather than hand-rolling client_golang instruments the
// way pg-pr's internal/telemetry/metrics.go does) and starts an HTTP
// listener on addr serving that catalog at /metrics.
//
// The exporter registers on its OWN prometheus.Registry, never
// prometheus.DefaultRegisterer: pg-router's metric catalog is the only
// thing this endpoint serves, and a dedicated registry keeps that true
// regardless of what any imported library's init() might register on the
// global default registry.
//
// Returns the MeterProvider (for resolveMeterProvider to bind onto cfg —
// see runRun) and a shutdown that stops the HTTP listener. The
// MeterProvider itself is force-flushed via metrics.Flush on every runRun
// exit path, same as the existing ManualReader default; shutdown here only
// tears down the listener.
//
// onListen, when non-nil, is called with the bound net.Listener before the
// server starts serving — the same "offer the real listener back to the
// caller" seam packages/pg-pr/internal/sync/daemon.go's own
// startMetricsServer uses (opts.MetricsListener) so a test can pass
// "127.0.0.1:0" and read back the actually-bound ephemeral port. Production
// callers pass nil.
func startMetricsServer(addr string, onListen func(net.Listener)) (metric.MeterProvider, func(context.Context) error, error) {
	reg := prometheus.NewRegistry()
	exporter, err := otelprometheus.New(otelprometheus.WithRegisterer(reg))
	if err != nil {
		return nil, nil, fmt.Errorf("construct otel prometheus exporter: %w", err)
	}
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(exporter))

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, nil, fmt.Errorf("listen on %s: %w", addr, err)
	}
	if onListen != nil {
		onListen(ln)
	}

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	srv := &http.Server{Handler: mux} //nolint:gosec // addr is operator-supplied (--metrics-addr), never network-untrusted input

	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Warn("metrics server exited", "addr", addr, "err", err)
		}
	}()
	slog.Info("pg-router metrics server listening", "addr", addr)

	shutdown := func(ctx context.Context) error {
		return srv.Shutdown(ctx)
	}
	return mp, shutdown, nil
}
