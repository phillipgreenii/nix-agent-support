// pr.go: the "pg-connector pr" CLI verb group, built by the "generic pr
// entity/capability" packet on top of the Tier-1 core's registry/dispatcher
// and outcome-reporting helper. pg-connector remains the only user-facing
// CLI surface — pr is one of its verb groups, never a separate binary
// [design: §4, §6.1].
//
// Each of these three verbs is a targeted op (resolves to the one backend
// registered under connector.pr) and uses the Tier-1 targeted-op exit-code
// scheme (0/4/1) via outcome.go's TargetedExitCode — this file calls the
// dispatcher and hands TargetedExitCode the raw per-call result/error it
// got back; it never decides the exit code itself [design: §4.5].
package main

import (
	"encoding/json"
	"fmt"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
	"github.com/spf13/cobra"
)

func newPrCmd() *cobra.Command {
	prCmd := &cobra.Command{
		Use:   "pr",
		Short: "PR capability commands",
	}
	prCmd.AddCommand(newPrShowCmd())
	prCmd.AddCommand(newPrCategorizeCmd())
	prCmd.AddCommand(newPrFeedbackSetCmd())
	return prCmd
}

func newPrShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <id>",
		Short: "Show a PR's current full state, including comments/review-thread entries",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, err := LoadRegistry()
			if err != nil {
				return err
			}
			resp, dispatchErr := Dispatch(cmd.Context(), reg, "pr", "show", map[string]string{"id": args[0]})
			return reportPrTargetedOutcome(cmd, resp, dispatchErr)
		},
	}
}

func newPrCategorizeCmd() *cobra.Command {
	var category string
	cmd := &cobra.Command{
		Use:   "categorize <id>",
		Short: "Set a PR's category (a plain set/overwrite; never written as a GitHub label)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, err := LoadRegistry()
			if err != nil {
				return err
			}
			resp, dispatchErr := Dispatch(cmd.Context(), reg, "pr", "categorize", map[string]string{
				"id":       args[0],
				"category": category,
			})
			return reportPrTargetedOutcome(cmd, resp, dispatchErr)
		},
	}
	cmd.Flags().StringVar(&category, "category", "", "category to set (required); a backend's own capabilities response declares its accepted vocabulary")
	_ = cmd.MarkFlagRequired("category")
	return cmd
}

func newPrFeedbackSetCmd() *cobra.Command {
	var disposition string
	cmd := &cobra.Command{
		Use:   "feedback-set <pr-id> <comment-id>",
		Short: "Set a PR comment/review-thread entry's disposition",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			d := schema.Disposition(disposition)
			if !d.IsValid() {
				return fmt.Errorf("pg-connector: --disposition %q must be one of %v", disposition, schema.ValidDispositions)
			}
			reg, err := LoadRegistry()
			if err != nil {
				return err
			}
			resp, dispatchErr := Dispatch(cmd.Context(), reg, "pr", "feedback_set", map[string]string{
				"id":          args[0],
				"comment_id":  args[1],
				"disposition": string(d),
			})
			return reportPrTargetedOutcome(cmd, resp, dispatchErr)
		},
	}
	cmd.Flags().StringVar(&disposition, "disposition", "", "one of open|will-fix|wont-fix|no-action (required)")
	_ = cmd.MarkFlagRequired("disposition")
	return cmd
}

// reportPrTargetedOutcome writes resp's wire envelope (its "result" on
// success, or its "error" body per the taxonomy on failure) to stdout —
// matching the wire protocol's own "only stdout JSON is the contract"
// convention — and translates err into pg-connector's own targeted-op exit
// code via outcome.go's TargetedExitCode, never deciding the exit code
// itself [design: §4.5]. A nil resp is a CLI-level failure before any
// well-formed wire response was produced (e.g. no backend registered,
// or an ambiguous multi-backend registration) — that case is returned as a
// plain error instead, so main's run() reports it on stderr rather than
// fabricating a JSON body.
func reportPrTargetedOutcome(cmd *cobra.Command, resp *scriptout.Response, err error) error {
	if resp == nil {
		return err
	}
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetEscapeHTML(false)
	if encErr := enc.Encode(resp); encErr != nil {
		return encErr
	}
	if code := TargetedExitCode(err); code != 0 {
		return &exitError{code: code}
	}
	return nil
}
