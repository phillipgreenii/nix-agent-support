// Package snapshot defines the JSON-serializable per-PR dashboard
// snapshot served by the pg-pr daemon's /api/v1/dashboard endpoint.
package snapshot

import (
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/freshness"
)

// Snapshot is the top-level dashboard payload.
//
// It carries BOTH halves of the freshness contract (pr-pool INV-FRESH-1): the
// as-of time (GeneratedAt) and the staleness verdict against a declared bound
// (StaleAfterSeconds / AgeSeconds / Stale). The bound is fixed when the snapshot
// is BUILT (it derives from the declared sync cadence); the verdict is stamped
// when the payload is SERVED, by WithFreshness — a snapshot held in memory ages
// while the daemon's next tick is pending or wedged, so "is this stale?" is only
// answerable at serve time, never at build time.
type Snapshot struct {
	GeneratedAt         time.Time `json:"generated_at"`
	SyncIntervalSeconds int       `json:"sync_interval_seconds"`
	// StaleAfterSeconds is the freshness BOUND: once the payload's age exceeds
	// it, the data must not be presented as current. Derived from
	// SyncIntervalSeconds via freshness.BoundSeconds, and emitted so a consumer
	// (or the external Grafana age panel) can see the yardstick the Stale flag
	// was judged against rather than hardcoding its own.
	StaleAfterSeconds int `json:"stale_after_seconds"`
	// AgeSeconds is now - GeneratedAt at the instant the payload was served,
	// floored at 0. Stamped by WithFreshness; zero on an unserved snapshot.
	AgeSeconds int `json:"age_seconds"`
	// Stale is the staleness FLAG: AgeSeconds has exceeded StaleAfterSeconds, so
	// every readiness/status signal in this payload is past its bound and MUST
	// NOT be treated as current. Stamped by WithFreshness.
	Stale bool      `json:"stale"`
	Mine  []MineRow `json:"mine"`
	Team  []TeamRow `json:"team"`
	// DroppedCount is the number of PRInputs Build() silently dropped this
	// pass — a draft PR not owned by me, or a non-mine non-draft PR matching
	// zero review-set reasons (pg2-4dz88.7.6) — purely observational over the
	// existing two-branch admitting switch in Build; it does not include the
	// pg2-ew4kf merged-retention drop (deliberate retention behavior, not
	// part of this count) or a Hidden PRInput (a different, user-initiated
	// exclusion, pg2-4dz88.4). Deliberately a plain int with NO `omitempty`:
	// it MUST serialize as the numeral 0 when nothing was dropped, never
	// vanish from the payload, so a consumer can distinguish "checked, zero
	// dropped" from "field absent."
	DroppedCount int `json:"dropped_count"`
}

// WithFreshness returns a shallow COPY of s with the serve-time half of the
// freshness contract stamped for the instant now: AgeSeconds and Stale, judged
// against the already-set StaleAfterSeconds bound.
//
// It copies rather than mutating because the held snapshot is shared across
// concurrent readers (snapshot.Store) and its age differs per request; mutating
// it in place would both race and back-date the next reader's verdict. The Mine
// and Team slices are shared with the original — the copy is for the freshness
// scalars only and callers MUST NOT mutate the rows through it.
//
// A snapshot whose StaleAfterSeconds was never set (a hand-built payload, or one
// decoded from an older producer) is judged against the default bound, so an
// unset bound can never read as "never stale".
func (s *Snapshot) WithFreshness(now time.Time) *Snapshot {
	out := *s
	if out.StaleAfterSeconds <= 0 {
		out.StaleAfterSeconds = freshness.BoundSeconds(out.SyncIntervalSeconds)
	}
	out.AgeSeconds = freshness.AgeSeconds(out.GeneratedAt, now)
	out.Stale = freshness.IsStale(out.GeneratedAt, now, out.StaleAfterSeconds)
	return &out
}

// MineRow is one row in the "My PRs" table.
type MineRow struct {
	Repo     string `json:"repo"`
	Number   int    `json:"number"`
	Title    string `json:"title"`
	URL      string `json:"url"`
	Draft    bool   `json:"draft"`
	CIStatus string `json:"ci_status"`
	// GateState is the approval-gate's own axis (INV-GATE-1), distinct from
	// CIStatus: one of "satisfied" | "partially-satisfied" | "unsatisfied".
	// Build never re-classifies a CI run to produce this value; it PROJECTS
	// the already-persisted verdict off the PR's latest revision
	// (store.Revision.GateState, schema v11 pg2-4dz88.2.5, written by sync's
	// gateStateFromSync via pg2-4dz88.2.6/.2.7) — the same source and the
	// same per-item facts CIStatus's own inputs ride alongside. Empty
	// (omitted) when the PR carries no gate observation at all: either no
	// revision has ever recorded one, or the persisted state is the store's
	// own "unknown" default. Per INV-GATE-2 an unmatched/absent gate MUST
	// read as unknown, never satisfied, and this omitted-field spelling is
	// how that floor surfaces here — never coerced to a positive state.
	//
	// This field carries no as-of/stale stamp of its own: like every other
	// fact in this row, it rides the SAME payload-level freshness contract
	// (Snapshot.GeneratedAt / WithFreshness, INV-ASOF-1/INV-ASOF-2/
	// INV-GATE-4) rather than a second, independently-computed staleness
	// story.
	GateState string `json:"gate_state,omitempty"`
	// GateStateN / GateStateM carry the gate's satisfied/total counts (e.g.
	// partially-satisfied(n,m) or unsatisfied(0,m)) — populated only when
	// GateState is "partially-satisfied" or "unsatisfied", mirroring
	// store.GateState.N/M and checkinterpret.Result.N/M exactly.
	GateStateN    int  `json:"gate_state_n,omitempty"`
	GateStateM    int  `json:"gate_state_m,omitempty"`
	HumanApproved bool `json:"human_approved"`
	AgentApproved bool `json:"agent_approved"`
	// HumanApprovers / AgentApprovers count the DISTINCT approvers whose
	// approval currently STANDS, split by whether the approver is a registered
	// agent. They are the per-approver facts (INV-APPROVAL-1): two approvers
	// approving reads as 2, which the HumanApproved/AgentApproved booleans
	// above structurally cannot express. Those booleans are RETAINED and
	// DERIVED (count > 0) because the wire keys are a consumer contract (the
	// external Grafana panel reads `human_approved`); they are no longer an
	// independent signal.
	HumanApprovers int  `json:"human_approvers"`
	AgentApprovers int  `json:"agent_approvers"`
	WaitingOnMe    bool `json:"waiting_on_me"`
	// MergeStateStatus is GitHub's authoritative merge-readiness (CLEAN/BLOCKED/
	// …); the mine panel shows it separately from CIStatus. (pg2-dwfld)
	MergeStateStatus string `json:"merge_state_status,omitempty"`
	// AutoMergeEnabled is true when GitHub auto-merge is armed.
	AutoMergeEnabled bool `json:"auto_merge_enabled"`
	// NeedsMergeReminder is true for MY PR that is ready to merge (CLEAN) but has
	// no auto-merge armed — the "you forgot to merge / arm automerge" nudge. (pg2-dwfld)
	NeedsMergeReminder bool       `json:"needs_merge_reminder"`
	JIRA               []JIRAItem `json:"jira"`
	Beads              []BeadItem `json:"beads"`
	// CoOwned marks a teammate-authored PR I have pushed commits onto (I can act
	// on it but did not open it). Rendered in the Mine panel with a badge.
	CoOwned bool `json:"co_owned,omitempty"`
	// HasConflicts is true when GitHub signals a merge conflict (CONFLICTING/DIRTY).
	// On a Mine-panel row (mine/co-owned) this IS the "resolve conflicts" nudge —
	// the panel is already scoped to PRs I can fix.
	HasConflicts bool `json:"has_conflicts,omitempty"`
	// Merged is true for a PR retained past merge under the
	// MergedRetentionWindow grace period rather than actively open/draft. A
	// surface renders this as de-emphasised (a greyed row or a "merged" tag —
	// deliberately NOT ANSI dim). Build sorts every Merged row below the
	// active ones in Mine (pg2-ew4kf).
	Merged bool `json:"merged,omitempty"`
	// Hidden mirrors the store's USER_HIDDEN flag (pg2-4dz88.4.3): a row only
	// reaches Mine/Team at all when Build's IncludeHidden input admitted it
	// (see BuilderInput.IncludeHidden); this field then carries the flag +
	// reason through so a consumer that opted in (e.g. `pg-pr open
	// --include-hidden`) can display why the operator hid it.
	Hidden bool `json:"hidden,omitempty"`
	// HiddenReason is the operator-supplied reason recorded with the hide, if
	// any. Empty when Hidden is false, or when no reason was given.
	HiddenReason string `json:"hidden_reason,omitempty"`

	// DependencyBlockedBy, DependencyBlockedByUnresolvedRef,
	// DependencyUnblockedFrom and DependencyOrderingKey are the PR-dependency
	// annotation (pg2-4dz88.3.7), projected once per Build call from
	// prdeps.DeriveWithNativeStack's whole-set Graph. See the identically
	// named TeamRow fields for the shared contract; all four are the zero
	// value (and thus omitted) for a PR with no derivable relation
	// (prdeps.ResolutionTrunk and friends), so a Trunk-resolved row is
	// byte-for-byte unchanged from before this bead.
	DependencyBlockedBy string `json:"dependency_blocked_by,omitempty"`
	// DependencyBlockedByUnresolvedRef mirrors the TeamRow field of the same
	// name; see its doc.
	DependencyBlockedByUnresolvedRef string `json:"dependency_blocked_by_unresolved_ref,omitempty"`
	// DependencyUnblockedFrom mirrors the TeamRow field of the same name; see
	// its doc.
	DependencyUnblockedFrom string `json:"dependency_unblocked_from,omitempty"`
	// DependencyOrderingKey mirrors the TeamRow field of the same name; see
	// its doc.
	DependencyOrderingKey int `json:"dependency_ordering_key,omitempty"`
}

// TeamRow is one row in the "PRs to Review" table (the not-mine review set:
// team-authored ∪ review-requested-of-me ∪ reviewed-by-me ∪ assigned-to-me ∪
// watch-labeled). The JSON key stays "team" for consumer compatibility (the
// external Grafana panel queries .team).
type TeamRow struct {
	Repo     string `json:"repo"`
	Number   int    `json:"number"`
	Title    string `json:"title"`
	Owner    string `json:"owner"`
	URL      string `json:"url"`
	CIStatus string `json:"ci_status"`
	// GateState / GateStateN / GateStateM mirror the identically-named
	// MineRow fields; see their doc for the full contract (INV-GATE-1,
	// INV-GATE-2, INV-GATE-4).
	GateState     string `json:"gate_state,omitempty"`
	GateStateN    int    `json:"gate_state_n,omitempty"`
	GateStateM    int    `json:"gate_state_m,omitempty"`
	HumanApproved bool   `json:"human_approved"`
	AgentApproved bool   `json:"agent_approved"`
	// HumanApprovers / AgentApprovers are the per-approver counts; see the
	// identically-named MineRow fields for the contract (INV-APPROVAL-1).
	HumanApprovers int        `json:"human_approvers"`
	AgentApprovers int        `json:"agent_approvers"`
	LinesChanged   int        `json:"lines_changed"`
	FilesChanged   int        `json:"files_changed"`
	JIRA           []JIRAItem `json:"jira"`
	// NeedsAttention flags a teammate PR that currently needs my review, derived
	// from the shared needsAttention predicate over persisted store facts. Stays
	// consistent with the open-attention-bead set (same predicate, same inputs).
	NeedsAttention bool `json:"needs_attention"`
	// AttentionReason is the (stable) reason string when NeedsAttention is true;
	// empty otherwise. See snapshot.AttentionReason* constants.
	AttentionReason string `json:"attention_reason,omitempty"`
	// MatchReason explains WHY this PR is in the review set: any of
	// MatchReasonTeamAuthored, MatchReasonReviewRequested,
	// MatchReasonReviewedByMe, MatchReasonAssignedToMe, and one
	// MatchReasonLabelPrefix+<label> per matched watch label. May be empty for a
	// PR the ingest surfaced but none of the reasons currently identify (e.g. a
	// review-requested PR before the B2 GraphQL node populates ReviewRequestedOfMe).
	MatchReason []string `json:"match_reason,omitempty"`
	// HasConflicts is true when GitHub signals a merge conflict; a conflicting
	// team PR is also dampened out of NeedsAttention (not worth reviewing until
	// the author rebases).
	HasConflicts bool `json:"has_conflicts,omitempty"`
	// BotDisapproved is true when an ALLOWLISTED approver (a login in
	// config.Config.ApproverAllowlist, threaded through as
	// BuilderInput.ApproverAllowlist — a set DELIBERATELY SEPARATE from the
	// agent-registration set AgentApproved/AgentApprovers use) currently holds
	// a STANDING "changes-requested" verdict on this PR. "Currently holds"
	// means not stale (store.Approval.IsStale against the PR's head) — a bot
	// disapproval of a superseded or host-dismissed head is treated as
	// WITHDRAWN, the same staleness treatment every other pr_approval reader
	// in this package already gives a row (INV-APPROVAL-3), rather than a
	// second, bot-specific staleness policy. See internal/snapshot/panels.go's
	// ActNow, the ONE predicate this field feeds (pg2-4dz88.7.3).
	BotDisapproved bool `json:"bot_disapproved,omitempty"`
	// Hidden / HiddenReason mirror the identically-named MineRow fields; see
	// their doc for the contract (pg2-4dz88.4.3).
	Hidden       bool   `json:"hidden,omitempty"`
	HiddenReason string `json:"hidden_reason,omitempty"`

	// DependencyBlockedBy names the OPEN/DRAFT PR in this input set that this
	// one is currently stacked on and waiting for — the ref this PR's native
	// stack entry or base-branch chain resolved to, in Ref.String()'s
	// `<repo>#<number>` form (prdeps.Node.Upstream, when Resolution is
	// prdeps.ResolutionUpstream). Presentation ruling #3 (pg2-4dz88.3.6/.3.7):
	// this is the FACT half of "rank lower, don't suppress" — the row renders
	// exactly like any other row plus this marker and the
	// DependencyOrderingKey effect; there is no grouped/collapsible stack
	// row. Empty for every other resolution.
	DependencyBlockedBy string `json:"dependency_blocked_by,omitempty"`
	// DependencyBlockedByUnresolvedRef names the ref this PR's native-or-base
	// chain resolved to when the winning target could NOT be turned into a
	// live edge (prdeps.ResolutionUpstreamOutOfSet) — either no PR in the set
	// heads that ref at all, or one does but is neither open/draft nor
	// merged. Per the out-of-set-upstream ruling, this is MARKER ONLY: no
	// fetch is ever made to pull the named PR into the set. It is still
	// populated (rather than left identical to "no relation at all") so a
	// consumer can render e.g. "blocked by <ref>, not otherwise tracked". The
	// value is a REF NAME (a branch name), not a `<repo>#<number>` — prdeps
	// has no PR identity to name here, only the branch it couldn't resolve.
	DependencyBlockedByUnresolvedRef string `json:"dependency_blocked_by_unresolved_ref,omitempty"`
	// DependencyUnblockedFrom names the PR this one was natively- or
	// base-chain-stacked on that has since MERGED (prdeps.Node.MergedUpstream,
	// set only when Resolution is prdeps.ResolutionUnblocked) — the
	// merged-middle ruling: this PR is no longer blocked by anything and is
	// NOT re-pointed to the merged PR's own upstream. Informational only;
	// DependencyOrderingKey is the zero value here, exactly like an
	// unrelated PR, because there is no live blocking relation left to rank
	// against.
	DependencyUnblockedFrom string `json:"dependency_unblocked_from,omitempty"`
	// DependencyOrderingKey is the ordering-key half of ruling #1
	// ("rank-lower, not suppress"): a PR that is waiting on another PR
	// (DependencyBlockedBy set) carries a value STRICTLY LOWER than the
	// value on the row for the PR it names — e.g. a PR one hop up a stack
	// gets -1 while the PR it waits on gets 0, and a PR two hops up gets -2
	// — so a comparator that sorts this key in DESCENDING order (the PR
	// with the numerically GREATEST key first) always places an upstream PR
	// ahead of anything waiting on it, satisfying "ordered after its
	// upstream" without this package ever sorting anything itself. It is
	// simply the negation of prdeps.Node.Depth, projected as-is (never
	// re-derived): 0 for every PR with no live blocking relation — Trunk,
	// Unblocked/merged-middle, the out-of-set marker, Foreign, Self,
	// Unresolvable, or a PR absent from this pass entirely — and -Depth for
	// a PR genuinely blocked on another PR in this set. This field is a KEY
	// ONLY: Build does not sort rows by it and no comparator lives in this
	// package; a later multi-key comparator (a different, later bead)
	// combines it with other signals.
	DependencyOrderingKey int `json:"dependency_ordering_key,omitempty"`
}

// JIRAItem is one resolved JIRA issue referenced by a PR.
type JIRAItem struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	State string `json:"state"`
	URL   string `json:"url"`
}

// BeadItem is one bead from the recursive dep tree of a merge-request bead.
type BeadItem struct {
	ID     string   `json:"id"`
	Title  string   `json:"title"`
	Status string   `json:"status"`
	Labels []string `json:"labels"`
	URL    string   `json:"url"`
}
