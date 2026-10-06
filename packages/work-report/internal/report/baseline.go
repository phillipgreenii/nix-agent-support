package report

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/rangespec"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/store"
)

// BaselineKind is the kind name of the always-available baseline generator.
const BaselineKind = "baseline"

func init() { Register(baseline{}) }

// baseline is the plain chronological rendering of the queried entries
// (INV-BASELINE-1). It depends on no external program and no option.
type baseline struct{}

func (baseline) Kind() string { return BaselineKind }

const (
	dayLayout    = "2006-01-02"
	clockLayout  = "15:04"
	minuteLayout = "2006-01-02 15:04"
)

// FormatRange renders r for a human, in the zone of r.Before. The result is
// what the baseline header and its empty-range line state (INV-REPORT-RANGE-1).
func FormatRange(r rangespec.Range) string {
	loc := r.Before.Location()
	end := r.Before.In(loc).Format(minuteLayout + " MST")
	if r.OpenStart {
		return "the beginning to " + end
	}
	return r.Since.In(loc).Format(minuteLayout) + " to " + end
}

// FormatNarrowing renders n for a human; "none" when nothing narrows.
func FormatNarrowing(n Narrowing) string {
	var parts []string
	if len(n.Labels) > 0 {
		parts = append(parts, "labels="+strings.Join(n.Labels, ","))
	}
	if len(n.Sources) > 0 {
		parts = append(parts, "sources="+strings.Join(n.Sources, ","))
	}
	if len(n.Types) > 0 {
		parts = append(parts, "types="+strings.Join(n.Types, ","))
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, "; ")
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func (baseline) Generate(_ context.Context, r Request) (Result, error) {
	loc := r.Range.Before.Location()
	rng := FormatRange(r.Range)

	var b strings.Builder
	b.WriteString("# Work report\n\n")
	fmt.Fprintf(&b, "- Range: %s\n", rng)
	fmt.Fprintf(&b, "- Kind: %s\n", BaselineKind)
	fmt.Fprintf(&b, "- Narrowing: %s\n", FormatNarrowing(r.Narrowing))

	entries := append([]store.Entry(nil), r.Entries...)
	sort.SliceStable(entries, func(i, j int) bool {
		if !entries[i].OccurredAt.Equal(entries[j].OccurredAt) {
			return entries[i].OccurredAt.Before(entries[j].OccurredAt)
		}
		return entries[i].ID < entries[j].ID
	})

	if len(entries) == 0 {
		fmt.Fprintf(&b, "\nNo entries for %s.\n", rng)
	}
	day := ""
	for _, e := range entries {
		at := e.OccurredAt.In(loc)
		if d := at.Format(dayLayout); d != day {
			day = d
			fmt.Fprintf(&b, "\n## %s\n\n", day)
		}
		meta := e.SourceID
		if len(e.Labels) > 0 {
			meta += ", " + strings.Join(e.Labels, ", ")
		}
		line := fmt.Sprintf("- %s %s %s (%s)", at.Format(clockLayout), e.Type, oneLine(e.Summary), meta)
		if e.URL != "" {
			line += " " + e.URL
		}
		b.WriteString(line + "\n")
	}

	writeFooter(&b, entries, r.Footer, loc)
	return Result{Content: b.String()}, nil
}

// writeFooter lists every source named by the request footer or appearing in
// the rendered entries, with the entry count of this report (counted here from
// the entries, never taken from an all-time figure) and the source's last
// successful pull time.
func writeFooter(b *strings.Builder, entries []store.Entry, footer []SourceFooter, loc *time.Location) {
	counts := map[string]int{}
	for _, e := range entries {
		counts[e.SourceID]++
	}
	lastOK := map[string]time.Time{}
	for _, f := range footer {
		if _, ok := counts[f.Source]; !ok {
			counts[f.Source] = 0
		}
		lastOK[f.Source] = f.LastSuccessAt
	}
	names := make([]string, 0, len(counts))
	for s := range counts {
		names = append(names, s)
	}
	sort.Strings(names)

	b.WriteString("\n## Sources\n\n")
	if len(names) == 0 {
		b.WriteString("No sources recorded.\n")
		return
	}
	for _, s := range names {
		last := "never"
		if t := lastOK[s]; !t.IsZero() {
			last = t.In(loc).Format(minuteLayout + " MST")
		}
		noun := "entries"
		if counts[s] == 1 {
			noun = "entry"
		}
		fmt.Fprintf(b, "- %s: %d %s, last successful pull %s\n", s, counts[s], noun, last)
	}
}
