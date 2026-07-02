package render

import (
	"testing"
	"time"
)

func pf(v float64) *float64 { return &v }

// TestRateLimitUsageLabel covers the authoritative status-line rate_limits display
// (ADR 0021 §1/§5): a fresh value renders as "NN%", a value older than staleAfter
// renders as "stale (age)", and an unknown value (nil pct / zero captured) renders
// as the empty string (the caller falls back / hides).
func TestRateLimitUsageLabel(t *testing.T) {
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	staleAfter := 10 * time.Minute

	cases := []struct {
		name       string
		pct        *float64
		capturedAt time.Time
		want       string
	}{
		{"fresh integer", pf(34), now.Add(-1 * time.Minute), "34%"},
		{"fresh zero is a real reading", pf(0), now.Add(-1 * time.Minute), "0%"},
		{"exactly at boundary is still fresh", pf(50), now.Add(-10 * time.Minute), "50%"},
		{"just past boundary is stale", pf(50), now.Add(-11 * time.Minute), "stale (11m)"},
		{"stale hours", pf(50), now.Add(-2 * time.Hour), "stale (2h)"},
		{"unknown pct -> empty", nil, now.Add(-1 * time.Minute), ""},
		{"unknown captured -> empty", pf(34), time.Time{}, ""},
	}
	for _, c := range cases {
		got := RateLimitUsageLabel(c.pct, c.capturedAt, now, staleAfter)
		if got != c.want {
			t.Errorf("%s: RateLimitUsageLabel(%v, %v) = %q, want %q", c.name, c.pct, c.capturedAt, got, c.want)
		}
	}
}

// TestRateLimitUsageLabel_NeverRendersEpochForZeroCaptured is the 1970 guard: a
// zero captured time must render as unknown (empty), never as a huge stale age.
func TestRateLimitUsageLabel_NeverRendersEpochForZeroCaptured(t *testing.T) {
	now := time.Now()
	if got := RateLimitUsageLabel(pf(34), time.Time{}, now, time.Minute); got != "" {
		t.Errorf("zero captured rendered %q, want empty (never a 1970-derived stale age)", got)
	}
}
