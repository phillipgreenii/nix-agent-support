package snapshot

import (
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/agentregistry"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/beads"
)

// PRInput is the per-PR data the sync loop has gathered.
type PRInput struct {
	PR        api.PR
	Reviews   []api.Review
	Comments  []api.Comment
	CIRuns    []api.CIRun
	JIRA      []api.Issue
	BeadsDeps []beads.DepNode // recursive deps of the merge-request bead
	// Revisions is the PR's persisted revision timeline in ascending seq order
	// (store.ListRevisions). It feeds the shared needsAttention predicate so the
	// dashboard attention signal is store-derived, matching the bead projector
	// (design §2.7, D4).
	Revisions []store.Revision
	// DraftReviewClosed is true iff pg2-4c5i.36 closed this PR's draft-review
	// bead (the "draft review ready" signal). Also feeds needsAttention.
	DraftReviewClosed bool
}

// BuilderInput is the full snapshot input.
type BuilderInput struct {
	GeneratedAt         time.Time
	SyncIntervalSeconds int
	Self                string
	TeamMembers         []string
	// WatchLabels are the configured review labels (union across repos). A PR
	// carrying one is part of the review set and gets a MatchReasonLabelPrefix
	// reason. Nil/empty is fine (label matching just never fires).
	WatchLabels []string
	Registry    *agentregistry.Registry
	PRs         []PRInput
}

// Match-reason strings on TeamRow.MatchReason, explaining why a PR is in the
// "PRs to Review" set.
const (
	MatchReasonTeamAuthored    = "team-authored"
	MatchReasonReviewRequested = "review-requested"
	// MatchReasonLabelPrefix is prepended to each matched watch-label name, e.g.
	// "label:team/findev".
	MatchReasonLabelPrefix = "label:"
)

// matchReasons returns why PR p is in the review set: team-authored, requested
// of me, and/or carrying configured watch labels (one reason per matched label).
func matchReasons(p PRInput, team map[string]struct{}, watchLabels []string) []string {
	var reasons []string
	if isTeam(p.PR.Author, team) {
		reasons = append(reasons, MatchReasonTeamAuthored)
	}
	if p.PR.ReviewRequestedOfMe {
		reasons = append(reasons, MatchReasonReviewRequested)
	}
	if len(watchLabels) > 0 && len(p.PR.Labels) > 0 {
		watch := make(map[string]struct{}, len(watchLabels))
		for _, l := range watchLabels {
			watch[l] = struct{}{}
		}
		for _, l := range p.PR.Labels {
			if _, ok := watch[l]; ok {
				reasons = append(reasons, MatchReasonLabelPrefix+l)
			}
		}
	}
	return reasons
}

// Build assembles a Snapshot from the given input. Pure; no IO.
func Build(in BuilderInput) *Snapshot {
	out := &Snapshot{
		GeneratedAt:         in.GeneratedAt,
		SyncIntervalSeconds: in.SyncIntervalSeconds,
		Mine:                []MineRow{},
		Team:                []TeamRow{},
	}
	teamSet := make(map[string]struct{}, len(in.TeamMembers))
	for _, m := range in.TeamMembers {
		teamSet[m] = struct{}{}
	}
	for _, p := range in.PRs {
		reasons := matchReasons(p, teamSet, in.WatchLabels)
		switch {
		case p.PR.Author == in.Self:
			out.Mine = append(out.Mine, buildMineRow(p, in.Registry))
		case !p.PR.Draft && len(reasons) > 0:
			// "PRs to Review": a non-mine, non-draft PR that STILL qualifies — it
			// carries at least one live match reason (team-authored ∪ review-requested
			// ∪ watch label). Requiring a reason here — rather than admitting every
			// non-draft non-mine PR — makes membership self-correcting: a PR that
			// ENTERED the set (labeled/requested) then lost the qualifier while still
			// open+non-draft drops out instead of lingering with an empty MatchReason
			// (pg2-ynhr.13 B5 review #1). Reasons are still SOURCED from ingest
			// (detector.go's buckets, B3); the builder only re-checks they hold. Others'
			// drafts and now-reasonless PRs fall through and are excluded.
			out.Team = append(out.Team, buildTeamRow(p, in.Registry, reasons))
		}
	}
	return out
}

func buildMineRow(p PRInput, reg *agentregistry.Registry) MineRow {
	hum, agt := classifyApprovals(p, reg)
	return MineRow{
		Repo:          p.PR.Repo,
		Number:        p.PR.Number,
		Title:         p.PR.Title,
		URL:           p.PR.URL,
		Draft:         p.PR.Draft,
		CIStatus:      rollupCI(p.CIRuns),
		HumanApproved: hum,
		AgentApproved: agt,
		WaitingOnMe:   beads.AllNonClosedHumanLabeled(p.BeadsDeps),
		JIRA:          mapJIRA(p.JIRA),
		Beads:         mapBeads(p.BeadsDeps),
	}
}

// buildTeamRow builds a "PRs to Review" row. reasons is the non-empty match-reason
// set Build already computed (and gated membership on), so it is threaded in rather
// than recomputed here.
func buildTeamRow(p PRInput, reg *agentregistry.Registry, reasons []string) TeamRow {
	hum, agt := classifyApprovals(p, reg)
	// Attention is STORE-derived through the shared predicate — the SAME function
	// and SAME inputs the bead projector uses, so the dashboard signal and the
	// open-attention-bead set can never diverge (design §2.7, D4 / R4).
	need, reason := NeedsAttention(p.Revisions, p.DraftReviewClosed)
	return TeamRow{
		Repo:            p.PR.Repo,
		Number:          p.PR.Number,
		Title:           p.PR.Title,
		Owner:           p.PR.Author,
		URL:             p.PR.URL,
		CIStatus:        rollupCI(p.CIRuns),
		HumanApproved:   hum,
		AgentApproved:   agt,
		LinesChanged:    p.PR.Additions + p.PR.Deletions,
		FilesChanged:    p.PR.ChangedFiles,
		JIRA:            mapJIRA(p.JIRA),
		NeedsAttention:  need,
		AttentionReason: reason,
		MatchReason:     reasons,
	}
}

func isTeam(author string, team map[string]struct{}) bool {
	_, ok := team[author]
	return ok
}

// classifyApprovals walks reviews, comments, and review-summary bodies to
// derive (human_approved, agent_approved).
//
// human_approved: any APPROVED review where the author is NOT a registered
// agent.
// agent_approved: any APPROVED review where the author IS a registered
// agent, OR any non-inline comment / review-summary body whose author is a
// registered agent and whose body matches that agent's approval regex.
func classifyApprovals(p PRInput, reg *agentregistry.Registry) (human bool, agent bool) {
	if reg == nil {
		// Treat all approvers as human when no registry configured.
		for _, r := range p.Reviews {
			if r.State == "APPROVED" {
				human = true
			}
		}
		return
	}
	for _, r := range p.Reviews {
		if r.State != "APPROVED" {
			continue
		}
		if reg.IsAgent(r.Author) {
			agent = true
		} else {
			human = true
		}
	}
	if !agent {
		// Comment-mining: only top-level / review-summary bodies. Inline
		// diff comments (Path/Line non-empty) are excluded.
		for _, c := range p.Comments {
			if c.Path != "" || c.Line != 0 {
				continue
			}
			if reg.MatchApproval(c.Author, c.Body) {
				agent = true
				break
			}
		}
		if !agent {
			for _, r := range p.Reviews {
				if reg.MatchApproval(r.Author, r.Body) {
					agent = true
					break
				}
			}
		}
	}
	return
}

// rollupCI reduces a slice of CIRun into success | failure | pending | none.
func rollupCI(runs []api.CIRun) string {
	if len(runs) == 0 {
		return "none"
	}
	pending := false
	for _, r := range runs {
		switch r.Conclusion {
		case "failure", "cancelled", "timed_out":
			return "failure"
		}
		if r.Status == "in_progress" || r.Status == "queued" || r.Conclusion == "" {
			pending = true
		}
	}
	if pending {
		return "pending"
	}
	return "success"
}

func mapJIRA(issues []api.Issue) []JIRAItem {
	out := make([]JIRAItem, 0, len(issues))
	for _, i := range issues {
		out = append(out, JIRAItem{ID: i.ID, Title: i.Title, State: i.State, URL: i.URL})
	}
	return out
}

func mapBeads(deps []beads.DepNode) []BeadItem {
	out := make([]BeadItem, 0, len(deps))
	for _, d := range deps {
		out = append(out, BeadItem{
			ID: d.ID, Title: d.Title, Status: d.Status, Labels: d.Labels,
			URL: "bd://" + d.ID,
		})
	}
	return out
}
