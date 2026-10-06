package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/narrative"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/query"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/rangespec"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/report"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/telemetry"
)

func init() { registerCommand(newReportCmd) }

// reportOutcome is INTF-REQUEST's "outcome reporting why no report was
// produced": an unknown kind, a generator failure, or a narrowing the
// generator could not honor. It is not a crash: the command prints it and
// exits 1.
type reportOutcome struct{ reason string }

func (o *reportOutcome) Error() string { return "report outcome: " + o.reason }

type reportRangeJSON struct {
	Since  *string `json:"since"`
	Before string  `json:"before"`
}

type reportNarrowingJSON struct {
	Labels  []string `json:"labels"`
	Sources []string `json:"sources"`
	Types   []string `json:"types"`
}

// reportJSON is the INTF-REQUEST response shape under --output json.
type reportJSON struct {
	Range     reportRangeJSON     `json:"range"`
	Kind      string              `json:"kind"`
	Narrowing reportNarrowingJSON `json:"narrowing"`
	Content   string              `json:"content"`
}

type reportOutcomeJSON struct {
	Outcome string `json:"outcome"`
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func toNarrowingJSON(n report.Narrowing) reportNarrowingJSON {
	return reportNarrowingJSON{Labels: nonNil(n.Labels), Sources: nonNil(n.Sources), Types: nonNil(n.Types)}
}

func rfcOrEmpty(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// buildFooter gives one line per source named by the store's status or by a
// rendered entry. Counts come from the entries of THIS report, never from the
// all-time entry count.
func buildFooter(d store.StatusData, entries []store.Entry) []report.SourceFooter {
	counts := map[string]int{}
	for _, e := range entries {
		counts[e.SourceID]++
	}
	lastOK := map[string]time.Time{}
	for _, s := range d.Sources {
		if _, ok := counts[s.Source]; !ok {
			counts[s.Source] = 0
		}
		lastOK[s.Source] = s.LastSuccessAt
	}
	names := make([]string, 0, len(counts))
	for s := range counts {
		names = append(names, s)
	}
	sort.Strings(names)
	out := make([]report.SourceFooter, 0, len(names))
	for _, s := range names {
		out = append(out, report.SourceFooter{Source: s, Count: counts[s], LastSuccessAt: lastOK[s]})
	}
	return out
}

func newReportCmd() *cobra.Command {
	var (
		rangeSpec string
		kind      string
		out       string
		labels    []string
		sources   []string
		types     []string
	)
	cmd := &cobra.Command{
		Use:   "report",
		Short: "Render a report for a range, optionally narrowed",
		Long: "Renders the entries of a range as a report of the requested kind (default " +
			"baseline, a plain chronological markdown view that needs no generator), stores " +
			"it, and writes it to stdout or --out. Narrowing flags compose AND across " +
			"dimensions and OR within one. A request that cannot produce a report (an " +
			"unknown kind, a generator failure, a narrowing the generator cannot honor) " +
			"prints an outcome naming the reason and exits 1. Range forms: " + rangespec.Forms + ".",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfgPath, rawOut, storeFlag := globalFlags(cmd)
			format, err := outputFormat(rawOut)
			if err != nil {
				return err
			}
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return err
			}
			loc, err := cfg.Location()
			if err != nil {
				return err
			}
			started := time.Now()
			rng, err := rangespec.Resolve(rangeSpec, started, loc)
			if err != nil {
				return err
			}
			logger := telemetry.New("")
			outcome := func(reason string) error {
				_ = logger.LogReport(telemetry.ReportLog{
					TS: started.UTC(), Kind: kind, Status: "outcome",
					Since: rfcOrEmpty(rng.Since), Before: rfcOrEmpty(rng.Before),
					Reason: reason, DurationMS: time.Since(started).Milliseconds(),
				})
				if format == "json" {
					_ = writeJSON(cmd.OutOrStdout(), reportOutcomeJSON{Outcome: reason})
				}
				return &reportOutcome{reason: reason}
			}

			// The narrative generator is built from the loaded config, so it is
			// registered per invocation, before the kind lookup.
			report.Register(narrative.New(narrative.Options{
				Model:            cfg.Kinds.Narrative.Model,
				SystemPromptFile: cfg.Kinds.Narrative.SystemPromptFile,
			}))

			gen, ok := report.Lookup(kind)
			if !ok {
				return outcome(fmt.Sprintf("unknown kind %q: no generator is registered for it (registered: %s); use --kind %s",
					kind, strings.Join(report.Kinds(), ", "), report.BaselineKind))
			}

			st, err := store.Open(config.StorePath(cfg, storeFlag))
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }()

			entries, err := query.Run(cmd.Context(), st, store.Filter{
				Since: rng.Since, Before: rng.Before, OpenStart: rng.OpenStart,
				Types: types, Labels: labels, Sources: sources,
			})
			if err != nil {
				return err
			}
			status, err := st.Status(cmd.Context())
			if err != nil {
				return err
			}
			nar := report.Narrowing{Labels: labels, Sources: sources, Types: types}
			res, err := gen.Generate(cmd.Context(), report.Request{
				Range: rng, Narrowing: nar, Entries: entries, Footer: buildFooter(status, entries),
			})
			if err != nil {
				if errors.Is(err, report.ErrNarrowingUnhonored) {
					return outcome(fmt.Sprintf("kind %q cannot honor the requested narrowing: %v", kind, err))
				}
				return outcome(fmt.Sprintf("kind %q failed to generate: %v; use --kind %s", kind, err, report.BaselineKind))
			}

			narJSON, err := json.Marshal(toNarrowingJSON(nar))
			if err != nil {
				return err
			}
			if err := st.RecordReport(cmd.Context(), store.ReportRow{
				Since: rng.Since, Before: rng.Before, Kind: kind, NarrowingJSON: string(narJSON),
				GeneratedAt: started.UTC(), Generator: gen.Kind(), Content: res.Content,
			}); err != nil {
				return err
			}
			if err := logger.LogReport(telemetry.ReportLog{
				TS: started.UTC(), Kind: kind, Status: "rendered",
				Since: rfcOrEmpty(rng.Since), Before: rfcOrEmpty(rng.Before),
				DurationMS: time.Since(started).Milliseconds(),
			}); err != nil {
				return err
			}

			var w io.Writer = cmd.OutOrStdout()
			if out != "" {
				f, err := os.Create(out)
				if err != nil {
					return err
				}
				defer func() { _ = f.Close() }()
				w = f
			}
			if format == "json" {
				return writeJSON(w, reportJSON{
					Range:     reportRangeJSON{Since: timePtr(rng.Since), Before: rng.Before.UTC().Format(time.RFC3339)},
					Kind:      kind,
					Narrowing: toNarrowingJSON(nar),
					Content:   res.Content,
				})
			}
			_, err = io.WriteString(w, res.Content)
			return err
		},
	}
	f := cmd.Flags()
	f.StringVar(&rangeSpec, "range", "today", "range spec: "+rangespec.Forms)
	f.StringVar(&kind, "kind", report.BaselineKind, "report kind to render")
	f.StringArrayVar(&labels, "label", nil, "only entries carrying this label (repeatable; OR within)")
	f.StringArrayVar(&sources, "source", nil, "only entries from this source (repeatable; OR within)")
	f.StringArrayVar(&types, "type", nil, "only entries of this type (repeatable; OR within)")
	f.StringVar(&out, "out", "", "write the report to this file instead of stdout")
	return cmd
}
