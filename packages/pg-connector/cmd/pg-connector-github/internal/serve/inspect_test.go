//go:build integration

package serve_test

import (
	"context"
	"testing"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ops/inspect"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/serve"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/servetest"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/telemetry"
)

// The per-PR inspection op and the tracking gauges as the assembled daemon shows
// them. The queries are the plain, unwatched form, so only a read can watch a
// PR: prA (a query member, known to the daemon) and prW (read once, listed by no
// query) are tracked, only a watch read makes either watched, and prB is watched
// without ever having been seen.

func inspectIDs(t *testing.T, d *servetest.Daemon, ids ...string) inspect.Result {
	t.Helper()
	return decode[inspect.Result](t, callOK(t, d, "pr", "inspect", map[string]any{"ids": ids}))
}

func itemOf(t *testing.T, res inspect.Result, id string) inspect.Item {
	t.Helper()
	for _, it := range res.Results {
		if it.ID == id {
			return it
		}
	}
	t.Fatalf("inspect answered no entry for %s: %+v", id, res.Results)
	return inspect.Item{}
}

func TestInspectSeparatesATrackedOnlyPRFromAWatchedOneOnTheRealBinary(t *testing.T) {
	d := servetest.NewDaemon(t, servetest.Options{Before: seedUnqueried, Config: unwatchedQueries()})
	d.Start(t)
	settle(t, d)
	awaitBaselines(t, d, servetest.QueryMine, servetest.QueryTeam)

	// prW is only read; prA is a query member and then watched by a direct read.
	callOK(t, d, "pr", "show", map[string]any{"id": prW.Ref()})
	before := inspectIDs(t, d, prA.Ref(), prW.Ref())
	for _, id := range []string{prA.Ref(), prW.Ref()} {
		if it := itemOf(t, before, id); !it.Known || !it.Tracked || it.Watched || it.WatchedAt != nil || it.WatchedVia != "" {
			t.Fatalf("%s before any watch = %+v, want known, tracked and not watched", id, it)
		}
	}
	if c := before.Counts; c != (inspect.Counts{Requested: 2, Known: 2, Tracked: 2}) {
		t.Fatalf("counts before any watch = %+v, want 2 known, 2 tracked, 0 watched", c)
	}

	callOK(t, d, "pr", "show", map[string]any{"id": prA.Ref(), "watch": true})
	callOK(t, d, "pr", "show", map[string]any{"id": prB.Ref(), "watch": true})
	after := inspectIDs(t, d, prA.Ref(), prW.Ref(), prB.Ref(), "example-org/ghost#99")

	watched, tracked := itemOf(t, after, prA.Ref()), itemOf(t, after, prW.Ref())
	if !watched.Watched || watched.WatchedVia != "direct" || watched.WatchedAt == nil || !watched.Tracked || watched.NodeID != prA.NodeID {
		t.Errorf("the watched PR = %+v, want watched via direct, with a time, and still tracked", watched)
	}
	if tracked.Watched || !tracked.Tracked {
		t.Errorf("the tracked-only PR = %+v, want tracked and not watched", tracked)
	}
	if watched.Watched == tracked.Watched {
		t.Error("the two answers must differ in watched")
	}
	if it := itemOf(t, after, prB.Ref()); !it.Watched || !it.Known {
		t.Errorf("a PR watched on its first sight = %+v, want known and watched", it)
	}
	if it := itemOf(t, after, "example-org/ghost#99"); it.Known || it.Tracked || it.Watched {
		t.Errorf("a PR the daemon never saw = %+v, want unknown, untracked, unwatched", it)
	}
	// The count the marking step reads: two of the four are watched, and the
	// unknown one is counted apart, not as a failure.
	if c := after.Counts; c.Requested != 4 || c.Known != 3 || c.Unknown != 1 || c.Watched != 2 {
		t.Errorf("counts = %+v, want 4 requested, 3 known, 1 unknown, 2 watched", c)
	}
	// The same PR by node id and by reference is one PR.
	dup := inspectIDs(t, d, prA.Ref(), prA.NodeID)
	if len(dup.Results) != 1 || dup.Counts.Requested != 1 || dup.Counts.Watched != 1 {
		t.Errorf("one PR named twice = %+v, want one entry and one watched", dup)
	}

	// The daemon's own count agrees with the store's flag.
	db := d.OpenStore(t)
	if got := readEntity(t, db, prA.NodeID); !got.watched || got.via != "direct" {
		t.Errorf("control: the store says %+v for prA", got)
	}

	// A malformed request is refused and answers nothing.
	for name, args := range map[string]map[string]any{
		"no ids":        {},
		"bad reference": {"ids": []string{"not a ref#"}},
		"empty id":      {"ids": []string{""}},
	} {
		if e := callErr(t, d, "pr", "inspect", args, 30*time.Second); e == nil || e.Code != "invalid_argument" {
			t.Errorf("%s: error = %+v, want invalid_argument", name, e)
		}
	}
}

func gaugeSeries(t *testing.T, reader *sdkmetric.ManualReader, name string) map[string]int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	out := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			g, ok := m.Data.(metricdata.Gauge[int64])
			if !ok {
				t.Fatalf("%s is a %T, want an int64 gauge", name, m.Data)
			}
			for _, dp := range g.DataPoints {
				class, _ := dp.Attributes.Value("class")
				out[class.AsString()] = dp.Value
			}
		}
	}
	return out
}

func TestTheTrackedWatchedAndLimitGaugesFollowTheStoreAndTheConfiguredKey(t *testing.T) {
	cfg := unwatchedQueries()
	cfg["tracked_set_limit"] = 900
	d := servetest.NewDaemon(t, servetest.Options{Before: seedUnqueried, Config: cfg})
	reader := sdkmetric.NewManualReader()
	runInProcess(t, d, func(o *serve.Options) { o.MeterProvider = sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)) })
	settle(t, d)
	awaitBaselines(t, d, servetest.QueryMine, servetest.QueryTeam)
	callOK(t, d, "pr", "show", map[string]any{"id": prW.Ref(), "watch": true})

	// Three fixture PRs are listed by the queries (members); prW is only read
	// (other open) and watched.
	tracked := gaugeSeries(t, reader, telemetry.MetricTrackedEntities)
	if tracked[telemetry.ClassMembers] != 3 || tracked[telemetry.ClassOtherOpen] != 1 || tracked[telemetry.ClassClosedGrace] != 0 || len(tracked) != 3 {
		t.Errorf("tracked.entities = %v, want members 3, other_open 1, closed_grace 0", tracked)
	}
	if got := gaugeSeries(t, reader, telemetry.MetricWatchedEntities)[""]; got != 1 {
		t.Errorf("watched.entities = %d, want 1 (prW)", got)
	}
	if got := gaugeSeries(t, reader, telemetry.MetricTrackedLimit)[""]; got != 900 {
		t.Errorf("tracked.limit = %d, want the configured 900", got)
	}
	backlog := gaugeSeries(t, reader, telemetry.MetricGovernorBacklog)
	if len(backlog) != 5 {
		t.Errorf("governor.backlog series = %v, want one per scheduler class", backlog)
	}
}

func TestNoTrackedLimitGaugeWithoutTheConfiguredKey(t *testing.T) {
	d := servetest.NewDaemon(t, servetest.Options{Before: seed3a, Config: unwatchedQueries()})
	reader := sdkmetric.NewManualReader()
	runInProcess(t, d, func(o *serve.Options) { o.MeterProvider = sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)) })
	if got := gaugeSeries(t, reader, telemetry.MetricTrackedLimit); len(got) != 0 {
		t.Errorf("tracked.limit = %v with no tracked_set_limit configured, want it unreported", got)
	}
}
