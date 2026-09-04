// issue.go: the "pg-connector issue" CLI verb group, built by the "generic
// issue entity/capability" packet on top of the Tier-1 core's
// registry/dispatcher and outcome-reporting helper, mirroring pr.go's
// identical structure. pg-connector remains the only user-facing CLI
// surface — issue is one of its verb groups, never a separate binary.
//
// Each of these four verbs is a targeted op (resolves to the one backend
// registered under connector.issue) and uses the Tier-1 targeted-op
// exit-code scheme (0/4/1) via outcome.go's TargetedExitCode — this file
// calls the dispatcher and hands TargetedExitCode the raw per-call
// result/error it got back; it never decides the exit code itself.
//
// transition's --state value is a plain string, never validated here
// against a fixed set: valid target-state values are declared per-backend
// in that backend's own capabilities response (vocabulary.state), since
// Jira/beads/GitHub Issues do not share one state vocabulary — unlike
// pr's feedback-set, which validates --disposition client-side against a
// genuinely closed cross-backend enum (schema.ValidDispositions).
package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
	"github.com/spf13/cobra"
)

func newIssueCmd() *cobra.Command {
	issueCmd := &cobra.Command{
		Use:   "issue",
		Short: "Issue capability commands",
	}
	issueCmd.AddCommand(newIssueShowCmd())
	issueCmd.AddCommand(newIssueCreateCmd())
	issueCmd.AddCommand(newIssueCommentCmd())
	issueCmd.AddCommand(newIssueTransitionCmd())
	return issueCmd
}

func newIssueShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <id>",
		Short: "Show an issue's current state",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, err := LoadRegistry()
			if err != nil {
				return err
			}
			resp, dispatchErr := Dispatch(cmd.Context(), reg, "issue", "show", map[string]string{"id": args[0]})
			return reportIssueTargetedOutcome(cmd, resp, dispatchErr, humanizeIssueShow)
		},
	}
}

func newIssueCreateCmd() *cobra.Command {
	var title, priority, issueType string
	var labels []string
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new issue",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, err := LoadRegistry()
			if err != nil {
				return err
			}
			resp, dispatchErr := Dispatch(cmd.Context(), reg, "issue", "create", map[string]any{
				"title":      title,
				"priority":   priority,
				"labels":     labels,
				"issue_type": issueType,
			})
			return reportIssueTargetedOutcome(cmd, resp, dispatchErr, humanizeIssueCreate)
		},
	}
	cmd.Flags().StringVar(&title, "title", "", "issue title (required)")
	cmd.Flags().StringVar(&priority, "priority", "", "issue priority")
	cmd.Flags().StringSliceVar(&labels, "labels", nil, "comma-separated labels")
	cmd.Flags().StringVar(&issueType, "issue-type", "", "issue type")
	_ = cmd.MarkFlagRequired("title")
	return cmd
}

func newIssueCommentCmd() *cobra.Command {
	var body string
	cmd := &cobra.Command{
		Use:   "comment <id>",
		Short: "Add a comment to an issue",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, err := LoadRegistry()
			if err != nil {
				return err
			}
			resp, dispatchErr := Dispatch(cmd.Context(), reg, "issue", "comment", map[string]string{
				"id":   args[0],
				"body": body,
			})
			return reportIssueTargetedOutcome(cmd, resp, dispatchErr, func(json.RawMessage) (string, error) {
				return fmt.Sprintf("Comment added to issue %s", args[0]), nil
			})
		},
	}
	cmd.Flags().StringVar(&body, "body", "", "comment body (required)")
	_ = cmd.MarkFlagRequired("body")
	return cmd
}

func newIssueTransitionCmd() *cobra.Command {
	var state string
	cmd := &cobra.Command{
		Use:   "transition <id>",
		Short: "Transition an issue to a backend-declared target state",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, err := LoadRegistry()
			if err != nil {
				return err
			}
			resp, dispatchErr := Dispatch(cmd.Context(), reg, "issue", "transition", map[string]string{
				"id":           args[0],
				"target_state": state,
			})
			return reportIssueTargetedOutcome(cmd, resp, dispatchErr, func(json.RawMessage) (string, error) {
				return fmt.Sprintf("Issue %s transitioned to %s", args[0], state), nil
			})
		},
	}
	cmd.Flags().StringVar(&state, "state", "", "target state (required); a backend's own capabilities response declares its accepted vocabulary")
	_ = cmd.MarkFlagRequired("state")
	return cmd
}

// reportIssueTargetedOutcome writes resp's outcome to stdout — in the
// default OutputJSON mode, its wire envelope verbatim, matching the wire
// protocol's own "only stdout JSON is the contract" convention; in
// OutputHuman mode, humanize's formatted rendering instead
// [bead pg2-ox1k6] — see output.go's writeTargetedResult, which this
// delegates to. It translates err into pg-connector's own targeted-op
// exit code via outcome.go's TargetedExitCode, never deciding the exit
// code itself. A nil resp is a CLI-level failure before any well-formed
// wire response was produced (e.g. no backend registered, or an ambiguous
// multi-backend registration) — that case is returned as a plain error
// instead, so main's run() reports it on stderr rather than fabricating a
// JSON body.
func reportIssueTargetedOutcome(cmd *cobra.Command, resp *scriptout.Response, err error, humanize humanizeResult) error {
	return writeTargetedResult(cmd, resp, err, humanize)
}

// formatIssue renders issue's identity/state (prefixed to distinguish a
// freshly created issue from one merely shown) as human-readable text.
func formatIssue(prefix string, issue schema.Issue) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s: %q [%s]\n", prefix, issue.ID, issue.Title, issue.State)
	if issue.Priority != "" {
		fmt.Fprintf(&b, "  priority: %s\n", issue.Priority)
	}
	if issue.IssueType != "" {
		fmt.Fprintf(&b, "  type: %s\n", issue.IssueType)
	}
	if issue.URL != "" {
		fmt.Fprintf(&b, "  url: %s\n", issue.URL)
	}
	if len(issue.Labels) > 0 {
		fmt.Fprintf(&b, "  labels: %s\n", strings.Join(issue.Labels, ", "))
	}
	return strings.TrimRight(b.String(), "\n")
}

// humanizeIssueShow formats an `issue show` result (schema.Issue) for
// human display.
func humanizeIssueShow(raw json.RawMessage) (string, error) {
	var issue schema.Issue
	if err := scriptout.Decode(raw, &issue); err != nil {
		return "", err
	}
	return formatIssue("issue", issue), nil
}

// humanizeIssueCreate formats an `issue create` result (schema.Issue,
// the backend-assigned identity/state of the newly created issue) for
// human display.
func humanizeIssueCreate(raw json.RawMessage) (string, error) {
	var issue schema.Issue
	if err := scriptout.Decode(raw, &issue); err != nil {
		return "", err
	}
	return formatIssue("created issue", issue), nil
}
