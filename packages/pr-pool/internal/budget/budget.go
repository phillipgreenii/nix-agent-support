// Package budget models a per-session usage budget and the escalation level a
// given usage snapshot implies. Pure: no I/O.
package budget

import (
	"fmt"
	"strings"
	"time"

	"github.com/phillipgreenii/pr-pool/internal/usage"
)

// Limit is a budget ceiling (tokens or cents). <= 0 means Unlimited.
type Limit int64

func (l Limit) Unlimited() bool { return l <= 0 }

// Thresholds holds the fraction-of-budget trigger points for each escalation level.
type Thresholds struct{ Reminder, Cancel, Hard float64 }

// Level represents the escalation state implied by the current usage percentage.
type Level int

const (
	None Level = iota
	Reminder
	Cancel
	Hard
)

// Budget describes a per-session resource ceiling across three dimensions:
// token count, estimated cost (cents), and elapsed wall-clock time.
type Budget struct {
	Tokens     Limit
	Cost       Limit
	Time       time.Duration
	Thresholds Thresholds
	Prices     usage.PriceTable
}

// Evaluate returns the max fraction-of-budget across the set dimensions and the
// escalation Level it implies. Unlimited dimensions contribute 0.
func (b Budget) Evaluate(s usage.Snapshot, elapsed time.Duration) (float64, Level) {
	pct := 0.0
	if !b.Tokens.Unlimited() {
		pct = max(pct, float64(s.Total())/float64(b.Tokens))
	}
	if !b.Cost.Unlimited() {
		pct = max(pct, float64(usage.EstimateCents(s, b.Prices))/float64(b.Cost))
	}
	if b.Time > 0 {
		pct = max(pct, float64(elapsed)/float64(b.Time))
	}
	return pct, b.level(pct)
}

func (b Budget) level(pct float64) Level {
	switch {
	case pct >= b.Thresholds.Hard:
		return Hard
	case pct >= b.Thresholds.Cancel:
		return Cancel
	case pct >= b.Thresholds.Reminder:
		return Reminder
	default:
		return None
	}
}

// PromptLine returns a one-sentence budget statement for the worker prompt, or
// "" when fully unlimited. Unlimited dimensions are omitted.
func (b Budget) PromptLine() string {
	var parts []string
	if !b.Tokens.Unlimited() {
		parts = append(parts, fmt.Sprintf("%d tokens", int64(b.Tokens)))
	}
	if !b.Cost.Unlimited() {
		parts = append(parts, fmt.Sprintf("$%.2f", float64(b.Cost)/100))
	}
	if b.Time > 0 {
		parts = append(parts, b.Time.String())
	}
	if len(parts) == 0 {
		return ""
	}
	return " You have a budget of up to " + strings.Join(parts, " / ") +
		" for this bead; if you receive a 'wrap up' message, commit your notes and finish promptly."
}
