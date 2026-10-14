package telemetry

import (
	"context"
	"os"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groupsvc"
)

// Metrics is the name the Phase 3a contract uses for the daemon's instrument
// holder; it is the Telemetry that New returns.
type Metrics = Telemetry

// The result words of a revalidation as the revalidation handler reports them,
// and the label values the metric carries for them.
const (
	resultExtended = "extended"
	resultRepulled = "repulled"
	resultMaxAge   = "max_age"

	labelExtended       = "extended"
	labelRepulled       = "repulled"
	labelForcedByMaxAge = "forced_by_max_age"
)

// RecordEvictions adds n to store.evictions, the count the expiry sweep reports
// (summary.Service.Sweep). A non-positive n records nothing.
func (t *Telemetry) RecordEvictions(n int) {
	if n > 0 {
		t.evictions.Add(context.Background(), int64(n))
	}
}

// NewGroupObserver returns the groupsvc.Observer that feeds revalidation and
// group.staleness_seconds from the group engine's events.
func NewGroupObserver(m *Metrics) groupsvc.Observer { return groupObserver{m: m} }

type groupObserver struct{ m *Metrics }

// Served records one group.staleness_seconds point: seconds past fresh_until,
// zero for a fresh group.
func (o groupObserver) Served(group string, staleness time.Duration) {
	if staleness < 0 {
		staleness = 0
	}
	o.m.staleness.Record(context.Background(), staleness.Seconds(),
		metric.WithAttributes(attribute.String(keyGroup, entityGroupLabel(group))))
}

// Revalidated counts one revalidation under its group and outcome.
func (o groupObserver) Revalidated(group, result string) {
	o.m.revalidation.Add(context.Background(), 1, metric.WithAttributes(
		attribute.String(keyGroup, entityGroupLabel(group)),
		attribute.String(keyResult, revalidationResultLabel(result)),
	))
}

// revalidationResultLabel maps the handler's result word to the spec's three
// meanings; an unrecognised word folds to "unknown".
func revalidationResultLabel(result string) string {
	switch result {
	case resultExtended:
		return labelExtended
	case resultRepulled:
		return labelRepulled
	case resultMaxAge:
		return labelForcedByMaxAge
	default:
		return unknown
	}
}

// entityGroupLabel passes a name from the closed set of entity groups and folds
// anything else to "unknown", so a group label can never carry an id.
func entityGroupLabel(g string) string {
	switch g {
	case "summary", "detail", "conversation", "files", "commits", "pending", "runs":
		return g
	default:
		return unknown
	}
}

// storeSize is the summed size of the store file and its -wal and -shm
// siblings; a file that does not exist counts as zero.
func storeSize(path string) int64 {
	var total int64
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if fi, err := os.Stat(p); err == nil {
			total += fi.Size()
		}
	}
	return total
}
