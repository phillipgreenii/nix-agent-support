// Package server exposes the published snapshot as Prometheus text.
package server

import (
	"bytes"
	"log/slog"
	"net/http"

	"github.com/phillipgreenii/beads-exporter/internal/collect"
	"github.com/phillipgreenii/beads-exporter/internal/metrics"
)

// ContentType is the Prometheus text exposition content type.
const ContentType = "text/plain; version=0.0.4; charset=utf-8"

// Handler serves /metrics from the collector's last published snapshot. A
// scrape reads the snapshot pointer and renders it; it never waits on a
// collection cycle.
func Handler(c *collect.Collector, reg *metrics.Registry, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		var buf bytes.Buffer
		if err := reg.Render(&buf, c.Snapshot().Samples()); err != nil {
			log.Error("render failed", "error", err.Error())
			http.Error(w, "render failed", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", ContentType)
		_, _ = w.Write(buf.Bytes())
	})
	return mux
}
