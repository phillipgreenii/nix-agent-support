package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/asklog"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/settingseval"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/setup"
	"github.com/spf13/cobra"
)

type baselineData struct {
	CapturedAt     string         `json:"captured_at"`
	Settings       string         `json:"settings"`
	Summary        map[string]int `json:"summary"`
	SandboxSummary map[string]int `json:"sandbox_summary,omitempty"`
	CorrectIDs     []int          `json:"correct_ids"`
}

func newBaselineCmd() *cobra.Command {
	var settingsPath, output string
	cmd := &cobra.Command{
		Use:   "baseline",
		Short: "Capture a baseline snapshot of decision categories",
		Long: `Capture a baseline snapshot by replaying every logged decision through
the current rule engine and the given settings file, then record which
decision IDs were categorized as correct.

The resulting JSON file can be compared against a later run using
'compare' to detect regressions and improvements.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			runBaseline(settingsPath, output)
			return nil
		},
	}
	cmd.Flags().StringVar(&settingsPath, "settings", "", "Path to settings file (required)")
	cmd.Flags().StringVar(&output, "output", "", "Output file path (required)")
	_ = cmd.MarkFlagRequired("settings")
	_ = cmd.MarkFlagRequired("output")
	return cmd
}

func runBaseline(settingsPathVal, outputVal string) {
	settingsPath := &settingsPathVal
	output := &outputVal

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

	counts := map[string]int{}
	sandboxCounts := map[string]int{"on": 0, "off": 0, "unknown": 0}
	var correctIDs []int

	for _, row := range rows {
		sandboxCounts[sandboxEnabledKey(row.SandboxEnabled)]++
		if _, err := os.Stat(row.CWD); os.IsNotExist(err) {
			counts["stale-cwd"]++
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

		counts[cat]++
		if cat == "correct" {
			correctIDs = append(correctIDs, row.ID)
		}
	}

	sort.Ints(correctIDs)

	data := baselineData{
		CapturedAt:     time.Now().UTC().Format(time.RFC3339),
		Settings:       *settingsPath,
		Summary:        counts,
		SandboxSummary: sandboxCounts,
		CorrectIDs:     correctIDs,
	}

	f, err := os.Create(*output)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error creating output file: %v\n", err)
		os.Exit(1)
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(data); err != nil {
		fmt.Fprintf(os.Stderr, "error writing baseline: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Baseline captured: %d correct IDs out of %d total rows\n", len(correctIDs), sumCounts(counts))
	for k, v := range counts {
		fmt.Printf("  %-25s %5d\n", k, v)
	}
	fmt.Printf("  sandbox                   on=%d off=%d unknown=%d\n",
		sandboxCounts["on"], sandboxCounts["off"], sandboxCounts["unknown"])
	fmt.Printf("Saved to %s\n", *output)
}

func hookDecStr(p *string) string {
	if p != nil {
		return *p
	}
	return ""
}

func sumCounts(m map[string]int) int {
	total := 0
	for _, v := range m {
		total += v
	}
	return total
}
