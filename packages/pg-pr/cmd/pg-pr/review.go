package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/marker"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/output"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/reviewstage"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/beads"
	"github.com/spf13/cobra"
)

// beadsClientForComment is overridable so tests can swap an in-memory client.
var beadsClientForComment = func() beadsFeedbackClient {
	return beads.NewClient()
}

// beadsFeedbackClient narrows beads.Client to the methods comment respond
// needs.
type beadsFeedbackClient interface {
	GetFeedback(ctx context.Context, id string) (*beads.Feedback, error)
	FindMergeRequestForFeedback(ctx context.Context, feedbackID string) (*beads.MergeRequest, error)
}

// reviewFlags holds the parsed CLI flags for the `pg-pr review` subcommands.
type reviewFlags struct {
	repo     string
	fromFile string
	body     string
	json     bool
}

var rvF reviewFlags

// readDraftInput loads a Draft body from --from-file or stdin.
func readDraftInput(cmd *cobra.Command, fromFile string) (*reviewstage.Draft, error) {
	var raw []byte
	var err error
	if fromFile != "" {
		raw, err = os.ReadFile(fromFile)
		if err != nil {
			return nil, fmt.Errorf("read --from-file: %w", err)
		}
	} else {
		stdin := cmd.InOrStdin()
		raw, err = io.ReadAll(stdin)
		if err != nil {
			return nil, fmt.Errorf("read stdin: %w", err)
		}
	}
	if len(raw) == 0 {
		return nil, errors.New("no review payload provided on stdin or --from-file")
	}
	var d reviewstage.Draft
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, fmt.Errorf("parse review JSON: %w", err)
	}
	return &d, nil
}

// ----------------------------------------------------------------------
// Cobra wiring
// ----------------------------------------------------------------------

var reviewCmd = &cobra.Command{
	Use:   "review",
	Short: "Author and post PR reviews",
	Long: `Author pending PR reviews and post them to the upstream VCS.

Comments and review bodies are automatically tagged with the agent
marker before posting. Existing review comments are deduplicated by
(path, line, body-prefix) to make re-runs idempotent.`,
}

var reviewDraftCmd = &cobra.Command{
	Use:   "draft <pr>",
	Short: "Stage a pending review locally without posting",
	Long: `Read a review JSON payload from stdin (or --from-file) and
persist it under $XDG_STATE_HOME/pg-pr/reviews/<repo-slug>-<pr>.json.

Re-runs replace any existing staged draft for the (repo, pr) pair.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		num, err := parsePR(args[0])
		if err != nil {
			return err
		}
		ctx := cmd.Context()
		repo, err := resolveRepo(ctx, rvF.repo)
		if err != nil {
			return err
		}
		draft, err := readDraftInput(cmd, rvF.fromFile)
		if err != nil {
			return err
		}
		draft.Repo = repo
		draft.PR = num

		path, err := reviewstage.Save(reviewstage.DefaultDir(), draft)
		if err != nil {
			return fmt.Errorf("save draft: %w", err)
		}
		if output.Resolve(rvF.json) {
			return writeJSON(cmd.OutOrStdout(), map[string]any{
				"status":   "staged",
				"path":     path,
				"comments": len(draft.Comments),
			})
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(),
			"ok Staged review for PR #%d (%d comment(s)) at %s\n",
			num, len(draft.Comments), path)
		return err
	},
}

// postStaged loads, dedups, marker-stamps, and POSTs the draft. Shared by
// review post + review submit.
func postStaged(ctx context.Context, draft *reviewstage.Draft, w io.Writer, emitJSON bool) error {
	provider := vcsProviderFor(draft.Repo)

	// Dedup against existing PR comments. We don't have an exact "review
	// comments only" reader here — ListComments returns everything. For
	// Phase 2 that's fine since the marker presence on existing comments
	// implies they were posted by us previously.
	existing, _ := provider.ListComments(ctx, draft.Repo, draft.PR)

	unique, skipped := reviewstage.Dedup(draft.Comments, existing)
	for i := range unique {
		unique[i].Body = marker.Markerify(unique[i].Body)
	}
	body := draft.Body
	if body != "" {
		body = marker.Markerify(body)
	}

	rev, err := provider.PostReview(ctx, draft.Repo, draft.PR, body, unique)
	if err != nil {
		return fmt.Errorf("post review: %w", err)
	}

	if emitJSON {
		return writeJSON(w, map[string]any{
			"status":             "posted",
			"comments_posted":    len(unique),
			"duplicates_skipped": skipped,
			"review_id":          rev.ID,
			"review_state":       rev.State,
		})
	}
	_, err = fmt.Fprintf(w,
		"ok Posted review for PR #%d: %d comment(s) (%d skipped as duplicates); review state=%s\n",
		draft.PR, len(unique), skipped, rev.State)
	return err
}

var reviewPostCmd = &cobra.Command{
	Use:   "post <pr>",
	Short: "Post a previously staged review",
	Long: `Load the staged draft for (repo, pr), deduplicate against
existing PR comments, apply the agent marker, post via the upstream
VCS, and clear the staged file on success.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		num, err := parsePR(args[0])
		if err != nil {
			return err
		}
		ctx := cmd.Context()
		repo, err := resolveRepo(ctx, rvF.repo)
		if err != nil {
			return err
		}
		draft, err := reviewstage.Load(reviewstage.DefaultDir(), repo, num)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("no staged draft for %s PR #%d; run 'pg-pr review draft' first", repo, num)
			}
			return err
		}
		if err := postStaged(ctx, draft, cmd.OutOrStdout(), output.Resolve(rvF.json)); err != nil {
			return err
		}
		return reviewstage.Clear(reviewstage.DefaultDir(), repo, num)
	},
}

var reviewSubmitCmd = &cobra.Command{
	Use:   "submit <pr>",
	Short: "Submit a review in one step (no staging)",
	Long: `Read a review JSON payload from stdin (or --from-file),
deduplicate against existing PR comments, apply the agent marker, and
post immediately. No state is persisted.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		num, err := parsePR(args[0])
		if err != nil {
			return err
		}
		ctx := cmd.Context()
		repo, err := resolveRepo(ctx, rvF.repo)
		if err != nil {
			return err
		}
		draft, err := readDraftInput(cmd, rvF.fromFile)
		if err != nil {
			return err
		}
		draft.Repo = repo
		draft.PR = num
		return postStaged(ctx, draft, cmd.OutOrStdout(), output.Resolve(rvF.json))
	},
}

// ----------------------------------------------------------------------
// Comment subcommands
// ----------------------------------------------------------------------

var commentCmd = &cobra.Command{
	Use:   "comment",
	Short: "Manage PR comments (add, respond, resolve)",
}

var commentAddCmd = &cobra.Command{
	Use:   "add <pr>",
	Short: "Post a top-level PR comment",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		num, err := parsePR(args[0])
		if err != nil {
			return err
		}
		ctx := cmd.Context()
		repo, err := resolveRepo(ctx, rvF.repo)
		if err != nil {
			return err
		}
		body, err := loadCommentBody(cmd, rvF.body, rvF.fromFile)
		if err != nil {
			return err
		}
		body = marker.Markerify(body)
		c, err := vcsProviderFor(repo).AddComment(ctx, repo, num, body)
		if err != nil {
			return err
		}
		if output.Resolve(rvF.json) {
			return writeJSON(cmd.OutOrStdout(), c)
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "ok Posted comment %s on PR #%d\n", c.ID, num)
		return err
	},
}

var commentRespondCmd = &cobra.Command{
	Use:   "respond <feedback-id>",
	Short: "Reply to a review thread by feedback bead id",
	Long: `Resolve a feedback bead id to (repo, upstream-thread-id) by reading
the feedback bead's metadata and walking parent-child deps up to the
merge-request bead, then reply on the upstream VCS via ReplyToThread.

Supports kind=comment-thread and kind=review-thread. Other kinds
(ci-failure, review-request, jira-link) cannot be responded to and
return an error.

The reply body comes from --body, --body-file, or stdin (exactly one).`,
	Args: cobra.ExactArgs(1),
	RunE: runCommentRespond,
}

func runCommentRespond(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	feedbackID := args[0]
	body, err := loadCommentBody(cmd, rvF.body, rvF.fromFile)
	if err != nil {
		return err
	}

	bdc := beadsClientForComment()
	if bdc == nil {
		return errors.New("comment respond: beads client not available")
	}
	fb, err := bdc.GetFeedback(ctx, feedbackID)
	if err != nil {
		return fmt.Errorf("comment respond: lookup feedback %s: %w", feedbackID, err)
	}
	if fb == nil {
		return fmt.Errorf("comment respond: feedback bead %s not found", feedbackID)
	}

	kind := fb.Fields.Kind
	switch kind {
	case string(beads.FeedbackKindCommentThread), string(beads.FeedbackKindReviewThread):
		// proceed
	case "":
		return fmt.Errorf("comment respond: feedback bead %s has no kind metadata", feedbackID)
	default:
		return fmt.Errorf("comment respond: cannot respond to %s feedback", kind)
	}

	externalID := fb.Fields.ExternalID
	if externalID == "" {
		return fmt.Errorf("comment respond: feedback bead %s missing external_id metadata", feedbackID)
	}

	mr, err := bdc.FindMergeRequestForFeedback(ctx, feedbackID)
	if err != nil {
		return fmt.Errorf("comment respond: resolve merge-request for feedback %s: %w", feedbackID, err)
	}
	if mr == nil {
		return fmt.Errorf("comment respond: no merge-request bead found for feedback %s", feedbackID)
	}
	repo := mr.Fields.Repo
	if repo == "" {
		return fmt.Errorf("comment respond: merge-request bead %s missing repo metadata", mr.ID)
	}

	body = marker.Markerify(body)
	c, err := vcsProviderFor(repo).ReplyToThread(ctx, repo, externalID, body)
	if err != nil {
		return fmt.Errorf("comment respond: reply to thread %s: %w", externalID, err)
	}
	if output.Resolve(rvF.json) {
		return writeJSON(cmd.OutOrStdout(), c)
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "ok Replied to %s thread %s on PR %s#%d\n",
		kind, externalID, repo, mr.Fields.PRNumber)
	return err
}

var commentResolveCmd = &cobra.Command{
	Use:   "resolve <thread-id>",
	Short: "Mark a review thread as resolved upstream",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		repo, err := resolveRepo(ctx, rvF.repo)
		if err != nil {
			return err
		}
		err = vcsProviderFor(repo).ResolveThread(ctx, repo, args[0])
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "ok Resolved thread %s\n", args[0])
		return err
	},
}

// loadCommentBody resolves the comment body from the most-specific source.
// Priority: --body > --from-file > stdin.
func loadCommentBody(cmd *cobra.Command, bodyFlag, fromFile string) (string, error) {
	if bodyFlag != "" {
		return bodyFlag, nil
	}
	var raw []byte
	var err error
	if fromFile != "" {
		raw, err = os.ReadFile(fromFile)
	} else {
		raw, err = io.ReadAll(cmd.InOrStdin())
	}
	if err != nil {
		return "", err
	}
	body := string(raw)
	if body == "" {
		return "", errors.New("no body provided (use --body, --from-file, or pipe via stdin)")
	}
	return body, nil
}

// _ guards against unused import when api is only used via reviewstage.
var _ = api.Comment{}

func init() {
	for _, c := range []*cobra.Command{reviewDraftCmd, reviewPostCmd, reviewSubmitCmd, commentAddCmd, commentRespondCmd, commentResolveCmd} {
		c.Flags().StringVar(&rvF.repo, "repo", "",
			"Repository in owner/name form (defaults to auto-detected remote)")
		c.Flags().BoolVar(&rvF.json, "json", false,
			"Emit machine-readable JSON instead of human-readable output")
	}
	for _, c := range []*cobra.Command{reviewDraftCmd, reviewSubmitCmd} {
		c.Flags().StringVar(&rvF.fromFile, "from-file", "",
			"Read the review payload from this file instead of stdin")
	}
	commentAddCmd.Flags().StringVar(&rvF.body, "body", "", "Comment body (alternative to stdin)")
	commentAddCmd.Flags().StringVar(&rvF.fromFile, "body-file", "", "Read the comment body from this file")
	commentRespondCmd.Flags().StringVar(&rvF.body, "body", "", "Reply body (alternative to stdin)")
	commentRespondCmd.Flags().StringVar(&rvF.fromFile, "body-file", "", "Read the reply body from this file")

	reviewCmd.AddCommand(reviewDraftCmd, reviewPostCmd, reviewSubmitCmd)
	commentCmd.AddCommand(commentAddCmd, commentRespondCmd, commentResolveCmd)
	rootCmd.AddCommand(reviewCmd, commentCmd)
}
