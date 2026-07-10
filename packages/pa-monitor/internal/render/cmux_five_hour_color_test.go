package render_test

import (
	"math"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/render"
)

func TestCmuxFiveHourColor(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	fp := func(v float64) *float64 { return &v }
	zero := time.Time{}

	cases := []struct {
		name          string
		fivePct       *float64
		resetsAt      time.Time
		wantColor     string
		wantCountdown string
	}{
		{
			name: "nil pct is clear", fivePct: nil, resetsAt: now.Add(2 * time.Hour),
			wantColor: "", wantCountdown: "",
		},
		{
			// The ptb >= 0 guard: a missing reset must NOT yellow everything.
			name: "under-80 with missing reset is clear (guard)", fivePct: fp(50), resetsAt: zero,
			wantColor: "", wantCountdown: "",
		},
		{
			name: "under-80 with expired reset is clear", fivePct: fp(50), resetsAt: now.Add(-1 * time.Hour),
			wantColor: "", wantCountdown: "",
		},
		{
			// rem = 3h = 10800s -> ptb = (18000-10800)*100/18000 = 40; 50 > 40 -> yellow.
			name: "under-80 ahead of pace is yellow", fivePct: fp(50), resetsAt: now.Add(3 * time.Hour),
			wantColor: "#e0b000", wantCountdown: "",
		},
		{
			// rem = 1h = 3600s -> ptb = (18000-3600)*100/18000 = 80; 50 <= 80 -> clear.
			name: "under-80 on pace is clear", fivePct: fp(50), resetsAt: now.Add(1 * time.Hour),
			wantColor: "", wantCountdown: "",
		},
		{
			// rem = 2h30m = 9000s -> h=2, m=30.
			name: "at-or-over-80 is red with countdown", fivePct: fp(85), resetsAt: now.Add(2*time.Hour + 30*time.Minute),
			wantColor: "#cc3333", wantCountdown: "(2h 30m)",
		},
		{
			name: "at-or-over-80 with missing reset is red without countdown", fivePct: fp(85), resetsAt: zero,
			wantColor: "#cc3333", wantCountdown: "",
		},
		{
			// floor(79.9) = 79 < 80; missing reset keeps it clear (not red).
			name: "boundary 79.9 is not red", fivePct: fp(79.9), resetsAt: zero,
			wantColor: "", wantCountdown: "",
		},
		{
			// floor(80.0) = 80 >= 80 -> red.
			name: "boundary 80.0 is red", fivePct: fp(80.0), resetsAt: zero,
			wantColor: "#cc3333", wantCountdown: "",
		},
		{
			// NaN is treated like unknown (same guard as nil) -> clear.
			name: "NaN pct is clear", fivePct: fp(math.NaN()), resetsAt: now.Add(2 * time.Hour),
			wantColor: "", wantCountdown: "",
		},
		{
			// red branch with an under-1h countdown: rem = 40m = 2400s -> h=0, m=40.
			name: "red with sub-hour countdown", fivePct: fp(85), resetsAt: now.Add(40 * time.Minute),
			wantColor: "#cc3333", wantCountdown: "(0h 40m)",
		},
		{
			// Strict-boundary used == ptb: rem = 2h = 7200s -> ptb = (18000-7200)*100/18000 = 60.
			// floor(60) == 60 == ptb, and the guard is `>` (not `>=`), so 60 > 60 is
			// false -> clear (NOT yellow). Confirms the strict comparison.
			name: "under-80 exactly on pace boundary is clear", fivePct: fp(60), resetsAt: now.Add(2 * time.Hour),
			wantColor: "", wantCountdown: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			color, countdown := render.CmuxFiveHourColor(tc.fivePct, tc.resetsAt, now)
			if color != tc.wantColor {
				t.Errorf("color = %q, want %q", color, tc.wantColor)
			}
			if countdown != tc.wantCountdown {
				t.Errorf("countdown = %q, want %q", countdown, tc.wantCountdown)
			}
		})
	}
}
