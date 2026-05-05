package main

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/asklog"
	"github.com/spf13/cobra"
)

type showTraceEntry struct {
	RuleOrder int    `json:"rule_order"`
	RuleName  string `json:"rule_name"`
	Decision  string `json:"decision"`
	Reason    string `json:"reason,omitempty"`
}

type showResult struct {
	ID                    int              `json:"id"`
	SessionID             string           `json:"session_id"`
	CWD                   string           `json:"cwd"`
	ToolName              string           `json:"tool_name"`
	ToolInputJSON         string           `json:"tool_input_json"`
	ToolSummary           string           `json:"tool_summary"`
	HookDecision          string           `json:"hook_decision"`
	HookReason            string           `json:"hook_reason"`
	Outcome               string           `json:"outcome"`
	Excluded              int              `json:"excluded"`
	ExcludedReason        string           `json:"excluded_reason,omitempty"`
	CorrectDec            string           `json:"correct_hook_decision,omitempty"`
	CorrectDecExplanation string           `json:"correct_hook_decision_explanation,omitempty"`
	CreatedAt             string           `json:"created_at"`
	SandboxEnabled        *int             `json:"sandbox_enabled"`
	Trace                 []showTraceEntry `json:"trace,omitempty"`
}

func newShowCmd() *cobra.Command {
	var format string
	cmd := &cobra.Command{
		Use:   "show ID [ID ...]",
		Short: "Show full details and rule-trace for logged decision rows",
		Long: `Display the full details of one or more logged decision rows,
including the rule-evaluation trace that produced the hook decision.

Use --format=json for machine-readable output or --format=table
(default) for a human-readable tabular view.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			runShow(format, args)
			return nil
		},
	}
	cmd.Flags().StringVar(&format, "format", "table", "Output format: json|table")
	return cmd
}

func runShow(formatVal string, args []string) {
	format := &formatVal

	ids, err := parseIDs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	store, err := asklog.NewStore(asklog.DefaultDBPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	rows, err := store.QueryRowsByIDs(ids)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	results := make([]showResult, len(rows))
	for i, r := range rows {
		results[i] = showResult{
			ID:                    r.ID,
			SessionID:             r.SessionID,
			CWD:                   r.CWD,
			ToolName:              r.ToolName,
			ToolInputJSON:         r.ToolInputJSON,
			ToolSummary:           r.ToolSummary,
			HookDecision:          r.HookDecision,
			HookReason:            r.HookReason,
			Outcome:               r.Outcome,
			Excluded:              r.Excluded,
			ExcludedReason:        r.ExcludedReason,
			CorrectDec:            r.CorrectDec,
			CorrectDecExplanation: r.CorrectDecExplanation,
			CreatedAt:             r.CreatedAt,
			SandboxEnabled:        sandboxEnabledPtr(r.SandboxEnabled),
		}

		// Query trace entries for this decision
		traceRows, err := store.QueryTraceByDecisionID(r.ID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not query trace for id=%d: %v\n", r.ID, err)
			continue
		}
		for _, tr := range traceRows {
			results[i].Trace = append(results[i].Trace, showTraceEntry{
				RuleOrder: tr.RuleOrder,
				RuleName:  tr.RuleName,
				Decision:  tr.Decision,
				Reason:    tr.Reason,
			})
		}
	}

	switch *format {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(results)
	default:
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tTOOL\tSUMMARY\tHOOK\tOUTCOME\tCORRECT\tSANDBOX")
		for _, r := range results {
			correct := r.CorrectDec
			if correct == "" {
				correct = "-"
			}
			sandbox := "-"
			if r.SandboxEnabled != nil {
				if *r.SandboxEnabled != 0 {
					sandbox = "on"
				} else {
					sandbox = "off"
				}
			}
			fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\t%s\n",
				r.ID, r.ToolName, r.ToolSummary, r.HookDecision, r.Outcome, correct, sandbox)
			if len(r.Trace) > 0 {
				fmt.Fprintln(w, "  TRACE:")
				for _, tr := range r.Trace {
					if tr.Reason != "" {
						fmt.Fprintf(w, "    %d. %s\t-> %s: %s\n", tr.RuleOrder, tr.RuleName, tr.Decision, tr.Reason)
					} else {
						fmt.Fprintf(w, "    %d. %s\t-> %s\n", tr.RuleOrder, tr.RuleName, tr.Decision)
					}
				}
			}
		}
		w.Flush()
	}
}
