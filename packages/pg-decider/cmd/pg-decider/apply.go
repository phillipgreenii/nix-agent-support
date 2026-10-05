// apply.go: `pg-decider apply <type> <id> [--from-item <path|->]`.
package main

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"time"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/pg-decider/internal/action"
	"github.com/phillipgreenii/pg-decider/internal/apply"
	"github.com/phillipgreenii/pg-decider/internal/audit"
	"github.com/phillipgreenii/pg-decider/internal/config"
	"github.com/phillipgreenii/pg-decider/internal/decide"
	"github.com/phillipgreenii/pg-decider/internal/exitcode"
	"github.com/phillipgreenii/pg-decider/internal/item"
	"github.com/phillipgreenii/pg-decider/internal/view"
)

// decideFn computes the action list from the view; tests swap it.
var decideFn = func(v *view.View, entityType string) action.PlanResult {
	return decide.Decide(v, entityType)
}

// applyCommand is the factory every pg-connector and pg-desk write goes
// through; tests swap it. The view read has its own seam (view.ExecCommand).
var applyCommand apply.CmdFactory = exec.CommandContext

// applyHooks returns the per-action hooks of an apply run. The audit and
// failure packets register theirs here; the audit hook is installed.
var applyHooks = func() []apply.Hook { return []apply.Hook{audit.New()} }

// applyFn is the implementation of `apply` behavior. It runs only after the
// view was freshly read (retrieve it with viewFromContext); it is nil
// without --from-item. apply never decides from the item's kind: it computes
// the actions from the view with the decide core and executes them. It
// returns the process exit code.
var applyFn = func(ctx context.Context, out, errOut io.Writer, typ, id string, it *item.Routed) int {
	v := viewFromContext(ctx)
	if v == nil {
		fmt.Fprintf(errOut, "pg-decider: no %s view of %s to apply\n", typ, id)
		return exitcode.ViewUnreadable
	}
	if !decide.HasDecider(typ) {
		fmt.Fprintf(errOut, "pg-decider: no decider is registered for entity type %q\n", typ)
		return exitcode.Failure
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(errOut, "pg-decider: %v\n", err)
		return exitcode.Failure
	}
	plan := decideFn(v, typ)
	res := apply.Run(ctx, apply.Input{
		Type: typ, ID: id, View: v, Actions: plan.Actions, Item: it, Hooks: applyHooks(),
		Env: apply.Env{Command: applyCommand, Config: cfg, Clock: time.Now, Stderr: errOut},
	})
	for _, ev := range res.Events {
		if ev.Err != nil {
			fmt.Fprintf(errOut, "pg-decider: %s %s (rule %s): %s: %v\n", ev.Action.Op, ev.WorkItemID, ev.Action.Rule, ev.Outcome, ev.Err)
		}
	}
	return res.ExitCode
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
