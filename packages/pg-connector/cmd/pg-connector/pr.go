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
	"strings"

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
				return reportPrTargetedOutcome(cmd, nil, err, humanizePRShow)
			}
			resp, dispatchErr := Dispatch(cmd.Context(), reg, "pr", "show", map[string]string{"id": args[0]})
			return reportPrTargetedOutcome(cmd, resp, dispatchErr, humanizePRShow)
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
				return reportPrTargetedOutcome(cmd, nil, err, humanizePRCategorize)
			}
			resp, dispatchErr := Dispatch(cmd.Context(), reg, "pr", "categorize", map[string]string{
				"id":       args[0],
				"category": category,
			})
			return reportPrTargetedOutcome(cmd, resp, dispatchErr, humanizePRCategorize)
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
				return reportPrTargetedOutcome(cmd, nil, err, humanizePRFeedbackSet)
			}
			resp, dispatchErr := Dispatch(cmd.Context(), reg, "pr", "feedback_set", map[string]string{
				"id":          args[0],
				"comment_id":  args[1],
				"disposition": string(d),
			})
			return reportPrTargetedOutcome(cmd, resp, dispatchErr, humanizePRFeedbackSet)
		},
	}
	cmd.Flags().StringVar(&disposition, "disposition", "", "one of open|will-fix|wont-fix|no-action (required)")
	_ = cmd.MarkFlagRequired("disposition")
	return cmd
}

// reportPrTargetedOutcome writes resp's outcome to stdout — in the
// default OutputJSON mode, its wire envelope ("result" on success, or
// "error" per the taxonomy on failure) verbatim, matching the wire
// protocol's own "only stdout JSON is the contract" convention; in
// OutputHuman mode, humanize's formatted rendering instead
// [bead pg2-ox1k6] — see output.go's writeTargetedResult, which this
// delegates to. It translates err into pg-connector's own targeted-op
// exit code via outcome.go's TargetedExitCode, never deciding the exit
// code itself [design: §4.5]. A nil resp is a Tier-1 CLI-level failure
// before any well-formed wire response was produced (e.g. no backend
// registered, or an ambiguous multi-backend registration) — rather than
// returning a plain error, writeTargetedResult now builds a synthetic
// error envelope for it via scriptout.ErrorResponse and reports it
// through stdout exactly like a backend-reported failure
// [bug pg2-njx27].
func reportPrTargetedOutcome(cmd *cobra.Command, resp *scriptout.Response, err error, humanize humanizeResult) error {
	return writeTargetedResult(cmd, resp, err, humanize)
}

// humanizePRShow formats a `pr show` result (schema.PR) for human display.
func humanizePRShow(raw json.RawMessage) (string, error) {
	var pr schema.PR
	if err := scriptout.Decode(raw, &pr); err != nil {
		return "", err
	}
	return formatPR(pr), nil
}

// formatPR renders pr's identity, review/feedback state, and the two
// dedicated write fields (category, disposition) as human-readable text.
func formatPR(pr schema.PR) string {
	var b strings.Builder
	fmt.Fprintf(&b, "PR %s: %s#%d %q [%s]\n", pr.ID, pr.Repo, pr.Number, pr.Title, pr.State)
	fmt.Fprintf(&b, "  branch: %s -> %s\n", pr.Branch, pr.Base)
	fmt.Fprintf(&b, "  author: %s\n", pr.Author)
	fmt.Fprintf(&b, "  url: %s\n", pr.URL)
	fmt.Fprintf(&b, "  draft: %t  merged: %t\n", pr.Draft, pr.Merged)
	if pr.Category != "" {
		fmt.Fprintf(&b, "  category: %s\n", pr.Category)
	}
	if len(pr.Labels) > 0 {
		fmt.Fprintf(&b, "  labels: %s\n", strings.Join(pr.Labels, ", "))
	}
	if len(pr.Comments) > 0 {
		fmt.Fprintf(&b, "  comments (%d):\n", len(pr.Comments))
		for _, c := range pr.Comments {
			fmt.Fprintf(&b, "    - [%s] %s (%s): %s\n", c.ID, c.Author, prCommentStatus(c), c.Body)
		}
	}
	if len(pr.Reviews) > 0 {
		fmt.Fprintf(&b, "  reviews (%d):\n", len(pr.Reviews))
		for _, r := range pr.Reviews {
			fmt.Fprintf(&b, "    - [%s] %s: %s\n", r.ID, r.Author, r.State)
			for _, c := range r.Comments {
				fmt.Fprintf(&b, "        - [%s] %s (%s): %s\n", c.ID, c.Author, prCommentStatus(c), c.Body)
			}
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// prCommentStatus reports a PR comment/review-thread entry's current
// disposition, or its plain resolved/open state when no disposition has
// been set yet (Disposition is only ever populated once feedback_set has
// been called on it — see schema.PRComment).
func prCommentStatus(c schema.PRComment) string {
	if c.Disposition != "" {
		return string(c.Disposition)
	}
	if c.Resolved {
		return "resolved"
	}
	return "open"
}

// humanizePRCategorize formats a `pr categorize` result
// (schema.CategorizeResult) for human display.
func humanizePRCategorize(raw json.RawMessage) (string, error) {
	var r schema.CategorizeResult
	if err := scriptout.Decode(raw, &r); err != nil {
		return "", err
	}
	return fmt.Sprintf("PR %s: category set to %q", r.ID, r.Category), nil
}

// humanizePRFeedbackSet formats a `pr feedback-set` result
// (schema.FeedbackSetResult) for human display.
func humanizePRFeedbackSet(raw json.RawMessage) (string, error) {
	var r schema.FeedbackSetResult
	if err := scriptout.Decode(raw, &r); err != nil {
		return "", err
	}
	return fmt.Sprintf("PR %s: comment %s disposition set to %q", r.ID, r.CommentID, r.Disposition), nil
}
