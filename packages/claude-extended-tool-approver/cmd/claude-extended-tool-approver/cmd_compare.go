package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/asklog"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/settingseval"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/setup"
	"github.com/spf13/cobra"
)

type compareResult struct {
	BaselineSummary        map[string]int `json:"baseline_summary"`
	CurrentSummary         map[string]int `json:"current_summary"`
	BaselineSandboxSummary map[string]int `json:"baseline_sandbox_summary,omitempty"`
	CurrentSandboxSummary  map[string]int `json:"current_sandbox_summary,omitempty"`
	Regressions            []int          `json:"regressions"`
	Improvements           []int          `json:"improvements"`
}

func newCompareCmd() *cobra.Command {
	var settingsPath, baselinePath, format string
	cmd := &cobra.Command{
		Use:   "compare",
		Short: "Compare current decisions against a captured baseline",
		Long: `Replay every logged decision through the current rule engine and
compare the resulting categories against a baseline file produced by
'baseline'.

Exits non-zero if any regressions are found (decisions that were
correct in the baseline but are no longer correct).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			runCompare(settingsPath, baselinePath, format)
			return nil
		},
	}
	cmd.Flags().StringVar(&settingsPath, "settings", "", "Path to settings file (required)")
	cmd.Flags().StringVar(&baselinePath, "baseline", "", "Path to baseline JSON file (required)")
	cmd.Flags().StringVar(&format, "format", "summary", "Output format: json|summary")
	_ = cmd.MarkFlagRequired("settings")
	_ = cmd.MarkFlagRequired("baseline")
	return cmd
}

func runCompare(settingsPathVal, baselinePathVal, formatVal string) {
	settingsPath := &settingsPathVal
	baselinePath := &baselinePathVal
	format := &formatVal

	// Load baseline
	baselineFile, err := os.Open(*baselinePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error opening baseline: %v\n", err)
		os.Exit(1)
	}
	defer baselineFile.Close()

	var baseline baselineData
	if err := json.NewDecoder(baselineFile).Decode(&baseline); err != nil {
		fmt.Fprintf(os.Stderr, "error parsing baseline: %v\n", err)
		os.Exit(1)
	}

	baselineSet := make(map[int]bool, len(baseline.CorrectIDs))
	for _, id := range baseline.CorrectIDs {
		baselineSet[id] = true
	}

	// Evaluate current state
	store, err := asklog.NewStore(asklog.DefaultDBPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	rows, err := store.QueryRows("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error querying rows: %v\n", err)
		os.Exit(1)
	}

	se, err := settingseval.NewSettingsEvaluator(*settingsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error loading settings: %v\n", err)
		os.Exit(1)
	}

	currentCounts := map[string]int{}
	currentSandbox := map[string]int{"on": 0, "off": 0, "unknown": 0}
	currentCorrect := map[int]bool{}

	for _, row := range rows {
		currentSandbox[sandboxEnabledKey(row.SandboxEnabled)]++
		if _, err := os.Stat(row.CWD); os.IsNotExist(err) {
			currentCounts["stale-cwd"]++
			continue
		}

		eng := setup.NewEngineForCWD(row.CWD)
		input := &hookio.HookInput{
			ToolName:  row.ToolName,
			ToolInput: json.RawMessage(row.ToolInputJSON),
			CWD:       row.CWD,
		}
		result := eng.Evaluate(input)
		replayResult := decisionToDBString(result.Decision)
		settingsResult := se.Evaluate(row.ToolName, json.RawMessage(row.ToolInputJSON), row.CWD)

		cat := categorize(evalResult{
			ID:             row.ID,
			ToolName:       row.ToolName,
			HookDecision:   hookDecStr(row.HookDecision),
			ReplayResult:   replayResult,
			SettingsResult: settingsResult,
		}, row)

		currentCounts[cat]++
		if cat == "correct" {
			currentCorrect[row.ID] = true
		}
	}

	// Find regressions (was correct, now isn't)
	var regressions []int
	for _, id := range baseline.CorrectIDs {
		if !currentCorrect[id] {
			regressions = append(regressions, id)
		}
	}
	sort.Ints(regressions)

	// Find improvements (wasn't correct, now is)
	var improvements []int
	for id := range currentCorrect {
		if !baselineSet[id] {
			improvements = append(improvements, id)
		}
	}
	sort.Ints(improvements)

	cr := compareResult{
		BaselineSummary:        baseline.Summary,
		CurrentSummary:         currentCounts,
		BaselineSandboxSummary: baseline.SandboxSummary,
		CurrentSandboxSummary:  currentSandbox,
		Regressions:            regressions,
		Improvements:           improvements,
	}

	switch *format {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(cr)
	default:
		fmt.Println("=== Baseline vs Current ===")
		fmt.Printf("%-25s %8s %8s %8s\n", "Category", "Baseline", "Current", "Delta")
		allKeys := mergeKeys(baseline.Summary, currentCounts)
		sort.Strings(allKeys)
		for _, k := range allKeys {
			b := baseline.Summary[k]
			c := currentCounts[k]
			delta := c - b
			sign := ""
			if delta > 0 {
				sign = "+"
			}
			fmt.Printf("%-25s %8d %8d %7s%d\n", k, b, c, sign, delta)
		}
		fmt.Println()
		fmt.Println("=== Sandbox state ===")
		for _, k := range []string{"on", "off", "unknown"} {
			b := baseline.SandboxSummary[k]
			c := currentSandbox[k]
			delta := c - b
			sign := ""
			if delta > 0 {
				sign = "+"
			}
			fmt.Printf("%-25s %8d %8d %7s%d\n", k, b, c, sign, delta)
		}
		fmt.Println()
		fmt.Printf("Regressions:  %d\n", len(regressions))
		fmt.Printf("Improvements: %d\n", len(improvements))
		if len(regressions) > 0 {
			fmt.Printf("\nRegressed IDs: %v\n", regressions)
		}
	}

	if len(regressions) > 0 {
		os.Exit(1)
	}
}

func mergeKeys(a, b map[string]int) []string {
	seen := map[string]bool{}
	for k := range a {
		seen[k] = true
	}
	for k := range b {
		seen[k] = true
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	return keys
}
