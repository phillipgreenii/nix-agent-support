package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/asklog"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/settingseval"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/setup"
	"github.com/spf13/cobra"
)

type evalResult struct {
	ID             int    `json:"id"`
	ToolName       string `json:"tool_name"`
	ToolSummary    string `json:"tool_summary"`
	HookDecision   string `json:"hook_decision"`
	ReplayResult   string `json:"replay_result"`
	SettingsResult string `json:"settings_result,omitempty"`
	Category       string `json:"category"`
	Outcome        string `json:"outcome"`
	SandboxEnabled *int   `json:"sandbox_enabled"`
}

func newEvaluateCmd() *cobra.Command {
	var days int
	var since, settingsPath, format string
	var missesOnly bool
	cmd := &cobra.Command{
		Use:   "evaluate",
		Short: "Replay logged decisions and categorize them as correct or miss",
		Long: `Replay every logged decision through the current rule engine and
categorize each as correct, miss-caught-by-settings, miss-uncaught,
needs-review, or stale-cwd.

Use --settings to additionally evaluate each decision against a
Claude Code settings file so misses can be attributed to settings
coverage.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			runEvaluate(days, since, settingsPath, format, missesOnly)
			return nil
		},
	}
	cmd.Flags().IntVar(&days, "days", 0, "Only evaluate rows from the last N days")
	cmd.Flags().StringVar(&since, "since", "", "Only evaluate rows after this date (ISO8601)")
	cmd.Flags().StringVar(&settingsPath, "settings", "", "Path to settings file for settings evaluation")
	cmd.Flags().StringVar(&format, "format", "summary", "Output format: json|summary")
	cmd.Flags().BoolVar(&missesOnly, "misses-only", false, "Only show rows where hook is wrong")
	return cmd
}

func runEvaluate(daysVal int, sinceVal, settingsPathVal, formatVal string, missesOnlyVal bool) {
	days := &daysVal
	since := &sinceVal
	settingsPath := &settingsPathVal
	format := &formatVal
	missesOnly := &missesOnlyVal

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
		fmt.Fprintf(os.Stderr, "error querying rows: %v\n", err)
		os.Exit(1)
	}

	var se *settingseval.SettingsEvaluator
	if *settingsPath != "" {
		se, err = settingseval.NewSettingsEvaluator(*settingsPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error loading settings: %v\n", err)
			os.Exit(1)
		}
	}

	counts := map[string]int{
		"correct":              0,
		"miss-caught-by-settings": 0,
		"miss-uncaught":        0,
		"needs-review":         0,
		"stale-cwd":            0,
	}

	// sandboxCounts tallies rows by sandbox state ("on"/"off"/"unknown").
	// It includes every row, not just misses, and mirrors the totals line.
	sandboxCounts := map[string]int{"on": 0, "off": 0, "unknown": 0}

	var results []evalResult

	for _, row := range rows {
		sandboxCounts[sandboxEnabledKey(row.SandboxEnabled)]++
		r := evalResult{
			ID:             row.ID,
			ToolName:       row.ToolName,
			ToolSummary:    row.ToolSummary,
			Outcome:        row.Outcome,
			SandboxEnabled: sandboxEnabledPtr(row.SandboxEnabled),
		}
		if row.HookDecision != nil {
			r.HookDecision = *row.HookDecision
		}

		// Check if CWD exists
		if _, err := os.Stat(row.CWD); os.IsNotExist(err) {
			r.Category = "stale-cwd"
			counts["stale-cwd"]++
			if !*missesOnly {
				results = append(results, r)
			}
			continue
		}

		// Replay through engine
		eng := setup.NewEngineForCWD(row.CWD)
		input := &hookio.HookInput{
			ToolName:  row.ToolName,
			ToolInput: json.RawMessage(row.ToolInputJSON),
			CWD:       row.CWD,
		}
		result := eng.Evaluate(input)
		r.ReplayResult = decisionToDBString(result.Decision)

		// Settings evaluation
		if se != nil {
			r.SettingsResult = se.Evaluate(row.ToolName, json.RawMessage(row.ToolInputJSON), row.CWD)
		}

		// Categorize
		r.Category = categorize(r, row)

		counts[r.Category]++
		if *missesOnly && r.Category == "correct" {
			continue
		}
		results = append(results, r)
	}

	switch *format {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(results)
	default:
		total := 0
		for _, c := range counts {
			total += c
		}
		fmt.Printf("Total rows:          %5d\n", total)
		fmt.Printf("Stale CWD:           %5d\n", counts["stale-cwd"])
		fmt.Printf("Correct:             %5d\n", counts["correct"])
		fmt.Printf("Misses (settings):   %5d\n", counts["miss-caught-by-settings"])
		fmt.Printf("Misses (uncaught):   %5d\n", counts["miss-uncaught"])
		fmt.Printf("Needs review:        %5d\n", counts["needs-review"])
		fmt.Printf("By sandbox:          on=%d off=%d unknown=%d\n",
			sandboxCounts["on"], sandboxCounts["off"], sandboxCounts["unknown"])
	}
}

func categorize(r evalResult, row asklog.DecisionRow) string {
	// If correct_hook_decision is set, compare against that
	if row.CorrectDec != nil {
		if r.ReplayResult == *row.CorrectDec {
			return "correct"
		}
		if r.SettingsResult != "" {
			return "miss-caught-by-settings"
		}
		return "miss-uncaught"
	}

	// Infer from outcome
	expectedDecision := outcomeToExpectedDecision(row.Outcome)
	if expectedDecision == "" {
		return "needs-review"
	}

	if r.ReplayResult == expectedDecision {
		return "correct"
	}

	// Hook allows but user denied — ambiguous. The user may have redirected
	// (provided text feedback) rather than truly rejecting the tool. Since we
	// can't distinguish denial from correction, classify as needs-review.
	if r.ReplayResult == "allow" && row.Outcome == "denied" {
		return "needs-review"
	}

	// Hook got it wrong — check if settings would catch it
	if r.SettingsResult != "" {
		return "miss-caught-by-settings"
	}
	return "miss-uncaught"
}

func outcomeToExpectedDecision(outcome string) string {
	switch outcome {
	case "approved":
		return "allow"
	case "denied":
		return "deny"
	default:
		return ""
	}
}

func decisionToDBString(d hookio.Decision) string {
	switch d {
	case hookio.Approve:
		return "allow"
	case hookio.Reject:
		return "deny"
	case hookio.Ask:
		return "ask"
	case hookio.Abstain:
		return "abstain"
	default:
		return "unknown"
	}
}
