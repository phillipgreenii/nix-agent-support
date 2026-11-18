package levels

import "time"

// Reading is the optional numeric reading behind a level.
type Reading struct {
	Value float64
	Unit  string
}

// Observation is what a sensor measured for one resource, before the state
// machine decides the level it may announce.
type Observation struct {
	Resource string
	Scale    Scale
	// Level is the level of the raw reading (its band, with no hysteresis).
	Level string
	// Reading is the numeric reading behind Level, when there is one.
	Reading *Reading
	// ObservedAt is the time of the measurement; the zero value means "now".
	ObservedAt time.Time
	// CanRise is the hysteresis gate: it is asked, with the current level,
	// before a rise to the next better level. Nil means "no margin needed".
	CanRise func(current string) bool
	// BypassDwell lets a rise skip the dwell and the margin and land on Level
	// at once (the refresh variable for a credential, a claude window reset).
	BypassDwell bool
}

// State is the dwell state of one resource: the level last announced and when
// it last changed. Extra is a generic key/value slot for a sensor's own
// counters (the network failure count).
type State struct {
	Level string            `json:"level"`
	Since time.Time         `json:"since"`
	Extra map[string]string `json:"extra,omitempty"`
}

// Initial is the state of a resource with no (or a lost) record: the WORST
// level of the scale, entered now, so the sensor rises one level per dwell and
// a lost file never releases the herd.
func Initial(s Scale, now time.Time) State {
	return State{Level: s.Worst(), Since: now}
}

// Step applies one observation to the state and returns the new state and
// whether the level changed.
//
//   - A fall (observed worse than current) is immediate and may skip levels.
//   - A rise moves ONE level, only when the dwell has elapsed since the last
//     change and the hysteresis gate passes.
//   - With bypass (or obs.BypassDwell) a rise lands on the observed level at
//     once.
//
// A state whose level is not in the scale is treated as lost (Initial).
func Step(st State, obs Observation, now time.Time, dwell time.Duration, bypass bool) (State, bool) {
	cur := obs.Scale.Index(st.Level)
	if cur < 0 {
		st = State{Level: obs.Scale.Worst(), Since: now, Extra: st.Extra}
		cur = 0
	}
	want := obs.Scale.Index(obs.Level)
	if want < 0 {
		return st, false
	}
	switch {
	case want < cur:
		return State{Level: obs.Level, Since: now, Extra: st.Extra}, true
	case want == cur:
		return st, false
	}
	if bypass || obs.BypassDwell {
		return State{Level: obs.Level, Since: now, Extra: st.Extra}, true
	}
	if now.Sub(st.Since) < dwell {
		return st, false
	}
	if obs.CanRise != nil && !obs.CanRise(st.Level) {
		return st, false
	}
	return State{Level: obs.Scale[cur+1], Since: now, Extra: st.Extra}, true
}
