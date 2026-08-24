package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/marker"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/output"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/reviewinput"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/reviewstage"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/beads"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/vcs"
	"github.com/spf13/cobra"
)

// reviewReadyLister is the minimal bd seam `review ready` needs. Tests inject
// a fake; production uses a beads.Client.
type reviewReadyLister interface {
	ListReadyDraftReviews(ctx context.Context) ([]beads.DraftReviewRef, error)
}

// reviewReadyBeadsClient constructs the beads client used by `review ready`.
// Tests replace this variable to inject a fake reviewReadyLister.
var reviewReadyBeadsClient = func(repo string) reviewReadyLister {
	if repo != "" {
		return beads.NewClientForRepo(repo)
	}
	return beads.NewClient()
}

// reviewFlags holds the parsed CLI flags for the `pg-pr review` subcommands.
type reviewFlags struct {
	repo     string
	fromFile string
	body     string
	json     bool
}

var rvF reviewFlags

// readDraftInput loads a Draft from --from-file or stdin.
//
// Decoding goes through reviewinput.Decode — the single authoritative
// review-input schema — NOT a bare json.Unmarshal into reviewstage.Draft. A bare
// unmarshal silently dropped every key the agent assets actually emit
// (`message`, `lines`, `severity`, `warnings`), so each finding decoded to
// {Path: "…", Line: 0, Body: ""} and the review posted blank (pg2-cns7a).
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
	return reviewinput.Decode(raw)
}

// ----------------------------------------------------------------------
// Cobra wiring
// ----------------------------------------------------------------------

// reviewCmd's Long embeds reviewinput.SchemaDoc: the reviewer agent assets and
// the pr-pool review-role prompt both instruct their agent to "see `pg-pr review
// --help`" for the payload schema, so this text is the schema's only
// user-visible documentation (pg2-cns7a AC4).
var reviewCmd = &cobra.Command{
	Use:   "review",
	Short: "Author and post PR reviews",
	Long: `Author pending PR reviews and post them to the upstream VCS.

Comments and review bodies are automatically tagged with the agent
marker before posting. Existing review comments are deduplicated by
(path, line, body-prefix) to make re-runs idempotent.

` + reviewinput.SchemaDoc,
}

var reviewDraftCmd = &cobra.Command{
	Use:   "draft <pr>",
	Short: "Stage a pending review locally without posting",
	Long: `Read a review JSON payload from stdin (or --from-file) and
persist it under $XDG_STATE_HOME/pg-pr/reviews/<repo-slug>-<pr>.json.

Re-runs replace any existing staged draft for the (repo, pr) pair.

Run 'pg-pr review --help' for the payload schema and an example.`,
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

// draftReviewGate enforces the WIP-scoped draft-review rule (`INV-REVIEW-2`,
// pg2-4dz88.4.6): reviewing a PR that is CURRENTLY draft is allowed only when
// the PR is self-authored AND its store-recorded WIP suppression flag is
// false. A draft PR authored by anyone else is refused regardless of WIP. A
// PR that is not currently draft is never gated by WIP at all.
//
// The draft/author check reads the PROVIDER's live PR state — never an
// assumption derived only from WIP, and never a possibly stale store row —
// so ordinary sync lag, or a human manually re-drafting a PR without
// touching WIP, can never leave a still-draft PR reviewable just because WIP
// happens to read false (the "residual edge" pg2-4dz88.4 calls out).
func draftReviewGate(ctx context.Context, provider vcs.Provider, repo string, num int) error {
	pr, err := provider.GetPR(ctx, repo, num)
	if err != nil {
		return fmt.Errorf("check draft state for %s#%d: %w", repo, num, err)
	}
	if !pr.Draft {
		return nil
	}

	// Fail-closed: an unresolvable self identity (no config, or config load
	// error) is never treated as self-authored, matching
	// internal/sync.Engine.isSelfAuthored's existing "empty self/author means
	// team-mate" precedent.
	cfg, cfgErr := newConfigLoader(ctx)
	selfAuthored := cfgErr == nil && cfg != nil &&
		cfg.SelfLogin != "" && pr.Author != "" && pr.Author == cfg.SelfLogin
	if !selfAuthored {
		return fmt.Errorf(
			"review %s#%d: refused — PR is draft and authored by someone else (INV-REVIEW-2)",
			repo, num,
		)
	}

	wip, err := selfDraftWIP(ctx, repo, num)
	if err != nil {
		return fmt.Errorf("review %s#%d: %w", repo, num, err)
	}
	if wip {
		return fmt.Errorf(
			"review %s#%d: refused — your own PR is draft and marked WIP (INV-REVIEW-2)",
			repo, num,
		)
	}
	return nil
}

// selfDraftWIP reads the store-only WIP flag for (repo, num). Store-optional,
// matching clearWIPOverrideIfSet's contract (pr_write.go): a PR pg-pr has
// never observed — no store file at all, or no row for this identity — reads
// as WIP=false, never an error.
func selfDraftWIP(ctx context.Context, repo string, num int) (bool, error) {
	if _, statErr := os.Stat(store.DefaultPath()); statErr != nil {
		return false, nil
	}
	db, err := store.Open(store.DefaultPath())
	if err != nil {
		return false, fmt.Errorf("open store: %w", err)
	}
	defer func() { _ = db.Close() }()
	pr, err := db.GetPR(ctx, repo, num)
	if err != nil {
		return false, fmt.Errorf("read store: %w", err)
	}
	if pr == nil {
		return false, nil
	}
	return pr.WIP, nil
}

// postStaged loads, dedups, marker-stamps, and POSTs the draft. Shared by
// review post + review submit. It reports skipped=true (without posting) when
// the authenticated viewer already has a PENDING review on the PR, so neither
// path stacks a second PENDING review on a re-run (pg2-3fo3c, pg2-ynhr.18). The
// guard lives here — the single choke-point both commands funnel through — so a
// draft->post->re-draft->post sequence is guarded the same as a submit re-run.
// Callers MUST honor skipped: `review post` suppresses its Clear so the
// freshly-staged draft survives the skip.
func postStaged(ctx context.Context, draft *reviewstage.Draft, w io.Writer, emitJSON bool) (skipped bool, err error) {
	provider := vcsProviderFor(draft.Repo)

	// WIP-scoped draft-review gate (INV-REVIEW-2, pg2-4dz88.4.6). Checked
	// first so a refusal never depends on the shape of the staged content.
	if err := draftReviewGate(ctx, provider, draft.Repo, draft.PR); err != nil {
		return false, err
	}

	// Refuse to post a blank comment. `review submit` came through
	// reviewinput.Decode, which already rejects an empty body; `review post`
	// reads the staged FILE, which may have been written by a pre-pg2-cns7a
	// pg-pr (every comment blank) or hand-edited. Posting it would stamp the
	// marker onto nothing and publish marker-only comments upstream, so fail
	// loudly and let the reviewer re-stage.
	if err := errOnBlankComments(draft); err != nil {
		return false, err
	}

	// Skip if this reviewer already has a PENDING review on the PR, so a re-run
	// (the pr-pool review role may re-review on head advance) does not stack a
	// second PENDING review (pg2-3fo3c). Fail-closed on detection error.
	if skip, err := skipExistingPendingReview(ctx, provider, draft.Repo, draft.PR, w, emitJSON); err != nil || skip {
		return skip, err
	}

	// Dedup against existing PR comments. We don't have an exact "review
	// comments only" reader here — ListComments returns everything. For
	// Phase 2 that's fine since the marker presence on existing comments
	// implies they were posted by us previously.
	existing, _ := provider.ListComments(ctx, draft.Repo, draft.PR)

	unique, dupSkipped := reviewstage.Dedup(draft.Comments, existing)
	for i := range unique {
		unique[i].Body = marker.Stamp(unique[i].Body)
	}
	body := draft.Body
	if body != "" {
		body = marker.Stamp(body)
	}

	// Anchor inline comments to the reviewed commit (draft.HeadSHA) so a PR head
	// that advanced between review and post does not 422 "line must be part of
	// the diff" (pg2-pipw). Empty HeadSHA (a human-authored draft with no sidecar)
	// falls back to GitHub's latest-commit anchoring, unchanged.
	rev, err := provider.PostReview(ctx, draft.Repo, draft.PR, draft.HeadSHA, body, unique)
	if err != nil {
		return false, fmt.Errorf("post review: %w", err)
	}

	if emitJSON {
		return false, writeJSON(w, map[string]any{
			"status":             "posted",
			"comments_posted":    len(unique),
			"duplicates_skipped": dupSkipped,
			"review_id":          rev.ID,
			"review_state":       rev.State,
		})
	}
	_, err = fmt.Fprintf(w,
		"ok Posted review for PR #%d: %d comment(s) (%d skipped as duplicates); review state=%s\n",
		draft.PR, len(unique), dupSkipped, rev.State)
	return false, err
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
		skipped, err := postStaged(ctx, draft, cmd.OutOrStdout(), output.Resolve(rvF.json))
		if err != nil {
			return err
		}
		if skipped {
			// A PENDING review already exists; postStaged did not post. Suppress
			// Clear so the freshly-staged draft is preserved rather than silently
			// discarded (pg2-ynhr.18).
			return nil
		}
		return reviewstage.Clear(reviewstage.DefaultDir(), repo, num)
	},
}

var reviewSubmitCmd = &cobra.Command{
	Use:   "submit <pr>",
	Short: "Submit a review in one step (no staging)",
	Long: `Read a review JSON payload from stdin (or --from-file),
deduplicate against existing PR comments, apply the agent marker, and
post immediately. No state is persisted.

Run 'pg-pr review --help' for the payload schema and an example.`,
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
		// postStaged owns the skip-if-pending guard (pg2-3fo3c) at the shared
		// choke-point, so submit no longer probes separately (avoids a double
		// HasPendingReviewByViewer round-trip). Skip vs post is immaterial here:
		// submit persists nothing, so there is no staged draft to preserve.
		_, err = postStaged(ctx, draft, cmd.OutOrStdout(), output.Resolve(rvF.json))
		return err
	},
}

// pendingReviewChecker is the optional provider capability for detecting an
// existing PENDING review authored by the authenticated viewer. It mirrors the
// optional capability-interface pattern in pkg/provider/vcs (AuthChecker,
// FingerprintProvider): a provider that cannot see pending reviews (e.g. an
// exec: plugin) simply does not implement it, and the guard is skipped.
type pendingReviewChecker interface {
	HasPendingReviewByViewer(ctx context.Context, repo string, number int) (bool, error)
}

// skipExistingPendingReview reports whether a review submit should be skipped
// because the authenticated viewer already has a PENDING review on the PR — the
// idempotency guard that stops a re-run from stacking a second PENDING review
// (pg2-3fo3c). Detection failure is fatal (fail closed) so we never post over a
// review we merely could not see, matching the daemon team sink
// (internal/reviewsink.ApplyPendingReview). The provider is probed for the
// capability; providers without it proceed unguarded.
func skipExistingPendingReview(ctx context.Context, provider any, repo string, num int, w io.Writer, emitJSON bool) (bool, error) {
	pc, ok := provider.(pendingReviewChecker)
	if !ok {
		return false, nil
	}
	hasPending, err := pc.HasPendingReviewByViewer(ctx, repo, num)
	if err != nil {
		return false, fmt.Errorf("detect pending review %s#%d: %w", repo, num, err)
	}
	if !hasPending {
		return false, nil
	}
	if emitJSON {
		return true, writeJSON(w, map[string]any{
			"status": "skipped",
			"reason": "pending_review_exists",
		})
	}
	_, err = fmt.Fprintf(w, "ok Skipped review for PR #%d: a PENDING review by this reviewer already exists\n", num)
	return true, err
}

// readyDraftReviewJSON is the machine-readable shape of one ready
// draft-review bead emitted by `pg-pr review ready --json`.
type readyDraftReviewJSON struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Repo   string `json:"repo"`
	Number int    `json:"number"`
	Mine   bool   `json:"mine"`
}

var reviewReadyCmd = &cobra.Command{
	Use:   "ready",
	Short: "List ready draft-review beads (bd ready, filtered)",
	Long: `List the draft-review work items that are ready to be produced.

Wraps 'bd ready', filters to draft-review beads, and resolves each to its
target PR (<owner/repo>#<number>) plus the mine/team ownership flag. This is
the same detection the daemon's review-consumption hook uses.`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		ctx := cmd.Context()
		repoPath := resolveRepoPath(ctx, rvF.repo)
		client := reviewReadyBeadsClient(repoPath)
		refs, err := client.ListReadyDraftReviews(ctx)
		if err != nil {
			return fmt.Errorf("review ready: %w", err)
		}
		// Always emit a non-nil slice so empty renders as [] not null.
		out := make([]readyDraftReviewJSON, 0, len(refs))
		for _, r := range refs {
			out = append(out, readyDraftReviewJSON{
				ID:     r.ID,
				Title:  fmt.Sprintf("draft-review: %s#%d", r.Repo, r.Number),
				Repo:   r.Repo,
				Number: r.Number,
				Mine:   r.Mine,
			})
		}
		if output.Resolve(rvF.json) {
			return writeJSON(cmd.OutOrStdout(), out)
		}
		if len(out) == 0 {
			_, err := fmt.Fprintln(cmd.OutOrStdout(), "no ready draft-review beads")
			return err
		}
		for _, r := range out {
			owner := "team"
			if r.Mine {
				owner = "mine"
			}
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s#%d\t%s\n", r.ID, r.Repo, r.Number, owner); err != nil {
				return err
			}
		}
		return nil
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
		body = marker.Stamp(body)
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

// errOnBlankComments reports the indexes of draft comments carrying no text.
// A blank comment is never legitimate: it posts as a bare attribution marker.
func errOnBlankComments(draft *reviewstage.Draft) error {
	var blank []int
	for i, c := range draft.Comments {
		if strings.TrimSpace(marker.Strip(c.Body)) == "" {
			blank = append(blank, i)
		}
	}
	if len(blank) == 0 {
		return nil
	}
	return fmt.Errorf("staged draft for %s PR #%d has %d comment(s) with no text (comments%v); "+
		"re-stage with 'pg-pr review draft' — see 'pg-pr review --help' for the payload schema",
		draft.Repo, draft.PR, len(blank), blank)
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
	for _, c := range []*cobra.Command{reviewDraftCmd, reviewPostCmd, reviewSubmitCmd, reviewReadyCmd, commentAddCmd, commentResolveCmd} {
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

	reviewCmd.AddCommand(reviewDraftCmd, reviewPostCmd, reviewSubmitCmd, reviewReadyCmd)
	commentCmd.AddCommand(commentAddCmd, commentResolveCmd)
	rootCmd.AddCommand(reviewCmd, commentCmd)
}
