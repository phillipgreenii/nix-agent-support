package serve_test

import (
	"context"
	"testing"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ops/explain"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/serve"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/servetest"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/telemetry"
)

// Revalidation wiring and dependent-group queuing, observable end to end. The
// daemon runs in this process (serve.Run, with its gh the counting shim) so the
// test can read the revalidation counter from a manual metric reader.

// revalidations sums the revalidation counter by its result label.
func revalidations(t *testing.T, reader *sdkmetric.ManualReader) map[string]int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	out := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != telemetry.MetricRevalidation {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("%s is a %T, want an int64 sum", m.Name, m.Data)
			}
			for _, dp := range sum.DataPoints {
				result, _ := dp.Attributes.Value("result")
				out[result.AsString()] += dp.Value
			}
		}
	}
	return out
}

func TestRevalidation_AnUnchangedSummaryExtendsDetailAndConversationAndAHeadChangeRepullsTheRest(t *testing.T) {
	// The summary is re-read every second; detail and conversation are fresh for
	// two seconds and may be extended up to an hour.
	d := servetest.NewDaemon(t, servetest.Options{
		Before: seed3a,
		Config: map[string]any{
			"summary_ttl": "1s", "terminal_ttl": "1s", "detail_ttl": "2s", "detail_max_age": "1h",
			"conversation_ttl": "2s", "conversation_max_age": "1h",
		},
	})
	reader := sdkmetric.NewManualReader()
	p := runInProcess(t, d, func(o *serve.Options) { o.MeterProvider = sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)) })
	defer p.stop(t)
	for _, q := range []string{servetest.QueryMine, servetest.QueryTeam} {
		mustList(t, d, q, nil)
	}

	// A first sighting populates detail, conversation and runs; files and commits
	// are not populated (they are fetched on a read, or on a head change).
	until(t, 30*time.Second, "the first-sighting population", func() bool {
		return d.Shim.CountKind("runs") >= 3 && d.Shim.CountKind("reviews") >= 3 && d.Shim.CountKind("detail") >= 1
	})
	if got := d.Shim.CountKind("files") + d.Shim.CountKind("commits"); got != 0 {
		t.Errorf("a first sighting fetched files or commits (%d calls)", got)
	}
	// Fill files and commits for prA so a head change has something to re-pull.
	callOK(t, d, "pr", "files", map[string]any{"id": prA.Ref()})
	callOK(t, d, "pr", "commits", map[string]any{"id": prA.Ref()})

	detailFreshUntil := func() time.Time {
		res := decode[explain.Result](t, callOK(t, d, "pr", "explain", map[string]any{"id": prA.Ref()}))
		for _, g := range res.Groups {
			if g.Group == "detail" && g.FreshUntil != nil {
				return *g.FreshUntil
			}
		}
		t.Fatal("no detail group")
		return time.Time{}
	}
	detailCalls, conversationCalls := d.Shim.CountKind("detail"), d.Shim.CountKind("reviews")
	firstUntil, summariesBefore := detailFreshUntil(), d.Shim.CountKind("nodes")

	// Summary refreshes keep coming and find the PR unchanged: the detail and
	// conversation groups are extended instead of re-pulled, and the counter says
	// so, before their hard age.
	until(t, 30*time.Second, "an extension to be counted", func() bool { return revalidations(t, reader)["extended"] > 0 })
	until(t, 30*time.Second, "the detail group's fresh_until to move", func() bool { return detailFreshUntil().After(firstUntil) })
	time.Sleep(2500 * time.Millisecond) // longer than detail_ttl: without extension a re-pull would have happened
	if got := d.Shim.CountKind("nodes"); got <= summariesBefore {
		t.Fatalf("fixture: the summary was not refreshed in the meantime (%d calls)", got)
	}
	if got := d.Shim.CountKind("detail"); got != detailCalls {
		t.Errorf("detail was fetched %d more times although every summary refresh was unchanged", got-detailCalls)
	}
	if got := d.Shim.CountKind("reviews"); got != conversationCalls {
		t.Errorf("the conversation was fetched %d more times although every summary refresh was unchanged", got-conversationCalls)
	}
	if got := revalidations(t, reader); got["extended"] == 0 || got["repulled"] == 0 {
		t.Errorf("revalidation counter = %v, want extended (unchanged refreshes) and repulled (the first sightings)", got)
	}

	// A head change in the shim: the next summary refresh queues files, commits
	// and runs for a re-pull, with no read asking for them.
	filesBefore, commitsBefore, runsBefore := d.Shim.CountKind("files"), d.Shim.CountKind("commits"), d.Shim.CountKind("runs")
	changed := prA
	changed.Head = "head-3-new"
	d.Shim.SetPRs(servetest.MineExpr, changed, prB, prC)
	d.Shim.SetPRs(servetest.TeamExpr, changed, prB)
	until(t, 30*time.Second, "files, commits and runs to be re-pulled after the head change", func() bool {
		return d.Shim.CountKind("files") > filesBefore && d.Shim.CountKind("commits") > commitsBefore && d.Shim.CountKind("runs") > runsBefore
	})
	if got := revalidations(t, reader)["repulled"]; got < 4 {
		t.Errorf("repulled = %d, want the first sightings plus the head change's groups", got)
	}
}
