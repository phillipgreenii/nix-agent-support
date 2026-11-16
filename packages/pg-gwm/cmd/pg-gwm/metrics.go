package main

import (
	"fmt"
	"net/http"

	"github.com/phillipgreenii/pg-gwm/internal/config"
	"github.com/phillipgreenii/pg-gwm/internal/metrics"
	"github.com/phillipgreenii/pg-gwm/internal/state"
)

// collector reads the configuration and the user-level state paths at scrape
// time, so a scrape always sees the current files.
func (a *app) collector() metrics.Collector {
	return metrics.Collector{
		Version:      Version,
		ConfigPath:   config.DefaultPath(),
		RegistryPath: state.RegistryPath(),
		CountersPath: state.CountersPath(),
		ReaperPath:   state.ReaperPath(),
		Git:          a.git,
	}
}

// metricsHandler serves /metrics: every scrape rediscovers the repositories
// and reads their files without any repository lock.
func (a *app) metricsHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		fmt.Fprint(w, metrics.Render(a.collector().Collect(r.Context())))
	})
	return mux
}
