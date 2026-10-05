// apply.go: `pg-decider apply <type> <id> [--from-item <path|->]`.
package main

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/pg-decider/internal/exitcode"
	"github.com/phillipgreenii/pg-decider/internal/item"
)

// applyFn is the replaceable seam for `apply` behavior. It runs only after
// the view was freshly read (retrieve it with viewFromContext); it is nil
// without --from-item. apply never decides from the item's kind. It returns
// the process exit code. A sibling packet replaces the default.
var applyFn = func(ctx context.Context, out, errOut io.Writer, typ, id string, it *item.Routed) int {
	fmt.Fprintln(errOut, "pg-decider apply: not implemented")
	return exitcode.Failure
}

func newApplyCmd() *cobra.Command {
	var fromItem string
	cmd := &cobra.Command{
		Use:   "apply <type> <id>",
		Short: "Apply the actions the decider calls for on an entity",
		Long: "Re-read the composite view of <type> <id> (type is pr, issue or thread)\n" +
			"through `pg-desk <type> show <id> --json` and apply the actions the decider\n" +
			"calls for. --from-item names the routed pg-router item that triggered the run\n" +
			"(a file path, or - for stdin); it only identifies the trigger and never\n" +
			"decides what is applied. Exit 0 when all actions are applied or none are\n" +
			"needed, 2 when some failed, 3 when the view is unreadable (nothing applied).",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			typ, id := args[0], args[1]
			if err := checkType(typ); err != nil {
				return err
			}
			var it *item.Routed
			if cmd.Flags().Changed("from-item") {
				var err error
				if it, err = item.Read(fromItem); err != nil {
					return err
				}
			}
			ctx, ok := readView(cmd.Context(), cmd.ErrOrStderr(), typ, id)
			if !ok {
				return errExit{exitcode.ViewUnreadable}
			}
			if code := applyFn(ctx, cmd.OutOrStdout(), cmd.ErrOrStderr(), typ, id, it); code != exitcode.OK {
				return errExit{code}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&fromItem, "from-item", "", "routed pg-router item that triggered this run: a file path, or - for stdin")
	return cmd
}
