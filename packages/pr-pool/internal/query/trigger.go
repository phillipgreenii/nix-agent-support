package query

import "time"

// Trigger is a query's firing strategy (Q1) — the Strategy pattern. A query's
// firing is decoupled from the drain loop: the driver interprets the concrete
// trigger to decide whether the query runs on a given tick. The interface is a
// sealed union (the unexported kind marker prevents out-of-package
// implementations, so the driver's type switch is exhaustive).
type Trigger interface{ kind() triggerKind }

type triggerKind int

const (
	kindPeriod triggerKind = iota
	kindThreshold
	kindManual
)

// PeriodTrigger fires on each period tick — reproducing today's once-per-pass
// PollInterval pull. It is the default for the built-in roles, so dispatch
// matches today's coupled role+query.
type PeriodTrigger struct{ Every time.Duration }

func (PeriodTrigger) kind() triggerKind { return kindPeriod }

// ThresholdTrigger ("enough-events") fires when at least Count events of the
// Binds types are queued on the bus — the producer/consumer back-pressure case
// (e.g. only re-run worker discovery once feedback has produced >=1 work bead).
type ThresholdTrigger struct {
	Binds []string
	Count int
}

func (ThresholdTrigger) kind() triggerKind { return kindThreshold }

// ManualTrigger fires only on run-query / run-role (the spec-A smoke harness):
// out of band, once, regardless of cadence.
type ManualTrigger struct{}

func (ManualTrigger) kind() triggerKind { return kindManual }

// IsPeriod reports whether t is a period trigger (fires on the drain tick). A
// nil trigger defaults to period, so a query with no explicit trigger reproduces
// today's behavior.
func IsPeriod(t Trigger) bool {
	if t == nil {
		return true
	}
	return t.kind() == kindPeriod
}

// IsManual reports whether t is a manual trigger (never fires on a tick; only
// via the smoke harness).
func IsManual(t Trigger) bool {
	return t != nil && t.kind() == kindManual
}

// Threshold returns the trigger's threshold spec and true iff t is a
// ThresholdTrigger.
func Threshold(t Trigger) (ThresholdTrigger, bool) {
	if t == nil {
		return ThresholdTrigger{}, false
	}
	tt, ok := t.(ThresholdTrigger)
	return tt, ok
}
