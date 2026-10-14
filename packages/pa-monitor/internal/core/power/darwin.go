package power

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultDarwinTTL is how long DarwinReader reuses one pmset reading. The
// daemon ticks about once a second; the charge moves about 1 point per minute
// (pg2-bqcqi), so refreshing every 15 seconds costs a seventeenth of the
// spawns without hiding a threshold crossing for longer than a tick or two of
// the keep-awake grace.
const DefaultDarwinTTL = 15 * time.Second

// pmsetTimeout bounds one `pmset -g batt` run so a wedged powerd cannot stall
// the daemon's tick.
const pmsetTimeout = 3 * time.Second

var (
	sourceLine = regexp.MustCompile(`Now drawing from '([^']*)'`)
	percentRe  = regexp.MustCompile(`(\d+)%`)
)

// DarwinReader reads `pmset -g batt` (read-only). Run is the seam tests use to
// feed canned output; the zero Run is the real command.
type DarwinReader struct {
	// Run returns the stdout of `pmset -g batt`. Nil means the real command.
	Run func() ([]byte, error)
	// Now is the clock for the TTL cache. Nil means time.Now.
	Now func() time.Time
	// TTL is how long a reading (or error) is reused. Zero disables caching.
	TTL time.Duration

	mu      sync.Mutex
	at      time.Time
	have    bool
	reading Reading
	err     error
}

// NewDarwinReader returns a DarwinReader running the real pmset with the
// default cache TTL.
func NewDarwinReader() *DarwinReader {
	return &DarwinReader{TTL: DefaultDarwinTTL}
}

// Read implements Reader.
func (d *DarwinReader) Read() (Reading, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := time.Now
	if d.Now != nil {
		now = d.Now
	}
	if d.have && d.TTL > 0 && now().Sub(d.at) < d.TTL {
		return d.reading, d.err
	}
	run := d.Run
	if run == nil {
		run = runPmset
	}
	out, err := run()
	if err != nil {
		d.reading, d.err = Reading{Percent: NoPercent}, fmt.Errorf("pmset -g batt: %w", err)
	} else {
		d.reading, d.err = ParsePmsetBatt(string(out))
	}
	d.at, d.have = now(), true
	return d.reading, d.err
}

func runPmset() ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), pmsetTimeout)
	defer cancel()
	return exec.CommandContext(ctx, "pmset", "-g", "batt").Output()
}

// ParsePmsetBatt parses `pmset -g batt` output, for example:
//
//	Now drawing from 'Battery Power'
//	 -InternalBattery-0 (id=27459683)	92%; discharging; 4:23 remaining present: true
//
// Only the source line decides OnBattery ('Battery Power' => true; 'AC Power',
// a UPS, or anything else => false). A battery-powered reading with no
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
