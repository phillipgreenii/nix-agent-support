package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/asklog"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/settlegrade"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/trackeradapter"
	"github.com/spf13/cobra"
)

type reportEntry struct {
	Key   string `json:"key"`
	Count int    `json:"count"`
}

func newReportCmd() *cobra.Command {
	var groupBy, format, since string
	var src sourceFlags
	var missesOnly bool
	var days int
	cmd := &cobra.Command{
		Use:   "report",
		Short: "Summarize logged decisions grouped by tool, hook decision, outcome, or command",
		Long: `Summarize logged decisions from the asklog database, grouped by
tool_name, hook_decision, outcome, sandbox_enabled, or command.

Grouping by "command" keys on the full normalized command class (see
CommandClass): compound commands are bucketed by their real constituent
commands rather than a newline-truncated summary, so "cd foo && work" and
"cd foo\nwork" fall in the same bucket. Optionally filter to only show miss
categories (where hook disagreed with the recorded outcome) and restrict the
time window.

--misses-only skips rows whose outcome records no decision at all ("pending"
and "unresolved"): with nothing having decided the call, the hook cannot have
disagreed with anything.

Under --source tracker, --group-by also takes settled_by and settlement (the
tracker's "who settled it" columns) and grading: the settlement grading table of
evaluate, one entry per cell and group, keyed "<cell> | <group>", cells in
priority order and groups ranked by count within a cell (ties by name), with
Bash grouped by command class. Calls CETA settled itself and calls that fit no
cell are left out of grading.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := src.validate(); err != nil {
				return err
			}
			switch groupBy {
			case "settled_by", "settlement", "grading":
				if !src.isTracker() {
					return fmt.Errorf("--group-by %s needs --source tracker (the ask log does not record who settled a call)", groupBy)
				}
			}
			runReport(groupBy, missesOnly, format, days, since, src)
			return nil
		},
	}
	cmd.Flags().StringVar(&groupBy, "group-by", "tool_name", "Group results by: tool_name|hook_decision|outcome|sandbox_enabled|command (tracker source also: settled_by|settlement|grading)")
	cmd.Flags().BoolVar(&missesOnly, "misses-only", false, "Only show miss categories")
	cmd.Flags().StringVar(&format, "format", "table", "Output format: json|table")
	cmd.Flags().IntVar(&days, "days", 0, "Only report rows from the last N days")
	cmd.Flags().StringVar(&since, "since", "", "Only report rows after this date (ISO8601)")
	addSourceFlags(cmd, &src)
	return cmd
}

func runReport(groupByVal string, missesOnlyVal bool, formatVal string, daysVal int, sinceVal string, src sourceFlags) {
	groupBy := &groupByVal
	missesOnly := &missesOnlyVal
	format := &formatVal
	days := &daysVal
	since := &sinceVal

	sinceDate := *since
	if *days > 0 && sinceDate == "" {
		sinceDate = time.Now().AddDate(0, 0, -*days).UTC().Format(time.RFC3339)
	}

	// report only ever reads rows to summarize them — it never writes to the ask
	// log, so it opens read-only (pg2-cbihz).
	rows, trackerRows := loadSourceRows(src, sinceDate)

	if *groupBy == "grading" {
		printGrading(rows, trackerRows, *missesOnly, *format)
		return
	}

	counts := map[string]int{}
	for _, row := range rows {
		if *missesOnly && !isReportMiss(row) {
			continue
		}

		var key string
		switch *groupBy {
		case "hook_decision":
			if row.HookDecision != nil {
				key = *row.HookDecision
			} else {
				key = "NULL"
			}
		case "outcome":
			key = row.Outcome
		case "sandbox_enabled":
			key = sandboxEnabledKey(row.SandboxEnabled)
		case "settled_by":
			key = emptyAsNull(trackerRows[row.ID].SettledBy)
		case "settlement":
			key = emptyAsNull(trackerRows[row.ID].Settlement)
		case "command":
			key = asklog.CommandClass(row.ToolName, json.RawMessage(row.ToolInputJSON), row.CWD)
		default:
			key = row.ToolName
		}
		counts[key]++
	}

	entries := make([]reportEntry, 0, len(counts))
	for k, v := range counts {
		entries = append(entries, reportEntry{Key: k, Count: v})
	}
	sort.Slice(entries, func(i, j int) bool {
		// ties break on the key so the output is deterministic
		if entries[i].Count != entries[j].Count {
			return entries[i].Count > entries[j].Count
		}
		return entries[i].Key < entries[j].Key
	})

	switch *format {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(entries)
	default:
		for _, e := range entries {
			fmt.Printf("%-50s %5d\n", e.Key, e.Count)
		}
	}
}

// isReportMiss reports whether a row belongs in the `report --misses-only` set.
//
// A row nobody ever decided (pending / unresolved) can be neither correct nor a
// miss: there is no recorded decision to disagree with. Counting it also made
// the miss set depend on nothing but timing — the SAME row is 'pending' before
// SessionEnd and 'unresolved' after, so it would drift in and out of the miss
// set as sessions closed. A decided row is a miss unless the hook's recorded
// decision already matches the decision the outcome implies.
func isReportMiss(row asklog.DecisionRow) bool {
	if !asklog.OutcomeIsDecision(row.Outcome) {
		return false
	}
	hookDec := ""
	if row.HookDecision != nil {
		hookDec = *row.HookDecision
	}
	expected := outcomeToExpectedDecision(row.Outcome)
	return expected == "" || hookDec != expected
}

func emptyAsNull(s string) string {
	if s == "" {
		return "NULL"
	}
	return s
}

// printGrading is report --group-by grading: one entry per cell and group of
// the settlement grading table. --misses-only keeps the rows isReportMiss keeps.
func printGrading(rows []asklog.DecisionRow, trackerRows map[int]trackeradapter.Row, missesOnly bool, format string) {
	var kept []trackeradapter.Row
	for _, row := range rows {
		if missesOnly && !isReportMiss(row) {
			continue
		}
		kept = append(kept, trackerRows[row.ID])
	}
	entries := []reportEntry{}
	for _, c := range settlegrade.Build(kept).Cells {
		for _, g := range c.Groups {
			entries = append(entries, reportEntry{Key: string(c.Cell) + " | " + g.Group, Count: g.Count})
		}
	}
	switch format {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(entries)
	default:
		for _, e := range entries {
			fmt.Printf("%-70s %5d\n", e.Key, e.Count)
		}
	}
}
