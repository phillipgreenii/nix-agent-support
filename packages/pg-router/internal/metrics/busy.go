package metrics

import (
	"context"
	"sort"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// MetricBusy is a gauge, per role and per pull source: 1 while that entity is
// busy, else 0 (bead pg2-8ij5o, operator ruling 30 of docket pg2-12gr4). The
// `kind` label is BusyKindRole or BusyKindSource and `name` is the role or
// source name (both config-bounded, DEC-OBS-5). A [[query]] is a pull source
// under the same name, so there is no separate query series. Exposed as
// pg_router_busy.
//
// BUSY IS DERIVED FROM STATE THE CORE ALREADY HOLDS AND NOTHING ELSE
// (GOAL-MIN-1): a role is busy while it has an offer in flight or has been
// declined busy since its last progress (the busy count of
// MetricRoleBusySinceProgress, which counts the decline KIND, never the
// decline's reason string); a source is busy while a fetch of it is in flight.
const MetricBusy = "pg_router_busy"

// The two values of MetricBusy's `kind` label.
const (
	BusyKindRole   = "role"
	BusyKindSource = "source"
)

// RoleBusy is the one definition of a busy role, shared by MetricBusy and the
// status reply so the two cannot disagree: a role is busy while it has an offer
// in flight, or when it has been declined busy since it last made progress. It
// reads only the in-flight state and p.Busy, which OnRoleDeclined counts from the
// decline's kind; it never sees a decline reason.
func RoleBusy(inFlight bool, p RoleProgress) bool {
	return inFlight || p.Busy > 0
}

// WithBusySources registers MetricBusy's source series, reading fn on each
// collect: the names of the sources with a fetch in flight right now (typically
// the status reply's own in-flight reader, so metric and status agree). Every
// WithPullSources source gets a series, 0 while it is not in the list. Without
// this option MetricBusy carries role series only (and is registered only when
// WithRoleProgress is given).
func WithBusySources(fn func() []string) Option {
	return func(o *options) { o.busySources = fn }
}

// RoleBusy returns, per role with a progress series, whether it is busy right
// now (see the package-level RoleBusy), applying the same in-flight read as the
// age gauge. It returns nil when WithRoleProgress was not given.
func (e *Emitter) RoleBusy() map[string]bool {
	if e.progress == nil {
		return nil
	}
	inFlight := e.progressInFlight()
	e.progMu.Lock()
	defer e.progMu.Unlock()
	out := make(map[string]bool, len(e.progress))
	for role, rp := range e.progress {
		out[role] = RoleBusy(inFlight[role] > 0, RoleProgress{Busy: rp.busy})
	}
	return out
}

// registerBusy registers MetricBusy. seeded is the set of sources that get a
// series from the start.
func (e *Emitter) registerBusy(m metric.Meter, sources func() []string, seeded []string) error {
	sort.Strings(seeded)
	_, err := m.Int64ObservableGauge(
		MetricBusy,
		metric.WithUnit("{entity}"),
		metric.WithDescription("1 while a role (kind=role) or pull source (kind=source) is busy, else 0: a role is busy while an offer to it is in flight or a busy-kind decline has been counted since its last progress (the decline kind, never its reason); a source is busy while a fetch of it is in flight"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			one := func(b bool) int64 {
				if b {
					return 1
				}
				return 0
			}
			for role, b := range e.RoleBusy() {
				o.Observe(one(b), metric.WithAttributes(attribute.String("kind", BusyKindRole), attribute.String("name", role)))
			}
			if sources == nil {
				return nil
			}
			busy := map[string]bool{}
			for _, name := range sources() {
				busy[name] = true
			}
			for _, name := range seeded {
				if !busy[name] {
					o.Observe(0, metric.WithAttributes(attribute.String("kind", BusyKindSource), attribute.String("name", name)))
				}
			}
			for name := range busy {
				o.Observe(1, metric.WithAttributes(attribute.String("kind", BusyKindSource), attribute.String("name", name)))
			}
			return nil
		}),
	)
	return err
}
