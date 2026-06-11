package sync

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	stdsync "sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/snapshot"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/telemetry"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
)

// scrapeText returns the package metrics endpoint's exposition text.
func scrapeText(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(telemetry.MetricsHandler())
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("scrape: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read scrape body: %v", err)
	}
	return string(body)
}

// scrapeValue parses the float value of the metric sample line whose series
// (name + optional labels) exactly matches series, e.g. `pg_pr_snapshot_present`
// or `pg_pr_sync_errors_total{repo="o/r"}`. Returns (0, false) if absent.
func scrapeValue(t *testing.T, series string) (float64, bool) {
	t.Helper()
	for _, line := range strings.Split(scrapeText(t), "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.HasPrefix(line, series+" ") {
			continue
		}
		fields := strings.Fields(line)
		v, err := strconv.ParseFloat(fields[len(fields)-1], 64)
		if err != nil {
			t.Fatalf("parse value from %q: %v", line, err)
		}
		return v, true
	}
	return 0, false
}

// TestRunSnapshotOwner_SetsSnapshotPresent verifies the daemon's snapshot
// owner flips pg_pr_snapshot_present to 1 once it publishes a snapshot — the
// daemon path previously never set it (only the retired full-Sync path did),
// so the Ops "Snapshot present" tile read absent forever.
func TestRunSnapshotOwner_SetsSnapshotPresent(t *testing.T) {
	telemetry.SnapshotPresent.Set(0)
	e := newRefreshEngine(t, "me", &refreshFakeBeads{}, api.PR{Repo: "o/r", Number: 1, Author: "me", State: "open"})
	store := snapshot.NewStore()

	updates := make(chan snapshotUpdate, 1)
	in := snapshot.PRInput{PR: api.PR{Repo: "o/r", Number: 1, Author: "me", State: "open"}}
	updates <- snapshotUpdate{Key: prKey{Repo: "o/r", Number: 1}, Input: &in}
	close(updates)

	e.runSnapshotOwner(updates, store) // drains the one update, then returns on close

	if got, ok := scrapeValue(t, "pg_pr_snapshot_present"); !ok || got != 1 {
		t.Fatalf("pg_pr_snapshot_present: got %v (present=%v) want 1", got, ok)
	}
}

// TestRunWorker_RefreshFailureIncrementsSyncErrors verifies a failing per-PR
// refresh bumps pg_pr_sync_errors_total{repo} in daemon mode. Previously only
// the full-Sync path incremented it, so the Ops "Sync errors / sec" headline
// panel could never reflect daemon refresh failures (they only showed in the
// Loki log panel).
func TestRunWorker_RefreshFailureIncrementsSyncErrors(t *testing.T) {
	e := newRefreshEngine(t, "me", &refreshFakeBeads{}, api.PR{Repo: "o/r", Number: 1, Author: "me", State: "open"})
	before, _ := scrapeValue(t, `pg_pr_sync_errors_total{repo="o/r"}`)

	q := newRefreshQueue()
	// #999 is NOT in the fake provider's views -> GetPR returns "not found" ->
	// refreshPR returns an error.
	q.enqueue(prKey{Repo: "o/r", Number: 999})

	updates := make(chan snapshotUpdate, 4)
	ctx, cancel := context.WithCancel(context.Background())
	var wg stdsync.WaitGroup
	wg.Add(1)
	go e.runWorker(ctx, q, "team", updates, NewTextLogger(), &wg)

	// Condition-based wait: stop as soon as the counter moves (ctx still live).
	deadline := time.Now().Add(5 * time.Second)
	for {
		if v, _ := scrapeValue(t, `pg_pr_sync_errors_total{repo="o/r"}`); v > before {
			break
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	wg.Wait()

	if after, _ := scrapeValue(t, `pg_pr_sync_errors_total{repo="o/r"}`); after <= before {
		t.Fatalf("pg_pr_sync_errors_total{repo=o/r}: expected increment on refresh failure; before=%v after=%v", before, after)
	}
}

// TestSeedDaemonMetricSeries_MakesSeriesScrapeVisible verifies the daemon
// 0-initializes the counters that otherwise never appear until their first
// event, so the Ops dashboard shows a flat-zero "healthy" line instead of
// "no data" for sync errors and roster truncation.
func TestSeedDaemonMetricSeries_MakesSeriesScrapeVisible(t *testing.T) {
	e := newRefreshEngine(t, "me", &refreshFakeBeads{}, api.PR{Repo: "o/r", Number: 1, Author: "me", State: "open"})
	// Unique remote so the scrape assertion is unambiguous across the shared
	// package-global registry.
	e.ReplaceCfg(&config.Config{SelfLogin: "me", Repos: []config.RepoConfig{{Remote: "seedonly/repo"}}})

	e.seedDaemonMetricSeries()

	text := scrapeText(t)
	for _, want := range []string{
		`pg_pr_sync_errors_total{repo="seedonly/repo"}`,
		`pg_pr_fingerprint_poll_truncated_total{group="mine"}`,
		`pg_pr_fingerprint_poll_truncated_total{group="team"}`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("scrape missing 0-initialized series %q", want)
		}
	}
}
