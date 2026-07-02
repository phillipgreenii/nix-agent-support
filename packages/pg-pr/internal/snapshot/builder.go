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
	Registry            *agentregistry.Registry
	PRs                 []PRInput
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
		switch {
		case p.PR.Author == in.Self:
			out.Mine = append(out.Mine, buildMineRow(p, in.Registry))
		case isTeam(p.PR.Author, teamSet) && !p.PR.Draft:
			out.Team = append(out.Team, buildTeamRow(p, in.Registry))
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

func buildTeamRow(p PRInput, reg *agentregistry.Registry) TeamRow {
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
