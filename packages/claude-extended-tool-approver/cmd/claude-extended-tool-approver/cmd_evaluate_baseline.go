package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"
)

// --- --baseline: fold a decision-delta report into `evaluate` ---
//
// The baseline is simply an EARLIER RUN OF THE SAME REPORT (pg2-f1vss) — there
// is no second binary to build and no git ref to check out. The intended
// workflow is the SAME `evaluate --baseline <file>` invocation run twice:
//
//	ceta evaluate --format json --baseline base.json   # BEFORE the change: base.json
//	                                                    #   does not exist yet, so this
//	                                                    #   CAPTURES the current report.
//	# ... edit rules, rebuild, run unit tests (not this command's business) ...
//	ceta evaluate --format json --baseline base.json   # AFTER: base.json now exists, so
//	                                                    #   this run DIFFS against it and
//	                                                    #   reports the moved rows.
//
// A captured baseline file is never rewritten by a later compare run — the
// "before" snapshot stays fixed until the operator deletes it and captures a
// new one. This is deliberate: a baseline that silently moved out from under a
// comparison would make "no rows moved" unfalsifiable.

// evaluateFilters is the filter set a report was captured under. It backs the
// refuse-rather-than-silently-diff check: comparing across two different
// filter sets (e.g. a `--misses-only` baseline against a full current run)
// produces a delta that LOOKS like "lots of new rows, nothing moved" but is
// actually just the filters disagreeing — the worst possible output from a
// regression gate, because it reads as good news. All fields are comparable
// so two evaluateFilters values compare with plain `==`.
type evaluateFilters struct {
	Days           int    `json:"days"`
	Since          string `json:"since"`
	ApprovalSource string `json:"approval_source"`
	MissesOnly     bool   `json:"misses_only"`
	Settings       string `json:"settings"`
}

// evaluateReport is the envelope `evaluate` reads and writes for baseline
// work. The bare array stays the DEFAULT `--format json` shape (unchanged, see
// cmd_evaluate_test.go's pin) — this envelope only appears when `--baseline`
// is given, on both the capture and the compare side, which is what lets the
// compare side recover the capture side's filter set for the mismatch check.
type evaluateReport struct {
	CapturedAt string          `json:"captured_at"`
	Filters    evaluateFilters `json:"filters"`
	Totals     map[string]int  `json:"totals"`
	Results    []evalResult    `json:"results"`
	Delta      *baselineDelta  `json:"delta,omitempty"`
}

// baselineRowRef identifies a row present on only one side of a comparison.
type baselineRowRef struct {
	ID           int    `json:"id"`
	ToolName     string `json:"tool_name"`
	ReplayResult string `json:"replay_result"`
}

// movedRow is a row whose replay_result differs between the baseline and the
// current run. Module/Reason are read off the CURRENT run's attribution
// (replay_module/replay_reason already carry per-site attribution for the
// verdict that produced them — pg2-f1vss's design explicitly avoids a second
// pass through the binary for this).
type movedRow struct {
	ID          int    `json:"id"`
	ToolName    string `json:"tool_name"`
	FromVerdict string `json:"from_verdict"`
	ToVerdict   string `json:"to_verdict"`
	// Direction is "more-restrictive" (allow -> ask/deny), "less-restrictive"
	// (ask/deny -> allow), or "lateral" for any other differing pair (e.g. a
	// move into/out of abstain, whose effective restrictiveness depends on
	// downstream settings and is not classified here).
	Direction string `json:"direction"`
	Module    string `json:"module"`
	Reason    string `json:"reason"`
}

// baselineDelta is the join of a baseline report and a current report on row
// id. Rows present on only one side are reported explicitly (Added/Removed),
// never silently dropped.
type baselineDelta struct {
	Moved   []movedRow       `json:"moved"`
	Added   []baselineRowRef `json:"added"`   // present now, absent from baseline
	Removed []baselineRowRef `json:"removed"` // present in baseline, absent now
}

// classifyDirection names the restrictiveness direction of a verdict
// transition. Only the two pairs pg2-f1vss names explicitly get a directional
// label; anything else (an abstain/unknown on either side, or a move between
// ask and deny) is "lateral" rather than guessed.
func classifyDirection(from, to string) string {
	if from == to {
		return ""
	}
	if from == "allow" && (to == "ask" || to == "deny") {
		return "more-restrictive"
	}
	if (from == "ask" || from == "deny") && to == "allow" {
		return "less-restrictive"
	}
	return "lateral"
}

// computeBaselineDelta joins baseline and current results on ID and reports
// every row whose ReplayResult moved, plus rows present on only one side.
// Deterministic: all three lists are sorted by ID.
func computeBaselineDelta(baseline, current []evalResult) *baselineDelta {
	baseByID := make(map[int]evalResult, len(baseline))
	for _, r := range baseline {
		baseByID[r.ID] = r
	}
	curByID := make(map[int]evalResult, len(current))
	for _, r := range current {
		curByID[r.ID] = r
	}

	delta := &baselineDelta{}
	for id, cur := range curByID {
		base, ok := baseByID[id]
		if !ok {
			delta.Added = append(delta.Added, baselineRowRef{ID: id, ToolName: cur.ToolName, ReplayResult: cur.ReplayResult})
			continue
		}
		if base.ReplayResult != cur.ReplayResult {
			delta.Moved = append(delta.Moved, movedRow{
				ID:          id,
				ToolName:    cur.ToolName,
				FromVerdict: base.ReplayResult,
				ToVerdict:   cur.ReplayResult,
				Direction:   classifyDirection(base.ReplayResult, cur.ReplayResult),
				Module:      cur.ReplayModule,
				Reason:      cur.ReplayReason,
			})
		}
	}
	for id, base := range baseByID {
		if _, ok := curByID[id]; !ok {
			delta.Removed = append(delta.Removed, baselineRowRef{ID: id, ToolName: base.ToolName, ReplayResult: base.ReplayResult})
		}
	}

	sort.Slice(delta.Moved, func(i, j int) bool { return delta.Moved[i].ID < delta.Moved[j].ID })
	sort.Slice(delta.Added, func(i, j int) bool { return delta.Added[i].ID < delta.Added[j].ID })
	sort.Slice(delta.Removed, func(i, j int) bool { return delta.Removed[i].ID < delta.Removed[j].ID })
	return delta
}

// loadEvaluateReport reads a baseline file written by a prior `evaluate
// --baseline` capture. It refuses a bare JSON array (the DEFAULT `--format
// json` shape, produced by a run WITHOUT --baseline) with a message pointing
// at how to capture a proper baseline, rather than silently treating it as an
// empty/unknown envelope.
func loadEvaluateReport(path string) (*evaluateReport, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		return nil, fmt.Errorf(
			"%s is a bare evaluate array (produced without --baseline), not a baseline capture; "+
				"delete it and re-run with --baseline pointed at a new path to capture one", path,
		)
	}
	var report evaluateReport
	if err := json.Unmarshal(trimmed, &report); err != nil {
		return nil, fmt.Errorf("parsing %s as a baseline capture: %w", path, err)
	}
	return &report, nil
}

func writeEvaluateReport(path string, report evaluateReport) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(report)
}

// mismatchedFilters names every field on which two evaluateFilters disagree,
// for a refusal message an operator can act on immediately.
func mismatchedFilters(loaded, current evaluateFilters) []string {
	var mismatches []string
	if loaded.Days != current.Days {
		mismatches = append(mismatches, fmt.Sprintf("--days: baseline=%d current=%d", loaded.Days, current.Days))
	}
	if loaded.Since != current.Since {
		mismatches = append(mismatches, fmt.Sprintf("--since: baseline=%q current=%q", loaded.Since, current.Since))
	}
	if loaded.ApprovalSource != current.ApprovalSource {
		mismatches = append(mismatches, fmt.Sprintf("--approval-source: baseline=%q current=%q", loaded.ApprovalSource, current.ApprovalSource))
	}
	if loaded.MissesOnly != current.MissesOnly {
		mismatches = append(mismatches, fmt.Sprintf("--misses-only: baseline=%v current=%v", loaded.MissesOnly, current.MissesOnly))
	}
	if loaded.Settings != current.Settings {
		mismatches = append(mismatches, fmt.Sprintf("--settings: baseline=%q current=%q", loaded.Settings, current.Settings))
	}
	return mismatches
}

// runEvaluateBaseline handles the --baseline branch of `evaluate`: capture
// (baselinePath does not exist yet) or compare (it does). It owns all output
// and process-exit decisions for that branch so runEvaluate's existing
// no-baseline path stays untouched.
//
// Exit codes follow this repo's convention that 1 is reserved for a generic/
// unexpected error: a bad baseline file or a filter mismatch is a generic
// usage error (1), while "the replay found a less-restrictive move" is the
// one branchable, automation-relevant signal and gets its own code (2) so a
// CI-style caller can distinguish "broken invocation" from "found the thing
// the gate exists to catch."
func runEvaluateBaseline(baselinePath, format string, filters evaluateFilters, totals map[string]int, results []evalResult) {
	_, statErr := os.Stat(baselinePath)
	if os.IsNotExist(statErr) {
		report := evaluateReport{
			CapturedAt: time.Now().UTC().Format(time.RFC3339),
			Filters:    filters,
			Totals:     totals,
			Results:    results,
		}
		if err := writeEvaluateReport(baselinePath, report); err != nil {
			fmt.Fprintf(os.Stderr, "error writing baseline: %v\n", err)
			os.Exit(1)
		}
		writeEvaluateOutput(os.Stdout, format, report)
		fmt.Fprintf(os.Stderr, "baseline captured to %s (nothing to compare against yet)\n", baselinePath)
		return
	}
	if statErr != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", statErr)
		os.Exit(1)
	}

	baseline, err := loadEvaluateReport(baselinePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if mismatches := mismatchedFilters(baseline.Filters, filters); len(mismatches) > 0 {
		fmt.Fprintf(os.Stderr, "error: baseline %s was captured under different filters — refusing to diff incomparable row sets:\n", baselinePath)
		for _, m := range mismatches {
			fmt.Fprintf(os.Stderr, "  %s\n", m)
		}
		fmt.Fprintf(os.Stderr, "capture a new baseline (delete %s and re-run) with matching filters, or match the current invocation to it.\n", baselinePath)
		os.Exit(1)
	}

	delta := computeBaselineDelta(baseline.Results, results)
	report := evaluateReport{
		CapturedAt: time.Now().UTC().Format(time.RFC3339),
		Filters:    filters,
		Totals:     totals,
		Results:    results,
		Delta:      delta,
	}
	writeEvaluateOutput(os.Stdout, format, report)

	for _, m := range delta.Moved {
		if m.Direction == "less-restrictive" {
			os.Exit(2)
		}
	}
}

// writeEvaluateOutput renders the baseline envelope in the requested format.
func writeEvaluateOutput(w io.Writer, format string, report evaluateReport) {
	if format == "json" {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(report)
		return
	}
	renderEvaluateReportSummary(w, report)
}

func renderEvaluateReportSummary(w io.Writer, report evaluateReport) {
	fmt.Fprintf(w, "Captured at:         %s\n", report.CapturedAt)
	for _, k := range []string{"correct", "miss-caught-by-settings", "miss-uncaught", "needs-review", "unresolved", "stale-cwd"} {
		fmt.Fprintf(w, "%-20s %5d\n", capLabel(k)+":", report.Totals[k])
	}
	if report.Delta == nil {
		return
	}
	moreRestrictive, lessRestrictive, lateral := 0, 0, 0
	for _, m := range report.Delta.Moved {
		switch m.Direction {
		case "more-restrictive":
			moreRestrictive++
		case "less-restrictive":
			lessRestrictive++
		default:
			lateral++
		}
	}
	fmt.Fprintf(w, "\n=== Baseline delta (%d moved, %d added, %d removed) ===\n",
		len(report.Delta.Moved), len(report.Delta.Added), len(report.Delta.Removed))
	fmt.Fprintf(w, "Direction: more-restrictive=%d less-restrictive=%d lateral=%d\n",
		moreRestrictive, lessRestrictive, lateral)
	if len(report.Delta.Moved) > 0 {
		fmt.Fprintln(w, "\nMoved:")
		for _, m := range report.Delta.Moved {
			fmt.Fprintf(w, "  id=%d %-30s %s -> %s (%s) [%s]\n", m.ID, m.ToolName, m.FromVerdict, m.ToVerdict, m.Direction, m.Module)
		}
	}
	if len(report.Delta.Added) > 0 {
		fmt.Fprintln(w, "\nAdded (present now, absent from baseline):")
		for _, r := range report.Delta.Added {
			fmt.Fprintf(w, "  id=%d %-30s %s\n", r.ID, r.ToolName, r.ReplayResult)
		}
	}
	if len(report.Delta.Removed) > 0 {
		fmt.Fprintln(w, "\nRemoved (present in baseline, absent now):")
		for _, r := range report.Delta.Removed {
			fmt.Fprintf(w, "  id=%d %-30s %s\n", r.ID, r.ToolName, r.ReplayResult)
		}
	}
}

func capLabel(s string) string {
	if len(s) == 0 {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
