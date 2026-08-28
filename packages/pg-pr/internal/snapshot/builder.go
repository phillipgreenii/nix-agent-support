package snapshot

import (
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/agentregistry"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/checkinterpret"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/cirollup"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/freshness"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/ownership"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/prdeps"
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
	// Hidden mirrors the store's USER_HIDDEN flag (store.PullRequest.UserHidden,
	// pg2-4dz88.4.2/.4.3), populated by the sync layer from the persisted PR row.
	// Build drops a Hidden PRInput from BOTH Mine and Team unless
	// BuilderInput.IncludeHidden is set — see Build's doc. Hiding is
	// DISPLAY-LAYER ONLY: it never affects ingestion, so this field influences
	// nothing upstream of Build.
	Hidden bool
	// HiddenReason is the operator-supplied reason recorded with the hide, if
	// any (store.PullRequest.UserHiddenReason).
	HiddenReason string
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
	// CheckInterpretersByRepo maps a repo remote (PR.Repo) to its configured
	// check/status interpreter declarations (RepoConfig.CheckInterpreters,
	// mirrored here as the plain-data checkinterpret.Interpreter so this
	// package needn't import internal/config — see that package's doc for
	// the rationale). Rebuilt from live config each snapshot (like
	// WatchLabels) so SIGHUP edits apply immediately.
	//
	// This replaces the pre-pg2-4dz88.2 ExcludedChecksByRepo (a flat
	// per-repo regex-pattern list, matching the now-removed
	// excluded_ci_checks config key, pg2-dw73b). Build derives the SAME
	// per-repo CI-rollup Excluder from the union of every entry's Patterns,
	// regardless of Type — mirroring internal/sync/revision.go's
	// excluderFromCheckInterpreters — so a check/status any configured
	// interpreter claims is excluded from CI health exactly as
	// excluded_ci_checks used to exclude it (pg2-4dz88.2.8). nil/absent →
	// nothing excluded, matching the prior default. (pg2-qs46b)
	CheckInterpretersByRepo map[string][]checkinterpret.Interpreter
	// IncludeHidden, when false (the default), makes Build DROP every PRInput
	// with Hidden==true from BOTH Mine and Team — the human-facing default per
	// the pg2-4dz88.4/.4.3 operator ruling (fork #1: hidden PRs are excluded
	// from human-facing surfaces by default). When true, a hidden PRInput is
	// admitted normally, with MineRow/TeamRow.Hidden + HiddenReason carrying the
	// flag through for display. Callers decide how to source this: the daemon's
	// shared dashboard snapshot sets it true (so `pg-pr open --include-hidden`,
	// reading that SAME payload, can actually surface hidden rows) and leaves
	// the human-default filtering to the CLI layer that owns the flag
	// (cmd/pg-pr/open.go's selectRows) — see that file's doc for the rationale.
	IncludeHidden bool
	// TrunkRefs are the ref names that mean "bottom of a chain" for the
	// whole-set PR-dependency pass (prdeps.DeriveWithNativeStack; pg2-4dz88.3.7):
	// a PR whose base ref names one of these is not stacked on anything.
	// Deliberately a flat, repo-unscoped UNION rather than a
	// repo-keyed map — prdeps.Input.TrunkRefs's own doc prescribes exactly
	// this shape ("the same shape snapshot.BuilderInput.WatchLabels uses"),
	// and no per-repo config for it exists anywhere yet (this is the first
	// consumer). Nil/empty is valid and simply means no ref is recognised as
	// trunk, matching prdeps.Input.TrunkRefs's own documented default.
	TrunkRefs []string
	// ApproverAllowlist mirrors config.Config.ApproverAllowlist: the set of
	// logins whose verdict counts toward approval AND, as of pg2-4dz88.7.3,
	// toward the bot-disapproval clause of the team-panel ACT-NOW predicate
	// (see TeamRow.BotDisapproved, internal/snapshot/panels.go's ActNow).
	// This package must not import internal/config (see
	// CheckInterpretersByRepo's doc for the same rationale), so the caller
	// (internal/sync) supplies the plain login list. Nil/empty means no
	// login is allowlisted, so botDisapproved never fires — matching
	// config.Config.ApproverAllowlist's own documented absent/empty default.
	ApproverAllowlist []string
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
//
// A Hidden PRInput is dropped from both Mine and Team unless
// in.IncludeHidden is set (pg2-4dz88.4.3) — see BuilderInput.IncludeHidden.
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
	excluders := make(map[string]*cirollup.Excluder, len(in.CheckInterpretersByRepo))
	for repo, interps := range in.CheckInterpretersByRepo {
		excluders[repo] = excluderFromInterpreters(interps)
	}
	approverAllowlist := make(map[string]struct{}, len(in.ApproverAllowlist))
	for _, login := range in.ApproverAllowlist {
		approverAllowlist[login] = struct{}{}
	}
	// The PR-dependency pass (pg2-4dz88.3.7) is a WHOLE-SET pass, computed once
	// here over every PRInput regardless of admission below — a PR's place in a
	// stack does not depend on whether it ends up in Mine, Team, or dropped
	// (hidden) — and then looked up per row inside buildMineRow/buildTeamRow.
	depGraph := prdeps.DeriveWithNativeStack(prdeps.Input{
		PRs:       toPRDeps(in.PRs),
		TrunkRefs: in.TrunkRefs,
	})
	unresolvedRefNames := unresolvedUpstreamRefNames(depGraph)
	// mergedMine collects retained merged-PR-of-mine rows separately so they
	// can be appended AFTER every active Mine row below — "sort below every
	// open/active PR" (pg2-ew4kf). Kept in in.PRs iteration order (repo then
	// number, per snapshotModel.sortedInputs) among themselves.
	var mergedMine []MineRow
	for _, p := range in.PRs {
		// Hidden-PR default exclusion (pg2-4dz88.4.3, operator fork #1 ruling):
		// a PR the operator hid is omitted from BOTH Mine and Team unless the
		// caller opted in via IncludeHidden. This is deliberately the FIRST
		// check in the loop, ahead of ownership/reasons, so a hidden PR never
		// reaches either arm below regardless of who authored it or why it
		// would otherwise qualify.
		if p.Hidden && !in.IncludeHidden {
			continue
		}
		reasons := matchReasons(p, teamSet, in.WatchLabels, in.Self)
		excl := excluders[p.PR.Repo]
		deps := dependencyFactsFor(depGraph, unresolvedRefNames, prdeps.Ref{Repo: p.PR.Repo, Number: p.PR.Number})
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
				row := buildMineRow(p, in.Registry, excl, deps)
				row.Merged = true
				mergedMine = append(mergedMine, row)
				continue
			}
			out.Mine = append(out.Mine, buildMineRow(p, in.Registry, excl, deps))
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
			out.Team = append(out.Team, buildTeamRow(p, in.Registry, in.Self, reasons, excl, deps, approverAllowlist))
		default:
			// Purely observational (pg2-4dz88.7.6): a draft PR I don't own, or a
			// non-mine non-draft PR matching zero review-set reasons, falls
			// through both admitting cases above and is silently dropped today.
			// This branch only COUNTS that drop — it does not admit the row to
			// either Mine or Team, and it does not change either case above.
			out.DroppedCount++
		}
	}
	// Retained merged rows sort BELOW every active Mine row (pg2-ew4kf).
	out.Mine = append(out.Mine, mergedMine...)
	return out
}

// excluderFromInterpreters derives a cirollup.Excluder from the union of
// every Interpreter's Patterns, regardless of Type — a check/status any
// configured interpreter claims is excluded from the CI rollup, mirroring
// internal/sync/revision.go's excluderFromCheckInterpreters exactly. That
// function cannot be called directly (it takes
// []config.CheckInterpreterConfig, and this package must not import
// internal/config — see BuilderInput.CheckInterpretersByRepo's doc), so the
// same small union-of-Patterns logic is duplicated here rather than shared;
// see checkinterpret's package doc for why cirollup/checkinterpret/config
// deliberately do not share one abstraction.
func excluderFromInterpreters(interps []checkinterpret.Interpreter) *cirollup.Excluder {
	var patterns []string
	for _, ip := range interps {
		patterns = append(patterns, ip.Patterns...)
	}
	return cirollup.NewExcluder(patterns)
}

// gateStateFacts is one PR's projection of the persisted approval-gate
// verdict onto the row-level fields MineRow/TeamRow carry (INV-GATE-1: its
// own axis, never folded into CIStatus).
type gateStateFacts struct {
	State string
	N, M  int
}

// gateStateFor projects the PR's LATEST revision's persisted gate verdict
// (store.Revision.GateState/GateStateN/GateStateM, schema v11
// pg2-4dz88.2.5) — the same revs[len(revs)-1] "latest" idiom
// NeedsAttention already uses for this same slice. Build never
// re-classifies a CI run to produce this value; it only projects what sync
// already observed and persisted (gateStateFromSync,
// internal/sync/revision.go), so there is exactly one place a check/status
// description is ever parsed into a gate verdict.
//
// A PR with no revisions at all, or whose latest revision has never had
// SetRevisionGateState called on it (the store's "unknown" default), both
// collapse to the zero value here: per INV-GATE-2 an unmatched/absent gate
// MUST read as unknown, never satisfied, and "never observed" is exactly
// that same not-yet-asserted signal, not a distinct state consumers need
// to tell apart from it.
func gateStateFor(revs []store.Revision) gateStateFacts {
	if len(revs) == 0 {
		return gateStateFacts{}
	}
	latest := revs[len(revs)-1]
	if latest.GateState == "" || latest.GateState == "unknown" {
		return gateStateFacts{}
	}
	return gateStateFacts{State: latest.GateState, N: latest.GateStateN, M: latest.GateStateM}
}

func buildMineRow(p PRInput, reg *agentregistry.Registry, excl *cirollup.Excluder, deps dependencyFacts) MineRow {
	appr := classifyApprovals(p, reg)
	gate := gateStateFor(p.Revisions)
	return MineRow{
		Repo:               p.PR.Repo,
		Number:             p.PR.Number,
		Title:              p.PR.Title,
		URL:                p.PR.URL,
		Draft:              p.PR.Draft,
		CIStatus:           cirollup.Compute(p.CIRuns, excl).State,
		GateState:          gate.State,
		GateStateN:         gate.N,
		GateStateM:         gate.M,
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
		Hidden:             p.Hidden,
		HiddenReason:       p.HiddenReason,

		DependencyBlockedBy:              deps.BlockedBy,
		DependencyBlockedByUnresolvedRef: deps.BlockedByUnresolvedRef,
		DependencyUnblockedFrom:          deps.UnblockedFrom,
		DependencyOrderingKey:            deps.OrderingKey,
	}
}

// buildTeamRow builds a "PRs to Review" row. reasons is the non-empty match-reason
// set Build already computed (and gated membership on), so it is threaded in rather
// than recomputed here. allowlist is Build's precomputed set from
// BuilderInput.ApproverAllowlist, feeding BotDisapproved.
func buildTeamRow(p PRInput, reg *agentregistry.Registry, self string, reasons []string, excl *cirollup.Excluder, deps dependencyFacts, allowlist map[string]struct{}) TeamRow {
	appr := classifyApprovals(p, reg)
	gate := gateStateFor(p.Revisions)
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
		GateState:       gate.State,
		GateStateN:      gate.N,
		GateStateM:      gate.M,
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
		BotDisapproved:  botDisapproved(p.Approvals, allowlist, p.PR.HeadSHA),
		Hidden:          p.Hidden,
		HiddenReason:    p.HiddenReason,

		DependencyBlockedBy:              deps.BlockedBy,
		DependencyBlockedByUnresolvedRef: deps.BlockedByUnresolvedRef,
		DependencyUnblockedFrom:          deps.UnblockedFrom,
		DependencyOrderingKey:            deps.OrderingKey,
	}
}

// dependencyFacts is one PR's projection of prdeps.DeriveWithNativeStack's
// Graph onto the FACT + ordering-key fields TeamRow/MineRow carry (pg2-4dz88.3.7).
// See those fields' doc comments (on TeamRow, the canonical copy) for the full
// contract; this struct exists only so buildMineRow/buildTeamRow can take the
// same four values as one parameter rather than four.
type dependencyFacts struct {
	BlockedBy              string
	BlockedByUnresolvedRef string
	UnblockedFrom          string
	OrderingKey            int
}

// toPRDeps projects the snapshot's PRInput set into the minimal prdeps.PR
// shape DeriveWithNativeStack consumes. State is re-derived with the SAME
// merged/draft precedence internal/sync's stateForPR uses (Merged wins,
// then Draft, then the raw GitHub State) rather than passed through
// PRInput.PR.State as-is: that field carries GitHub's raw open/closed enum,
// not the merged/draft-aware spelling prdeps.IsOpen/isMerged compare against.
func toPRDeps(prs []PRInput) []prdeps.PR {
	out := make([]prdeps.PR, len(prs))
	for i, p := range prs {
		out[i] = prdeps.PR{
			Repo:               p.PR.Repo,
			Number:             p.PR.Number,
			Head:               p.PR.Branch,
			Base:               p.PR.Base,
			State:              prdepsState(p.PR),
			NativeUpstreamHead: p.PR.StackUpstreamHeadRefName,
		}
	}
	return out
}

// prdepsState mirrors internal/sync's stateForPR spelling (merged | draft |
// open | closed), which prdeps.PR.State's doc requires. It cannot call that
// function directly (internal/sync is a different package and importing it
// here would invert the dependency the sync layer already has on
// internal/snapshot), so the same three-way precedence is duplicated: a
// Merged PR reads "merged" regardless of its raw State; an open, Draft PR
// reads "draft"; anything else reports its raw State lower-cased.
func prdepsState(p api.PR) string {
	if p.Merged {
		return "merged"
	}
	if strings.EqualFold(p.State, "open") {
		if p.Draft {
			return "draft"
		}
		return "open"
	}
	return strings.ToLower(p.State)
}

// unresolvedUpstreamRefNames indexes DiagnosticUpstreamOutOfSet's RefName by
// the blocked PR's Ref. Graph.Node carries no field naming the target ref for
// ResolutionUpstreamOutOfSet (Upstream/MergedUpstream both stay the zero Ref —
// there is no live PR to name), so the ref name that makes
// DependencyBlockedByUnresolvedRef useful only exists on the accompanying
// Diagnostic. Each blocked PR contributes at most one
// DiagnosticUpstreamOutOfSet (native.go's per-PR switch has exactly one arm
// that appends it), so the map never overwrites an entry.
func unresolvedUpstreamRefNames(g prdeps.Graph) map[prdeps.Ref]string {
	out := make(map[prdeps.Ref]string, len(g.Diagnostics))
	for _, d := range g.Diagnostics {
		if d.Kind == prdeps.DiagnosticUpstreamOutOfSet && len(d.Refs) > 0 {
			out[d.Refs[0]] = d.RefName
		}
	}
	return out
}

// dependencyFactsFor looks up ref's node in g and projects it to the row-level
// facts, WITHOUT re-deriving anything the Graph already decided: the
// resolution kind, the live upstream ref, the merged-upstream ref, and the
// ordering key are all read straight off the Node (see prdeps.Node's doc).
// A ref absent from g (should not happen — g is built from the same PR set
// the caller iterates) or resolved to anything other than ResolutionUpstream /
// ResolutionUpstreamOutOfSet / ResolutionUnblocked returns the zero value,
// which is exactly "no relation" — the unchanged-row-shape requirement for
// ResolutionTrunk and the other no-relation resolutions (Foreign, Self,
// Unresolvable).
func dependencyFactsFor(g prdeps.Graph, unresolvedRefNames map[prdeps.Ref]string, ref prdeps.Ref) dependencyFacts {
	node, ok := g.Lookup(ref)
	if !ok {
		return dependencyFacts{}
	}
	f := dependencyFacts{OrderingKey: -node.Depth}
	switch node.Resolution {
	case prdeps.ResolutionUpstream:
		f.BlockedBy = node.Upstream.String()
	case prdeps.ResolutionUpstreamOutOfSet:
		f.BlockedByUnresolvedRef = unresolvedRefNames[ref]
	case prdeps.ResolutionUnblocked:
		f.UnblockedFrom = node.MergedUpstream.String()
	}
	return f
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
			// A review's body may feed the fallback only in states APPROVED
			// and COMMENTED (pg2-4dz88.9). DISMISSED is an approval GitHub has
			// already invalidated — resurrecting it from body text alone is
			// the defect this guard exists to close; CHANGES_REQUESTED
			// contradicts a positive verdict; PENDING is unsubmitted; and an
			// unknown/empty state fails closed. COMMENTED MUST stay allowed —
			// a review-summary verdict is normally state COMMENTED, not
			// APPROVED, so narrowing this to an APPROVED-only allow list would
			// delete the fallback documented above rather than fix the guard.
			if r.State != "APPROVED" && r.State != "COMMENTED" {
				continue
			}
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

// botDisapproved reports whether p currently carries a STANDING
// "changes-requested" verdict from an ALLOWLISTED approver — a login in
// allowlist, Build's set from BuilderInput.ApproverAllowlist
// (config.Config.ApproverAllowlist).
//
// # Why not AgentApproved/AgentApprovers (pg2-4dz88.7.3's read-source decision)
//
// ApproverAllowlist is a SEPARATE set from the agent-registration set
// agentregistry.Registry.IsAgent that classifyApprovals's Human/Agent split
// uses — config.Config.ApproverAllowlist's own doc comment says membership
// here is "never implied" by an agent registration. A login can be a
// registered, ingested agent (counted in AgentApproved/AgentApprovers)
// without being an allowlisted approver, or vice versa. Reusing
// AgentApproved/AgentApprovers here would silently misclassify a bot verdict
// whenever the two sets diverge, which is exactly the failure mode this
// bead's description flags. This function therefore filters p.Approvals
// (store.Approval, the same per-approver source classifyApprovals reads)
// directly against allowlist, never against the registry.
//
// A row counts only when it is BOTH:
//   - state "changes-requested" — the mapping internal/sync/approver.go's
//     approverApprovalState gives verdict.Withheld, regardless of Findings; and
//   - currently STANDING — !Approval.IsStale(headSHA).
//
// # Staleness decision (pg2-4dz88.7.3)
//
// A disapproval that no longer stands for the PR's current head — an
// earlier-head observation, or one the code host dismissed — is treated as
// WITHDRAWN, not blocking. This mirrors classifyApprovals and NeedsAttention,
// which already give every other pr_approval row this exact staleness
// treatment (INV-APPROVAL-3): the row still records that the disapproval
// happened, it just no longer holds the PR back once superseded. A second,
// bot-specific staleness policy would diverge from that established meaning
// of "stale" for no documented reason.
func botDisapproved(approvals []store.Approval, allowlist map[string]struct{}, headSHA string) bool {
	for _, a := range approvals {
		if _, ok := allowlist[a.Approver]; !ok {
			continue
		}
		if a.State != "changes-requested" {
			continue
		}
		if a.IsStale(headSHA) {
			continue
		}
		return true
	}
	return false
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
