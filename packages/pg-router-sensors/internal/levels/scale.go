// Package levels is the shared, scheduler-free library of the sensors binary:
// the level scales, the band tables that classify a numeric reading, and the
// level state machine (a fall is immediate, a rise is one level per dwell, a
// rise out of a band needs the reading to clear the boundary with margin).
//
// A sensor owns its thresholds, hysteresis and dwell and the state they need;
// the router holds none of them. The machine is a pure function of
// (state, observation, time), so a one-shot sensor persists the state in a
// file between runs (see Store) and the tests drive it with a fake clock.
package levels

// Scale is an ordered list of level names, WORST first. A level's position is
// its rank: a higher index is better.
type Scale []string

// The scales the sensors use. Callers MAY define their own.
var (
	// FourStep is the graded scale of the host and claude resources.
	FourStep = Scale{"EXHAUSTED", "TIGHT", "OK", "PLENTY"}
	// Binary is the two-level scale of credentials and reachability.
	Binary = Scale{"UNAVAILABLE", "AVAILABLE"}
	// UpDown is the two-level scale of a service that is up or down.
	UpDown = Scale{"DOWN", "UP"}
)

// Index returns the rank of level in the scale, or -1 when it is not a member.
func (s Scale) Index(level string) int {
	for i, l := range s {
		if l == level {
			return i
		}
	}
	return -1
}

// Worst returns the worst level of the scale (the empty string for an empty
// scale).
func (s Scale) Worst() string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}

// Best returns the best level of the scale (the empty string for an empty
// scale).
func (s Scale) Best() string {
	if len(s) == 0 {
		return ""
	}
	return s[len(s)-1]
}
