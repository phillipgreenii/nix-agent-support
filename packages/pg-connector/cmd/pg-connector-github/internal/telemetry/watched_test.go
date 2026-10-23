package telemetry

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/changes"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/classify"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/feed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groupsvc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

func TestWatchedEntitiesReportsTheSourceCountAndSkipsOnError(t *testing.T) {
	f := newChangeFixture(t)
	n, fail := 4, false
	f.build(func(o *Options) {
		o.Watched = func(context.Context) (int, error) {
			if fail {
				return 0, errors.New("store closed")
			}
			return n, nil
		}
	})
	if v := gaugeInt(t, f.metric(MetricWatchedEntities), ""); v != 4 {
		t.Errorf("watched.entities = %d, want 4", v)
	}
	fail = true
	if _, ok := f.collect()[MetricWatchedEntities]; ok {
		t.Error("watched.entities must be unreported while its source fails")
	}
}

func TestWatchedEntitiesIsUnreportedWithoutASource(t *testing.T) {
	f := newChangeFixture(t)
	f.build(nil)
	if _, ok := f.collect()[MetricWatchedEntities]; ok {
		t.Error("watched.entities reported with its source unset")
	}
}

// TestChangeInstrumentsKeepTheirNamesAndLabelSetsOnTheSharedPackage is the
// telemetry golden of the hoist: the eight instruments the shared package
// registers appear under this daemon's prefix with the label keys the dashboard
// and the alert rules read.
func TestChangeInstrumentsKeepTheirNamesAndLabelSetsOnTheSharedPackage(t *testing.T) {
	f := newChangeFixture(t)
	tel := f.build(func(o *Options) {
		o.ConfiguredQueries = func() []string { return []string{"mine"} }
		o.Pending = func(context.Context) (int, error) { return 1, nil }
		o.Watched = func(context.Context) (int, error) { return 1, nil }
		o.Queries = func(context.Context) ([]store.QueryRow, error) { return nil, nil }
		o.Consumers = fixedPositions(feed.Position{Consumer: "desk", View: feed.KindPR, Scope: "mine", LagRows: 1})
	})
	NewChangeObserver(tel).Detected(classify.KindMerged, groupsvc.OriginQuery, time.Second)
	NewChangeObserver(tel).Settled(changes.ResultSettled)
	f.collect() // a poll records desk as a stored consumer
	NewFeedObserver(tel).Redelivered("desk", "mine", 1)

	got := f.collect()
	for name, want := range map[string]string{
		MetricChangeSettle:          "result=settled",
		MetricChangeSettlePending:   "",
		MetricChangeDetectionDelay:  "kind=merged,origin=query",
		MetricConsumerLagRows:       "consumer=desk,query=mine",
		MetricConsumerOldestUnacked: "consumer=desk,query=mine",
		MetricConsumerRedeliveries:  "consumer=desk,query=mine",
		MetricQueryLastSuccessAge:   "query=mine",
		MetricWatchedEntities:       "",
	} {
		m, ok := got[name]
		if !ok {
			t.Errorf("instrument %s absent", name)
			continue
		}
		sets := labelSets(t, m)
		if len(sets) != 1 || sets[0] != want {
			t.Errorf("%s label sets = %v, want [%s]", name, sets, want)
		}
	}
}
