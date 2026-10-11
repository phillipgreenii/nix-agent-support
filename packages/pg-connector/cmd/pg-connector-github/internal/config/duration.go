package config

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Duration is a time.Duration that the config file writes as a string:
// anything time.ParseDuration accepts ("60s", "10m", "24h") plus a leading
// whole-day count ("7d", "14d", "1d12h"). A bare JSON number is rejected so
// the unit is never implicit. It marshals back to its canonical String() form,
// which makes "1m" and "60s" hash alike.
type Duration time.Duration

// Std returns d as a time.Duration.
func (d Duration) Std() time.Duration { return time.Duration(d) }

// MarshalJSON implements json.Marshaler.
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// UnmarshalJSON implements json.Unmarshaler.
func (d *Duration) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return fmt.Errorf("duration must be a string such as \"60s\", \"10m\" or \"7d\", got %s", strings.TrimSpace(string(data)))
	}
	v, err := parseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

// parseDuration parses s as a Go duration with an optional leading "<n>d".
func parseDuration(s string) (time.Duration, error) {
	orig := s
	var days time.Duration
	if i := strings.IndexByte(s, 'd'); i > 0 {
		n, err := strconv.ParseUint(s[:i], 10, 32)
		if err == nil {
			days = time.Duration(n) * 24 * time.Hour
			s = s[i+1:]
			if s == "" {
				return days, nil
			}
		}
	}
	rest, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q: want a string such as \"60s\", \"10m\" or \"7d\"", orig)
	}
	return days + rest, nil
}

// ByteSize is a size in bytes that the config file writes as a JSON integer
// (bytes) or as a string with an optional binary suffix: "4096", "4KiB",
// "1MiB". It marshals as an integer.
type ByteSize int64

// MarshalJSON implements json.Marshaler.
func (b ByteSize) MarshalJSON() ([]byte, error) {
	return json.Marshal(int64(b))
}

// UnmarshalJSON implements json.Unmarshaler.
func (b *ByteSize) UnmarshalJSON(data []byte) error {
	var n int64
	if err := json.Unmarshal(data, &n); err == nil {
		*b = ByteSize(n)
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return fmt.Errorf("size must be an integer (bytes) or a string such as \"4KiB\", got %s", strings.TrimSpace(string(data)))
	}
	mult := int64(1)
	num := s
	for _, u := range []struct {
		suffix string
		mult   int64
	}{{"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10}, {"B", 1}} {
		if strings.HasSuffix(s, u.suffix) {
			mult = u.mult
			num = strings.TrimSuffix(s, u.suffix)
			break
		}
	}
	v, err := strconv.ParseInt(strings.TrimSpace(num), 10, 64)
	if err != nil {
		return fmt.Errorf("invalid size %q: want an integer (bytes) or a string such as \"4KiB\"", s)
	}
	*b = ByteSize(v * mult)
	return nil
}
