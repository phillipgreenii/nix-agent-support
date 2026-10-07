package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
)

// exitHeadMoved is `pg-desk pr head-check`'s exit code for "the PR's head is
// no longer the head you asked about". It is distinct from 0 (still current)
// and 1 (the lookup itself failed) so a caller can branch on it; 4 mirrors
// pg-connector's targeted "well-formed negative answer" code.
const exitHeadMoved = 4

// headCheckHead reads the PR's head commit as of now; a test seam. Production
// goes through gather's single pg-connector exec chokepoint.
var headCheckHead = func(ctx context.Context, cfg *config.Config, entityID string) (string, error) {
	return gather.NewGatherer(cfg, nil).CurrentHeadSHA(ctx, entityID)
}

func init() {
	typeGroup(entityTypePR).AddCommand(newHeadCheckCmd())
}

// newHeadCheckCmd builds `pg-desk pr head-check <id> --head-sha <sha>`
// (bead pg2-a9yhn): a read-only, store-free check that a PR's head is still
// the commit a long-running review started on. A review that discovers late
// that its head moved is refused at submit and everything it spent is lost
// (the 72h trigger trace found one 16 minute review wasted that way), so the
// review prompt calls this at its mid-point, and again before it starts
// posting, and stops early on exit 4.
func newHeadCheckCmd() *cobra.Command {
	var headSHA string
	c := &cobra.Command{
		Use:   "head-check <id> --head-sha <sha>",
		Short: "Check that a PR's head is still the commit a review started on",
		Long: `Read the PR's head commit now (pg-connector pr show --fresh) and compare it
with --head-sha, the commit the review was started on. Nothing is stored and
no entity is hydrated.

Exit codes: 0 the head is unchanged, the review may continue; 4 the head
moved, the review is stale and SHOULD stop now (the new head is printed);
1 the lookup failed (nothing is known about the head; the caller decides
whether to continue); 2 and 3 are not used.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runHeadCheck(cmd, args[0], headSHA)
		},
	}
	c.Flags().StringVar(&headSHA, "head-sha", "", "The head commit the review started on (required)")
	_ = c.MarkFlagRequired("head-sha")
	return c
}

func runHeadCheck(cmd *cobra.Command, ref, want string) error {
	want = strings.TrimSpace(want)
	if want == "" {
		return fmt.Errorf("head-check: --head-sha must not be empty")
	}
	cfg, err := deskConfigLoad(cmd.Context())
	if err != nil {
		return fmt.Errorf("head-check: load config: %w", err)
	}
	_, id, err := resolvePRRef(cfg, ref)
	if err != nil {
		return fmt.Errorf("head-check: %w", err)
	}
	cmd.SilenceUsage = true

	got, err := headCheckHead(cmd.Context(), cfg, id)
	if err != nil {
		if errors.Is(err, gather.ErrPRNotFound) {
			return fmt.Errorf("head-check: %s: not found", id)
		}
		return fmt.Errorf("head-check: %s: %w", id, err)
	}
	if !sameCommit(want, got) {
		return newExitError(exitHeadMoved, fmt.Errorf("head moved: %s was reviewed at %s but the PR head is now %s; the review is stale", id, want, got))
	}
	_, perr := fmt.Fprintf(cmd.OutOrStdout(), "head unchanged: %s is still at %s\n", id, got)
	return perr
}

// sameCommit reports whether two commit ids name the same commit: equal, or
// one a prefix of the other (a caller may hold the abbreviated form). Both
// must be at least 7 characters for a prefix match, so an empty or tiny
// string never matches everything.
func sameCommit(a, b string) bool {
	a, b = strings.ToLower(a), strings.ToLower(b)
	if a == b {
		return true
	}
	const minPrefix = 7
	if len(a) < minPrefix || len(b) < minPrefix {
		return false
	}
	return strings.HasPrefix(a, b) || strings.HasPrefix(b, a)
}
