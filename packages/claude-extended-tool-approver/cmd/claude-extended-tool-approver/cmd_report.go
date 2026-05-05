package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/asklog"
	"github.com/spf13/cobra"
)

type reportEntry struct {
	Key   string `json:"key"`
	Count int    `json:"count"`
}

func newReportCmd() *cobra.Command {
	var groupBy, format, since string
	var missesOnly bool
	var days int
	cmd := &cobra.Command{
		Use:   "report",
		Short: "Summarize logged decisions grouped by tool, hook decision, or outcome",
		Long: `Summarize logged decisions from the asklog database, grouped by
tool_name, hook_decision, or outcome. Optionally filter to only show
miss categories (where hook disagreed with user outcome) and to
restrict the time window.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			runReport(groupBy, missesOnly, format, days, since)
			return nil
		},
	}
	cmd.Flags().StringVar(&groupBy, "group-by", "tool_name", "Group results by: tool_name|hook_decision|outcome|sandbox_enabled")
	cmd.Flags().BoolVar(&missesOnly, "misses-only", false, "Only show miss categories")
	cmd.Flags().StringVar(&format, "format", "table", "Output format: json|table")
	cmd.Flags().IntVar(&days, "days", 0, "Only report rows from the last N days")
	cmd.Flags().StringVar(&since, "since", "", "Only report rows after this date (ISO8601)")
	return cmd
}

func runReport(groupByVal string, missesOnlyVal bool, formatVal string, daysVal int, sinceVal string) {
	groupBy := &groupByVal
	missesOnly := &missesOnlyVal
	format := &formatVal
	days := &daysVal
	since := &sinceVal

	sinceDate := *since
	if *days > 0 && sinceDate == "" {
		sinceDate = time.Now().AddDate(0, 0, -*days).UTC().Format(time.RFC3339)
	}

	store, err := asklog.NewStore(asklog.DefaultDBPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	rows, err := store.QueryRows(sinceDate)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	counts := map[string]int{}
	for _, row := range rows {
		if *missesOnly {
			hookDec := ""
			if row.HookDecision != nil {
				hookDec = *row.HookDecision
			}
			expected := outcomeToExpectedDecision(row.Outcome)
			if expected != "" && hookDec == expected {
				continue
			}
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
		return entries[i].Count > entries[j].Count
	})

	switch *format {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(entries)
	default:
		for _, e := range entries {
			fmt.Printf("%-50s %5d\n", e.Key, e.Count)
		}
	}
}
