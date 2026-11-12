package levels

import (
	"slices"
	"strconv"
)

// Step counts reported by Shortfalls when no distance can be measured.
const (
	// StepsUnknown: the level is unknown (no level held) or stale, so how far it
	// is from an allowed level is not a measurement.
	StepsUnknown = -1
	// StepsNone: the resource's clauses allow no level at all (an empty
	// intersection), so no relaxation of the threshold could clear them.
	StepsNone = -2
)

// Shortfall says how far one resource is from satisfying a requirement: the
// observability companion of Decide, never an input to it.
type Shortfall struct {
	Resource string
	// Steps is the distance, in scale steps, from the level the resource reads at
	// to the nearest level that satisfies every clause the requirement has on it
	// (1: relaxing the threshold by one step would clear it), or StepsUnknown or
	// StepsNone. A two-level scale always reports 1 for a level that is held.
	Steps int
	// Sole is true when this is the only resource with an unmet clause.
	Sole bool
}

// StepsLabel renders steps as a metric label value: a number, "unknown" or "none".
func StepsLabel(steps int) string {
	switch steps {
	case StepsUnknown:
		return "unknown"
	case StepsNone:
		return "none"
	}
	return strconv.Itoa(steps)
}

// Shortfalls returns one Shortfall per resource that has an unmet clause in req,
// in the order of the first unmet clause on each. It is pure and reads the same
// unmet clauses Decide reports, so the two cannot disagree on which resources
// block: it returns nothing exactly when Decide admits.
func Shortfalls(snap Snapshot, reg Registry, req Requirement) []Shortfall {
	failing := failingClauses(snap, reg, req)
	var out []Shortfall
	seen := map[string]bool{}
	for _, f := range failing {
		if seen[f.Resource] {
			continue
		}
		seen[f.Resource] = true
		out = append(out, Shortfall{Resource: f.Resource, Steps: stepsBelow(reg[f.Resource], req, f)})
	}
	for i := range out {
		out[i].Sole = len(out) == 1
	}
	return out
}

// stepsBelow measures the distance for the resource of failing entry f.
func stepsBelow(sc Scale, req Requirement, f Entry) int {
	if f.Kind != KindLevel {
		return StepsUnknown
	}
	if len(sc.Levels) == 2 {
		return 1
	}
	pos := slices.Index(sc.Levels, f.Level)
	if pos < 0 {
		return StepsUnknown
	}
	// Every clause on the resource must hold, so the nearest level that clears
	// them all is the nearest member of the intersection of their allowed sets.
	best := StepsNone
	for i, l := range sc.Levels {
		ok := true
		for _, c := range req.Clauses {
			if c.Resource == f.Resource && !slices.Contains(c.Allowed, l) {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		d := i - pos
		if d < 0 {
			d = -d
		}
		if best == StepsNone || d < best {
			best = d
		}
	}
	return best
}
