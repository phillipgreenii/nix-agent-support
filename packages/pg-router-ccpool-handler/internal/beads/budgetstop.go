package beads

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// BudgetStopLabelPrefix prefixes the per-session label that records one
// watchdog budget hard stop on a bead (bead pg2-6akgz). The full label is
// BudgetStopLabelPrefix + <session id>.
const BudgetStopLabelPrefix = "budget-stop:"

// LabelNeedsSplitReview parks a budget-exhausted bead for the split-triage role
// (bead pg2-mab1w). Every worker/review/feedback discovery query excludes it.
const LabelNeedsSplitReview = "needs-split-review"

// RecordBudgetStop records one budget hard stop for session on bead id as the
// label `budget-stop:<session>`. The count is derived from the SET of such
// labels (see BudgetStops), not from a read-modify-write number, so re-writing
// the same session id is an idempotent no-op and two racing writers cannot lose
// an increment.
func RecordBudgetStop(ctx context.Context, r Runner, id, session string) error {
	if session == "" {
		return fmt.Errorf("record budget stop %s: empty session id", id)
	}
	return AddLabel(ctx, r, id, BudgetStopLabelPrefix+session)
}

// BudgetStops returns the number of distinct sessions recorded as budget-stopped
// on bead id. The record is cleared only when the bead is closed
// (ClearBudgetStops); a reopened bead keeps its history.
func BudgetStops(ctx context.Context, r Runner, id string) (int, error) {
	iss, err := ShowObj(ctx, r, id)
	if err != nil {
		return 0, err
	}
	return countBudgetStops(iss), nil
}

func budgetStopLabels(iss Issue) []string {
	seen := map[string]bool{}
	var out []string
	for _, l := range iss.Labels {
		if strings.HasPrefix(l, BudgetStopLabelPrefix) && !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	return out
}

func countBudgetStops(iss Issue) int { return len(budgetStopLabels(iss)) }

// BudgetStopCount is the number of distinct budget-stop labels already read
// into iss (the same count BudgetStops returns, without a second bd call).
func BudgetStopCount(iss Issue) int { return countBudgetStops(iss) }

// BudgetStopSessions lists the session ids of iss's budget-stop labels, sorted, without the label prefix.
func BudgetStopSessions(iss Issue) []string {
	var out []string
	for _, l := range budgetStopLabels(iss) {
		out = append(out, strings.TrimPrefix(l, BudgetStopLabelPrefix))
	}
	sort.Strings(out)
	return out
}

// ClearBudgetStops removes every budget-stop label from bead id iff the bead is
// closed; a non-closed bead (including a reopened one) keeps its history.
// Best-effort per label; returns the first error.
func ClearBudgetStops(ctx context.Context, r Runner, id string) error {
	iss, err := ShowObj(ctx, r, id)
	if err != nil {
		return err
	}
	if iss.Status != "closed" {
		return nil
	}
	var first error
	for _, l := range budgetStopLabels(iss) {
		if err := RemoveLabel(ctx, r, id, l); err != nil && first == nil {
			first = err
		}
	}
	return first
}
