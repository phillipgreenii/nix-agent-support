package sync

import (
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/verdict"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
)

// botVerdictApproval is one allowlisted approver's resolved verdict for the
// current ingest cycle, derived from the single LATEST (by
// commentEffectiveTime) top-level PR comment that approver posted. See
// botVerdictApprovals for the full resolution rule.
type botVerdictApproval struct {
	Approver string
	Result   verdict.Result
	// ObservedAt is commentEffectiveTime of the winning comment — the
	// timestamp persisted as pr_approval.observed_at.
	ObservedAt string
}

// commentEffectiveTime returns the timestamp used to order a comment for
// latest-wins resolution: UpdatedAt when present, else CreatedAt.
//
// Documented empty-updatedAt fallback (pg2-4dz88.1.6): some providers/test
// fixtures do not report updated_at for a comment that has never been
// edited (see api.Comment.UpdatedAt's doc), but its creation time is still
// a faithful "as of" timestamp for latest-wins purposes — falling back to
// it (rather than, say, treating an empty updatedAt as "always loses" or
// "always wins") keeps an unedited comment competitive against an edited
// one from the same login.
//
// Both fields are RFC3339 strings produced by this codebase's providers, so
// plain string comparison orders them correctly (see revision_test.go's
// fixedRFC3339 precedent).
func commentEffectiveTime(c api.Comment) string {
	if c.UpdatedAt != "" {
		return c.UpdatedAt
	}
	return c.CreatedAt
}

// approverAllowlistSet builds a login membership set from
// cfg.ApproverAllowlist for O(1) lookup in botVerdictApprovals.
func approverAllowlistSet(logins []string) map[string]bool {
	set := make(map[string]bool, len(logins))
	for _, l := range logins {
		set[l] = true
	}
	return set
}

// buildVerdictClassifier converts cfg's ORDERED VerdictGenerations
// (internal/config.Config.VerdictGenerations) into internal/verdict's own
// Generation type and compiles them into a *verdict.Classifier.
//
// internal/verdict deliberately does not import internal/config (see
// verdict.go's package doc, "a pure classifier package takes plain data");
// this is the conversion its own doc comment calls out as a separate,
// out-of-scope sibling leaf's job — this bead (pg2-4dz88.1.6) is that leaf.
//
// Declaration order is preserved element-for-element: config.go's own doc
// on VerdictGenerations says order is load-bearing for verdict.Classify's
// "highest declared generation wins" tie-break, so this conversion must not
// reorder, sort, or otherwise disturb it.
//
// A nil/empty gens compiles to a Classifier that classifies every body
// FindingsUnknown/Absent, with no error (verdict.New's own documented
// zero-generations contract) — so a deployment with no verdict_generations
// configured keeps behaving exactly as before this leaf: botVerdictApprovals
// then never resolves a definite verdict for anyone, and no pr_approval row
// is ever written by this path.
func buildVerdictClassifier(gens []config.VerdictGeneration) (*verdict.Classifier, error) {
	converted := make([]verdict.Generation, len(gens))
	for i, g := range gens {
		converted[i] = verdict.Generation{
			ID:                g.ID,
			BodyMarker:        g.BodyMarker,
			FindingsPatterns:  g.FindingsPatterns,
			AuthorityPatterns: g.AuthorityPatterns,
		}
	}
	return verdict.New(converted)
}

// botVerdictApprovals resolves, for each ALLOWLISTED login, the single
// per-approver verdict that ingestFeedbackToStore should persist this
// ingest cycle — the latest-wins integration point tying together the
// updatedAt fetch (pkg/provider/vcs/github/enrich.go), the config-declared
// verdict grammar (internal/config.Config.VerdictGenerations), and the
// verdict parser (internal/verdict) into the per-approver table
// (internal/store.Approval) that internal/sync/revision.go's three
// GitHub-REVIEW-based sources (mySubmittedReviews, othersApprovedReviews,
// othersChangesRequestedReviews) already write into. This is a FOURTH,
// PARALLEL source into the SAME table, derived from a bot's own COMMENT
// body rather than a GitHub review object.
//
// # Allowlist decision (pg2-4dz88.1.6 — the decision this bead was asked to
// make and document)
//
// Gated SOLELY by cfg.ApproverAllowlist (a flat login list on
// internal/config.Config) — NOT by agentregistry.Registry.IsApprover, and
// NOT by a combination of the two. Justification:
//
//  1. The root design bead (pg2-4dz88.1)'s recorded operator ruling states,
//     verbatim: "Verdict grammar is CONFIG-DRIVEN, starting from a
//     configurable approver allowlist." That names the mechanism singularly,
//     and Config.ApproverAllowlist's own doc comment was written to
//     describe exactly this bead's purpose, almost verbatim: "the set of
//     logins whose verdict is allowed to count toward PR approval."
//  2. The config-schema leaf (pg2-4dz88.1.3) that introduced BOTH surfaces
//     in the same change explicitly required the allowlist to be "a
//     SEPARATE set from the agent-registration set" specifically so that a
//     registered/ingested agent that must remain findings-only (per that
//     leaf's own operator rulings) never implicitly counts toward approval.
//     Gating this path on agentregistry.Entry.Approver/IsApprover instead
//     would force every bot that should count as an approver to ALSO carry
//     a full agentregistry.Entry (with its own required Policy, and
//     optionally BodyMarker/ApprovalRegex) just to flip one bool —
//     re-coupling the two concerns pg2-4dz88.1.3 deliberately split apart.
//  3. agentregistry.Entry.Approver/IsApprover has ZERO callers anywhere in
//     this module as of this bead (verified: the OLD, still-live
//     ApprovalRegex mechanism in internal/snapshot/builder.go's
//     classifyApprovals calls IsAgent + MatchApproval, never IsApprover).
//     It is therefore not an established pattern this leaf would be
//     extending; ApproverAllowlist is the surface whose own documentation
//     already anticipates this exact consumer.
//
// A login absent from cfg.ApproverAllowlist NEVER produces an approval here,
// even when it is a registered, ingested agent with Approver: true — that
// combination (or widening the gate to an OR of both surfaces) is left for
// a future leaf to wire, if a deployment ever needs it.
//
// # Selection rule
//
// Only TOP-LEVEL PR comments (c.Path == "") are considered. The root
// design bead's corpus evidence shows bot verdict summaries are posted as
// top-level pr-comments, never inline diff comments; inline/thread
// comments are the unrelated feedback-fingerprinting machinery
// ingestFeedbackToStore already handles elsewhere.
//
// Comments are grouped by author login, filtered to allowlisted logins (a
// verdict from a non-allowlisted login is dropped before any ordering
// happens, so it can never win regardless of timestamp). Within each
// login's group, comments are walked in enriched.Comments' original slice
// order and the comment with the greatest-or-equal commentEffectiveTime
// replaces the running winner — so:
//
//   - Latest updatedAt wins outright, REGARDLESS of createdAt (the
//     adversarial case an empty updatedAt made unrepresentable before the
//     updatedAt-fetch leaf landed).
//   - An exact tie on commentEffectiveTime resolves to whichever comment
//     appears LATER in slice order — a simple, deterministic, explicitly
//     tested rule.
//
// The winning comment's body is classified exactly once via clf.Classify.
//
//   - Authority Approved or Withheld (a "definite" verdict) → returned as a
//     botVerdictApproval for that login.
//   - Authority Pending (a configured BodyMarker matched but no
//     generation's patterns resolved a Findings value) or Absent (no
//     configured BodyMarker appeared at all) → NO entry for that login.
//     This function deliberately does not invent a store state for
//     either case (see approverApprovalState's doc); the caller simply
//     never calls SetApproval for that login this cycle, leaving any
//     previously-recorded approval untouched — which is also why an
//     ordinary non-verdict comment (Absent) posted by an approver after
//     their real verdict comment can never silently erase it: it is
//     evaluated as this cycle's candidate winner only if it is, in fact,
//     the latest comment, and even then it simply produces no write rather
//     than a false one.
func botVerdictApprovals(comments []api.Comment, allowlist map[string]bool, clf *verdict.Classifier) []botVerdictApproval {
	winners := map[string]api.Comment{}
	order := make([]string, 0, len(allowlist)) // login first-seen order, for deterministic output

	for _, c := range comments {
		if c.Path != "" {
			continue // inline/thread comment — not a verdict summary
		}
		if !allowlist[c.Author] {
			continue
		}
		cur, exists := winners[c.Author]
		if !exists {
			order = append(order, c.Author)
			winners[c.Author] = c
			continue
		}
		if commentEffectiveTime(c) >= commentEffectiveTime(cur) {
			winners[c.Author] = c
		}
	}

	out := make([]botVerdictApproval, 0, len(order))
	for _, login := range order {
		c := winners[login]
		res := clf.Classify(c.Body)
		if res.Authority != verdict.Approved && res.Authority != verdict.Withheld {
			continue // Pending/Absent — no store-representable verdict; see doc above
		}
		out = append(out, botVerdictApproval{
			Approver:   login,
			Result:     res,
			ObservedAt: commentEffectiveTime(c),
		})
	}
	return out
}

// approverApprovalState maps a DEFINITE verdict.Result (Authority Approved
// or Withheld — callers MUST NOT pass a Pending/Absent result; see
// botVerdictApprovals's doc) onto the store's pr_approval.state
// CHECK-constrained enum (approved|changes-requested|commented;
// internal/store/migrate.go's v9 migration), which predates the two-axis
// verdict grammar and is shared with the GitHub-REVIEW-based sources in
// internal/sync/revision.go.
//
// Mapping (pg2-4dz88.1.6's documented, tested choice):
//
//   - Authority Approved → "approved".
//   - Authority Withheld → "changes-requested", REGARDLESS of Findings.
//     Withheld always means "this approver's verdict does not currently
//     stand as an approval" (verdict.go's own doc on Authority), which is
//     the CHANGES_REQUESTED-shaped state of the three the store allows —
//     including the Findings-Clean-but-Withheld case (e.g. an explicit
//     approval-blocked signal, or the grammar's own "clean but not
//     approved" contradiction default): even without a specific finding to
//     point at, the approver's authority is still withheld, which reads
//     closer to "changes requested" than to a neutral "commented".
//
// "commented" is deliberately UNUSED by this mapping: it is reserved for a
// genuine neutral GitHub review, and is never a truthful description of
// either a granted or a withheld bot verdict.
func approverApprovalState(r verdict.Result) string {
	if r.Authority == verdict.Approved {
		return "approved"
	}
	return "changes-requested"
}
