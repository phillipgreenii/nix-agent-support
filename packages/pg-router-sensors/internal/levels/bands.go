package levels

import (
	"errors"
	"fmt"
)

// Direction says which way a reading improves.
type Direction int

const (
	// HigherBetter: a larger reading is a better level (free disk).
	HigherBetter Direction = iota
	// LowerBetter: a smaller reading is a better level (load, swap, pace).
	LowerBetter
)

// Band is one level's numeric range. Min is INCLUSIVE and Max EXCLUSIVE; a nil
// bound is open. So a value exactly on a boundary belongs to the band that
// states it as its lower bound.
type Band struct {
	Level string
	Min   *float64
	Max   *float64
}

// Contains reports whether v lies in [Min, Max).
func (b Band) Contains(v float64) bool {
	if b.Min != nil && v < *b.Min {
		return false
	}
	if b.Max != nil && v >= *b.Max {
		return false
	}
	return true
}

// Num returns a pointer to v, for writing band tables.
func Num(v float64) *float64 { return &v }

// Hysteresis is the margin a reading must clear to leave a band upward. The
// required value is boundary*Factor + Add; a Factor of 0 is read as 1. The
// factors of the removed disk-watchdog are kept: 1.25 for a higher-is-better
// reading and 0.8 for a lower-is-better one; a pace rise uses Add = -0.05.
type Hysteresis struct {
	Factor float64
	Add    float64
}

// Standard hysteresis settings.
var (
	// HysteresisHigher is the multiplicative margin of a higher-is-better reading.
	HysteresisHigher = Hysteresis{Factor: 1.25}
	// HysteresisLower is the multiplicative margin of a lower-is-better reading.
	HysteresisLower = Hysteresis{Factor: 0.8}
	// HysteresisPace is the additive recovery band of the claude pace.
	HysteresisPace = Hysteresis{Factor: 1, Add: -0.05}
)

// Required returns the reading a rise across boundary must reach.
func (h Hysteresis) Required(boundary float64) float64 {
	f := h.Factor
	if f == 0 {
		f = 1
	}
	return boundary*f + h.Add
}

// Table classifies a numeric reading into a level of its scale. Bands holds
// one band per level of the scale, in scale order (worst first) and contiguous.
type Table struct {
	Resource string
	Unit     string
	Scale    Scale
	Dir      Direction
	Bands    []Band
	Hyst     Hysteresis
}

// Validate checks that the table has one band per scale level, in scale order.
func (t Table) Validate() error {
	if len(t.Bands) != len(t.Scale) {
		return fmt.Errorf("table %q: %d bands for %d levels", t.Resource, len(t.Bands), len(t.Scale))
	}
	for i, b := range t.Bands {
		if b.Level != t.Scale[i] {
			return fmt.Errorf("table %q: band %d is %q, want %q", t.Resource, i, b.Level, t.Scale[i])
		}
	}
	return nil
}

// Classify returns the level whose band contains v. A value in no band (a
// malformed table) is an error.
func (t Table) Classify(v float64) (string, error) {
	for _, b := range t.Bands {
		if b.Contains(v) {
			return b.Level, nil
		}
	}
	return "", fmt.Errorf("table %q: no band holds %v", t.Resource, v)
}

// boundary returns the numeric edge between level index i and i+1 (i < len-1):
// the lower bound of the better band for a higher-is-better table, the upper
// bound of the better band for a lower-is-better one.
func (t Table) boundary(i int) (float64, bool) {
	better := t.Bands[i+1]
	if t.Dir == HigherBetter {
		if better.Min == nil {
			return 0, false
		}
		return *better.Min, true
	}
	if better.Max == nil {
		return 0, false
	}
	return *better.Max, true
}

// CanRise reports whether v clears, with margin, the boundary between the
// current level and the next better one. It is the hysteresis gate to pass as
// Observation.CanRise. A current level that is not in the scale, or the best
// level, cannot rise.
func (t Table) CanRise(current string, v float64) bool {
	i := t.Scale.Index(current)
	if i < 0 || i >= len(t.Scale)-1 {
		return false
	}
	b, ok := t.boundary(i)
	if !ok {
		return false
	}
	need := t.Hyst.Required(b)
	if t.Dir == HigherBetter {
		return v >= need
	}
	return v <= need
}

// Observe classifies v and pairs it with the hysteresis gate of this table.
func (t Table) Observe(v float64) (Observation, error) {
	lvl, err := t.Classify(v)
	if err != nil {
		return Observation{}, err
	}
	return Observation{
		Resource: t.Resource,
		Scale:    t.Scale,
		Level:    lvl,
		Reading:  &Reading{Value: v, Unit: t.Unit},
		CanRise:  func(current string) bool { return t.CanRise(current, v) },
	}, nil
}

// BandInfo is one band of the `--bands` output: min inclusive, max exclusive,
// null for an open bound.
type BandInfo struct {
	Level string   `json:"level"`
	Min   *float64 `json:"min"`
	Max   *float64 `json:"max"`
}

// Info is the `--bands` record of one resource.
type Info struct {
	Resource string     `json:"resource"`
	Unit     string     `json:"unit"`
	Bands    []BandInfo `json:"bands"`
}

// Info renders the table for `--bands`.
func (t Table) Info() Info {
	out := Info{Resource: t.Resource, Unit: t.Unit, Bands: []BandInfo{}}
	for _, b := range t.Bands {
		out.Bands = append(out.Bands, BandInfo(b))
	}
	return out
}

// NoBands is the `--bands` record of a resource with a two-level scale: an
// empty band list.
func NoBands(resource, unit string) Info {
	return Info{Resource: resource, Unit: unit, Bands: []BandInfo{}}
}

// ErrNoReading is returned by a sensor's measurement when it has nothing to say
// about any resource.
var ErrNoReading = errors.New("no reading")
