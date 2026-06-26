// Package duration parses human duration strings with millisecond granularity
// and day units. Go's time.ParseDuration covers ns..h but not "d"; this adds d=24h
// and rejects anything below 1ms (the minimum resolvable stale-after unit).
package duration

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// dayRe matches a leading "<int>d" segment so we can translate it to hours
// before delegating the rest to time.ParseDuration. Supports e.g. "3d", "1d12h".
var dayRe = regexp.MustCompile(`^(\d+)d`)

// ParseDuration parses s as a duration. It accepts time.ParseDuration units
// (ns, us, µs, ms, s, m, h) plus a leading "<int>d" (days = 24h). It rejects the
// empty string, a bare number (no unit), and any non-positive or sub-millisecond
// total.
func ParseDuration(s string) (time.Duration, error) {
	if s == "" {
		return 0, errors.New("empty duration")
	}
	var days time.Duration
	rest := s
	if m := dayRe.FindStringSubmatch(s); m != nil {
		n, err := strconv.Atoi(m[1])
		if err != nil {
			return 0, fmt.Errorf("invalid day count in %q: %w", s, err)
		}
		days = time.Duration(n) * 24 * time.Hour
		rest = strings.TrimPrefix(s, m[0])
	}
	var sub time.Duration
	if rest != "" {
		var err error
		sub, err = time.ParseDuration(rest)
		if err != nil {
			return 0, fmt.Errorf("invalid duration %q: %w", s, err)
		}
	}
	total := days + sub
	if total < time.Millisecond {
		return 0, fmt.Errorf("duration %q is below the 1ms minimum", s)
	}
	return total, nil
}
