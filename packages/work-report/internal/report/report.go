// Package report renders a range of work-report entries into the built-in
// baseline report (INV-BASELINE-1). The baseline needs no optional dependency
// and no generator process: the report command writes it for the baseline
// preset and sends its text to external generators that ask for the baseline
// input. Other report kinds are external generator programs run by
// internal/runner (INV-GEN-PLUGIN-1); this package no longer knows them.
package report

import "time"

// Narrowing is the scope restriction a report applied: AND across the three
// dimensions, OR within one. A nil or empty dimension is not narrowed.
type Narrowing struct{ Labels, Sources, Types []string }

// SourceFooter is one source's line in the report footer.
type SourceFooter struct {
	Source        string
	Count         int
	LastSuccessAt time.Time // zero = no successful pull yet
	// LastOutcome is the source's last pull status as the store records it
	// (succeeded, degraded or disabled); empty when it never pulled.
	LastOutcome string
}
