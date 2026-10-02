package main

import (
	"fmt"
	"strconv"
	"strings"
)

// iecUnits maps the accepted size suffixes to their byte multiplier. Only IEC
// (binary) units and a bare "B" are accepted: "GB" is ambiguous (10^9 vs 2^30)
// and a threshold flag must not be, so it is rejected rather than guessed.
var iecUnits = map[string]uint64{
	"B":   1,
	"KIB": 1 << 10,
	"MIB": 1 << 20,
	"GIB": 1 << 30,
	"TIB": 1 << 40,
}

// parseSize parses "20GiB", "512MiB", "1.5TiB" or a bare byte count ("1048576")
// into bytes.
func parseSize(s string) (uint64, error) {
	in := strings.TrimSpace(s)
	if in == "" {
		return 0, fmt.Errorf("empty size")
	}
	cut := len(in)
	for cut > 0 {
		c := in[cut-1]
		if (c >= '0' && c <= '9') || c == '.' {
			break
		}
		cut--
	}
	num, unit := strings.TrimSpace(in[:cut]), strings.ToUpper(strings.TrimSpace(in[cut:]))
	mult := uint64(1)
	if unit != "" {
		m, ok := iecUnits[unit]
		if !ok {
			return 0, fmt.Errorf("size %q: unknown unit %q (want B, KiB, MiB, GiB or TiB)", s, in[cut:])
		}
		mult = m
	}
	f, err := strconv.ParseFloat(num, 64)
	if err != nil || f < 0 {
		return 0, fmt.Errorf("size %q: not a non-negative number", s)
	}
	return uint64(f * float64(mult)), nil
}

// formatSize renders bytes for a human-facing gate description ("12.3 GiB").
func formatSize(b uint64) string {
	switch {
	case b >= 1<<40:
		return fmt.Sprintf("%.1f TiB", float64(b)/(1<<40))
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(b)/(1<<20))
	default:
		return fmt.Sprintf("%d B", b)
	}
}
