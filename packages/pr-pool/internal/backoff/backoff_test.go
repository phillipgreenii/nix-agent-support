package backoff

import (
	"testing"
	"time"
)

func TestDuration_growsByFactorThenCaps(t *testing.T) {
	p := Policy{Initial: time.Second, Factor: 2, Max: 10 * time.Second}
	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{0, time.Second}, // <= 1 treated as 1
		{1, time.Second}, // Initial
		{2, 2 * time.Second},
		{3, 4 * time.Second},
		{4, 8 * time.Second},
		{5, 10 * time.Second}, // would be 16s, capped at Max
		{6, 10 * time.Second}, // stays at Max
		{50, 10 * time.Second},
	}
	for _, c := range cases {
		if got := p.Duration(c.attempt); got != c.want {
			t.Errorf("Duration(%d) = %v, want %v", c.attempt, got, c.want)
		}
	}
}

func TestDuration_zeroPolicySanitizesToDefault(t *testing.T) {
	var p Policy // zero value
	d := Default()
	if got := p.Duration(1); got != d.Initial {
		t.Errorf("Duration(1) on zero Policy = %v, want Default().Initial = %v", got, d.Initial)
	}
}

func TestDuration_partiallySpecifiedPolicyFillsFromDefault(t *testing.T) {
	// Only Factor set; Initial/Max must fall back to Default()'s.
	p := Policy{Factor: 3}
	d := Default()
	if got := p.Duration(1); got != d.Initial {
		t.Errorf("Duration(1) = %v, want Default().Initial = %v", got, d.Initial)
	}
	if got := p.Duration(2); got != d.Initial*3 {
		t.Errorf("Duration(2) = %v, want Initial*Factor = %v", got, d.Initial*3)
	}
}

func TestDuration_factorLessThanOrEqualOneFallsBackToDefaultFactor(t *testing.T) {
	// A Factor <= 1 would never climb toward Max, which defeats the "growing on
	// repeated failures" shape — sanitized() rejects it in favor of Default().
	p := Policy{Initial: time.Second, Factor: 1, Max: time.Minute}
	d := Default()
	if got := p.Duration(2); got != time.Duration(float64(time.Second)*d.Factor) {
		t.Errorf("Duration(2) with Factor<=1 = %v, want growth via Default().Factor = %v", got, d.Factor)
	}
}
