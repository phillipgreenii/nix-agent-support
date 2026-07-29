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
	CommandClass   string `json:"command_class"`
	HookDecision   string `json:"hook_decision"`
	ReplayResult   string `json:"replay_result"`
	SettingsResult string `json:"settings_result,omitempty"`
	Category       string `json:"category"`
	Outcome        string `json:"outcome"`
	SandboxEnabled *int   `json:"sandbox_enabled"`
	// approval_source is the derived approval-MECHANISM axis
	// {unknown,bypass,auto,settings,hook,user}; the four raw fields below back
	// it and let the skill segment orthogonally (e.g. agent_type IS NOT NULL).
	ApprovalSource string          `json:"approval_source"`
	PermissionMode *string         `json:"permission_mode"`
	AgentType      *string         `json:"agent_type"`
	OutcomeNotes   *string         `json:"outcome_notes"`
	ToolResponse   json.RawMessage `json:"tool_response"`
}

func newEvaluateCmd() *cobra.Command {
	var days int
	var since, settingsPath, format, approvalSource string
	var missesOnly bool
	cmd := &cobra.Command{
		Use:   "evaluate",
		Short: "Replay logged decisions and categorize them as correct or miss",
		Long: `Replay every logged decision through the current rule engine and
categorize each as correct, miss-caught-by-settings, miss-uncaught,
needs-review, unresolved, or stale-cwd.

A row whose outcome is "unresolved" (never resolved — interrupted, abandoned,
or swept at SessionEnd) carries no ground truth, so it is categorized
"unresolved" and is never counted as correct or as a miss.

Use --settings to additionally evaluate each decision against a
Claude Code settings file so misses can be attributed to settings
coverage.

Use --approval-source to restrict evaluation to a single approval-mechanism
bucket (unknown|bypass|auto|settings|hook|user).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			runEvaluate(days, since, settingsPath, format, approvalSource, missesOnly)
			return nil
		},
	}
	cmd.Flags().IntVar(&days, "days", 0, "Only evaluate rows from the last N days")
	cmd.Flags().StringVar(&since, "since", "", "Only evaluate rows after this date (ISO8601)")
	cmd.Flags().StringVar(&settingsPath, "settings", "", "Path to settings file for settings evaluation")
	cmd.Flags().StringVar(&format, "format", "summary", "Output format: json|summary")
	cmd.Flags().StringVar(&approvalSource, "approval-source", "", "Only evaluate rows with this approval_source (unknown|bypass|auto|settings|hook|user)")
	cmd.Flags().BoolVar(&missesOnly, "misses-only", false, "Only show rows where hook is wrong")
	return cmd
}

func runEvaluate(daysVal int, sinceVal, settingsPathVal, formatVal, approvalSourceVal string, missesOnlyVal bool) {
	days := &daysVal
	since := &sinceVal
	settingsPath := &settingsPathVal
	format := &formatVal
	approvalSourceFilter := &approvalSourceVal
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
	defer func() { _ = store.Close() }()

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
		"correct":                 0,
		"miss-caught-by-settings": 0,
		"miss-uncaught":           0,
		"needs-review":            0,
		"stale-cwd":               0,
		"unresolved":              0,
	}

	// sandboxCounts tallies rows by sandbox state ("on"/"off"/"unknown").
	// It includes every row, not just misses, and mirrors the totals line.
	sandboxCounts := map[string]int{"on": 0, "off": 0, "unknown": 0}

	var results []evalResult

	for _, row := range rows {
		// approval_source classifies CONTEXT, not outcome, so it is derived and
		// filtered before anything else (including the stale-cwd short-circuit).
		approvalSource := asklog.ApprovalSource(row.PermissionMode, row.PromptID, row.HookDecision)
		if *approvalSourceFilter != "" && approvalSource != *approvalSourceFilter {
			continue
		}

		sandboxCounts[sandboxEnabledKey(row.SandboxEnabled)]++
		r := evalResult{
			ID:             row.ID,
			ToolName:       row.ToolName,
			ToolSummary:    row.ToolSummary,
			CommandClass:   asklog.CommandClass(row.ToolName, json.RawMessage(row.ToolInputJSON), row.CWD),
			Outcome:        row.Outcome,
			SandboxEnabled: sandboxEnabledPtr(row.SandboxEnabled),
			ApprovalSource: approvalSource,
			PermissionMode: row.PermissionMode,
			AgentType:      row.AgentType,
			OutcomeNotes:   row.OutcomeNotes,
		}
		if row.ToolResponse != nil && *row.ToolResponse != "" {
			r.ToolResponse = json.RawMessage(*row.ToolResponse)
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
		result := eng.EvaluateHook(input)
		r.ReplayResult = decisionToDBString(result.Decision)

		// Settings evaluation
		if se != nil {
			r.SettingsResult = se.Evaluate(row.ToolName, json.RawMessage(row.ToolInputJSON), row.CWD)
		}

		// Categorize
		r.Category = categorize(r, row)

		counts[r.Category]++
		// "unresolved" is excluded from --misses-only for the same reason
		// "stale-cwd" is: it carries no ground truth, so it is not a miss and
		// must not inflate the miss dataset downstream analysis ranks.
		if *missesOnly && (r.Category == "correct" || r.Category == "unresolved") {
			continue
		}
		results = append(results, r)
	}

	switch *format {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(results)
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
		fmt.Printf("Unresolved:          %5d\n", counts["unresolved"])
		fmt.Printf("By sandbox:          on=%d off=%d unknown=%d\n",
			sandboxCounts["on"], sandboxCounts["off"], sandboxCounts["unknown"])
	}
}

func categorize(r evalResult, row asklog.DecisionRow) string {
	// If correct_hook_decision is set, compare against that. An explicit human
	// annotation is real ground truth, so it outranks even a never-resolved
	// outcome.
	if row.CorrectDec != nil {
		if r.ReplayResult == *row.CorrectDec {
			return "correct"
		}
		if r.SettingsResult != "" {
			return "miss-caught-by-settings"
		}
		return "miss-uncaught"
	}

	// Nobody ever decided this call, so there is no ground truth to grade
	// against. 'unresolved' gets its own terminal category — never "correct",
	// never a "miss-*" — so a SessionEnd sweep can no longer masquerade as a
	// user denial (and can no longer be credited as a correct deny either).
	if !asklog.OutcomeIsDecision(row.Outcome) {
		if row.Outcome == asklog.OutcomeUnresolved {
			return "unresolved"
		}
		return "needs-review"
	}

	expectedDecision := outcomeToExpectedDecision(row.Outcome)

	if r.ReplayResult == expectedDecision {
		return "correct"
	}

	// Hook allows but the call was DECLINED — ambiguous. The user may have
	// redirected (provided text feedback) rather than truly rejecting the tool.
	// Since we can't distinguish denial from correction, classify as
	// needs-review. This carve-out is deliberately scoped to OutcomeDenied: a
	// hook Reject (OutcomeRejected) involved no user, so there is no redirection
	// to confuse it with — a Reject that now replays to allow is a real engine
	// change and MUST stay visible as a miss.
	if r.ReplayResult == "allow" && row.Outcome == asklog.OutcomeDenied {
		return "needs-review"
	}

	// Hook got it wrong — check if settings would catch it
	if r.SettingsResult != "" {
		return "miss-caught-by-settings"
	}
	return "miss-uncaught"
}

// outcomeToExpectedDecision maps a recorded outcome to the hook decision that
// would have been RIGHT for it. It returns "" for the outcomes that record no
// decision at all (pending, unresolved) — those have no expected decision, and
// asklog.OutcomeIsDecision MUST be consulted before grading against them.
func outcomeToExpectedDecision(outcome string) string {
	switch outcome {
	case asklog.OutcomeApproved:
		return "allow"
	case asklog.OutcomeDenied, asklog.OutcomeRejected:
		// Both are a refusal of the call, so a replayed "deny" is correct for
		// either: OutcomeDenied is somebody declining it, OutcomeRejected is the
		// hook refusing it itself (a self-consistency check on the engine).
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
