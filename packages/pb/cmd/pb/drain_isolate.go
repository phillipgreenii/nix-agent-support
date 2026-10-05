package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/phillipgreenii/pb/internal/drain"
	"github.com/phillipgreenii/pb/internal/run"
	"github.com/spf13/cobra"
)

// beadIDRe: the id lands in a filesystem path and a branch ref. Dots are legal
// (live ids like pg2-4dz88.2.3); separators are not.
var beadIDRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

func newDrainIsolateCmd() *cobra.Command {
	var (
		bead, repo string
		asJSON     bool
		gitTimeout time.Duration
	)
	cmd := &cobra.Command{
		Use:   "isolate",
		Short: "Create or reuse the bead's worktree (.worktrees/<bead> on drain/<bead>); report the hook-bundle state",
		Long: `Idempotent isolation for one bead: reuses an existing worktree or parked
branch, otherwise branches off the repo's primary branch.

It then reports the clone's hook-bundle state in the precommit field
(bundle|stale|missing|broken, the PRECOMMIT vocabulary of
integrate-branch-support --facts, from "pg-hooks status --porcelain") and
writes NO file into the worktree: git runs the bundle's hooks from the shared
common dir. An absent pg-hooks or an unrecognized state reports missing.

Exit codes: 0 isolated (created or reused); 1 generic failure; 3 conflicting
isolation state (the worktree path holds another branch, or drain/<bead> is
checked out elsewhere) — never forced; route the bead to STUCK.

Hang bound: every git call runs with core.fsmonitor forced off for that call only
(per-call GIT_CONFIG_COUNT environment; no git config is ever changed — fsmonitor
stays on in the repos), and each call is killed (whole process group) if it
exceeds --git-timeout (default 5m0s). On a timeout pb removes ONLY the worktree
(and the branch, when it created that too) that this call created — any
pre-existing isolation is left alone — prints an error naming fsmonitor/fseventsd
contention, and exits 1.

Read-only canonical-clone diagnosis: if the canonical .git/config carries a
stray core.worktree (or git's toplevel disagrees with --repo), a
"pb: warning: core.worktree set in canonical config ..." line is printed to
stderr (and a "warning" field added to --json). Isolation still succeeds
(exit 0); the key is never cleared by pb (R-3) — it is the operator's to unset.
Without the warning, git's lie surfaces only later as a phantom dirty tree
halting the land at FF-0a.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !filepath.IsAbs(repo) {
				return fmt.Errorf("--repo must be an absolute path, got %q", repo)
			}
			if bead == "." || bead == ".." || !beadIDRe.MatchString(bead) {
				return fmt.Errorf("--bead %q: want a bead id (letters, digits, dot, dash, underscore)", bead)
			}
			if gitTimeout <= 0 {
				return fmt.Errorf("--git-timeout must be positive, got %s", gitTimeout)
			}
			out, err := drain.Isolate(context.Background(), run.CLIRunner{},
				drain.Params{RepoPath: repo, BeadID: bead, GitTimeout: gitTimeout})
			if err != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), "pb:", err)
				if errors.Is(err, drain.ErrConflict) {
					os.Exit(3)
				}
				os.Exit(1)
			}
			if out.Warning != "" {
				// Exit stays 0 (the bead IS isolated) but the diagnosis is loud on
				// stderr, before the land step would trip over it (pg2-4c4nv).
				fmt.Fprintln(cmd.ErrOrStderr(), "pb: warning:", out.Warning)
			}
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(out)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "worktree=%s branch=%s reused=%s precommit=%s\n",
				out.Worktree, out.Branch, out.Reused, out.Precommit)
			return nil
		},
	}
	cmd.Flags().StringVar(&bead, "bead", "", "bead id (required)")
	cmd.Flags().StringVar(&repo, "repo", "", "absolute path to the canonical clone (required)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "JSON output")
	cmd.Flags().DurationVar(&gitTimeout, "git-timeout", drain.DefaultGitTimeout,
		"upper bound for EACH git call; on expiry its process group is killed and only what this call created is cleaned up")
	_ = cmd.MarkFlagRequired("bead")
	_ = cmd.MarkFlagRequired("repo")
	return cmd
}
