// pr_review.go: the "pg-connector pr review" verb group. submit puts content
// into the acting identity's PENDING (unsubmitted) review through the targeted
// review_submit wire op, creating the review when there is none and appending
// to it otherwise (contract 9.1 of the entity-change-flow design); pending
// reads the acting identity's pending review back as a structured record
// through the targeted review_pending wire op (contract 9.1a).
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

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
// and --backend pinning apply). The 9.1 request body is JSON, read from stdin
// or, with --from-file, from the named file (the two are mutually exclusive);
// the exit code follows INV-EXIT-1's Targeted scheme (0/4/1) via
// writeTargetedResult. Every status the op reports (posted, append, no_change)
// is a well-formed result and so exits 0: a caller MUST read the output's
// status, never infer what was written from the exit code. A run in which some
// comments did not land is an error and exits 1.
func newPrReviewSubmitCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use: "submit <id>",
		Short: "Add content to the acting identity's PENDING review, creating it when there is none " +
			"(JSON request on stdin or --from-file: head_sha, body, comments); the output's status says what happened",
		Long: "Add content to the acting identity's PENDING review on a PR. A pending review that already exists " +
			"is appended to and one is created when there is none; nothing is ever deleted, replaced or submitted.\n\n" +
			"The request is a JSON object {head_sha, body, comments} read from stdin, or from the file given " +
			"with --from-file (an absolute path; the file is opened by this command, so it may live outside the " +
			"working directory). Giving both, or a missing or unreadable file, is an invalid_argument error. " +
			"A comment is a new point {path, line, side?, body} or a reply {thread_id, body}.\n\n" +
			"The output's status is posted (a review was created), append (content was added to an existing " +
			"review) or no_change (nothing needed writing); all three exit 0.",
		Args: cobra.ExactArgs(1),
	}
	backendFlag := addBackendFlag(cmd, "pin to exactly this backend, skipping the multi-instance try-each resolution policy")
	fromFile := cmd.Flags().String("from-file", "", "read the request JSON from this absolute file path instead of stdin")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		wireArgs, err := readReviewSubmitArgs(cmd.InOrStdin(), args[0], *fromFile)
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

// stdinInputWait bounds how long --from-file waits to learn whether stdin also
// carries a request: stdin that is idle (no data and not closed) is treated as
// not given, so a caller that leaves a pipe open cannot hang the command.
const stdinInputWait = 250 * time.Millisecond

// stdinHasInput reports whether in carries any non-whitespace bytes. A
// terminal or /dev/null never does. It reads at most one chunk and never
// blocks longer than stdinInputWait.
func stdinHasInput(in io.Reader) bool {
	if f, ok := in.(*os.File); ok {
		if info, err := f.Stat(); err != nil || info.Mode()&os.ModeCharDevice != 0 {
			return false
		}
	}
	got := make(chan bool, 1)
	go func() {
		buf := make([]byte, 4096)
		n, _ := in.Read(buf)
		got <- strings.TrimSpace(string(buf[:n])) != ""
	}()
	select {
	case v := <-got:
		return v
	case <-time.After(stdinInputWait):
		return false
	}
}

// readReviewSubmitArgs builds the op args {"id": id, ...request JSON fields}.
// The request is read from fromFile when it is set, else from in. With
// fromFile set, stdin input too is an error (the sources are mutually
// exclusive), as is a path that is not absolute, missing or unreadable. The
// positional id always wins over an "id" field in the request object.
func readReviewSubmitArgs(in io.Reader, id, fromFile string) (map[string]any, error) {
	var raw []byte
	var err error
	source := "stdin"
	if fromFile != "" {
		if !filepath.IsAbs(fromFile) {
			return nil, fmt.Errorf("--from-file must be an absolute path, got %q", fromFile)
		}
		if stdinHasInput(in) {
			return nil, fmt.Errorf("--from-file and stdin input are mutually exclusive; give the request one way")
		}
		source = "--from-file " + fromFile
		if raw, err = os.ReadFile(fromFile); err != nil {
			return nil, fmt.Errorf("read request from --from-file: %w", err)
		}
	} else if raw, err = io.ReadAll(in); err != nil {
		return nil, fmt.Errorf("read request from stdin: %w", err)
	}
	fields := map[string]any{}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, fmt.Errorf("%s must be a JSON object: %w", source, err)
	}
	fields["id"] = id
	return fields, nil
}

// humanizeReviewSubmit formats a review_submit result for human display. The
// status is authoritative.
func humanizeReviewSubmit(raw json.RawMessage) (string, error) {
	var r pr.ReviewSubmitResult
	if err := scriptout.Decode(raw, &r); err != nil {
		return "", err
	}
	var s string
	switch {
	case r.State == pr.StateNone || r.ReviewID == "":
		s = fmt.Sprintf("%s: no pending review at %s and none created (as of %s)", r.Status, r.HeadSHA, r.AsOf)
	default:
		s = fmt.Sprintf("%s: review %s [%s] at %s (as of %s)", r.Status, r.ReviewID, r.State, r.HeadSHA, r.AsOf)
	}
	s += fmt.Sprintf("\n  comments: %d added, %d already present, %d dismissed  body: %s  extra pending reviews: %d",
		r.Added, r.AlreadyPresent, r.Dismissed, r.Body, r.ExtraPendingReviews)
	if r.URL != "" {
		s += "\n  review: " + r.URL
	}
	if la := r.LastAppend; la != nil {
		s += fmt.Sprintf("\n  last append: %s (+%d at %s)", la.At, la.Added, la.Head)
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
	s := fmt.Sprintf("pending review %s (database id %d) at %s, head %s [%s], as of %s\n  comments: %d total, %d at head  reviewed head: %t  extra pending reviews: %d",
		rv.ReviewID, rv.DatabaseID, rv.CommitSHA, r.HeadSHA, staleness, r.AsOf, rv.CommentsTotal, rv.CommentsAtHead, rv.ReviewedHead, rv.ExtraPendingReviews)
	if la := rv.LastAppend; la != nil {
		s += fmt.Sprintf("  last append: %s (+%d at %s)", la.At, la.Added, la.Head)
	}
	for _, c := range rv.Comments {
		s += fmt.Sprintf("\n    - %s:%d at %s", c.Path, c.Line, c.OriginalCommit)
	}
	return s, nil
}
