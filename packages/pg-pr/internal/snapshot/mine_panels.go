package snapshot

import (
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/cirollup"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
)

// MineView identifies which of the three "My PRs" panel membership views
// (pg2-4dz88.7.4) one Mine row belongs to.
type MineView int

const (
	// MineViewActNow: something for ME to address right now.
	MineViewActNow MineView = iota
	// MineViewAwaitingOthers: approvals outstanding — every other axis is
	// clean but a required human approval has not landed.
	MineViewAwaitingOthers
	// MineViewAwaitingOtherThings: in flight but not actionable by me right
	// now. Reachable ONLY via the CI-pending sub-shape shipped here (approved,
	// clean, CI still pending) — see ClassifyMine's doc for why the
	// "waiting on another PR" sub-shape is deliberately absent.
	MineViewAwaitingOtherThings
)

// MineActNowFacts is the per-row raw-fact set backing ACT NOW's OR-composed
// predicate (pg2-4dz88.7.4 acceptance criterion 2). Each field is one clause;
// see mineViewFactsFor for how a PRInput is projected onto these booleans.
type MineActNowFacts struct {
	// HasConflict: api.PR.HasConflict() — a merge conflict.
	HasConflict bool
	// BlockingBotVerdict: an allowlisted approver's current, non-stale
	// pr_approval row reads "changes-requested". See blockingBotVerdict.
	BlockingBotVerdict bool
	// WIPReadyForPromotion: a WIP-flagged draft that now meets
	// pg2-4dz88.4.5's promotion predicate (minus the WIP gate itself, which
	// is exactly what is still blocking it). See wipReadyForPromotion.
	WIPReadyForPromotion bool
	// CIRed: CI is red/failing on this PR, policy-bot excluded.
	CIRed bool
	// OpenConversationOnly: this bead's new clause — an otherwise-clean PR
	// of mine blocked only by an unresolved review thread. See
	// OpenConversationOnly.
	OpenConversationOnly bool
}

// MineActNow is ACT NOW's OR-composed predicate: any one clause routes a row
// to ACT NOW.
func MineActNow(f MineActNowFacts) bool {
	return f.HasConflict ||
		f.BlockingBotVerdict ||
		f.WIPReadyForPromotion ||
		f.CIRed ||
		f.OpenConversationOnly
}

// MineViewFacts is the full per-row raw-fact set ClassifyMine consumes: ACT
// NOW's clauses (embedded) plus the additional facts the AWAITING-OTHER-
// THINGS carve-out needs.
type MineViewFacts struct {
	MineActNowFacts
	// HumanApproved: a standing human approval is present (the same fact
	// MineRow.HumanApproved carries — see mineViewFactsFor's doc for why this
	// is threaded in rather than recomputed).
	HumanApproved bool
	// MergeStateClean: api.PR.MergeStateStatus == "CLEAN".
	MergeStateClean bool
	// CIPending: the CI rollup (policy-bot excluded) reads "pending".
	CIPending bool
}

// ClassifyMine partitions f into exactly one of the three mine-panel
// membership views, applying the ACT NOW > AWAITING OTHERS > AWAITING OTHER
// THINGS precedence recorded on pg2-4dz88.7.4: a row whose raw facts satisfy
// more than one view's predicate always resolves to the higher-precedence
// one.
//
// AWAITING OTHER THINGS is implemented as ONLY the one shipped sub-shape —
// approved, clean, CI still pending (the acceptance criterion "ships only
// the CI-pending sub-shape") — rather than a second hand-rolled predicate
// that also has to carve AWAITING OTHERS's territory back out. AWAITING
// OTHERS is instead the LOGICAL COMPLEMENT of (ACT NOW OR that carve-out),
// mirroring sibling pg2-4dz88.7.3's "complement, not a second hand-rolled
// predicate" convention for team-panel membership. This also correctly
// resolves the bead's own explicitly-out-of-scope "waiting on another PR"
// sub-shape, for which no signal exists anywhere in this module (internal/
// snapshot, internal/sync, pkg/api — the team-panel comparator's upstream-
// of-another-PR sort key, pg2-4dz88.7.2, is an ORDERING key among already-
// admitted rows, not a membership signal): such a row's raw facts simply
// fall through to whichever of ACT NOW or AWAITING OTHERS its OTHER axes
// already put it in — exactly the fallback this bead's description names
// ("falls through to AWAITING OTHERS or ACT NOW depending on the other
// axes") — rather than landing nowhere and violating the exhaustive-
// partition acceptance criterion.
func ClassifyMine(f MineViewFacts) MineView {
	switch {
	case MineActNow(f.MineActNowFacts):
		return MineViewActNow
	case f.HumanApproved && f.MergeStateClean && f.CIPending:
		return MineViewAwaitingOtherThings
	default:
		return MineViewAwaitingOthers
	}
}

// OpenConversationOnly implements this bead's new ACT-NOW clause: an
// otherwise-clean PR of mine that is blocked only by an unresolved review
// thread routes to ACT NOW (Phillip's 2026-08-24 ruling via pg2-4dz88.7.1 —
// "go resolve the thread yourself or nudge whoever needs to close it").
//
// Gated narrowly, matching the honesty level of pg-pr's other read-seam
// signals (e.g. MineRow.NeedsMergeReminder's own doc comment), so this is
// never a false-positive nudge — every one of the following must hold:
//
//   - humanApproved: a standing human approval is present. The operator's
//     own framing of the scenario is "CI is green, APPROVED, and
//     conflict-free" — omitting this check would let an UNAPPROVED PR with
//     an unrelated stale thread misfire into ACT NOW via this clause alone
//     (corrected in this bead's 2026-08-24 grooming review).
//   - mergeStateStatus != "CLEAN" AND != "" — something is blocking, reusing
//     the existing field rather than inferring from absence. Empty is
//     GitHub's own REST-fallback degenerate value (see prview's
//     mergeStateSection doc comment, "empty is GitHub's own REST-fallback
//     degenerate value") — MergeStateStatus was simply never POPULATED on
//     that path, which is not evidence of anything blocking, so it must not
//     be allowed to satisfy "!= CLEAN" and fire this clause purely because
//     the field is unpopulated.
//   - ci == "success" — the CI rollup (policy-bot excluded, same
//     cirollup.Excluder mechanism used throughout this decomposition) reads
//     green.
//   - !hasConflict — api.PR.HasConflict() is false.
//   - at least one unresolved INLINE review thread: c.ThreadID != "" &&
//     !c.Resolved. A top-level issue comment never carries a ThreadID at all
//     (see commentsFromGHNode, pkg/provider/vcs/github/enrich.go — only
//     n.ReviewThreads.Nodes comments get ThreadID/Resolved populated), so
//     its Resolved field is always the zero value (false) and carries no
//     thread-resolution semantics whatsoever; the ThreadID gate is what
//     excludes it, not Resolved alone.
//
// This does NOT prove GitHub's exact block reason (pg-pr never fetches
// reviewDecision) — it proves every axis pg-pr CAN see is clean plus an
// unresolved thread exists. Documented honestly here, mirroring
// mergeStateSection's own candor about the REST-fallback degenerate value.
func OpenConversationOnly(humanApproved bool, mergeStateStatus string, ci string, hasConflict bool, comments []api.Comment) bool {
	if !humanApproved {
		return false
	}
	if mergeStateStatus == "" || mergeStateStatus == "CLEAN" {
		return false
	}
	if ci != "success" {
		return false
	}
	if hasConflict {
		return false
	}
	for _, c := range comments {
		if c.ThreadID != "" && !c.Resolved {
			return true
		}
	}
	return false
}

// blockingBotVerdict reports whether any ALLOWLISTED approver's current
// (non-stale) approval row reads "changes-requested" — a currently-standing
// bot disapproval. Filters PRInput.Approvals (store.Approval) to rows whose
// Approver is in allowlist (config.Config.ApproverAllowlist), the SAME read
// path as sibling pg2-4dz88.7.3's team-panel bot-verdict clause. Deliberately
// NOT internal/agentregistry.Registry.IsAgent's set — a different,
// independently-configured signal that this config's own doc comment treats
// as expected to diverge from the allowlist; reusing it here would silently
// miss or misclassify a real bot-verdict row whenever the two sets disagree.
//
// Staleness (!Approval.IsStale(p.PR.HeadSHA)) is checked because every other
// consumer of pr_approval rows in this codebase does — classifyApprovals in
// this same file, and internal/sync/draft_promotion.go's
// draftPromoteBlockedByWIPOrBotVerdict — a stale row no longer represents a
// CURRENT verdict against the PR's current head. Neither this bead's own
// body nor sibling .7.3 (itself still unimplemented as of this writing)
// states the staleness question explicitly for this exact clause; treating
// a stale bot disapproval as withdrawn is an implementer's default
// consistent with the rest of the codebase, not a re-litigation of .7.3's own
// open staleness fork for the team-panel case.
func blockingBotVerdict(p PRInput, allowlist map[string]struct{}) bool {
	if len(allowlist) == 0 {
		return false
	}
	for _, a := range p.Approvals {
		if _, ok := allowlist[a.Approver]; !ok {
			continue
		}
		if a.State == "changes-requested" && !a.IsStale(p.PR.HeadSHA) {
			return true
		}
	}
	return false
}

// wipReadyForPromotion implements ACT NOW's WIP clause: "a WIP-flagged draft
// that now meets the promotion predicate" (pg2-4dz88.7's design). The
// PROMOTION action itself lives in internal/sync/draft_promotion.go's
// maybePromoteDraft, gated (among other things) on the SAME WIP flag this
// clause reads — WIP is exactly what is still blocking maybePromoteDraft even
// though every OTHER promotion gate is satisfied, so "meets the promotion
// predicate" here reads as "meets it MINUS the WIP gate that is the only
// thing currently stopping it": draft, WIP on, no conflict, CI green
// (policy-bot excluded), and no blocking bot verdict.
//
// Unlike the sync-layer gate (draftPromoteBlockedByConflict), this reads
// hasConflict (api.PR.HasConflict()) directly rather than fail-closing on an
// UNKNOWN merge state — that fail-closed rule is scoped by its own operator
// ruling (pg2-4dz88.4.5) to "is it safe to make an upstream write on the
// author's behalf", a question this read-only dashboard signal never asks;
// it mirrors ACT NOW's own first clause (api.PR.HasConflict()) instead, for
// consistency with every other clause in this predicate.
func wipReadyForPromotion(p PRInput, ci string, hasConflict bool, allowlist map[string]struct{}) bool {
	if !p.PR.Draft || !p.WIP {
		return false
	}
	if hasConflict || ci != "success" {
		return false
	}
	return !blockingBotVerdict(p, allowlist)
}

// mineViewFactsFor derives f's raw facts from one active (non-merged) mine
// PRInput. excl is the repo's CI-rollup excluder (policy-bot etc., the same
// one Build derives for buildMineRow); allowlist is the allowlisted-approver
// set (config.Config.ApproverAllowlist, via BuilderInput.ApproverAllowlist)
// for the bot-verdict clauses. humanApproved is threaded in from the SAME
// classifyApprovals call Build already makes for this row (appr.Human > 0)
// rather than recomputed here, so this function's notion of "approved" can
// never diverge from MineRow.HumanApproved.
func mineViewFactsFor(p PRInput, excl *cirollup.Excluder, allowlist map[string]struct{}, humanApproved bool) MineViewFacts {
	ci := cirollup.Compute(p.CIRuns, excl).State
	hasConflict := p.PR.HasConflict()
	return MineViewFacts{
		MineActNowFacts: MineActNowFacts{
			HasConflict:          hasConflict,
			BlockingBotVerdict:   blockingBotVerdict(p, allowlist),
			WIPReadyForPromotion: wipReadyForPromotion(p, ci, hasConflict, allowlist),
			CIRed:                ci == "failure",
			OpenConversationOnly: OpenConversationOnly(humanApproved, p.PR.MergeStateStatus, ci, hasConflict, p.Comments),
		},
		HumanApproved:   humanApproved,
		MergeStateClean: p.PR.MergeStateStatus == "CLEAN",
		CIPending:       ci == "pending",
	}
}

// allowlistSet turns BuilderInput.ApproverAllowlist into a lookup set.
// Nil/empty input yields a nil map (blockingBotVerdict's len()==0 check
// treats that as "disabled", matching ApproverAllowlist's own documented
// absent-means-disabled default).
func allowlistSet(allowlist []string) map[string]struct{} {
	if len(allowlist) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(allowlist))
	for _, a := range allowlist {
		out[a] = struct{}{}
	}
	return out
}
