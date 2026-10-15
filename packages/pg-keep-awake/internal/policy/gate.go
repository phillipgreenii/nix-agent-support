package policy

import "fmt"

// Origin: the gate, the hysteresis latch and the fail-open rule are ported from
// pa-monitor's internal/core/caffeinate/battery.go (BatteryPolicy, BatteryGate,
// evalBattery). The blind rule is new in pg-keep-awake.

// Default levels of the battery rule (this repo's ADR 0095, pa-monitor stops
// holding the Mac awake on a low battery).
const (
	DefaultHoldPct       = 30
	DefaultReleasePct    = 20
	DefaultHysteresisPct = 5
)

// BlindReads is how many consecutive failed reads make the blind rule apply.
const BlindReads = 3

// Policy is the low-battery rule. While the Mac draws from its battery:
//
//   - at or below HoldPct no NEW assertion is started;
//   - at or below ReleasePct a RUNNING assertion is stopped;
//   - the normal rule resumes once AC returns, or the charge rises above
//     HoldPct+HysteresisPct (so a charge hovering at the threshold does not
//     flap the assertion).
//
// A HoldPct of 0 (or less) disables the rule entirely; a ReleasePct of 0 (or
// less) never stops a running assertion from a reading. A ReleasePct above
// HoldPct behaves as HoldPct.
type Policy struct {
	HoldPct       int
	ReleasePct    int
	HysteresisPct int
}

// DefaultPolicy returns the default levels: hold 30, release 20, hysteresis 5.
func DefaultPolicy() Policy {
	return Policy{HoldPct: DefaultHoldPct, ReleasePct: DefaultReleasePct, HysteresisPct: DefaultHysteresisPct}
}

// Enabled reports whether the rule is on (hold above 0).
func (p Policy) Enabled() bool { return p.HoldPct > 0 }

// EffectiveRelease is the release level after the cap: min(release, hold). A
// result of 0 or less means a reading never releases.
func (p Policy) EffectiveRelease() int { return min(p.ReleasePct, p.HoldPct) }

// Gate is the verdict of the low-battery rule for one tick.
type Gate int

// The gate names are the stable machine tokens of pa-monitor's battery.go and
// of the status wire: ok, no_spawn, released, unknown, disabled.
const (
	// GateDisabled: the rule is off (hold is 0). Behaves as ok.
	GateDisabled Gate = iota
	// GateOK: on AC, or charged above the hold level plus hysteresis. Hold
	// while any lease is live.
	GateOK
	// GateNoSpawn: on battery at or below the hold level, above the release
	// level. A running assertion is kept; none is started.
	GateNoSpawn
	// GateReleased: on battery at or below the release level (or blind). Stop
	// the assertion; leases stay.
	GateReleased
	// GateUnknown: the battery could not be read. Fails open (behaves as ok)
	// except for the blind rule.
	GateUnknown
)

// String is the stable machine token used on the status wire and in logs.
func (g Gate) String() string {
	switch g {
	case GateOK:
		return "ok"
	case GateNoSpawn:
		return "no_spawn"
	case GateReleased:
		return "released"
	case GateUnknown:
		return "unknown"
	default:
		return "disabled"
	}
}

// ParseGate is the inverse of String; it fails on any other token.
func ParseGate(s string) (Gate, error) {
	for _, g := range []Gate{GateDisabled, GateOK, GateNoSpawn, GateReleased, GateUnknown} {
		if g.String() == s {
			return g, nil
		}
	}
	return GateDisabled, fmt.Errorf("policy: unknown gate %q", s)
}

// Restricts reports whether the gate withholds the assertion (no new start).
func (g Gate) Restricts() bool { return g == GateNoSpawn || g == GateReleased }

// Blind is the blind-rule predicate, usable by the anchor. After BlindReads
// consecutive failed reads, if the last good reading was on battery at or below
// the hold level, the battery is treated as released. The hold level (not the
// release level) is the line: by the time a reading at or below release has
// been seen the assertion is already gone, so a rule keyed on release could
// never fire. haveLastGood is false when no good reading was ever seen; a rule
// with hold at or below 0 is off and is never blind.
func Blind(consecutiveFailures int, lastGood Reading, haveLastGood bool, holdPct int) bool {
	if holdPct <= 0 || consecutiveFailures < BlindReads || !haveLastGood {
		return false
	}
	return lastGood.OnBattery && lastGood.Percent != NoPercent && lastGood.Percent <= holdPct
}

// Status is the observable state of the rule after one Observe.
type Status struct {
	Gate Gate
	// Reading is the reading this verdict is based on: the current one after a
	// good read, the zero-charge-unknown reading after a failed one.
	Reading Reading
	// ReadFailures is the current count of consecutive failed reads.
	ReadFailures int
	// Blind is true when the gate is released by the blind rule.
	Blind bool
}

// Evaluator holds the in-memory state of the rule: the hysteresis latch, the
// consecutive read-failure counter and the last good reading. The latch is lost
// on a restart, which is accepted (a keeper restart on battery between hold+1
// and hold+hysteresis loses it; the anchor still releases at the release
// level). An Evaluator is not safe for concurrent use.
type Evaluator struct {
	// Policy holds the levels; it MAY be replaced between Observe calls.
	Policy Policy

	latched      bool
	failures     int
	lastGood     Reading
	haveLastGood bool
}

// NewEvaluator returns an Evaluator with an unset latch.
func NewEvaluator(p Policy) *Evaluator { return &Evaluator{Policy: p} }

// Latched reports the hysteresis latch, for tests and diagnostics.
func (e *Evaluator) Latched() bool { return e.latched }

// Observe feeds one read outcome (the reading and the error of Reader.Read) to
// the rule and returns the verdict. A failed read is err != nil, or a reading
// on battery with no percentage.
func (e *Evaluator) Observe(r Reading, err error) Status {
	p := e.Policy
	if !p.Enabled() {
		e.latched, e.failures, e.haveLastGood = false, 0, false
		e.lastGood = Reading{Percent: NoPercent}
		return Status{Gate: GateDisabled, Reading: Reading{Percent: NoPercent}}
	}
	if err != nil || (r.OnBattery && r.Percent == NoPercent) {
		// Fail open: forget the latch so an unreadable battery cannot keep the
		// hold withheld, and never release on a guess, except the blind rule.
		e.latched = false
		e.failures++
		st := Status{Gate: GateUnknown, Reading: Reading{Percent: NoPercent}, ReadFailures: e.failures}
		if Blind(e.failures, e.lastGood, e.haveLastGood, p.HoldPct) {
			st.Gate, st.Blind = GateReleased, true
		}
		return st
	}
	e.failures = 0
	e.lastGood, e.haveLastGood = r, true
	switch {
	case !r.OnBattery:
		e.latched = false
	case r.Percent <= p.HoldPct:
		e.latched = true
	case r.Percent > p.HoldPct+p.HysteresisPct:
		e.latched = false
	}
	st := Status{Reading: r}
	switch rel := p.EffectiveRelease(); {
	case !e.latched:
		st.Gate = GateOK
	case rel > 0 && r.Percent <= rel:
		st.Gate = GateReleased
	default:
		st.Gate = GateNoSpawn
	}
	return st
}

// Evaluate reads the battery once through rd and observes the outcome.
func (e *Evaluator) Evaluate(rd Reader) Status {
	if !e.Policy.Enabled() {
		return e.Observe(Reading{Percent: NoPercent}, nil) // no read when disabled
	}
	r, err := rd.Read()
	return e.Observe(r, err)
}
