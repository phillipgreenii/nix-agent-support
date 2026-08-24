package main

import (
	"context"
	"io"
	"os"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/agentregistry"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/freshness"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/output"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/vcs"
	"github.com/spf13/cobra"
)

// prListItem is the JSON shape emitted per open PR by `pg-pr pr list --json`.
// It is the read seam the pr-pool ACL consumes: the base fields (repo, number,
// head_sha, ownership, draft, state, branch) come from the store; reviewer
// roster + labels are added best-effort from a live provider round-trip.
//
// Freshness is PER ITEM, not payload-level, deliberately: the top-level JSON is a
// bare array and wrapping it in an envelope would break every existing consumer,
// whereas the store already records a PER-ROW as-of time (last_synced_at), which
// is strictly more informative than one payload-wide stamp — a repo mid-refresh
// can hold rows of different ages. Both halves of the freshness contract
// (pr-pool INV-FRESH-1) therefore ride on each item: LastSyncedAt is the as-of
// time, Stale is the verdict against pg-pr's own bound.
type prListItem struct {
	Repo      string `json:"repo"`
	Number    int    `json:"number"`
	State     string `json:"state"`
	Ownership string `json:"ownership"`
	Draft     bool   `json:"draft"`
	Branch    string `json:"branch"`
	HeadSHA   string `json:"head_sha"`
	// LastSyncedAt is this row's AS-OF time: the store's own
	// pull_request.last_synced_at column, emitted VERBATIM (RFC3339 UTC, written
	// by the sync engine on every observation). It answers "how old are these
	// facts?" — which the base listing otherwise could not, since it makes no
	// network call and the row may predate the current tick by any amount.
	LastSyncedAt string `json:"last_synced_at"`
	// Stale is the freshness FLAG: LastSyncedAt has aged past
	// freshness.BoundSeconds, so these facts MUST NOT be treated as current. A
	// consumer that acts on the seam (the pr-pool ACL) reads THIS rather than
	// re-deriving the bound, keeping the staleness POLICY owned by pg-pr, which
	// owns the sync cadence it is measured against.
	Stale     bool             `json:"stale"`
	Labels    []string         `json:"labels"`
	Reviewers []prListReviewer `json:"reviewers"`
	// Hidden / Reason mirror the store's USER_HIDDEN flag verbatim
	// (pull_request.user_hidden / user_hidden_reason, pg2-4dz88.4.2/.4.3).
	// This machine seam NEVER filters on the flag -- unlike the human-facing
	// surfaces (`pg-pr open`, the dashboard), which exclude a hidden PR by
	// default -- per the fork #1 operator ruling (2026-08-24): `pr list
	// --json` is the read seam pr-pool's ACL consumes (ADR 0034), so it
	// reports facts and leaves the judgement to that consumer.
	Hidden bool   `json:"hidden"`
	Reason string `json:"reason,omitempty"`
}

// prListNow is the clock the freshness stamp is computed against; overridable in
// tests. Mirrors the store's own nowRFC3339 clock seam.
var prListNow = func() time.Time { return time.Now().UTC() }

// prListReviewer is one classified reviewer in a prListItem's roster.
type prListReviewer struct {
	Login string `json:"login"`
	State string `json:"state"`
	Kind  string `json:"kind"` // "agent" | "person"
}

var prListCmd = &cobra.Command{
	Use:   "list",
	Short: "List open PRs as the data seam consumed by pr-pool",
	Long: `List the open/draft pull requests for a repo.

Base fields (repo, number, head_sha, ownership, draft, state, branch) are read
from the local store — this is the cheap default the pr-pool ACL polls, and it
makes no network calls.

Because those facts come from the store rather than the network, each PR also
carries its own freshness: last_synced_at (the store's as-of time for that row,
RFC3339 UTC) and stale (true once that as-of time has aged past twice pg-pr's
default sync interval). A consumer MUST NOT act on a PR whose stale flag is set —
the sync daemon is behind or stopped, so the row's state, head_sha and ownership
may no longer be true. A row with no usable as-of time is reported stale.

With --reviewers, each PR is additionally augmented with its labels and a
classified reviewer roster from a live provider round-trip. When the provider
supports bulk enrichment this is one round-trip for the whole repo; otherwise it
falls back to one round-trip per PR. That augmentation is best-effort: a provider
failure leaves labels/roster empty rather than failing the command.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		ctx := cmd.Context()
		repo, err := resolveRepo(ctx, prF.repo)
		if err != nil {
			return err
		}
		items, err := listOpenPRItems(ctx, repo, prF.reviewers)
		if err != nil {
			return err
		}
		if output.Resolve(prF.jsonOutput) {
			return writeJSON(cmd.OutOrStdout(), items)
		}
		return renderPRList(cmd.OutOrStdout(), items)
	},
}

// listOpenPRItems reads the open PRs for repo from the store and returns their
// base-field items. When augment is true, each item is additionally enriched
// with its labels + classified reviewer roster via a live provider round-trip;
// when false (the default the ACL uses) no provider call is made at all.
// A missing store yields an empty list (and does NOT create a store file as a
// side effect), mirroring the stat-guard the enrichment reader uses.
func listOpenPRItems(ctx context.Context, repo string, augment bool) ([]prListItem, error) {
	if _, statErr := os.Stat(store.DefaultPath()); statErr != nil {
		return []prListItem{}, nil
	}
	db, err := store.Open(store.DefaultPath())
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()

	prs, err := db.ListOpenPRs(ctx, repo)
	if err != nil {
		return nil, err
	}
	// One clock read + one bound for the whole listing, so every item in a single
	// payload is judged against the same instant. The bound falls back to pg-pr's
	// default cadence: a one-shot CLI read cannot ask the running daemon what
	// interval it ticks at (freshness.DefaultSyncIntervalSeconds, pinned to
	// sync.DefaultDaemonInterval).
	now := prListNow()
	bound := freshness.BoundSeconds(0)
	items := make([]prListItem, 0, len(prs))
	for _, pr := range prs {
		asOf := freshness.ParseAsOf(pr.LastSyncedAt)
		items = append(items, prListItem{
			Repo:      pr.Repo,
			Number:    pr.Number,
			State:     pr.State,
			Ownership: pr.Ownership,
			Draft:     pr.State == "draft",
			Branch:    pr.Branch,
			HeadSHA:   pr.HeadSHA,
			// Verbatim column pass-through: the seam reports what the store
			// recorded, un-reformatted, so a consumer can reconcile the two.
			// An empty or malformed column parses to the zero time and is
			// therefore flagged Stale (fail closed), rather than silently
			// reading as freshly synced.
			LastSyncedAt: pr.LastSyncedAt,
			Stale:        freshness.IsStale(asOf, now, bound),
			Labels:       []string{},
			Reviewers:    []prListReviewer{},
			Hidden:       pr.UserHidden,
			Reason:       pr.UserHiddenReason,
		})
	}
	if augment {
		augmentPRItems(ctx, repo, items)
	}
	return items, nil
}

// augmentPRItems adds labels and a classified reviewer roster to each item via a
// live provider round-trip. It is BEST-EFFORT: any provider error leaves that
// PR's labels/roster empty rather than failing the listing (the base fields
// from the store are authoritative). This keeps the read verb exit-0 on a
// transient upstream failure, which the pr-pool ACL relies on.
//
// When the provider implements the vcs.EnrichedPRsProvider bulk capability, the
// per-PR GetPR+ListReviews fan-out (2 gh subprocess spawns per PR, sequential)
// collapses into ONE bulk round-trip that bundles labels + reviews for every
// open PR — mirroring the sync engine's short-circuit (internal/sync
// Engine.tryEnumerateEnriched). The bulk result is matched onto the store's base
// list by PR number. Falls back to the per-PR path when the capability is absent
// OR the single bulk call fails, so behavior (and best-effort semantics) are
// unchanged for providers that cannot bulk-fetch.
func augmentPRItems(ctx context.Context, repo string, items []prListItem) {
	if len(items) == 0 {
		return
	}
	reg := prListAgentRegistry(ctx)
	prov := vcsProviderFor(repo)
	if enricher, ok := prov.(vcs.EnrichedPRsProvider); ok {
		if augmentPRItemsBulk(ctx, enricher, repo, items, reg) {
			return
		}
	}
	augmentPRItemsPerPR(ctx, prov, repo, items, reg)
}

// augmentPRItemsBulk augments items via ONE EnrichedPRs round-trip, matching the
// bulk result onto the base list by PR number. It reports false (having mutated
// nothing) when the bulk call fails, so the caller falls back to the per-PR
// path. Output parity with augmentPRItemsPerPR is preserved by reusing the same
// nonNilStrings / classifyRoster helpers on the same source data (the bulk
// EnrichedPR carries PR.Labels and Reviews); base-list ordering is preserved
// because items are iterated in place — only the map lookup source changes.
func augmentPRItemsBulk(ctx context.Context, enricher vcs.EnrichedPRsProvider, repo string, items []prListItem, reg *agentregistry.Registry) bool {
	enriched, err := enricher.EnrichedPRs(ctx, repo, buildListEnrichedQuery(repo))
	if err != nil {
		return false
	}
	byNumber := make(map[int]vcs.EnrichedPR, len(enriched))
	for _, ep := range enriched {
		byNumber[ep.PR.Number] = ep
	}
	for i := range items {
		ep, ok := byNumber[items[i].Number]
		if !ok {
			// Not in the bulk result — leave base-only fields (empty
			// labels/roster), matching the per-PR path's error-leaves-empty
			// best-effort contract.
			continue
		}
		items[i].Labels = nonNilStrings(ep.PR.Labels)
		items[i].Reviewers = classifyRoster(ep.Reviews, reg)
	}
	return true
}

// augmentPRItemsPerPR is the original per-PR path: one GetPR (labels) + one
// ListReviews (roster) per PR. Used when the provider lacks the bulk capability
// or the bulk call fails. Each round-trip is best-effort — an error leaves that
// PR's labels/roster empty.
func augmentPRItemsPerPR(ctx context.Context, prov vcs.Provider, repo string, items []prListItem, reg *agentregistry.Registry) {
	for i := range items {
		num := items[i].Number
		if pr, err := prov.GetPR(ctx, repo, num); err == nil && pr != nil {
			items[i].Labels = nonNilStrings(pr.Labels)
		}
		if reviews, err := prov.ListReviews(ctx, repo, num); err == nil {
			items[i].Reviewers = classifyRoster(reviews, reg)
		}
	}
}

// buildListEnrichedQuery composes the provider-native search string for the bulk
// augmentation: all open PRs in the repo. The store's base list is the authority
// for WHICH PRs are surfaced (matched by number in augmentPRItemsBulk); this
// query only needs to bring back their labels + reviews, so — unlike the sync
// engine's buildEnrichedSearchQuery — no author: constraint is applied. Mirrors
// the `is:pr is:open repo:<repo>` shape the github EnrichedPRs query consumes.
func buildListEnrichedQuery(repo string) string {
	return "is:pr is:open repo:" + repo
}

// prListAgentRegistry builds the agent registry from the loaded config,
// best-effort: a config-load or compile failure yields an empty registry (every
// reviewer then classifies as a person) rather than failing the listing.
func prListAgentRegistry(ctx context.Context) *agentregistry.Registry {
	empty, _ := agentregistry.New(nil)
	cfg, err := loadConfigForRepoPath(ctx)
	if err != nil || cfg == nil {
		return empty
	}
	reg, err := agentregistry.New(cfg.Agents)
	if err != nil {
		return empty
	}
	return reg
}

// classifyRoster reduces a PR's reviews to one entry per reviewer (latest state
// wins, first-appearance order preserved), classifying each login as agent or
// person via the registry. "Latest state wins" assumes the provider returns a
// PR's reviews oldest-first, which the github provider does (gh pr view --json
// reviews is chronological); a provider that inverts this would surface a stale
// review state.
func classifyRoster(reviews []api.Review, reg *agentregistry.Registry) []prListReviewer {
	order := make([]string, 0, len(reviews))
	state := make(map[string]string, len(reviews))
	for _, r := range reviews {
		if r.Author == "" {
			continue
		}
		if _, seen := state[r.Author]; !seen {
			order = append(order, r.Author)
		}
		state[r.Author] = r.State
	}
	out := make([]prListReviewer, 0, len(order))
	for _, login := range order {
		kind := "person"
		if reg != nil && reg.IsAgent(login) {
			kind = "agent"
		}
		out = append(out, prListReviewer{Login: login, State: state[login], Kind: kind})
	}
	return out
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// renderPRList writes the human-readable table. The SYNCED column carries the
// same freshness the JSON seam does — the table is itself a surface an operator
// acts on, so it must not present store-derived state without saying how old it
// is (pr-pool INV-FRESH-1).
func renderPRList(w io.Writer, items []prListItem) error {
	if len(items) == 0 {
		_, err := io.WriteString(w, "(no open PRs)\n")
		return err
	}
	now := prListNow()
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if _, err := io.WriteString(tw, "PR\tOWNER\tSTATE\tBRANCH\tHEAD\tSYNCED\n"); err != nil {
		return err
	}
	for _, it := range items {
		// Store state is already one of open/draft (ListOpenPRs filters to
		// those), and it.Draft is derived from it, so no draft-collapse is
		// needed here (unlike renderPR, which handles the live api.PR shape).
		state := it.State
		head := it.HeadSHA
		if len(head) > 12 {
			head = head[:12]
		}
		if _, err := io.WriteString(tw, "#"+strconv.Itoa(it.Number)+"\t"+it.Ownership+"\t"+state+"\t"+it.Branch+"\t"+head+"\t"+syncedCell(it, now)+"\n"); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// syncedCell renders one PR's freshness for the human table: its age in seconds,
// suffixed STALE when the item's stale flag is set, or "unknown STALE" when the
// row carries no usable as-of time (whose age cannot be stated).
func syncedCell(it prListItem, now time.Time) string {
	asOf := freshness.ParseAsOf(it.LastSyncedAt)
	if asOf.IsZero() {
		return "unknown STALE"
	}
	cell := strconv.Itoa(freshness.AgeSeconds(asOf, now)) + "s ago"
	if it.Stale {
		cell += " STALE"
	}
	return cell
}

func init() {
	prListCmd.Flags().BoolVar(&prF.jsonOutput, "json", false,
		"Emit machine-readable JSON instead of human-readable output")
	prListCmd.Flags().StringVar(&prF.repo, "repo", "",
		"Repository in owner/name form (defaults to auto-detected remote)")
	prListCmd.Flags().BoolVar(&prF.reviewers, "reviewers", false,
		"Augment each PR with its live reviewer roster and labels (one bulk round-trip when the provider supports it, else one per PR)")
	prCmd.AddCommand(prListCmd)
}
