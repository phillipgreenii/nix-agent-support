package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/output"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/prview"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	"github.com/spf13/cobra"
)

// prViewNow is the clock the view's freshness verdict is judged against;
// overridable in tests, mirroring cmd/pg-pr/pr_list.go's prListNow.
var prViewNow = func() time.Time { return time.Now().UTC() }

// prViewCmd is the consolidated single-PR view (pg2-4dz88.5), superseding the
// former `pr show` / `pr info` (alias `pr-info`) pair. Per the operator
// ruling on pg2-4dz88.5.2 (naming/deprecation), the surviving name is `view`
// and the retired names are removed outright — no alias, no deprecation
// warning — so invoking `pr show`/`pr info`/`pr-info` now falls through to
// cobra's own default unknown-command error, which is the ruled disposition,
// not an oversight.
var prViewCmd = &cobra.Command{
	Use:   "view <pr>",
	Short: "Show the consolidated view of a PR: identity, ownership, enrichment, CI, merge state, feedback, revisions, links",
	Long: `Show everything pg-pr knows about one PR: identity and state, ownership,
enrichment (kind/size/languages/urgency), CI rollup, merge/conflict state,
feedback, revision history, linked tickets and bead links — carrying an
as-of time and a staleness verdict for the whole view.

Reads from the local store by default and makes no network call (matching
'pr list'); a missing store is not an error, it just means less is known
yet about this PR.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		num, err := parsePR(args[0])
		if err != nil {
			return err
		}
		ctx := cmd.Context()
		repo, err := resolveRepo(ctx, prF.repo)
		if err != nil {
			return err
		}
		v, err := loadPRView(ctx, repo, num)
		if err != nil {
			return err
		}
		if output.Resolve(prF.jsonOutput) {
			b, err := prview.MarshalView(v)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s\n", b)
			return err
		}
		return prview.RenderHuman(cmd.OutOrStdout(), v)
	},
}

// loadPRView assembles the consolidated view for (repo, num) from
// already-persisted facts only — no network call — per internal/prview's
// store-read-default ruling (pg2-4dz88.5.3, INV-READ-1). It stat-guards
// store.DefaultPath() exactly like listOpenPRItems/appendEnrichment did, so a
// machine with no store file yet never has one created as a side effect: the
// view still renders, from the (repo, number) identity alone.
func loadPRView(ctx context.Context, repo string, num int) (prview.View, error) {
	in := prview.PRViewInput{
		PR:  api.PR{Repo: repo, Number: num},
		Now: prViewNow(),
	}
	if _, statErr := os.Stat(store.DefaultPath()); statErr != nil {
		return prview.Assemble(in), nil
	}
	db, err := store.Open(store.DefaultPath())
	if err != nil {
		return prview.View{}, err
	}
	defer func() { _ = db.Close() }()

	row, err := db.GetPR(ctx, repo, num)
	if err != nil {
		return prview.View{}, err
	}
	if row == nil {
		return prview.Assemble(in), nil
	}
	in.PR = storeRowToAPIPR(*row)
	in.Store = row

	revs, err := db.ListRevisions(ctx, row.ID)
	if err != nil {
		return prview.View{}, err
	}
	in.Revisions = revs

	fb, err := db.ListFeedback(ctx, row.ID, store.ListFilter{})
	if err != nil {
		return prview.View{}, err
	}
	in.Feedback = fb

	return prview.Assemble(in), nil
}

// storeRowToAPIPR maps the store-authoritative PR row onto the
// live-provider-shaped api.PR type that PRViewInput.PR carries, feeding
// Assemble's Identity/MergeState axes. The store row does not persist every
// field api.PR can carry (title, draft, additions/deletions, mergeable,
// merge_state_status, …) — those axes render through their own unknown
// marker rather than a network round-trip, matching this view's
// store-read-default posture (pg2-4dz88.5.3); a future bead may widen the
// store row to carry more of them.
func storeRowToAPIPR(row store.PullRequest) api.PR {
	return api.PR{
		Repo:    row.Repo,
		Number:  row.Number,
		State:   row.State,
		Branch:  row.Branch,
		Base:    row.Base,
		Author:  row.Author,
		URL:     row.URL,
		HeadSHA: row.HeadSHA,
	}
}

func init() {
	prViewCmd.Flags().BoolVar(&prF.jsonOutput, "json", false,
		"Emit machine-readable JSON instead of human-readable output")
	prViewCmd.Flags().StringVar(&prF.repo, "repo", "",
		"Repository in owner/name form (defaults to auto-detected remote)")
	prCmd.AddCommand(prViewCmd)
}
