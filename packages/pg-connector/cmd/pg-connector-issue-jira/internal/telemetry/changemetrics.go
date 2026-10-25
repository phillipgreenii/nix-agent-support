package telemetry

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/engine"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/feed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/groupsvc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// The instruments of the change engine and the change feed live in
// pkg/connectord/changemetrics, registered by New under metricPrefix, so their
// names are the Metric* constants of telemetry.go. This file adapts them to the
// daemon's observer wiring and holds the Jira-specific change series.

var _ groupsvc.Observer = (*Telemetry)(nil)

// ChangeObserver returns the engine.Observer that feeds change.settle and
// change.detection_delay_seconds from the change engine's events. On a nil
// Telemetry (the daemon runs on when telemetry fails to start) it does nothing.
func (t *Telemetry) ChangeObserver() engine.Observer {
	if t == nil {
		return noopChangeObserver{}
	}
	return t.changes.ChangeObserver()
}

// FeedObserver returns the feed.Observer that feeds consumer.redeliveries from
// the change feed's events. On a nil Telemetry it does nothing.
func (t *Telemetry) FeedObserver() feed.Observer {
	if t == nil {
		return noopFeedObserver{}
	}
	return t.changes.FeedObserver()
}

type noopChangeObserver struct{}

func (noopChangeObserver) Detected(string, string, time.Duration) {}
func (noopChangeObserver) Settled(string)                         {}

type noopFeedObserver struct{}

func (noopFeedObserver) Redelivered(string, string, int) {}

// Served records one group.staleness_seconds point: seconds past fresh_until,
// zero for a fresh group (groupsvc.Observer).
func (t *Telemetry) Served(group string, staleness time.Duration) {
	if t == nil {
		return
	}
	if staleness < 0 {
		staleness = 0
	}
	t.staleness.Record(context.Background(), staleness.Seconds(),
		metric.WithAttributes(attribute.String(keyGroup, entityGroupLabel(group))))
}

// Revalidated is the other half of groupsvc.Observer. The Jira catalogue has no
// revalidation series, so it records nothing.
func (t *Telemetry) Revalidated(string, string) {}

// entityGroupLabel passes a name from the closed set of stored groups and folds
// anything else to "unknown", so a group label can never carry an issue key.
func entityGroupLabel(g string) string {
	for _, k := range groups.All() {
		if g == k {
			return g
		}
	}
	return unknown
}

// CountRowsWithoutKind counts the stored change rows that carry no kind: an
// empty kinds list, an empty column or a list holding only the empty word. It
// is the source of change.rows_without_kind (Options.RowsWithoutKind), and the
// value MUST be zero.
func CountRowsWithoutKind(ctx context.Context, st *store.Store) (int, error) {
	var n int
	err := st.DB().QueryRowContext(ctx,
		`SELECT count(*) FROM change_log WHERE kinds IS NULL OR trim(kinds) IN ('', '[]', '[""]')`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("telemetry: count change rows without kind: %w", err)
	}
	return n, nil
}
