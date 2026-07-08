package main

import (
	"context"
	"io"
	"os"
	"strconv"
	"text/tabwriter"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/agentregistry"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/output"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	"github.com/spf13/cobra"
)

// prListItem is the JSON shape emitted per open PR by `pg-pr pr list --json`.
// It is the read seam the pr-pool ACL consumes: the base fields (repo, number,
// head_sha, ownership, draft, state, branch) come from the store; reviewer
// roster + labels are added best-effort from a live provider round-trip.
type prListItem struct {
	Repo      string           `json:"repo"`
	Number    int              `json:"number"`
	State     string           `json:"state"`
	Ownership string           `json:"ownership"`
	Draft     bool             `json:"draft"`
	Branch    string           `json:"branch"`
	HeadSHA   string           `json:"head_sha"`
	Labels    []string         `json:"labels"`
	Reviewers []prListReviewer `json:"reviewers"`
}

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

With --reviewers, each PR is additionally augmented with its labels and a
classified reviewer roster from a live provider round-trip (one round-trip per
PR). That augmentation is best-effort: a provider failure leaves labels/roster
empty rather than failing the command.`,
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
	items := make([]prListItem, 0, len(prs))
	for _, pr := range prs {
		items = append(items, prListItem{
			Repo:      pr.Repo,
			Number:    pr.Number,
			State:     pr.State,
			Ownership: pr.Ownership,
			Draft:     pr.State == "draft",
			Branch:    pr.Branch,
			HeadSHA:   pr.HeadSHA,
			Labels:    []string{},
			Reviewers: []prListReviewer{},
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
func augmentPRItems(ctx context.Context, repo string, items []prListItem) {
	if len(items) == 0 {
		return
	}
	reg := prListAgentRegistry(ctx)
	prov := vcsProviderFor(repo)
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

func renderPRList(w io.Writer, items []prListItem) error {
	if len(items) == 0 {
		_, err := io.WriteString(w, "(no open PRs)\n")
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if _, err := io.WriteString(tw, "PR\tOWNER\tSTATE\tBRANCH\tHEAD\n"); err != nil {
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
		if _, err := io.WriteString(tw, "#"+strconv.Itoa(it.Number)+"\t"+it.Ownership+"\t"+state+"\t"+it.Branch+"\t"+head+"\n"); err != nil {
			return err
		}
	}
	return tw.Flush()
}

func init() {
	prListCmd.Flags().BoolVar(&prF.jsonOutput, "json", false,
		"Emit machine-readable JSON instead of human-readable output")
	prListCmd.Flags().StringVar(&prF.repo, "repo", "",
		"Repository in owner/name form (defaults to auto-detected remote)")
	prListCmd.Flags().BoolVar(&prF.reviewers, "reviewers", false,
		"Augment each PR with its live reviewer roster and labels (one provider round-trip per PR)")
	prCmd.AddCommand(prListCmd)
}
