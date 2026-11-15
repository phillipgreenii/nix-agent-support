// Package config reads the per-repository configuration file and checks it.
package config

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Duration is a configured time span: "none", or a positive whole number with
// a unit of s, m, h or d.
type Duration struct {
	D    time.Duration
	None bool
}

// NoDuration is the "none" value.
var NoDuration = Duration{None: true}

// ParseDuration parses "none" or <int><s|m|h|d>. Zero, negative and fractional
// values, other units and the empty string fail.
func ParseDuration(s string) (Duration, error) {
	if s == "none" {
		return NoDuration, nil
	}
	if len(s) < 2 {
		return Duration{}, fmt.Errorf("duration %q: want none or <int><s|m|h|d>", s)
	}
	var unit time.Duration
	switch s[len(s)-1] {
	case 's':
		unit = time.Second
	case 'm':
		unit = time.Minute
	case 'h':
		unit = time.Hour
	case 'd':
		unit = 24 * time.Hour
	default:
		return Duration{}, fmt.Errorf("duration %q: unit must be s, m, h or d", s)
	}
	digits := s[:len(s)-1]
	for _, c := range digits {
		if c < '0' || c > '9' {
			return Duration{}, fmt.Errorf("duration %q: want a positive whole number before the unit", s)
		}
	}
	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || n <= 0 || n > int64(time.Duration(1<<63-1)/unit) {
		return Duration{}, fmt.Errorf("duration %q: must be a positive number in range", s)
	}
	return Duration{D: time.Duration(n) * unit}, nil
}

// String prints "none", or the value in the largest unit that divides it
// exactly.
func (d Duration) String() string {
	if d.None {
		return "none"
	}
	for _, u := range []struct {
		d time.Duration
		s string
	}{{24 * time.Hour, "d"}, {time.Hour, "h"}, {time.Minute, "m"}} {
		if d.D > 0 && d.D%u.d == 0 {
			return strconv.FormatInt(int64(d.D/u.d), 10) + u.s
		}
	}
	return strconv.FormatInt(int64(d.D/time.Second), 10) + "s"
}

// UnmarshalText implements encoding.TextUnmarshaler for TOML.
func (d *Duration) UnmarshalText(b []byte) error {
	v, err := ParseDuration(strings.TrimSpace(string(b)))
	if err != nil {
		return err
	}
	*d = v
	return nil
}

// MarshalText implements encoding.TextMarshaler.
func (d Duration) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

// IsZero reports an unset value.
func (d Duration) IsZero() bool { return !d.None && d.D == 0 }
