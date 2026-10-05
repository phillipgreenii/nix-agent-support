// plan.go: `pg-decider plan <type> <id> [--json]`.
package main

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/pg-decider/internal/decide"
	"github.com/phillipgreenii/pg-decider/internal/exitcode"
	"github.com/phillipgreenii/pg-decider/internal/plan"
)

// planFn is the replaceable seam for `plan` behavior (tests stub it). It runs
// only after the view was read successfully (retrieve it with
// viewFromContext) and returns the process exit code. The default evaluates
// the registered rules over the view (decide.Decide, which writes nothing) and
// renders the plan: exit 0 on any successful plan, listed actions or not, and
// 1 when the entity type has no decider. An unreadable view never reaches
// here; the command exits 3 before calling the seam.
var planFn = func(ctx context.Context, out, errOut io.Writer, typ, id string, asJSON bool) int {
	v := viewFromContext(ctx)
	if v == nil {
		fmt.Fprintf(errOut, "pg-decider: no %s view of %s to plan\n", typ, id)
		return exitcode.ViewUnreadable
	}
	if !decide.HasDecider(typ) {
		fmt.Fprintf(errOut, "pg-decider: no decider is registered for entity type %q\n", typ)
		return exitcode.Failure
	}
	res := decide.Decide(v, typ)
	var err error
	if asJSON {
		err = plan.JSON(out, res)
	} else {
		err = plan.Text(out, v, res)
	}
	if err != nil {
		fmt.Fprintf(errOut, "pg-decider: cannot print the plan: %v\n", err)
		return exitcode.Failure
	}
	return exitcode.OK
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
