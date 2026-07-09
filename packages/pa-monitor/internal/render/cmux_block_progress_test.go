package render_test

import (
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/render"
)

func TestCmuxBlockProgress(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	fp := func(v float64) *float64 { return &v }
	stale := 10 * time.Minute

	cases := []struct {
		name       string
		fivePct    *float64
		capturedAt time.Time
		costPct    float64
		costOK     bool
		staleAfter time.Duration
		wantFrac   float64
		wantLabel  string
		wantOK     bool
	}{
		{
			name: "authoritative fresh wins over cost", fivePct: fp(32), capturedAt: now.Add(-1 * time.Minute),
			costPct: 139, costOK: true, staleAfter: stale,
			wantFrac: 0.32, wantLabel: "block 32%", wantOK: true,
		},
		{
			name: "authoritative stale shows value with hint (not cost)", fivePct: fp(32), capturedAt: now.Add(-11 * time.Minute),
			costPct: 139, costOK: true, staleAfter: stale,
			wantFrac: 0.32, wantLabel: "block 32% (stale)", wantOK: true,
		},
		{
			name: "authoritative over 100 clamps the bar", fivePct: fp(150), capturedAt: now,
			costPct: 0, costOK: false, staleAfter: stale,
			wantFrac: 1.0, wantLabel: "block 150%", wantOK: true,
		},
		{
			name: "never captured falls back to cost/cap", fivePct: nil, capturedAt: time.Time{},
			costPct: 139, costOK: true, staleAfter: stale,
			wantFrac: 1.0, wantLabel: "block 139% of cap", wantOK: true,
		},
		{
			name: "pct present but capturedAt zero is treated as not captured", fivePct: fp(32), capturedAt: time.Time{},
			costPct: 88, costOK: true, staleAfter: stale,
			wantFrac: 0.88, wantLabel: "block 88% of cap", wantOK: true,
		},
		{
			name: "no authoritative and no cost -> not ok", fivePct: nil, capturedAt: time.Time{},
			costPct: 0, costOK: false, staleAfter: stale,
			wantFrac: 0, wantLabel: "", wantOK: false,
		},
		{
			name: "staleAfter<=0 disables staleness (always fresh label)", fivePct: fp(32), capturedAt: now.Add(-1 * time.Hour),
			costPct: 139, costOK: true, staleAfter: 0,
			wantFrac: 0.32, wantLabel: "block 32%", wantOK: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			frac, label, ok := render.CmuxBlockProgress(tc.fivePct, tc.capturedAt, tc.costPct, tc.costOK, now, tc.staleAfter)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if label != tc.wantLabel {
				t.Errorf("label = %q, want %q", label, tc.wantLabel)
			}
			if frac != tc.wantFrac {
				t.Errorf("frac = %v, want %v", frac, tc.wantFrac)
			}
		})
	}
}
