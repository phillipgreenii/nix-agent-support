// pr_review.go: the "pg-connector pr review" verb group. submit posts a
// PENDING (unsubmitted) review through the targeted review_submit wire op
// (contract 9.1 of the entity-change-flow design).
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
	return reviewCmd
}

// newPrReviewSubmitCmd is "pr review submit <id>": an id-keyed targeted
// write (DispatchTargeted, never Dispatch, so INV-REG-2's try-each policy
// and --backend pinning apply). The 9.1 request body is read from stdin;
// the exit code follows INV-EXIT-1's Targeted scheme (0/4/1) via
// writeTargetedResult, so a posted review whose supersede delete failed
// still exits 0 (the failure is reported in the output's supersede field).
func newPrReviewSubmitCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "submit <id>",
		Short: "Post a PENDING review (JSON request on stdin: head_sha, body, comments, supersede_pending)",
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

// humanizeReviewSubmit formats a review_submit result for human display.
func humanizeReviewSubmit(raw json.RawMessage) (string, error) {
	var r pr.ReviewSubmitResult
	if err := scriptout.Decode(raw, &r); err != nil {
		return "", err
	}
	s := fmt.Sprintf("review %s [%s] at %s (as of %s)", r.ReviewID, r.State, r.HeadSHA, r.AsOf)
	if r.Supersede != nil {
		switch {
		case r.Supersede.Deleted:
			s += "\n  supersede: previous pending review deleted"
		case r.Supersede.Error != "":
			s += "\n  supersede: delete failed: " + r.Supersede.Error
		default:
			s += "\n  supersede: nothing to delete"
		}
	}
	return s, nil
}
