// Package power reads the Mac's power source and battery charge so the
// caffeinate manager can stop holding the machine awake on a low battery
// (bead pg2-s86z9).
//
// Reader is a Strategy: DarwinReader parses `pmset -g batt`, NoopReader
// stands in where there is no battery to consult (non-darwin hosts, or a
// caller that wants the gate off), and FakeReader is the injectable double for
// tests. NewReader picks the production strategy for the running platform.
package power

import (
	"errors"
	"runtime"
)

// NoPercent is Reading.Percent when the charge is unknown (no battery, or the
// reader could not determine it).
const NoPercent = -1

// ErrUnparseable means a power-source query succeeded but its output could not
// be understood. Callers treat every Read error as "unknown" and fail open.
var ErrUnparseable = errors.New("power: unparseable power-source output")

// Reading is one observation of the power source.
type Reading struct {
	// OnBattery is true only when the machine is drawing from its battery. It
	// is false on AC power, on a machine with no battery, and for any other
	// source (e.g. a UPS), so only a confirmed battery draw can restrict the
	// keep-awake assertion.
	OnBattery bool
	// Percent is the battery charge in [0,100], or NoPercent when unknown.
	Percent int
}

// Reader reports the current power source. Implementations MUST be safe for
// concurrent use.
type Reader interface {
	Read() (Reading, error)
}

// NewReader returns the production Reader for this platform: the pmset-backed
// DarwinReader on macOS, NoopReader everywhere else.
func NewReader() Reader {
	if runtime.GOOS == "darwin" {
		return NewDarwinReader()
	}
	return NoopReader{}
}

// NoopReader reports "on AC, charge unknown" and never errors, so a manager
// using it behaves exactly as it did before the battery gate existed.
type NoopReader struct{}

// Read implements Reader.
func (NoopReader) Read() (Reading, error) {
	return Reading{OnBattery: false, Percent: NoPercent}, nil
}
