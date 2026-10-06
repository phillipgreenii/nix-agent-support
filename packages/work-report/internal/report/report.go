// Package report renders a range of work-report entries into a report. It
// owns the generator seam (INTF-GENERATE): a Generator turns one Request, the
// already-queried and already-narrowed entries, into report content. The
// baseline kind is always registered (INV-BASELINE-1) and needs no optional
// dependency; adding another kind is registering another Generator behind the
// same request shape (INV-GEN-PLUGIN-1).
package report

import (
	"context"
	"errors"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/rangespec"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/store"
)

// Narrowing is the scope restriction a request applied: AND across the three
// dimensions, OR within one. A nil or empty dimension is not narrowed.
type Narrowing struct{ Labels, Sources, Types []string }

// SourceFooter is one source's line in the report footer.
type SourceFooter struct {
	Source        string
	Count         int
	LastSuccessAt time.Time // zero = no successful pull yet
}

// Request is everything a generator receives. Entries are already queried and
// narrowed, so a generator never re-reads the store.
//
// The zone a generator renders dates and times in is the zone of
// Range.Before (rangespec.Resolve always yields bounds in the configured
// zone).
type Request struct {
	Range     rangespec.Range
	Narrowing Narrowing
	Entries   []store.Entry
	Footer    []SourceFooter
}

// Result is a generator's rendered report.
type Result struct{ Content string }

// Generator renders one report kind.
type Generator interface {
	Kind() string
	Generate(ctx context.Context, r Request) (Result, error)
}

// ErrNarrowingUnhonored is returned (wrapped, naming the dimension) by a
// Generator that cannot honor a narrowing it was given, so the caller can
// report that distinctly instead of silently ignoring it (INV-CUSTOM-1).
var ErrNarrowingUnhonored = errors.New("generator cannot honor narrowing")
