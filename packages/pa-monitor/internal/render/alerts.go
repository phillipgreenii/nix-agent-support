package render

import (
	"fmt"
	"strings"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/render/wrap"
	"github.com/phillipgreenii/pa-monitor/internal/versioncmp"
)

// AlertsOpts carries the inputs needed to compose the alert row.
type AlertsOpts struct {
	Now             time.Time
	Width           int
	Theme           Theme
	AutoResume      bool
	WindowResetsAt  time.Time
	AutoResumeDelay time.Duration
	TopupPoolUSD    float64
	TopupConsumed   float64
	ClientVersion   string
	DaemonVersion   string
	// ReexecGaveUp, when true, means client self-restart exhausted its attempts
	// (or the exec failed): the version-mismatch segment becomes a persistent
	// "restart manually" give-up notice instead of the ordinary remediation.
	ReexecGaveUp bool
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

	if n := tree.AuthFailedCount(); n > 0 {
		var seg string
		switch tier {
		case wrap.TierWide:
			seg = "⊘ AUTHENTICATION FAILURE — run /login"
		case wrap.TierNarrow:
			seg = "⊘ auth — run /login"
		default:
			seg = "⊘ /login"
		}
		segs = append(segs, opts.Theme.Error.Render(seg))
	}

	if versioncmp.Mismatch(opts.ClientVersion, opts.DaemonVersion) {
		var seg string
		switch {
		case opts.ReexecGaveUp:
			// Persistent give-up: auto-restart could not converge within the
			// attempt budget (or the exec failed). Advise a manual client restart.
			switch tier {
			case wrap.TierWide:
				seg = fmt.Sprintf("⚠ auto-restart failed (daemon %s) — restart this TUI manually", opts.DaemonVersion)
			case wrap.TierNarrow:
				seg = "⚠ auto-restart failed — restart TUI"
			default:
				seg = "⚠ restart TUI"
			}
		default:
			// This feature targets the newer-daemon case, so the remediation is
			// to restart the CLIENT. WIDE shows both versions in full; NARROW/TINY
			// save space by showing only the daemon version — never by
			// splitting/shortening the id, which has no reliable delimiter.
			switch tier {
			case wrap.TierWide:
				seg = fmt.Sprintf("⚠ daemon %s ≠ this %s — restart this TUI",
					opts.DaemonVersion, opts.ClientVersion)
			case wrap.TierNarrow:
				seg = fmt.Sprintf("⚠ daemon %s — restart TUI", opts.DaemonVersion)
			default:
				seg = fmt.Sprintf("⚠ daemon %s", opts.DaemonVersion)
			}
		}
		segs = append(segs, opts.Theme.Error.Render(seg))
	}

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
