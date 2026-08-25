// Package prview assembles the consolidated single-PR view: one aggregate
// (View) that carries every axis of a tracked PR pg-pr knows about, built by
// the pure Assemble function from already-read facts.
//
// # Store-read default (pg2-4dz88.5.3)
//
// This package follows the STORE-read pattern `pg-pr pr list` already uses
// (cmd/pg-pr/pr_list.go's listOpenPRItems), not the live-provider-call
// pattern `pg-pr pr show` uses (cmd/pg-pr/pr.go's prShowCmd.RunE). Per
// `docs/behavior/invariants.md`'s `INV-READ-1`, the base machine read seam
// MUST read from the store with no network call and no store mutation; per
// `INV-ASOF-1`/`INV-ASOF-2`, the as-of/stale pair pg-pr publishes is only
// meaningful over a real persisted timestamp pg-pr itself judges, which a
// live call would make vacuous.
//
// Assemble itself makes NO IO calls of any kind — not even a store read. It
// takes every fact it needs as already-read input (mirroring
// internal/snapshot/builder.go's "Pure; no IO" contract for Build), so the
// actual store-open / SELECT plumbing is left for the CLI-wiring sibling that
// registers the `pr view` command to write, calling into Assemble directly in
// its own tests without going through IO. This mirrors internal/freshness's
// placement style: one small, pure, stdlib-adjacent leaf package.
//
// # New aggregate type, not api.PR or internal/snapshot's rows
//
// View is a NEW type, not a reuse of pkg/api.PR (documented as "the JSON
// shape returned by `pg-pr pr show`", with no fields for enrichment, CI
// rollup, feedback, revisions, ticket/bead links, or approvals) and not an
// extension of internal/snapshot's MineRow/TeamRow, which are dashboard-
// COLLAPSED aggregates (booleans, not per-approver detail) that this view's
// own scope forbids collapsing.
//
// # Explicit markers, never omitted keys
//
// No field on View carries `omitempty`. An axis whose underlying source is
// simply not present this call (e.g. no store row exists yet for this PR)
// renders as an explicit JSON `null` — a nil pointer, or a nil slice — never
// a dropped struct field, so a machine consumer can tell "unknown" apart from
// "false"/"empty". A nil slice and a non-nil empty slice are DIFFERENT JSON
// values (`null` vs `[]`) precisely because neither carries `omitempty`; that
// distinction is deliberate and is what makes "no data source at all" and
// "a data source that reports zero items" distinguishable on the wire.
//
// # Not-yet-existing axes
//
// Three axes — per-approver approvals with staleness, policy-bot state, and
// hide/WIP state — are each owned by a separate, not-yet-landed sibling epic
// elsewhere in this backlog. This bead does not block on them: View carries a
// field for each that ALWAYS renders the explicit UnavailableAxis marker
// (never derived from any input), so Assemble never errors or panics for
// lack of a real data source, and a later, separate change can replace the
// marker with real data once it lands.
package prview

import (
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/cirollup"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/freshness"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/beads"
)

// PRViewInput is the already-read facts Assemble needs for one PR. Every
// field is data some earlier IO step (a store SELECT, a `bd dep tree` call,
// a provider round-trip already performed by a different command) has
// already produced; Assemble reads them and performs no IO of its own.
//
// Modeled on internal/snapshot.PRInput (see builder.go), adapted to what this
// view actually needs:
//   - PR mirrors PRInput.PR exactly — the live-provider-shaped facts, always
//     given (a value, not a pointer, exactly like PRInput.PR), which is what
//     lets Assemble build an Identity/MergeState/CI section even when there is
//     no store row at all (acceptance criterion: "no store row -> still
//     produces a View from the provider-derived input alone").
//   - Store is the STORE-authoritative row (internal/store.PullRequest),
//     nil when no row exists yet for (repo, number) — the store-read-default
//     axes (Ownership, Enrichment) key off its presence.
//   - Revisions and Feedback mirror PRInput.Revisions and the PR's
//     store.Feedback rows (internal/store/feedback.go's ListFeedback) — both
//     store-only concepts with no live-provider equivalent.
//   - LinkedTicketKeys mirrors the established name and shape
//     internal/enrich.Input.LinkedTicketKeys already uses: ticket keys
//     extracted (by internal/ticketlink.Parse, purely, from branch/title/body
//     text) by an earlier step, not a live Jira fetch — keeping this
//     assembly step free of any network dependency, consistent with the
//     store-read-default posture even though ticket links are not literally
//     a store table.
//   - BeadLinks mirrors PRInput.BeadsDeps (pkg/beads.DepNode, from an
//     already-performed `bd dep tree` walk).
//   - CIRuns and ExcludedCIChecks mirror PRInput.CIRuns and
//     BuilderInput.ExcludedChecksByRepo (per-repo, here already scoped to
//     this PR's one repo) — internal/cirollup.Compute/NewExcluder are pure
//     (regexp compilation only), so building the CI rollup inside Assemble
//     adds no IO.
//   - Now is the instant the freshness verdict is judged against — threaded
//     in exactly like internal/snapshot.BuilderInput.GeneratedAt, so Assemble
//     never reads the clock itself.
//
// Approval data is deliberately ABSENT from this input: the
// per-approver-approvals-with-staleness axis is one of the three
// not-yet-existing axes (see the package doc), owned by a separate sibling
// epic, and there is currently no real data source in this view's own scope
// to vary View.Approvals against. When that sibling lands, it adds the
// field this input needs at that time.
type PRViewInput struct {
	// PR is the live-provider-shaped PR facts (see pkg/api.PR's doc comment).
	// Always given; a zero-value PR is the degenerate "nothing known yet"
	// case, not an error.
	PR api.PR
	// Store is the store-authoritative PR row, or nil when no row exists yet
	// for (PR.Repo, PR.Number). Ownership and Enrichment key off its presence.
	Store *store.PullRequest
	// Revisions is this PR's persisted revision timeline in ascending seq
	// order (internal/store.ListRevisions). nil means no store data at all;
	// a non-nil empty slice means the store was asked and reported zero
	// revisions — the two are deliberately NOT the same value (see the
	// package doc's "explicit markers" section).
	Revisions []store.Revision
	// Feedback is this PR's feedback rows (internal/store.ListFeedback). Same
	// nil-vs-empty distinction as Revisions.
	Feedback []store.Feedback
	// LinkedTicketKeys is the set of external ticket keys already extracted
	// for this PR (internal/ticketlink.Parse, via internal/enrich.Input's
	// established field name/shape). Same nil-vs-empty distinction.
	LinkedTicketKeys []string
	// BeadLinks is this PR's recursive merge-request bead dependency tree
	// (pkg/beads.DepNode, from an already-performed `bd dep tree` walk). Same
	// nil-vs-empty distinction.
	BeadLinks []beads.DepNode
	// CIRuns is this PR's CI runs, already fetched (pkg/api.CIRun). Same
	// nil-vs-empty distinction; zero runs is a real, defined rollup state
	// ("none" — internal/cirollup.Compute), not an unknown marker.
	CIRuns []api.CIRun
	// ExcludedCIChecks is this PR's repo's excluded_ci_checks regex patterns
	// (internal/config.RepoConfig), used to build the internal/cirollup
	// Excluder. nil/empty means nothing is excluded.
	ExcludedCIChecks []string
	// Now is the instant the freshness verdict is judged against. Threaded in
	// (never read from the clock inside Assemble) so Assemble stays pure —
	// mirrors internal/snapshot.BuilderInput.GeneratedAt.
	Now time.Time
}

// IdentityState is the PR's identity and lifecycle-state axis, sourced
// directly from the live-provider-shaped PR facts (PRViewInput.PR) — never
// from the store — so it is populated even when no store row exists yet.
//
// State and Draft are DELIBERATELY carried as two separate fields rather than
// collapsed the way cmd/pg-pr/pr.go's renderPR collapses them (Draft &&
// State=="open" -> State="draft") for human-readable text output. See
// TestAssemble_PreservesDraftSeparatelyFromState: collapsing here would throw
// away information a machine consumer needs (State stays GitHub's own
// open/closed/merged vocabulary; Draft stays independently addressable), and
// would create inconsistent semantics against internal/store.PullRequest.State,
// which already stores "draft" as its own distinct value at ingest time (see
// internal/store's ListOpenPRs: `state IN ('open','draft')`) — collapsing
// again here, over the live-provider shape, would just be a second,
// differently-timed collapse of the same fact.
type IdentityState struct {
	Repo   string `json:"repo"`
	Number int    `json:"number"`
	Title  string `json:"title"`
	// Body is the PR's own description text (api.PR.Body), sourced directly
	// from PRViewInput.PR exactly like Title. Regression fix (pg2-1o1dp): the
	// `pr show`/`pr info` -> `pr view` consolidation dropped it entirely —
	// the retired `pr show` used to marshal the live-provider api.PR (which
	// always carried Body) directly, but Assemble never copied it onto
	// Identity. No omitempty, consistent with the rest of this type: an
	// unmerged/no-store PR's empty description renders as "" like Title
	// does, not as a distinct unknown marker.
	Body         string   `json:"body"`
	State        string   `json:"state"`
	Draft        bool     `json:"draft"`
	Branch       string   `json:"branch"`
	Base         string   `json:"base"`
	Author       string   `json:"author"`
	URL          string   `json:"url"`
	HeadSHA      string   `json:"head_sha"`
	BaseSHA      string   `json:"base_sha"`
	Merged       bool     `json:"merged"`
	MergedAt     string   `json:"merged_at"`
	Additions    int      `json:"additions"`
	Deletions    int      `json:"deletions"`
	ChangedFiles int      `json:"changed_files"`
	Labels       []string `json:"labels"`
}

// MergeState is GitHub's merge-readiness/conflict axis, sourced directly from
// PRViewInput.PR (see pkg/api.PR's Mergeable/MergeStateStatus/AutoMergeEnabled
// doc comments and PR.HasConflict()). Mergeable and MergeStateStatus are
// empty on GitHub's REST fallback path — a real, documented degenerate value,
// not something this axis needs its own unknown marker for.
type MergeState struct {
	Mergeable        string `json:"mergeable"`
	MergeStateStatus string `json:"merge_state_status"`
	AutoMergeEnabled bool   `json:"auto_merge_enabled"`
	HasConflict      bool   `json:"has_conflict"`
}

// CIRollup is the CI-health rollup axis, computed via internal/cirollup.Compute
// exactly as internal/snapshot/builder.go does for MineRow/TeamRow. Given json
// tags of its own (cirollup.Rollup has none) so the wire shape stays
// snake_case like the rest of View. Zero runs is a real, defined value
// (State "none"), so this field is never nil — the "absent" test case for
// this axis is "no CI runs were given," not "unknown."
type CIRollup struct {
	State   string `json:"state"`
	Passed  int    `json:"passed"`
	Failed  int    `json:"failed"`
	Pending int    `json:"pending"`
}

// Enrichment is the computed enrichment axis (kind/size/languages/urgency),
// sourced from the STORE row (internal/store.PullRequest) — never computed
// live, matching this view's store-read-default posture. nil when no store
// row exists yet for this PR (see View.Enrichment).
type Enrichment struct {
	Kind           string   `json:"kind"`
	Languages      []string `json:"languages"`
	Size           string   `json:"size"`
	Urgency        string   `json:"urgency"`
	UrgencyScore   int      `json:"urgency_score"`
	UrgencyReasons []string `json:"urgency_reasons"`
}

// FeedbackItem is one feedback row (internal/store.Feedback), reduced to the
// fields relevant to a consolidated view. Deliberately excludes the
// agent-owned disposition/reply-delivery bookkeeping fields
// (DispositionNote, ReplyBody, ResponseID, RetryCount, ManagedUpstream, …) —
// DispositionAction alone says whether/how the item was resolved, which is
// what a view of the PR's current state needs; the rest is process
// bookkeeping for the agent that owns replying, not this view's concern.
type FeedbackItem struct {
	ID                int64  `json:"id"`
	Kind              string `json:"kind"`
	Status            string `json:"status"`
	Title             string `json:"title"`
	Body              string `json:"body"`
	AuthorLogin       string `json:"author_login"`
	AuthorKind        string `json:"author_kind"`
	Severity          string `json:"severity"`
	File              string `json:"file"`
	Line              int    `json:"line"`
	IsOutdated        bool   `json:"is_outdated"`
	IsMinimized       bool   `json:"is_minimized"`
	ThreadResolved    bool   `json:"thread_resolved"`
	DispositionAction string `json:"disposition_action"`
	Link              string `json:"link"`
}

// RevisionItem is one observed revision (internal/store.Revision), reduced to
// the fields relevant to a consolidated view. Deliberately excludes
// OthersApproved/OthersApprovedAt/ReviewedAt/MyReviewState: store.Revision's
// own doc comment marks those WRITE-ONLY as of pg2-4dz88.1.9 — nothing outside
// the store package reads them any more, replaced by the per-approver
// store.Approval rows (which this view's Approvals axis deliberately defers;
// see the package doc). ReviewedByAgentAt (the pg2-4c5i.36 agent-review
// cursor) was dropped entirely by pg2-ynhr.5, superseded by pr-pool's bead
// head_sha cursor.
type RevisionItem struct {
	Seq        int    `json:"seq"`
	HeadSHA    string `json:"head_sha"`
	BaseSHA    string `json:"base_sha"`
	ObservedAt string `json:"observed_at"`
	LastSeenAt string `json:"last_seen_at"`
	CIState    string `json:"ci_state"`
	CIPassed   int    `json:"ci_passed"`
	CIFailed   int    `json:"ci_failed"`
	CIPending  int    `json:"ci_pending"`
	GateState  string `json:"gate_state"`
	GateStateN int    `json:"gate_state_n"`
	GateStateM int    `json:"gate_state_m"`
}

// BeadLinkItem is one bead from the recursive dep tree of this PR's
// merge-request bead, mirroring internal/snapshot/builder.go's BeadItem
// shape/URL convention exactly (bd://<id>).
type BeadLinkItem struct {
	ID     string   `json:"id"`
	Title  string   `json:"title"`
	Status string   `json:"status"`
	Labels []string `json:"labels"`
	URL    string   `json:"url"`
}

// Axis reason strings for UnavailableAxis — stable names a consumer can
// filter/report on, distinct from the axis's eventual field name so a later
// wiring change can rename the View field without changing this identifier.
const (
	AxisApprovals = "approvals-with-staleness"
	AxisPolicyBot = "policy-bot"
	AxisHideWIP   = "hide-or-wip-state"
)

// UnavailableAxis marks a View axis whose real computation does not exist in
// pg-pr yet (see the package doc's "Not-yet-existing axes"). It is a
// DIFFERENT marker from a nil pointer/nil slice on an EXISTING axis (e.g. "no
// store row yet"): a nil pointer here would be ambiguous with that
// data-not-present-THIS-time case and could carry no machine-readable reason,
// so a consumer could not tell "pg-pr has no data for this PR right now" from
// "this feature has literally never been implemented." Available is always
// false and Reason is always one of the Axis* constants — Assemble never
// varies either, because (per the bead's own testing plan) there is no real
// data source yet to vary them against.
type UnavailableAxis struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason"`
}

func unavailable(reason string) UnavailableAxis {
	return UnavailableAxis{Available: false, Reason: reason}
}

// View is the consolidated single-PR view: one exported field per axis, each
// grounded in PRViewInput as documented on the corresponding type above. See
// the package doc for the "no omitempty / explicit marker" and
// "not-yet-existing axis" rulings this type follows.
type View struct {
	// AsOf is this view's as-of time — the store row's last_synced_at,
	// verbatim RFC3339 UTC (empty when there is no store row), mirroring
	// cmd/pg-pr/pr_list.go's prListItem.LastSyncedAt. Per INV-ASOF-1, an
	// item with no usable as-of time is reported Stale, never silently
	// treated as current.
	AsOf string `json:"as_of"`
	// Stale is the freshness verdict against pg-pr's own bound
	// (internal/freshness.BoundSeconds/IsStale), computed against Now —
	// never re-derived by a consumer (INV-ASOF-2).
	Stale bool `json:"stale"`

	Identity   IdentityState `json:"identity"`
	Ownership  *string       `json:"ownership"`
	Enrichment *Enrichment   `json:"enrichment"`
	CI         CIRollup      `json:"ci"`
	MergeState MergeState    `json:"merge_state"`

	Feedback         []FeedbackItem `json:"feedback"`
	Revisions        []RevisionItem `json:"revisions"`
	LinkedTicketKeys []string       `json:"linked_ticket_keys"`
	BeadLinks        []BeadLinkItem `json:"bead_links"`

	// Not-yet-existing axes (see the package doc). Always the explicit
	// UnavailableAxis marker; never derived from PRViewInput.
	Approvals UnavailableAxis `json:"approvals"`
	PolicyBot UnavailableAxis `json:"policy_bot"`
	HideWIP   UnavailableAxis `json:"hide_wip"`
}

// Assemble builds a View from already-read PR facts. Pure; no IO — mirrors
// internal/snapshot/builder.go's Build contract. Never errors and never
// panics, including on a zero-value PRViewInput{} (see
// TestAssemble_ZeroValueInputDoesNotPanic).
func Assemble(in PRViewInput) View {
	excl := cirollup.NewExcluder(in.ExcludedCIChecks)
	rollup := cirollup.Compute(in.CIRuns, excl)

	asOf := ""
	if in.Store != nil {
		asOf = in.Store.LastSyncedAt
	}
	bound := freshness.BoundSeconds(0)
	stale := freshness.IsStale(freshness.ParseAsOf(asOf), in.Now, bound)

	return View{
		AsOf:  asOf,
		Stale: stale,

		Identity: IdentityState{
			Repo:         in.PR.Repo,
			Number:       in.PR.Number,
			Title:        in.PR.Title,
			Body:         in.PR.Body,
			State:        in.PR.State,
			Draft:        in.PR.Draft,
			Branch:       in.PR.Branch,
			Base:         in.PR.Base,
			Author:       in.PR.Author,
			URL:          in.PR.URL,
			HeadSHA:      in.PR.HeadSHA,
			BaseSHA:      in.PR.BaseSHA,
			Merged:       in.PR.Merged,
			MergedAt:     in.PR.MergedAt,
			Additions:    in.PR.Additions,
			Deletions:    in.PR.Deletions,
			ChangedFiles: in.PR.ChangedFiles,
			Labels:       in.PR.Labels,
		},
		Ownership:  ownershipAxis(in.Store),
		Enrichment: enrichmentAxis(in.Store),
		CI: CIRollup{
			State:   rollup.State,
			Passed:  rollup.Passed,
			Failed:  rollup.Failed,
			Pending: rollup.Pending,
		},
		MergeState: MergeState{
			Mergeable:        in.PR.Mergeable,
			MergeStateStatus: in.PR.MergeStateStatus,
			AutoMergeEnabled: in.PR.AutoMergeEnabled,
			HasConflict:      in.PR.HasConflict(),
		},

		Feedback:         mapFeedback(in.Feedback),
		Revisions:        mapRevisions(in.Revisions),
		LinkedTicketKeys: in.LinkedTicketKeys,
		BeadLinks:        mapBeadLinks(in.BeadLinks),

		Approvals: unavailable(AxisApprovals),
		PolicyBot: unavailable(AxisPolicyBot),
		HideWIP:   unavailable(AxisHideWIP),
	}
}

// ownershipAxis returns the store-read-default ownership marker: nil when no
// store row exists yet, else a pointer to the row's raw ownership string
// ("mine"|"co-owned"|"team", per store.PullRequest.Ownership's doc comment).
func ownershipAxis(row *store.PullRequest) *string {
	if row == nil {
		return nil
	}
	v := row.Ownership
	return &v
}

// enrichmentAxis returns the store-read-default enrichment marker: nil when
// no store row exists yet, else the row's persisted enrichment fields.
func enrichmentAxis(row *store.PullRequest) *Enrichment {
	if row == nil {
		return nil
	}
	return &Enrichment{
		Kind:           row.Kind,
		Languages:      row.Languages,
		Size:           row.Size,
		Urgency:        row.Urgency,
		UrgencyScore:   row.UrgencyScore,
		UrgencyReasons: row.UrgencyReasons,
	}
}

// mapFeedback preserves the nil-vs-non-nil-empty distinction: nil in means
// "no store data at all" (stays nil, marshals to JSON null); a non-nil
// (possibly empty) slice in means "the store was asked and this is what it
// reported" (stays non-nil, marshals to JSON [] when empty).
func mapFeedback(in []store.Feedback) []FeedbackItem {
	if in == nil {
		return nil
	}
	out := make([]FeedbackItem, 0, len(in))
	for _, f := range in {
		out = append(out, FeedbackItem{
			ID:                f.ID,
			Kind:              f.Kind,
			Status:            f.Status,
			Title:             f.Title,
			Body:              f.Body,
			AuthorLogin:       f.AuthorLogin,
			AuthorKind:        f.AuthorKind,
			Severity:          f.Severity,
			File:              f.File,
			Line:              f.Line,
			IsOutdated:        f.IsOutdated,
			IsMinimized:       f.IsMinimized,
			ThreadResolved:    f.ThreadResolved,
			DispositionAction: f.DispositionAction,
			Link:              f.Link,
		})
	}
	return out
}

// mapRevisions preserves the same nil-vs-non-nil-empty distinction as
// mapFeedback.
func mapRevisions(in []store.Revision) []RevisionItem {
	if in == nil {
		return nil
	}
	out := make([]RevisionItem, 0, len(in))
	for _, r := range in {
		out = append(out, RevisionItem{
			Seq:        r.Seq,
			HeadSHA:    r.HeadSHA,
			BaseSHA:    r.BaseSHA,
			ObservedAt: r.ObservedAt,
			LastSeenAt: r.LastSeenAt,
			CIState:    r.CIState,
			CIPassed:   r.CIPassed,
			CIFailed:   r.CIFailed,
			CIPending:  r.CIPending,
			GateState:  r.GateState,
			GateStateN: r.GateStateN,
			GateStateM: r.GateStateM,
		})
	}
	return out
}

// mapBeadLinks preserves the same nil-vs-non-nil-empty distinction as
// mapFeedback, mirroring internal/snapshot/builder.go's mapBeads URL
// convention (bd://<id>).
func mapBeadLinks(in []beads.DepNode) []BeadLinkItem {
	if in == nil {
		return nil
	}
	out := make([]BeadLinkItem, 0, len(in))
	for _, d := range in {
		out = append(out, BeadLinkItem{
			ID:     d.ID,
			Title:  d.Title,
			Status: d.Status,
			Labels: d.Labels,
			URL:    "bd://" + d.ID,
		})
	}
	return out
}
