package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-gwm/internal/repo"
	"github.com/phillipgreenii/pg-gwm/internal/state"
	"github.com/phillipgreenii/x/cmdrun"
)

// A git call that outlives the scrape timeout must not hold the scrape: the
// response comes back inside the budget and still carries the reaper ages,
// which a missed scrape would otherwise turn into a stale-reaper alert.
func TestMetricsAnswersWithinScrapeTimeoutWhenGitBlocks(t *testing.T) {
	c := newCLI(t)
	rp, err := repo.Resolve(context.Background(), c.a.git, c.repo.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := (state.Registry{Path: state.RegistryPath()}).Add(rp.Main); err != nil {
		t.Fatal(err)
	}
	ok := time.Now().Add(-5 * time.Minute)
	if err := state.WriteReaperRecord(state.ReaperPath(), state.ReaperRecord{
		Started: ok, Ended: ok, Outcome: "completed", LastSuccess: &ok,
		Repos: map[string]state.RepoReap{rp.Label: {Label: rp.Label, At: ok, Outcome: "completed"}},
	}); err != nil {
		t.Fatal(err)
	}
	// The stand-in blocks until its context ends, as a git starved by host load does.
	c.a.git.Run = func(ctx context.Context, _ cmdrun.Call) cmdrun.Result {
		<-ctx.Done()
		return cmdrun.Result{Code: -1, CtxErr: ctx.Err()}
	}

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil)
	req.Header.Set("X-Prometheus-Scrape-Timeout-Seconds", "2")
	rec := httptest.NewRecorder()
	start := time.Now()
	c.a.metricsHandler().ServeHTTP(rec, req)
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("scrape took %s, want under the 2s scrape timeout", took)
	}
	body := rec.Body.String()
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	for _, w := range []string{"pg_gwm_reaper_last_success_age_seconds ", "pg_gwm_last_reap_age_seconds{repo="} {
		if !strings.Contains(body, w) {
			t.Errorf("missing %q in:\n%s", w, body)
		}
	}
	if strings.Contains(body, "pg_gwm_preserved") {
		t.Errorf("preserved branches reported although git never answered:\n%s", body)
	}
}
