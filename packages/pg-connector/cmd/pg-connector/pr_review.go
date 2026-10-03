// pr_review.go: the "pg-connector pr review" verb group. submit posts a
// PENDING (unsubmitted) review through the targeted review_submit wire op
// (contract 9.1 of the entity-change-flow design); pending reads the acting
// identity's pending review back as a structured record through the targeted
// review_pending wire op (contract 9.1a).
package main

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/pr"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
	"github.com/spf13/cobra"
)

func newPrReviewCmd() *cobra.Command {
	reviewCmd := &cobra.Command{
		Use:   "review",
		Short: "PR review commands",
	}
	reviewCmd.AddCommand(newPrReviewSubmitCmd())
	reviewCmd.AddCommand(newPrReviewPendingCmd())
	return reviewCmd
}

// newPrReviewSubmitCmd is "pr review submit <id>": an id-keyed targeted
// write (DispatchTargeted, never Dispatch, so INV-REG-2's try-each policy
// and --backend pinning apply). The 9.1 request body is read from stdin;
// the exit code follows INV-EXIT-1's Targeted scheme (0/4/1) via
// writeTargetedResult. Every status the op reports, blocked_human_pending
// included, is a well-formed result and so exits 0: a caller MUST read the
// output's status, never infer a posted review from the exit code.
func newPrReviewSubmitCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "submit <id>",
		Short: "Post a PENDING review (JSON request on stdin: head_sha, body, comments, supersede_pending); the output's status says what happened",
		Args:  cobra.ExactArgs(1),
	}
	backendFlag := addBackendFlag(cmd, "pin to exactly this backend, skipping the multi-instance try-each resolution policy")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		wireArgs, err := readReviewSubmitArgs(cmd.InOrStdin(), args[0])
		if err != nil {
			return reportPrTargetedOutcome(cmd, nil, scriptout.WrapError(scriptout.ErrInvalidArgument, err.Error()), humanizeReviewSubmit)
		}
		reg, err := LoadRegistry()
		if err != nil {
			return reportPrTargetedOutcome(cmd, nil, err, humanizeReviewSubmit)
		}
		resp, dispatchErr := DispatchTargeted(cmd.Context(), reg, "pr", "review_submit", wireArgs, *backendFlag)
		return reportPrTargetedOutcome(cmd, resp, dispatchErr, humanizeReviewSubmit)
	}
	return cmd
}

// readReviewSubmitArgs builds the op args {"id": id, ...stdin JSON fields}.
// The positional id always wins over an "id" field in the stdin object.
func readReviewSubmitArgs(in io.Reader, id string) (map[string]any, error) {
	raw, err := io.ReadAll(in)
	if err != nil {
		return nil, fmt.Errorf("read request from stdin: %w", err)
	}
	fields := map[string]any{}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, fmt.Errorf("stdin must be a JSON object: %w", err)
	}
	fields["id"] = id
	return fields, nil
}

// humanizeReviewSubmit formats a review_submit result for human display. The
// status is authoritative: a blocked_human_pending result posted nothing, so
// it is never shown as a review.
func humanizeReviewSubmit(raw json.RawMessage) (string, error) {
	var r pr.ReviewSubmitResult
	if err := scriptout.Decode(raw, &r); err != nil {
		return "", err
	}
	var s string
	switch r.Status {
	case pr.StatusBlockedHumanPending:
		s = fmt.Sprintf("BLOCKED (%s): nothing was posted at %s (as of %s)\n  %s", r.Reason, r.HeadSHA, r.AsOf, r.Message)
		if r.PendingReview != nil && r.PendingReview.URL != "" {
			s += "\n  review: " + r.PendingReview.URL
		}
		return s, nil
	case pr.StatusSkipped:
		s = fmt.Sprintf("skipped (%s): pending review %s already at %s (as of %s)", r.Reason, r.ReviewID, r.HeadSHA, r.AsOf)
	case pr.StatusReplaced:
		s = fmt.Sprintf("replaced: review %s [%s] at %s (as of %s)", r.ReviewID, r.State, r.HeadSHA, r.AsOf)
		if r.Superseded != nil {
			s += fmt.Sprintf("\n  superseded: %s (archived at %s)", r.Superseded.ReviewID, r.Superseded.ArchivePath)
		}
	default:
		s = fmt.Sprintf("review %s [%s] at %s (as of %s)", r.ReviewID, r.State, r.HeadSHA, r.AsOf)
		if r.Status != "" {
			s = r.Status + ": " + s
		}
	}
	return s, nil
}

// newPrReviewPendingCmd is "pr review pending <id>": an id-keyed targeted
// READ (DispatchTargeted, so INV-REG-2's try-each policy and --backend pinning
// apply). It takes no stdin. The default (JSON) output is the wire envelope:
// a result that is either the structured record or the explicit none result
// ("pending": false), or an error. Exit codes follow INV-EXIT-1's Targeted
// scheme (0/4/1) via writeTargetedResult: "no pending review" exits 0 (a
// well-formed answer), a failed lookup exits non-zero with an error.
func newPrReviewPendingCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pending <id>",
		Short: "Show the acting identity's PENDING review on a PR as a structured record (or an explicit none result)",
		Args:  cobra.ExactArgs(1),
	}
	backendFlag := addBackendFlag(cmd, "pin to exactly this backend, skipping the multi-instance try-each resolution policy")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		reg, err := LoadRegistry()
		if err != nil {
			return reportPrTargetedOutcome(cmd, nil, err, humanizeReviewPending)
		}
		resp, dispatchErr := DispatchTargeted(cmd.Context(), reg, "pr", "review_pending", map[string]any{"id": args[0]}, *backendFlag)
		return reportPrTargetedOutcome(cmd, resp, dispatchErr, humanizeReviewPending)
	}
	return cmd
}

// humanizeReviewPending formats a review_pending result for human display.
func humanizeReviewPending(raw json.RawMessage) (string, error) {
	var r pr.PendingReviewResult
	if err := scriptout.Decode(raw, &r); err != nil {
		return "", err
	}
	if !r.Pending || r.Review == nil {
		return fmt.Sprintf("no pending review (head %s, as of %s)", r.HeadSHA, r.AsOf), nil
	}
	rv := r.Review
	staleness := "current"
	if rv.Stale {
		staleness = "STALE"
	}
	s := fmt.Sprintf("pending review %s (database id %d) at %s, head %s [%s], as of %s\n  body marked: %t  all marked: %t  digest: %s  comments: %d",
		rv.ReviewID, rv.DatabaseID, rv.CommitSHA, r.HeadSHA, staleness, r.AsOf, rv.BodyMarked, rv.AllMarked, rv.DigestState, len(rv.Comments))
	for _, c := range rv.Comments {
		s += fmt.Sprintf("\n    - %s:%d marked=%t", c.Path, c.Line, c.Marked)
	}
	return s, nil
}
