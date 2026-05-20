package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/marker"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/reviewstage"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	"github.com/spf13/cobra"
)

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
		if rvF.json {
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
		if err := postStaged(ctx, draft, cmd.OutOrStdout(), rvF.json); err != nil {
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
		return postStaged(ctx, draft, cmd.OutOrStdout(), rvF.json)
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
		if rvF.json {
			return writeJSON(cmd.OutOrStdout(), c)
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "ok Posted comment %s on PR #%d\n", c.ID, num)
		return err
	},
}

var commentRespondCmd = &cobra.Command{
	Use:   "respond <feedback-id>",
	Short: "Reply to a review thread by feedback bead id",
	Long: `Resolve a feedback bead id to (repo, thread-id) and reply on
the upstream VCS. This subcommand depends on feedback beads carrying
the upstream thread id, which lands in Phase 3 (epic beads_pg2-ywy).`,
	Args: cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		_ = args
		return errors.New("pg-pr comment respond: not implemented in Phase 2; feedback beads' upstream thread ids land in Phase 3 (epic beads_pg2-ywy)")
	},
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

	reviewCmd.AddCommand(reviewDraftCmd, reviewPostCmd, reviewSubmitCmd)
	commentCmd.AddCommand(commentAddCmd, commentRespondCmd, commentResolveCmd)
	rootCmd.AddCommand(reviewCmd, commentCmd)
}
