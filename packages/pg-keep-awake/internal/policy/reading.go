package policy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Origin: ParsePmsetBatt and the pmset reader are ported from pa-monitor's
// internal/core/power (darwin.go, power.go); the Reading shape is unchanged.

// NoPercent is Reading.Percent when the charge is unknown.
const NoPercent = -1

// ErrUnparseable means a power-source query succeeded but its output could not
// be understood. Callers treat every Read error as a failed read.
var ErrUnparseable = errors.New("policy: unparseable power-source output")

// Reading is one observation of the power source.
type Reading struct {
	// OnBattery is true only when the machine is drawing from its battery. It
	// is false on AC power, on a machine with no battery, and for any other
	// source (for example a UPS), so only a confirmed battery draw can restrict
	// the keep-awake assertion.
	OnBattery bool
	// Percent is the battery charge in [0,100], or NoPercent when unknown.
	Percent int
}

// Reader reports the current power source. A non-nil error is a failed read.
type Reader interface {
	Read() (Reading, error)
}

// PmsetPath is the absolute path of the pmset binary. The reader never resolves
// pmset through PATH.
const PmsetPath = "/usr/bin/pmset"

// DefaultPmsetTimeout bounds one `pmset -g batt` run, so a wedged powerd (or a
// starved host) cannot stall a keeper tick.
const DefaultPmsetTimeout = 3 * time.Second

// BatterySourceEnv is the hidden environment variable that replaces the real
// reader with a FileReader. Its value MUST be file:<path>. The keeper's anchor
// child inherits it.
const BatterySourceEnv = "PG_KEEP_AWAKE_BATTERY_SOURCE"

const fileSourcePrefix = "file:"

var (
	sourceLine = regexp.MustCompile(`Now drawing from '([^']*)'`)
	percentRe  = regexp.MustCompile(`(\d+)%`)
)

// PmsetReader reads `pmset -g batt` (read-only) by absolute path with a
// timeout. A non-zero exit, a timeout and unparsable output are failed reads.
type PmsetReader struct {
	// Path is the pmset binary; empty means PmsetPath. Tests point it at a
	// fixture script.
	Path string
	// Timeout bounds one run; zero or less means DefaultPmsetTimeout.
	Timeout time.Duration
}

// Read implements Reader.
func (p PmsetReader) Read() (Reading, error) {
	path := p.Path
	if path == "" {
		path = PmsetPath
	}
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = DefaultPmsetTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "-g", "batt")
	// Do not wait on a grandchild that inherited the pipe after the kill.
	cmd.WaitDelay = 500 * time.Millisecond
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return Reading{Percent: NoPercent}, fmt.Errorf("%s -g batt: timed out after %s: %w", path, timeout, ctx.Err())
		}
		return Reading{Percent: NoPercent}, fmt.Errorf("%s -g batt: %w", path, err)
	}
	return ParsePmsetBatt(string(out))
}

// ParsePmsetBatt parses `pmset -g batt` output, for example:
//
//	Now drawing from 'Battery Power'
//	 -InternalBattery-0 (id=27459683)	92%; discharging; 4:23 remaining present: true
//
// Only the source line decides OnBattery ('Battery Power' is true; 'AC Power',
// a UPS, or anything else is false). A battery-powered reading with no
// parseable percentage is an error: the gate needs the charge to act, and a
// guess could release a hold wrongly. A desktop with no battery line reports
// on AC with NoPercent.
func ParsePmsetBatt(out string) (Reading, error) {
	m := sourceLine.FindStringSubmatch(out)
	if m == nil {
		return Reading{Percent: NoPercent}, ErrUnparseable
	}
	r := Reading{OnBattery: m[1] == "Battery Power", Percent: NoPercent}
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, "InternalBattery") {
			continue
		}
		if pm := percentRe.FindStringSubmatch(line); pm != nil {
			if n, err := strconv.Atoi(pm[1]); err == nil && n >= 0 && n <= 100 {
				r.Percent = n
			}
		}
		break
	}
	if r.OnBattery && r.Percent == NoPercent {
		return Reading{Percent: NoPercent}, ErrUnparseable
	}
	return r, nil
}

// FileReader is the test battery source: a plain text file whose first line is
// "<percent> <ac|battery>" (for example "64 ac" or "19 battery"). A missing or
// unreadable file or an unparsable line is a failed read, which is how tests
// drive the blind rule.
type FileReader struct {
	Path string
}

// Read implements Reader.
func (f FileReader) Read() (Reading, error) {
	data, err := os.ReadFile(f.Path)
	if err != nil {
		return Reading{Percent: NoPercent}, fmt.Errorf("battery source %s: %w", f.Path, err)
	}
	line, _, _ := strings.Cut(string(data), "\n")
	fields := strings.Fields(line)
	if len(fields) != 2 {
		return Reading{Percent: NoPercent}, fmt.Errorf("battery source %s: %w", f.Path, ErrUnparseable)
	}
	pct, err := strconv.Atoi(fields[0])
	if err != nil || pct < 0 || pct > 100 {
		return Reading{Percent: NoPercent}, fmt.Errorf("battery source %s: %w", f.Path, ErrUnparseable)
	}
	switch fields[1] {
	case "ac":
		return Reading{OnBattery: false, Percent: pct}, nil
	case "battery":
		return Reading{OnBattery: true, Percent: pct}, nil
	default:
		return Reading{Percent: NoPercent}, fmt.Errorf("battery source %s: %w", f.Path, ErrUnparseable)
	}
}

// failingReader is the reader for a malformed BatterySourceEnv value: every
// read fails.
type failingReader struct{ err error }

func (f failingReader) Read() (Reading, error) { return Reading{Percent: NoPercent}, f.err }

// NewReader returns the Reader for the process: when getenv(BatterySourceEnv)
// is non-empty, the file source it names (a value without the file: prefix
// yields a reader whose every read fails); otherwise the real PmsetReader.
func NewReader(getenv func(string) string) Reader {
	if getenv == nil {
		getenv = os.Getenv
	}
	v := getenv(BatterySourceEnv)
	if v == "" {
		return PmsetReader{}
	}
	path, ok := strings.CutPrefix(v, fileSourcePrefix)
	if !ok || path == "" {
		return failingReader{err: fmt.Errorf("%s: value must be file:<path>: %w", BatterySourceEnv, ErrUnparseable)}
	}
	return FileReader{Path: path}
}

// FakeReader is a scriptable Reader for tests: Set / SetErr between ticks. It
// is not safe for concurrent use.
type FakeReader struct {
	reading Reading
	err     error
	reads   int
}

// NewFakeReader returns a FakeReader reporting r.
func NewFakeReader(r Reading) *FakeReader { return &FakeReader{reading: r} }

// Set replaces the reading and clears any error.
func (f *FakeReader) Set(r Reading) { f.reading, f.err = r, nil }

// SetErr makes every following Read fail with err (nil clears it).
func (f *FakeReader) SetErr(err error) { f.err = err }

// Reads returns how many times Read was called.
func (f *FakeReader) Reads() int { return f.reads }

// Read implements Reader.
func (f *FakeReader) Read() (Reading, error) {
	f.reads++
	if f.err != nil {
		return Reading{Percent: NoPercent}, f.err
	}
	return f.reading, nil
}
