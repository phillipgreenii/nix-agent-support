package main

import (
	"fmt"
	"os"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/asklog"
	"github.com/spf13/cobra"
)

func newSetCorrectDecisionCmd() *cobra.Command {
	var decision, explanation string
	var src sourceFlags
	cmd := &cobra.Command{
		Use:   "set-correct-decision ID [ID ...]",
		Short: "Set the ground-truth decision for logged rows",
		Long: `Annotate one or more decision rows with a ground-truth decision
(allow, deny, ask, or abstain) and an explanation. These annotations
are used by 'evaluate' and 'compare' as the source of truth when
categorizing rows as correct or incorrect.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := src.validate(); err != nil {
				return err
			}
			runSetCorrectDecision(decision, explanation, args, src)
			return nil
		},
	}
	cmd.Flags().StringVar(&decision, "decision", "", "Correct decision: allow|deny|ask|abstain (required)")
	cmd.Flags().StringVar(&explanation, "explanation", "", "Why this is the correct decision (required)")
	addSourceFlags(cmd, &src)
	_ = cmd.MarkFlagRequired("decision")
	_ = cmd.MarkFlagRequired("explanation")
	return cmd
}

func runSetCorrectDecision(decisionVal, explanationVal string, args []string, src sourceFlags) {
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

	if src.isTracker() {
		// A tracker call is labelled under its lasting session_id + tool_use_id, in
		// CETA's own database; the tracker database itself is never written.
		keys := resolveTrackerKeys(src, ids)
		store, err := asklog.NewStore(asklog.DefaultDBPath())
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		defer func() { _ = store.Close() }()
		if err := store.SetTrackerCorrectDecision(keys, *decision, *explanation); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Set correct_hook_decision=%q on %d row(s)\n", *decision, len(ids))
		return
	}

	// set-correct-decision genuinely WRITES (UPDATE ... SET
	// correct_hook_decision = ...) — it MUST keep the read-write store
	// (pg2-cbihz audited this subcommand and confirmed it belongs on the
	// write side of the split).
	store, err := asklog.NewStore(asklog.DefaultDBPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = store.Close() }()

	if err := store.SetCorrectDecision(ids, *decision, *explanation); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Set correct_hook_decision=%q on %d row(s)\n", *decision, len(ids))
}
