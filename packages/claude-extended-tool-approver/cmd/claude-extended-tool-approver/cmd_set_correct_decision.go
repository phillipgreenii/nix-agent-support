package main

import (
	"fmt"
	"os"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/asklog"
	"github.com/spf13/cobra"
)

func newSetCorrectDecisionCmd() *cobra.Command {
	var decision, explanation string
	cmd := &cobra.Command{
		Use:   "set-correct-decision ID [ID ...]",
		Short: "Set the ground-truth decision for logged rows",
		Long: `Annotate one or more decision rows with a ground-truth decision
(allow, deny, ask, or abstain) and an explanation. These annotations
are used by 'evaluate' and 'compare' as the source of truth when
categorizing rows as correct or incorrect.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			runSetCorrectDecision(decision, explanation, args)
			return nil
		},
	}
	cmd.Flags().StringVar(&decision, "decision", "", "Correct decision: allow|deny|ask|abstain (required)")
	cmd.Flags().StringVar(&explanation, "explanation", "", "Why this is the correct decision (required)")
	_ = cmd.MarkFlagRequired("decision")
	_ = cmd.MarkFlagRequired("explanation")
	return cmd
}

func runSetCorrectDecision(decisionVal, explanationVal string, args []string) {
	decision := &decisionVal
	explanation := &explanationVal

	valid := map[string]bool{"allow": true, "deny": true, "ask": true, "abstain": true}
	if !valid[*decision] {
		fmt.Fprintf(os.Stderr, "error: --decision must be allow|deny|ask|abstain\n")
		os.Exit(1)
	}

	ids, err := parseIDs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if len(ids) == 0 {
		fmt.Fprintf(os.Stderr, "error: at least one ID is required\n")
		os.Exit(1)
	}

	store, err := asklog.NewStore(asklog.DefaultDBPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	if err := store.SetCorrectDecision(ids, *decision, *explanation); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Set correct_hook_decision=%q on %d row(s)\n", *decision, len(ids))
}
