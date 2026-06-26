package duration

import (
	"testing"
	"time"
)

func TestParseDuration_accepts(t *testing.T) {
	cases := map[string]time.Duration{
		"1ms":   time.Millisecond,
		"100ms": 100 * time.Millisecond,
		"30s":   30 * time.Second,
		"1m":    time.Minute,
		"2h":    2 * time.Hour,
		"3d":    3 * 24 * time.Hour,
		"1d12h": 24*time.Hour + 12*time.Hour,
	}
	for in, want := range cases {
		got, err := ParseDuration(in)
		if err != nil {
			t.Errorf("ParseDuration(%q) unexpected error: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ParseDuration(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestParseDuration_rejects(t *testing.T) {
	for _, in := range []string{"", "0", "0ms", "-1s", "500us", "5", "abc", "1d!"} {
		if _, err := ParseDuration(in); err == nil {
			t.Errorf("ParseDuration(%q) = nil error, want error", in)
		}
	}
}
