package main

import (
	"fmt"
	"os"
	"strconv"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/asklog"
	"github.com/spf13/cobra"
)

func newMarkExcludedCmd() *cobra.Command {
	var reason string
	cmd := &cobra.Command{
		Use:   "mark-excluded ID [ID ...]",
		Short: "Mark decision rows as excluded from evaluation",
		Long: `Mark one or more decision rows as excluded from evaluation, with a
reason. Excluded rows are skipped by 'evaluate', 'baseline', and
'compare'. Use this to filter out bugs, test data, or decisions that
are no longer relevant.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			runMarkExcluded(reason, args)
			return nil
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "Reason for excluding (required)")
	_ = cmd.MarkFlagRequired("reason")
	return cmd
}

func runMarkExcluded(reasonVal string, args []string) {
	reason := &reasonVal
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

	if err := store.MarkExcluded(ids, *reason); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Marked %d row(s) as excluded\n", len(ids))
}

func parseIDs(args []string) ([]int, error) {
	var ids []int
	for _, a := range args {
		id, err := strconv.Atoi(a)
		if err != nil {
			return nil, fmt.Errorf("invalid ID %q: %w", a, err)
		}
		ids = append(ids, id)
	}
	return ids, nil
}
