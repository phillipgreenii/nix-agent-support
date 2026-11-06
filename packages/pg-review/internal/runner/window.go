package runner

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Window is one explicit review window. Both bounds are UTC.
type Window struct {
	From, To time.Time
}

// FromText and ToText are the stored text of the bounds (RFC 3339, UTC, Z).
func (w Window) FromText() string { return w.From.UTC().Format(time.RFC3339Nano) }

// ToText is the stored text of the window end.
func (w Window) ToText() string { return w.To.UTC().Format(time.RFC3339Nano) }

var daysRe = regexp.MustCompile(`^(\d+)d$`)

// ParseDuration accepts Go durations plus an `Nd` form meaning N elapsed
// 24-hour periods.
func ParseDuration(s string) (time.Duration, error) {
	if m := daysRe.FindStringSubmatch(s); m != nil {
		n, err := strconv.Atoi(m[1])
		if err != nil {
			return 0, fmt.Errorf("duration %q: %v", s, err)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("duration %q: use a Go duration or Nd", s)
	}
	return d, nil
}

// ResolveWindow turns the window flags into an explicit window. Exactly one of
// (from with to) or duration must be given; at is only valid with duration and
// defaults to now. Offset-bearing RFC 3339 values are accepted. Every error is
// a usage error.
func ResolveWindow(from, to, duration, at string, now time.Time) (Window, error) {
	hasRange := from != "" || to != ""
	switch {
	case hasRange && duration != "":
		return Window{}, fmt.Errorf("give either --from with --to or --duration, not both")
	case !hasRange && duration == "":
		return Window{}, fmt.Errorf("give --from with --to, or --duration")
	case hasRange && (from == "" || to == ""):
		return Window{}, fmt.Errorf("--from and --to must be given together")
	case hasRange && at != "":
		return Window{}, fmt.Errorf("--at only applies with --duration")
	}
	if hasRange {
		f, err := time.Parse(time.RFC3339, from)
		if err != nil {
			return Window{}, fmt.Errorf("--from %q is not RFC 3339", from)
		}
		t, err := time.Parse(time.RFC3339, to)
		if err != nil {
			return Window{}, fmt.Errorf("--to %q is not RFC 3339", to)
		}
		if !f.Before(t) {
			return Window{}, fmt.Errorf("--from must be before --to")
		}
		return Window{From: f.UTC(), To: t.UTC()}, nil
	}
	d, err := ParseDuration(strings.TrimSpace(duration))
	if err != nil {
		return Window{}, err
	}
	if d <= 0 {
		return Window{}, fmt.Errorf("--duration must be positive")
	}
	end := now
	if at != "" {
		end, err = time.Parse(time.RFC3339, at)
		if err != nil {
			return Window{}, fmt.Errorf("--at %q is not RFC 3339", at)
		}
	}
	end = end.UTC()
	return Window{From: end.Add(-d), To: end}, nil
}
