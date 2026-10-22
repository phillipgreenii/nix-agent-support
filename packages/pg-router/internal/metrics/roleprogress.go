package metrics

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Decline kinds an exit-9 body may carry (the wire contract's optional decline
// kind; wireclient.BusyDecline.Kind relays the raw text). An absent or unknown
// kind counts as busy ("absent means busy", design section 5).
const (
	DeclineKindFault   = "fault"
	DeclineKindNeutral = "neutral"
)

// roleProgress is one role's state behind the three role-progress gauges.
type roleProgress struct {
	at           time.Time // last progress, or daemon start; carries the monotonic reading
	unsuccessful int64
	busy         int64
}

// roleProgressLocked returns role's state, seeding it at now on first sight.
// The caller holds progMu and has checked e.progress is non-nil.
func (e *Emitter) roleProgressLocked(role string) *roleProgress {
	rp := e.progress[role]
	if rp == nil {
		rp = &roleProgress{at: e.now()}
		e.progress[role] = rp
	}
	return rp
}

// OnRoleSucceeded implements orchestrator.RoleProgressObserver (structurally):
// an accepted offer whose handler returned no error is progress. It resets the
// age and both counts; the series stays.
func (e *Emitter) OnRoleSucceeded(role string) {
	if e.progress == nil {
		return
	}
	e.progMu.Lock()
	defer e.progMu.Unlock()
	*e.roleProgressLocked(role) = roleProgress{at: e.now()}
}

// OnRoleFailed implements orchestrator.RoleProgressObserver: the handler
// returned err from an offer that was not a shutdown hand-back. A routine
// sentinel error (IsRoutineHandlerError: the role did run) is progress, exactly
// like OnRoleSucceeded. Any other error increments the unsuccessful count and
// leaves the age alone. A dispatch-retry hand-back follows this same call and
// adds nothing of its own, so a retried failure is counted once.
func (e *Emitter) OnRoleFailed(role string, err error) {
	if IsRoutineHandlerError(role, err) {
		e.OnRoleSucceeded(role)
		return
	}
	if e.progress == nil {
		return
	}
	e.progMu.Lock()
	defer e.progMu.Unlock()
	e.roleProgressLocked(role).unsuccessful++
}

// OnRoleDeclined implements orchestrator.RoleProgressObserver: an exit-9
// decline carrying kind (the raw body text, "" when absent). Fault counts toward
// unsuccessful, neutral toward neither, and anything else (busy, absent or an
// unknown kind) toward busy.
func (e *Emitter) OnRoleDeclined(role, kind string) {
	if e.progress == nil || kind == DeclineKindNeutral {
		return
	}
	e.progMu.Lock()
	defer e.progMu.Unlock()
	rp := e.roleProgressLocked(role)
	if kind == DeclineKindFault {
		rp.unsuccessful++
	} else {
		rp.busy++
	}
}

// registerRoleProgress seeds the per-role progress state for roles at start and
// registers the three observable gauges that read it at collect time.
func (e *Emitter) registerRoleProgress(m metric.Meter, roles []string, start time.Time, inFlight func() map[string]int, ready func() bool) error {
	e.progress = make(map[string]*roleProgress, len(roles))
	for _, r := range roles {
		e.progress[r] = &roleProgress{at: start}
	}
	// snapshot copies the state under the lock so a callback never holds it
	// across the in-flight read.
	snapshot := func() map[string]roleProgress {
		e.progMu.Lock()
		defer e.progMu.Unlock()
		out := make(map[string]roleProgress, len(e.progress))
		for r, rp := range e.progress {
			out[r] = *rp
		}
		return out
	}
	if _, err := m.Float64ObservableGauge(
		MetricRoleLastProgressAge,
		metric.WithUnit("s"),
		metric.WithDescription("seconds since each role last made progress (an accepted offer with no handler error, or with a routine budget-stop, usage-limit or triager error), measured at collection on the router's monotonic clock; 0 while an offer is in flight and at daemon start"),
		metric.WithFloat64Callback(func(_ context.Context, o metric.Float64Observer) error {
			var busy map[string]int
			if inFlight != nil && ready() {
				busy = inFlight()
			}
			for role, rp := range snapshot() {
				age := 0.0
				if busy[role] == 0 {
					age = max(e.awakeSince(rp.at), 0).Seconds()
				}
				o.Observe(age, metric.WithAttributes(attribute.String("role", role)))
			}
			return nil
		}),
	); err != nil {
		return err
	}
	if _, err := m.Int64ObservableGauge(
		MetricRoleUnsuccessfulSinceProgress,
		metric.WithUnit("{offer}"),
		metric.WithDescription("per role, offers since its last progress that ended unsuccessfully: a handler error that is not a routine sentinel, or an exit-9 decline of kind fault; reset by progress and at daemon start"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			for role, rp := range snapshot() {
				o.Observe(rp.unsuccessful, metric.WithAttributes(attribute.String("role", role)))
			}
			return nil
		}),
	); err != nil {
		return err
	}
	_, err := m.Int64ObservableGauge(
		MetricRoleBusySinceProgress,
		metric.WithUnit("{offer}"),
		metric.WithDescription("per role, offers since its last progress that were declined busy (exit 9 of kind busy, absent or unknown; neutral and shutdown hand-backs do not count); reset by progress and at daemon start"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			for role, rp := range snapshot() {
				o.Observe(rp.busy, metric.WithAttributes(attribute.String("role", role)))
			}
			return nil
		}),
	)
	return err
}
