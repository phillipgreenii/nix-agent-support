package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/event"
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
yet about this PR.

With --force-reload, a live SyncPR refresh runs first (fetching from the
provider and updating the store) and the view is then assembled from the
post-refresh state.`,
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
		if prF.forceReload {
			if err := forceReloadPR(ctx, repo, num); err != nil {
				return err
			}
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
// store row to carry more of them. Body IS persisted (pg2-1o1dp, schema
// v15) — the PR's own description was previously dropped entirely by the
// `pr show`/`pr info` -> `pr view` consolidation, a real regression, so it
// was added to this column set rather than left alongside the
// still-unpersisted fields above.
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
		Body:    row.Body,
	}
}

// forceReloadPR runs a one-shot Engine.SyncPR refresh for (repo, num) before
// pr view's store read, wiring the same config/engine/store/dispatch
// construction the `pg-pr sync --pr` one-shot path uses (syncCmd.RunE in
// cmd/pg-pr/sync.go) — reusing newSyncEngineForCLI verbatim rather than a
// second engine-construction path — so this flag inherits SyncPR's
// beadsbridge dispatch and, with it, the cross-process per-PR lock the
// beadsbridge handler applies (pg2-4dz88.6.3), for free, with no new locking
// code here.
//
// event.Dispatcher.Dispatch and store.DB.RunOutbox both intentionally
// discard a handler's returned error (they only log it — see sync.go's
// flushOutbox and its callers' comments), so a lock give-up
// (prlock.ErrTimeout) would otherwise vanish instead of surfacing through
// `pr view`'s own RunE return. It is captured at the handler itself and
// relayed here, mirroring syncCmd.RunE's own lastDispatchErr wrapper for its
// non-daemon --pr path.
func forceReloadPR(ctx context.Context, repo string, num int) error {
	cfg, err := loadConfigForCLI(ctx)
	if err != nil {
		return err
	}
	engine, err := newSyncEngineForCLI(cfg)
	if err != nil {
		return err
	}

	// Unlike this default (no-flag) view path — which stat-guards
	// store.DefaultPath() so a machine with no store yet is never mutated —
	// --force-reload is itself a write path, so it is responsible for its
	// own state directory exactly like the "MUST succeed standalone, no
	// daemon required" contract demands: store.Open (unlike sync.go's own
	// syncCmd.RunE, which relies on the directory already existing from a
	// prior run) does not create missing parent directories, so a genuinely
	// first-ever invocation on a machine with no ~/.local/state/pg-pr yet
	// would otherwise fail here.
	if err := os.MkdirAll(filepath.Dir(engine.StoreFile()), 0o755); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	eventStore, err := store.Open(engine.StoreFile())
	if err != nil {
		return fmt.Errorf("open event store: %w", err)
	}
	defer func() { _ = eventStore.Close() }()

	disp := event.New()
	var lastDispatchErr error
	bridgeHandler := newBeadsBridgeHandler(engine.Config)
	disp.Register(func(ctx context.Context, e store.Event) error {
		err := bridgeHandler(ctx, e)
		if err != nil {
			lastDispatchErr = err
		}
		return err
	})
	engine.SetStoreAndDispatch(eventStore, disp.Dispatch)

	_, err = engine.SyncPR(ctx, repo, num)
	if err == nil && lastDispatchErr != nil {
		err = lastDispatchErr
	}
	return err
}

func init() {
	prViewCmd.Flags().BoolVar(&prF.jsonOutput, "json", false,
		"Emit machine-readable JSON instead of human-readable output")
	prViewCmd.Flags().StringVar(&prF.repo, "repo", "",
		"Repository in owner/name form (defaults to auto-detected remote)")
	prViewCmd.Flags().BoolVar(&prF.forceReload, "force-reload", false,
		"Refresh this PR from the provider (SyncPR) before rendering, so the view reflects post-refresh state")
	prCmd.AddCommand(prViewCmd)
}
