package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/phillipgreenii/pb/internal/drain"
	"github.com/phillipgreenii/pb/internal/run"
	"github.com/spf13/cobra"
)

// exitNoteFailed: the worktree WAS removed but the bead note could not be written.
const exitNoteFailed = 4

func newDrainReclaimCmd() *cobra.Command {
	var (
		bead, repo, branch, actor string
		asJSON                    bool
		gitTimeout                time.Duration
	)
	cmd := &cobra.Command{
		Use:   "reclaim",
		Short: "Remove the bead's isolation worktree when it holds nothing (clean tree, zero commits) and note the bead",
		Long: `Run by a drain session that is ending a bead WITHOUT landing it (park, defer,
dependency wiring, release), BEFORE the claim is released.

The bead's worktree (the one that has --branch checked out, default
drain/<bead>) is reclaimed only when BOTH hold at the instant they are read:
"git status --porcelain" in it is empty, and the branch has zero commits the
primary branch lacks ("git rev-list --count <primary>..<branch>" is 0). It is
then removed with wtdone (never forced; wtdone also deletes the zero-commit
branch with a plain "git branch -d"), and a clean-up note is added to the bead
with "bd comment --actor <actor>". A dirty worktree, a branch with commits, or
no worktree at all is left exactly as found and the bead gets NO note.

Exit codes: 0 handled (removed, or left alone: see the outcome field); 1 failure,
worktree left in place and NO note written (for example wtdone refused because a
process is anchored in the worktree); 4 the worktree WAS removed but the bead
note could not be written (post the note field's text yourself).

Output: outcome=<removed|none|kept-dirty|kept-commits> worktree=<abs> branch=<b>
[reason=...]. A failure to reclaim never blocks the park: leave the worktree in
place and carry on.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !filepath.IsAbs(repo) {
				return fmt.Errorf("--repo must be an absolute path, got %q", repo)
			}
			if bead == "." || bead == ".." || !beadIDRe.MatchString(bead) {
				return fmt.Errorf("--bead %q: want a bead id (letters, digits, dot, dash, underscore)", bead)
			}
			if strings.TrimSpace(actor) == "" {
				return fmt.Errorf("--actor must not be empty")
			}
			if gitTimeout <= 0 {
				return fmt.Errorf("--git-timeout must be positive, got %s", gitTimeout)
			}
			out, err := drain.Reclaim(context.Background(), run.CLIRunner{},
				drain.ReclaimParams{RepoPath: repo, BeadID: bead, Branch: branch, Actor: actor, GitTimeout: gitTimeout})
			if out.Outcome != "" {
				if asJSON {
					_ = json.NewEncoder(cmd.OutOrStdout()).Encode(out)
				} else {
					fmt.Fprintf(cmd.OutOrStdout(), "outcome=%s worktree=%s branch=%s", out.Outcome, out.Worktree, out.Branch)
					if out.Reason != "" {
						fmt.Fprintf(cmd.OutOrStdout(), " reason=%q", out.Reason)
					}
					fmt.Fprintln(cmd.OutOrStdout())
				}
			}
			if errors.Is(err, drain.ErrNoteFailed) {
				return newExitError(exitNoteFailed, fmt.Errorf("%w; post this note yourself: %s", err, out.Note))
			}
			if err != nil {
				return err
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&bead, "bead", "", "bead id (required)")
	cmd.Flags().StringVar(&repo, "repo", "", "absolute path to the canonical clone (required)")
	cmd.Flags().StringVar(&branch, "branch", "", "work branch (default drain/<bead>)")
	cmd.Flags().StringVar(&actor, "actor", "", "actor id for the bd comment (required)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "JSON output")
	cmd.Flags().DurationVar(&gitTimeout, "git-timeout", drain.DefaultGitTimeout,
		"upper bound for EACH git call; on expiry its process group is killed")
	_ = cmd.MarkFlagRequired("bead")
	_ = cmd.MarkFlagRequired("repo")
	_ = cmd.MarkFlagRequired("actor")
	return cmd
}
