package snapshot

import (
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/agentregistry"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/cirollup"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/freshness"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/ownership"
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
	// Approvals is the PR's PER-APPROVER approval timeline — one row per
	// approver login (store.ListApprovals), the source that replaced the
	// collapsed pr_revision.others_approved / my_review_state pair as the read
	// path (pg2-4dz88.1.9). It feeds BOTH classifyApprovals and the shared
	// NeedsAttention predicate, so "two approvers approved" and "this
	// approver's approval is stale/dismissed" are representable at all
	// (INV-APPROVAL-1, INV-APPROVAL-3).
	Approvals []store.Approval
	// Ownership is the PR's classification (mine/co-owned/team), computed by the
	// sync layer (buildPRInput) via internal/ownership. Build partitions on it.
	Ownership ownership.Ownership
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
	// ExcludedChecksByRepo maps a repo remote (PR.Repo) to its excluded_ci_checks
	// regex patterns. Rebuilt from live config each snapshot (like WatchLabels) so
	// SIGHUP edits apply immediately. nil/absent → nothing excluded. (pg2-qs46b)
	ExcludedChecksByRepo map[string][]string
}

// Match-reason strings on TeamRow.MatchReason, explaining why a PR is in the
// "PRs to Review" set.
const (
	MatchReasonTeamAuthored    = "team-authored"
	MatchReasonReviewRequested = "review-requested"
	// MatchReasonReviewedByMe marks a PR I have already submitted a review on.
	// It is the re-checkable counterpart of the detector's reviewed-by:<self>
	// retrieval bucket (internal/sync/detector.go's buildTeamQueries): GitHub
	// drops a PR from review-requested:<self> once I review it, so without this
	// reason a PR I am actively reviewing loses its last match reason and Build
	// drops it from the review set while it is still open and still waiting on me.
	MatchReasonReviewedByMe = "reviewed-by-me"
	// MatchReasonAssignedToMe fires when the configured self login is among
	// the PR's assignees (PRInput.PR.AssignedToMe, derived upstream by the sync
	// layer's assignedToSelf, mirroring ReviewRequestedOfMe) (pg2-4dz88.11.4).
	MatchReasonAssignedToMe = "assigned-to-me"
	// MatchReasonLabelPrefix is prepended to each matched watch-label name, e.g.
	// "label:lbl-one".
	MatchReasonLabelPrefix = "label:"
)

// selfSubmittedReviewStates are the review states that count as "I have
// reviewed this PR" for MatchReasonReviewedByMe.
//
// DISMISSED and PENDING are deliberately absent, which is where this predicate
// DIVERGES from internal/sync/revision.go's mySubmittedReviews: that mapping
// keeps a DISMISSED review as a STALE approval (INV-APPROVAL-3) because the
// approval record must remember that the approver DID approve. Here the question
// is whether my review still holds the PR in the review set, and a dismissed
// review no longer does — so a dismissal is the exit path this reason needs
// (mirroring how a removed label or a satisfied review request drops a PR out).
// A PENDING review has not been submitted at all.
var selfSubmittedReviewStates = map[string]struct{}{
	"APPROVED":          {},
	"CHANGES_REQUESTED": {},
	"COMMENTED":         {},
}

// hasSubmittedReviewBySelf reports whether self has a submitted review on the
// PR in one of selfSubmittedReviewStates. The author comparison is an EXACT
// GitHub-login match, matching internal/sync/refresh.go's reviewRequestedOfSelf
// — never case-insensitive or substring, so a bot login that merely contains
// self's login (or differs only in case) is a different reviewer. Empty self =>
// false (nothing to compare against).
func hasSubmittedReviewBySelf(reviews []api.Review, self string) bool {
	if self == "" {
		return false
	}
	for _, r := range reviews {
		if r.Author != self {
			continue
		}
		if _, ok := selfSubmittedReviewStates[r.State]; ok {
			return true
		}
	}
	return false
}

// matchReasons returns why PR p is in the review set: team-authored, requested
// of me, already reviewed by me, assigned to me, and/or carrying configured
// watch labels (one reason per matched label).
func matchReasons(p PRInput, team map[string]struct{}, watchLabels []string, self string) []string {
	var reasons []string
	if isTeam(p.PR.Author, team) {
		reasons = append(reasons, MatchReasonTeamAuthored)
	}
	if p.PR.ReviewRequestedOfMe {
		reasons = append(reasons, MatchReasonReviewRequested)
	}
	if hasSubmittedReviewBySelf(p.Reviews, self) {
		reasons = append(reasons, MatchReasonReviewedByMe)
	}
	if p.PR.AssignedToMe {
		reasons = append(reasons, MatchReasonAssignedToMe)
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
		// The freshness BOUND is a build-time property (it derives from the
		// declared cadence); the age/stale VERDICT against it is stamped at serve
		// time by Snapshot.WithFreshness, because a just-built snapshot is by
		// construction never stale.
		StaleAfterSeconds: freshness.BoundSeconds(in.SyncIntervalSeconds),
		Mine:              []MineRow{},
		Team:              []TeamRow{},
	}
	teamSet := make(map[string]struct{}, len(in.TeamMembers))
	for _, m := range in.TeamMembers {
		teamSet[m] = struct{}{}
	}
	excluders := make(map[string]*cirollup.Excluder, len(in.ExcludedChecksByRepo))
	for repo, pats := range in.ExcludedChecksByRepo {
		excluders[repo] = cirollup.NewExcluder(pats)
	}
	// mergedMine collects retained merged-PR-of-mine rows separately so they
	// can be appended AFTER every active Mine row below — "sort below every
	// open/active PR" (pg2-ew4kf). Kept in in.PRs iteration order (repo then
	// number, per snapshotModel.sortedInputs) among themselves.
	var mergedMine []MineRow
	for _, p := range in.PRs {
		reasons := matchReasons(p, teamSet, in.WatchLabels, in.Self)
		excl := excluders[p.PR.Repo]
		switch {
		case p.Ownership.ActsAsMine():
			// Retention (pg2-ew4kf): a merged PR is deliberately gated on
			// Ownership==Mine, NOT the broader ActsAsMine() (Mine|CoOwned) —
			// team/co-owned merges stay out of scope and are still dropped
			// immediately by the caller (internal/sync/refresh.go never
			// builds a PRInput for them in the first place). Recomputed
			// against in.GeneratedAt on every Build call — no persisted
			// "seen" state.
			if p.PR.Merged && p.Ownership == ownership.Mine {
				if !WithinMergedRetention(p.PR.MergedAt, in.GeneratedAt) {
					continue // merged more than MergedRetentionWindow ago: drop
				}
				row := buildMineRow(p, in.Registry, excl)
				row.Merged = true
				mergedMine = append(mergedMine, row)
				continue
			}
			out.Mine = append(out.Mine, buildMineRow(p, in.Registry, excl))
		case !p.PR.Draft && len(reasons) > 0:
			// "PRs to Review": a non-mine, non-draft PR that STILL qualifies — it
			// carries at least one live match reason (team-authored ∪ review-requested
			// ∪ reviewed-by-me ∪ assigned-to-me ∪ watch label). Requiring a reason here
			// — rather than admitting every non-draft non-mine PR — makes membership
			// self-correcting: a PR that ENTERED the set (labeled/requested/reviewed/
			// assigned) then lost the qualifier while still open+non-draft drops out
			// instead of lingering with an empty MatchReason (pg2-ynhr.13 B5 review #1).
			// That drop-out is a pure recomputation on the NEXT Build — there is no timer
			// and no persisted "seen" state, and it does NOT close the PR's merge-request
			// bead: bead closure is driven solely by the PR itself closing or merging
			// (internal/beadsbridge's EventPRClosed/EventPRMerged), never by a
			// match-reason change. Reasons are still SOURCED from ingest (detector.go's
			// buckets, B3); the builder only re-checks they hold. Others' drafts and
			// now-reasonless PRs fall through and are excluded.
			out.Team = append(out.Team, buildTeamRow(p, in.Registry, in.Self, reasons, excl))
		}
	}
	// Retained merged rows sort BELOW every active Mine row (pg2-ew4kf).
	out.Mine = append(out.Mine, mergedMine...)
	return out
}

func buildMineRow(p PRInput, reg *agentregistry.Registry, excl *cirollup.Excluder) MineRow {
	appr := classifyApprovals(p, reg)
	return MineRow{
		Repo:               p.PR.Repo,
		Number:             p.PR.Number,
		Title:              p.PR.Title,
		URL:                p.PR.URL,
		Draft:              p.PR.Draft,
		CIStatus:           cirollup.Compute(p.CIRuns, excl).State,
		HumanApproved:      appr.Human > 0,
		AgentApproved:      appr.Agent > 0,
		HumanApprovers:     appr.Human,
		AgentApprovers:     appr.Agent,
		WaitingOnMe:        beads.AllNonClosedHumanLabeled(p.BeadsDeps),
		MergeStateStatus:   p.PR.MergeStateStatus,
		AutoMergeEnabled:   p.PR.AutoMergeEnabled,
		NeedsMergeReminder: p.PR.MergeStateStatus == "CLEAN" && !p.PR.AutoMergeEnabled,
		JIRA:               mapJIRA(p.JIRA),
		Beads:              mapBeads(p.BeadsDeps),
		CoOwned:            p.Ownership == ownership.CoOwned,
		HasConflicts:       p.PR.HasConflict(),
	}
}

// buildTeamRow builds a "PRs to Review" row. reasons is the non-empty match-reason
// set Build already computed (and gated membership on), so it is threaded in rather
// than recomputed here.
func buildTeamRow(p PRInput, reg *agentregistry.Registry, self string, reasons []string, excl *cirollup.Excluder) TeamRow {
	appr := classifyApprovals(p, reg)
	// Attention is STORE-derived through the shared predicate — the SAME function
	// and SAME inputs the bead projector uses, so the dashboard signal and the
	// open-attention-bead set can never diverge (design §2.7, D4 / R4).
	need, reason := NeedsAttention(p.Revisions, p.Approvals, self, p.PR.HasConflict())
	return TeamRow{
		Repo:            p.PR.Repo,
		Number:          p.PR.Number,
		Title:           p.PR.Title,
		Owner:           p.PR.Author,
		URL:             p.PR.URL,
		CIStatus:        cirollup.Compute(p.CIRuns, excl).State,
		HumanApproved:   appr.Human > 0,
		AgentApproved:   appr.Agent > 0,
		HumanApprovers:  appr.Human,
		AgentApprovers:  appr.Agent,
		LinesChanged:    p.PR.Additions + p.PR.Deletions,
		FilesChanged:    p.PR.ChangedFiles,
		JIRA:            mapJIRA(p.JIRA),
		NeedsAttention:  need,
		AttentionReason: reason,
		MatchReason:     reasons,
		HasConflicts:    p.PR.HasConflict(),
	}
}

func isTeam(author string, team map[string]struct{}) bool {
	_, ok := team[author]
	return ok
}

// approvalFacts is one PR's PER-APPROVER approval summary: how many DISTINCT
// approvers hold an approval that currently STANDS, split by whether the
// approver is a registered agent.
//
// It replaced the `(human bool, agent bool)` pair classifyApprovals used to
// return (pg2-4dz88.1.9). That pair collapsed the whole approver set into two
// bits, so "two teammates approved" was indistinguishable from "one did" —
// exactly what INV-APPROVAL-1 ("per approver, never collapsed") forbids. The
// counts are per-approver, deduplicated by login: one approver contributes 1
// however many times they were observed.
type approvalFacts struct {
	Human int
	Agent int
}

// classifyApprovals derives the per-approver approval facts for one PR from
// the PER-APPROVER source (p.Approvals — store.ListApprovals over
// `pr_approval`), which is the read path as of pg2-4dz88.1.9.
//
// An approver is counted iff their recorded row is BOTH:
//
//   - an approval — State "approved". A recorded "changes-requested" or
//     "commented" row is a review, not an approval, and MUST NOT count (the
//     three states share one table; see internal/sync/revision.go's four
//     parallel writers); and
//   - currently STANDING — !Approval.IsStale(p.PR.HeadSHA). That excludes both
//     an approval of an EARLIER head and one the code host DISMISSED
//     (INV-APPROVAL-3). A dismissed approval is stale, never absent, so the row
//     still records that the approver DID approve — it just no longer counts
//     toward approval of the current head. Neither exclusion was expressible
//     before this cutover: `pr_revision.others_approved` was a single
//     head-anchored boolean with no per-approver identity and no dismissal.
//
// The human/agent split is registry-derived (reg.IsAgent). A nil registry means
// no agent is configured, so every approver counts as human — the documented
// pre-cutover behavior, preserved.
//
// # The legacy approval-regex fallback (deliberately RETAINED)
//
// After the store rows, top-level comment and review-summary bodies are still
// mined for a registered agent's ApprovalRegex, and a match counts that agent
// as an approver — but ONLY for a login the store has recorded NO row for. The
// store is authoritative for every login it knows: it carries state AND
// staleness, which a body match cannot, so letting a stale-but-matching body
// resurrect a dismissed approval would defeat the exclusion above.
//
// The fallback is retained rather than deleted because it has no store
// representation: the bot-verdict writer that DOES populate `pr_approval` from
// comment bodies (internal/sync/approver.go) is gated on
// config.Config.ApproverAllowlist, a set pg2-4dz88.1.3 deliberately kept
// SEPARATE from the agent registry. A deployment configuring `approval_regex`
// but no `approver_allowlist` therefore has no rows for its agent, and dropping
// the fallback here would silently retire its agent-approval signal.
//
// Inline diff comments (Path/Line set) are excluded from the mining, as before:
// only top-level / review-summary bodies are verdict-shaped.
func classifyApprovals(p PRInput, reg *agentregistry.Registry) approvalFacts {
	// standing maps an approver login to whether they are a registered agent.
	// A map is what makes the count PER APPROVER: repeated observations of one
	// login collapse to one entry, distinct logins never do.
	standing := make(map[string]bool, len(p.Approvals))
	recorded := make(map[string]struct{}, len(p.Approvals))
	for _, a := range p.Approvals {
		recorded[a.Approver] = struct{}{}
		if a.State != "approved" || a.IsStale(p.PR.HeadSHA) {
			continue
		}
		standing[a.Approver] = reg != nil && reg.IsAgent(a.Approver)
	}
	if reg != nil {
		mine := func(login, body string) {
			if _, known := recorded[login]; known {
				return // the store row is authoritative for this login
			}
			if reg.MatchApproval(login, body) {
				standing[login] = true // MatchApproval only matches registered agents
			}
		}
		for _, c := range p.Comments {
			if c.Path != "" || c.Line != 0 {
				continue
			}
			mine(c.Author, c.Body)
		}
		for _, r := range p.Reviews {
			mine(r.Author, r.Body)
		}
	}
	var f approvalFacts
	for _, isAgent := range standing {
		if isAgent {
			f.Agent++
		} else {
			f.Human++
		}
	}
	return f
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
