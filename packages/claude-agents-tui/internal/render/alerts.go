package render

import (
	"fmt"
	"strings"
	"time"

	"github.com/phillipgreenii/claude-agents-tui/internal/aggregate"
	"github.com/phillipgreenii/claude-agents-tui/internal/render/wrap"
)

// AlertsOpts carries the inputs needed to compose the alert row.
type AlertsOpts struct {
	Now             time.Time
	Width           int
	AutoResume      bool
	WindowResetsAt  time.Time
	AutoResumeDelay time.Duration
	TopupPoolUSD    float64
	TopupConsumed   float64
}

// Alerts returns "" when no alert is active, otherwise a single-line,
// pipe-joined summary in priority order:
//
//	⏸ resuming in N:NN          (when AutoResume && WindowResetsAt > Now)
//	Top-up $X / $Y remaining    (when tree.TopupShouldDisplay() && TopupPoolUSD > 0)
//
// Tier-aware compaction shortens labels at NARROW/TINY.
func Alerts(tree *aggregate.Tree, opts AlertsOpts) string {
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	tier := wrap.Tier(opts.Width)

	var segs []string

	if opts.AutoResume && !opts.WindowResetsAt.IsZero() {
		fireAt := opts.WindowResetsAt.Add(opts.AutoResumeDelay)
		remaining := fireAt.Sub(now)
		if remaining > 0 {
			mins := int(remaining.Minutes())
			secs := int(remaining.Seconds()) - mins*60
			switch tier {
			case wrap.TierWide:
				segs = append(segs, fmt.Sprintf("⏸ resuming in %d:%02d", mins, secs))
			case wrap.TierNarrow:
				segs = append(segs, fmt.Sprintf("⏸ resume %d:%02d", mins, secs))
			default:
				segs = append(segs, fmt.Sprintf("⏸ %d:%02d", mins, secs))
			}
		} else if remaining > -5*time.Second {
			segs = append(segs, "⏸ resuming…")
		}
	}

	if tree.TopupShouldDisplay() && opts.TopupPoolUSD > 0 {
		remaining := opts.TopupPoolUSD - opts.TopupConsumed
		switch tier {
		case wrap.TierWide:
			segs = append(segs, fmt.Sprintf("Top-up $%.0f / $%.0f remaining", remaining, opts.TopupPoolUSD))
		case wrap.TierNarrow:
			segs = append(segs, fmt.Sprintf("Top-up $%.0f/$%.0f", remaining, opts.TopupPoolUSD))
		default:
			segs = append(segs, fmt.Sprintf("T $%.0f/$%.0f", remaining, opts.TopupPoolUSD))
		}
	}

	return strings.Join(segs, " | ")
}
