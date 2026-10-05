// plan.go: `pg-decider plan <type> <id> [--json]`.
package main

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/pg-decider/internal/exitcode"
)

// planFn is the replaceable seam for `plan` behavior. It runs only after the
// view was read successfully (retrieve it with viewFromContext) and returns
// the process exit code. A sibling packet replaces the default.
var planFn = func(ctx context.Context, out, errOut io.Writer, typ, id string, asJSON bool) int {
	fmt.Fprintln(errOut, "pg-decider plan: not implemented")
	return exitcode.Failure
}

func newPlanCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "plan <type> <id>",
		Short: "Print the actions the decider would take for an entity, writing nothing",
		Long: "Read the composite view of <type> <id> (type is pr, issue or thread) through\n" +
			"`pg-desk <type> show <id> --json` and print the actions the decider would take.\n" +
			"Nothing is written. Exit 0 on success, 3 when the view is unreadable.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			typ, id := args[0], args[1]
			if err := checkType(typ); err != nil {
				return err
			}
			ctx, ok := readView(cmd.Context(), cmd.ErrOrStderr(), typ, id)
			if !ok {
				return errExit{exitcode.ViewUnreadable}
			}
			if code := planFn(ctx, cmd.OutOrStdout(), cmd.ErrOrStderr(), typ, id, asJSON); code != exitcode.OK {
				return errExit{code}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the plan as JSON")
	return cmd
}
