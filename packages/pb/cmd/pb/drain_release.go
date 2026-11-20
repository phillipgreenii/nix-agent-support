package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/phillipgreenii/pb/internal/drain"
	"github.com/phillipgreenii/pb/internal/run"
	"github.com/spf13/cobra"
)

func newDrainReleaseCmd() *cobra.Command {
	var (
		bead, repo, landed string
		asJSON             bool
	)
	cmd := &cobra.Command{
		Use:   "release",
		Short: "Give the bead's pg-gwm lease back after a landing (no wtdone, no git worktree remove)",
		Long: `Run by the drain finish step after a landing, in place of wtdone, when the
bead's worktree is a pg-gwm lease (see "pb drain isolate").

Both landings run "pg-gwm release --name drain/<bead>" and KEEP the branch
(pg-gwm keeps the released worktree as a reusable slot with the branch still
checked out, so git branch -d would refuse; deleting a merged drain/<bead>
branch later is not a pb step). After a pull-request landing the branch is
pushed and the worktree clean, so releasing loses nothing, and a lease that
outlived the run would hold one of the repo's few worktree slots for the whole
review. A review-fix session runs "pb drain isolate" again to lease the kept
branch.

No pg-gwm lease for the bead is not an error: outcome=none isolation=legacy
(the caller then uses its legacy cleanup). Exit codes: 0 handled; 1 failure
(including a worktree holding unsaved work, which pg-gwm will not release: save
it, or run "pb drain reclaim", which expires the lease); 2 usage error.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !filepath.IsAbs(repo) {
				return usageErrorf("--repo must be an absolute path, got %q", repo)
			}
			if bead == "." || bead == ".." || !beadIDRe.MatchString(bead) {
				return usageErrorf("--bead %q: want a bead id (letters, digits, dot, dash, underscore)", bead)
			}
			if landed != drain.LandedFFMerge && landed != drain.LandedPullRequest {
				return usageErrorf("--landed %q: want %s or %s", landed, drain.LandedFFMerge, drain.LandedPullRequest)
			}
			out, err := drain.Release(context.Background(), run.CLIRunner{},
				drain.ReleaseParams{RepoPath: repo, BeadID: bead, Landing: landed})
			if out.Outcome != "" {
				if asJSON {
					_ = json.NewEncoder(cmd.OutOrStdout()).Encode(out)
				} else {
					fmt.Fprintf(cmd.OutOrStdout(), "outcome=%s branch=%s isolation=%s\n",
						out.Outcome, out.Branch, out.Isolation)
				}
			}
			if errors.Is(err, drain.ErrReleaseDirty) {
				return fmt.Errorf("%w: save the work or run pb drain reclaim", err)
			}
			return err
		},
	}
	cmd.Flags().StringVar(&bead, "bead", "", "bead id (required)")
	cmd.Flags().StringVar(&repo, "repo", "", "absolute path to the canonical clone (required)")
	cmd.Flags().StringVar(&landed, "landed", "", "ff-merge | pull-request (required)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "JSON output")
	_ = cmd.MarkFlagRequired("bead")
	_ = cmd.MarkFlagRequired("repo")
	_ = cmd.MarkFlagRequired("landed")
	return cmd
}
