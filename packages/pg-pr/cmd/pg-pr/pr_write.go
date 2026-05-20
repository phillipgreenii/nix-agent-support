package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/branch"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/output"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/beads"
	"github.com/spf13/cobra"
)

// prWriteFlags holds the parsed CLI flags for the `pg-pr pr` write
// subcommands (create / update / close / ready / draft / merge / automerge).
//
// All write subcommands share these flags. They live in their own struct so
// resetting between tests doesn't clobber the read-side prF flags.
type prWriteFlags struct {
	jsonOutput bool
	repo       string

	// create-only.
	title     string
	head      string
	base      string
	reviewers string
	labels    string
	noDraft   bool

	// body sources (shared by create + update).
	body      string
	bodyFile  string
	bodyStdin bool
}

var prWF prWriteFlags

// beadsClientForPR is a var so tests can swap in an in-memory client.
var beadsClientForPR = func() beadsMergeRequestClient {
	return beads.NewClient()
}

// beadsMergeRequestClient narrows the beads.Client API to the methods used
// here; tests can satisfy it with an in-memory fake.
type beadsMergeRequestClient interface {
	EnsureMergeRequest(ctx context.Context, userTitle string, fields beads.MergeRequestFields) (string, bool, error)
	CloseMergeRequest(ctx context.Context, id, reason string) error
	FindByRepoAndNumber(ctx context.Context, repo string, prNumber int) (*beads.MergeRequest, error)
}

// resolveBody picks the PR body from at most one of --body, --body-file,
// --body-stdin. Returns an error if more than one is provided. An empty
// return string is allowed (caller decides if that's fatal).
func resolveBody(cmd *cobra.Command, body, bodyFile string, bodyStdin bool) (string, error) {
	count := 0
	if body != "" {
		count++
	}
	if bodyFile != "" {
		count++
	}
	if bodyStdin {
		count++
	}
	if count > 1 {
		return "", errors.New("specify at most one of --body, --body-file, --body-stdin")
	}
	if body != "" {
		return body, nil
	}
	if bodyFile != "" {
		if bodyFile == "-" {
			raw, err := io.ReadAll(cmd.InOrStdin())
			if err != nil {
				return "", fmt.Errorf("read stdin: %w", err)
			}
			return string(raw), nil
		}
		raw, err := os.ReadFile(bodyFile)
		if err != nil {
			return "", fmt.Errorf("read --body-file: %w", err)
		}
		return string(raw), nil
	}
	if bodyStdin {
		raw, err := io.ReadAll(cmd.InOrStdin())
		if err != nil {
			return "", fmt.Errorf("read stdin: %w", err)
		}
		return string(raw), nil
	}
	return "", nil
}

// detectCurrentBranch returns the current branch name when cwd is inside a
// git repository. Used by `pr create` when --head isn't passed.
func detectCurrentBranch(ctx context.Context) (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	info, err := branch.Detect(ctx, cwd, branch.Options{})
	if err != nil {
		return "", err
	}
	return info.Branch, nil
}

// splitCSV is a small helper for --reviewers / --labels.
func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if v := strings.TrimSpace(p); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// ----------------------------------------------------------------------
// pr create
// ----------------------------------------------------------------------

var prCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Open a new pull request (defaults to draft)",
	Long: `Open a new pull request via the configured VCS provider.

The PR is created in DRAFT state by default. Pass --no-draft to open a
ready-for-review PR directly. The PR body may be supplied via --body,
--body-file <path> (use - for stdin), or --body-stdin.

On success, a corresponding merge-request bead is created via
beads.EnsureMergeRequest so subsequent pg-pr sync runs treat the PR as
known.

NOTE: --reviewers and --labels are accepted but only used to populate
the merge-request bead metadata in this phase; upstream PR-level
reviewer-assignment and label-application land in a later phase.`,
	Args: cobra.NoArgs,
	RunE: runPRCreate,
}

func runPRCreate(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	if strings.TrimSpace(prWF.title) == "" {
		return errors.New("pr create: --title is required")
	}
	body, err := resolveBody(cmd, prWF.body, prWF.bodyFile, prWF.bodyStdin)
	if err != nil {
		return err
	}

	repo, err := resolveRepo(ctx, prWF.repo)
	if err != nil {
		return err
	}

	headBranch := prWF.head
	if headBranch == "" {
		headBranch, err = detectCurrentBranch(ctx)
		if err != nil {
			return fmt.Errorf("auto-detect head branch: %w; pass --head <branch>", err)
		}
	}
	base := prWF.base
	if base == "" {
		base = "origin/main"
	}
	// gh expects a bare branch name for --base; trim the origin/ prefix.
	base = strings.TrimPrefix(base, "origin/")

	draft := !prWF.noDraft
	provider := vcsProviderFor(repo)
	pr, err := provider.CreatePR(ctx, repo, draft, prWF.title, body, headBranch, base)
	if err != nil {
		return err
	}

	// Best-effort: record the merge-request bead. Failure here doesn't
	// fail the command since the PR is already created upstream.
	beadID := ""
	bdc := beadsClientForPR()
	if bdc != nil {
		id, _, berr := bdc.EnsureMergeRequest(ctx, prWF.title, beads.MergeRequestFields{
			Repo:     repo,
			PRNumber: pr.Number,
			State:    "open",
			Branch:   pr.Branch,
			Base:     pr.Base,
			Author:   pr.Author,
			URL:      pr.URL,
			Draft:    draft,
		})
		if berr == nil {
			beadID = id
		} else {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "WARNING: failed to record merge-request bead: %v\n", berr)
		}
	}

	if output.Resolve(prWF.jsonOutput) {
		return writeJSON(cmd.OutOrStdout(), map[string]any{
			"pr":      pr,
			"bead_id": beadID,
		})
	}
	state := "draft"
	if !draft {
		state = "ready"
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(),
		"ok Opened PR #%d (%s) on %s: %s\n", pr.Number, state, pr.Repo, pr.URL)
	return err
}

// ----------------------------------------------------------------------
// pr update
// ----------------------------------------------------------------------

var prUpdateCmd = &cobra.Command{
	Use:   "update <pr>",
	Short: "Update an existing PR's body",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		num, err := parsePR(args[0])
		if err != nil {
			return err
		}
		ctx := cmd.Context()
		body, err := resolveBody(cmd, prWF.body, prWF.bodyFile, prWF.bodyStdin)
		if err != nil {
			return err
		}
		if body == "" {
			_, _ = fmt.Fprintln(cmd.OutOrStdout(),
				"pr update: no body source provided (--body / --body-file / --body-stdin); nothing to do")
			return nil
		}
		repo, err := resolveRepo(ctx, prWF.repo)
		if err != nil {
			return err
		}
		if err := vcsProviderFor(repo).UpdatePR(ctx, repo, num, body); err != nil {
			return err
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "ok Updated PR #%d on %s\n", num, repo)
		return err
	},
}

// ----------------------------------------------------------------------
// pr close
// ----------------------------------------------------------------------

var prCloseCmd = &cobra.Command{
	Use:   "close <pr>",
	Short: "Close a PR without merging",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		num, err := parsePR(args[0])
		if err != nil {
			return err
		}
		ctx := cmd.Context()
		repo, err := resolveRepo(ctx, prWF.repo)
		if err != nil {
			return err
		}
		if err := vcsProviderFor(repo).Close(ctx, repo, num); err != nil {
			return err
		}

		// Best-effort: close the corresponding merge-request bead. The
		// cascade rule on close also closes children (processing-cycle /
		// feedback / action) under that bead. If no bead is found, we
		// silently skip — the next sync will reconcile.
		beadID := ""
		bdc := beadsClientForPR()
		if bdc != nil {
			mr, ferr := bdc.FindByRepoAndNumber(ctx, repo, num)
			if ferr != nil {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(),
					"WARNING: failed to look up merge-request bead for %s#%d: %v\n",
					repo, num, ferr)
			} else if mr != nil && mr.Status != "closed" {
				if cerr := bdc.CloseMergeRequest(ctx, mr.ID,
					"closed via pg-pr pr close"); cerr != nil {
					_, _ = fmt.Fprintf(cmd.ErrOrStderr(),
						"WARNING: failed to close merge-request bead %s: %v\n",
						mr.ID, cerr)
				} else {
					beadID = mr.ID
				}
			}
		}

		_, err = fmt.Fprintf(cmd.OutOrStdout(), "ok Closed PR #%d on %s\n", num, repo)
		if err != nil {
			return err
		}
		if beadID != "" {
			_, err = fmt.Fprintf(cmd.OutOrStdout(),
				"ok Closed merge-request bead %s\n", beadID)
		}
		return err
	},
}

// ----------------------------------------------------------------------
// pr ready / pr draft
// ----------------------------------------------------------------------

var prReadyCmd = &cobra.Command{
	Use:   "ready <pr>",
	Short: "Mark a draft PR as ready for review",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		num, err := parsePR(args[0])
		if err != nil {
			return err
		}
		ctx := cmd.Context()
		repo, err := resolveRepo(ctx, prWF.repo)
		if err != nil {
			return err
		}
		if err := vcsProviderFor(repo).SetDraft(ctx, repo, num, false); err != nil {
			return err
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(),
			"ok PR #%d marked ready for review.\n", num)
		return err
	},
}

var prDraftCmd = &cobra.Command{
	Use:   "draft <pr>",
	Short: "Convert a PR back to draft state",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		num, err := parsePR(args[0])
		if err != nil {
			return err
		}
		ctx := cmd.Context()
		repo, err := resolveRepo(ctx, prWF.repo)
		if err != nil {
			return err
		}
		if err := vcsProviderFor(repo).SetDraft(ctx, repo, num, true); err != nil {
			return err
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(),
			"ok PR #%d converted to draft.\n", num)
		return err
	},
}

// ----------------------------------------------------------------------
// pr automerge {on, off}
// ----------------------------------------------------------------------

// humanOnlyWarning is printed to stderr before any human-only mutation
// (automerge on/off, merge). It is constant text so callers (humans + tests)
// can assert on it.
const humanOnlyWarning = "WARNING: automerge is a human-only verb. Agents must not invoke this.\n"
const humanOnlyMergeWarning = "WARNING: merge is a human-only verb. Agents must not invoke this.\n"

var prAutomergeCmd = &cobra.Command{
	Use:   "automerge",
	Short: "Enable or disable PR automerge (HUMAN-ONLY)",
	Long: `Enable or disable automerge on a PR. This is a human-only verb;
agents are forbidden from invoking it. Each subcommand prints a prominent
warning to stderr.`,
}

var prAutomergeOnCmd = &cobra.Command{
	Use:   "on <pr>",
	Short: "Enable automerge for a PR",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		_, _ = fmt.Fprint(cmd.ErrOrStderr(), humanOnlyWarning)
		num, err := parsePR(args[0])
		if err != nil {
			return err
		}
		ctx := cmd.Context()
		repo, err := resolveRepo(ctx, prWF.repo)
		if err != nil {
			return err
		}
		if err := vcsProviderFor(repo).SetAutomerge(ctx, repo, num, true); err != nil {
			return err
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(),
			"ok Automerge enabled for PR #%d.\n", num)
		return err
	},
}

var prAutomergeOffCmd = &cobra.Command{
	Use:   "off <pr>",
	Short: "Disable automerge for a PR",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		_, _ = fmt.Fprint(cmd.ErrOrStderr(), humanOnlyWarning)
		num, err := parsePR(args[0])
		if err != nil {
			return err
		}
		ctx := cmd.Context()
		repo, err := resolveRepo(ctx, prWF.repo)
		if err != nil {
			return err
		}
		if err := vcsProviderFor(repo).SetAutomerge(ctx, repo, num, false); err != nil {
			return err
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(),
			"ok Automerge disabled for PR #%d.\n", num)
		return err
	},
}

// ----------------------------------------------------------------------
// pr merge
// ----------------------------------------------------------------------

var prMergeCmd = &cobra.Command{
	Use:   "merge <pr>",
	Short: "Merge a PR immediately (HUMAN-ONLY)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		_, _ = fmt.Fprint(cmd.ErrOrStderr(), humanOnlyMergeWarning)
		num, err := parsePR(args[0])
		if err != nil {
			return err
		}
		ctx := cmd.Context()
		repo, err := resolveRepo(ctx, prWF.repo)
		if err != nil {
			return err
		}
		if err := vcsProviderFor(repo).Merge(ctx, repo, num); err != nil {
			return err
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(),
			"ok Merged PR #%d on %s\n", num, repo)
		return err
	},
}

// ----------------------------------------------------------------------
// Cobra wiring
// ----------------------------------------------------------------------

// resetPRWriteFlags clears mutable state between cobra tests; flag values
// persist across rootCmd.Execute() calls.
func resetPRWriteFlags() {
	prWF = prWriteFlags{}
}

// addRepoFlag attaches the --repo flag to a command.
func addRepoFlag(c *cobra.Command) {
	c.Flags().StringVar(&prWF.repo, "repo", "",
		"Repository in owner/name form (defaults to auto-detected remote)")
}

// addJSONFlag attaches the --json flag to a command.
func addJSONFlag(c *cobra.Command) {
	c.Flags().BoolVar(&prWF.jsonOutput, "json", false,
		"Emit machine-readable JSON instead of human-readable output")
}

// addBodyFlags attaches body-source flags to a command.
func addBodyFlags(c *cobra.Command) {
	c.Flags().StringVar(&prWF.body, "body", "",
		"PR body (literal string)")
	c.Flags().StringVar(&prWF.bodyFile, "body-file", "",
		"Read PR body from file (use - for stdin)")
	c.Flags().BoolVar(&prWF.bodyStdin, "body-stdin", false,
		"Read PR body from stdin until EOF")
}

func init() {
	// create
	prCreateCmd.Flags().StringVar(&prWF.title, "title", "", "PR title (required)")
	prCreateCmd.Flags().StringVar(&prWF.head, "head", "", "Head branch (defaults to current branch)")
	prCreateCmd.Flags().StringVar(&prWF.base, "base", "origin/main", "Base branch")
	prCreateCmd.Flags().StringVar(&prWF.reviewers, "reviewers", "", "Comma-separated list of reviewers (metadata-only this phase)")
	prCreateCmd.Flags().StringVar(&prWF.labels, "labels", "", "Comma-separated list of labels (metadata-only this phase)")
	prCreateCmd.Flags().BoolVar(&prWF.noDraft, "no-draft", false, "Open the PR ready-for-review instead of as a draft")
	addRepoFlag(prCreateCmd)
	addBodyFlags(prCreateCmd)
	addJSONFlag(prCreateCmd)

	// update
	addRepoFlag(prUpdateCmd)
	addBodyFlags(prUpdateCmd)
	addJSONFlag(prUpdateCmd)

	// close / ready / draft / merge / automerge children
	for _, c := range []*cobra.Command{prCloseCmd, prReadyCmd, prDraftCmd, prMergeCmd, prAutomergeOnCmd, prAutomergeOffCmd} {
		addRepoFlag(c)
	}

	prAutomergeCmd.AddCommand(prAutomergeOnCmd, prAutomergeOffCmd)
	prCmd.AddCommand(prCreateCmd, prUpdateCmd, prCloseCmd, prReadyCmd, prDraftCmd, prAutomergeCmd, prMergeCmd)
}

// avoid unused-warning if splitCSV is ever inlined out.
var _ = splitCSV
