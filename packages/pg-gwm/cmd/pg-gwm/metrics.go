package main

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/phillipgreenii/pg-gwm/internal/config"
	"github.com/phillipgreenii/pg-gwm/internal/metrics"
	"github.com/phillipgreenii/pg-gwm/internal/state"
)

// defaultScrapeTimeout is Prometheus's default scrape timeout, used when the
// request does not carry X-Prometheus-Scrape-Timeout-Seconds.
const defaultScrapeTimeout = 10 * time.Second

// gitBudget is the share of the scrape timeout that git-derived values may
// use, so the file-derived gauges are written well before Prometheus gives up.
func gitBudget(r *http.Request) time.Duration {
	timeout := defaultScrapeTimeout
	if v, err := strconv.ParseFloat(r.Header.Get("X-Prometheus-Scrape-Timeout-Seconds"), 64); err == nil && v > 0 {
		timeout = time.Duration(v * float64(time.Second))
	}
	return timeout * 6 / 10
}

// collector reads the configuration and the user-level state paths at scrape
// time, so a scrape always sees the current files.
func (a *app) collector(budget time.Duration) metrics.Collector {
	return metrics.Collector{
		GitBudget:    budget,
		Version:      Version,
		ConfigPath:   config.DefaultPath(),
		RegistryPath: state.RegistryPath(),
		CountersPath: state.CountersPath(),
		ReaperPath:   state.ReaperPath(),
		Git:          a.git,
	}
}

// metricsHandler serves /metrics: every scrape rediscovers the repositories
// and reads their files without any repository lock. Git work is bounded to
// 60 percent of the scrape timeout; what it does not finish is omitted.
func (a *app) metricsHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		fmt.Fprint(w, metrics.Render(a.collector(gitBudget(r)).Collect(r.Context())))
	})
	return mux
}
