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
	Repo          string `json:"repo"`
	Number        int    `json:"number"`
	Title         string `json:"title"`
	URL           string `json:"url"`
	Draft         bool   `json:"draft"`
	CIStatus      string `json:"ci_status"`
	HumanApproved bool   `json:"human_approved"`
	AgentApproved bool   `json:"agent_approved"`
	WaitingOnMe   bool   `json:"waiting_on_me"`
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
}

// TeamRow is one row in the "PRs to Review" table (the not-mine review set:
// team-authored ∪ review-requested-of-me ∪ watch-labeled). The JSON key stays
// "team" for consumer compatibility (the external Grafana panel queries .team).
type TeamRow struct {
	Repo          string     `json:"repo"`
	Number        int        `json:"number"`
	Title         string     `json:"title"`
	Owner         string     `json:"owner"`
	URL           string     `json:"url"`
	CIStatus      string     `json:"ci_status"`
	HumanApproved bool       `json:"human_approved"`
	AgentApproved bool       `json:"agent_approved"`
	LinesChanged  int        `json:"lines_changed"`
	FilesChanged  int        `json:"files_changed"`
	JIRA          []JIRAItem `json:"jira"`
	// NeedsAttention flags a teammate PR that currently needs my review, derived
	// from the shared needsAttention predicate over persisted store facts. Stays
	// consistent with the open-attention-bead set (same predicate, same inputs).
	NeedsAttention bool `json:"needs_attention"`
	// AttentionReason is the (stable) reason string when NeedsAttention is true;
	// empty otherwise. See snapshot.AttentionReason* constants.
	AttentionReason string `json:"attention_reason,omitempty"`
	// MatchReason explains WHY this PR is in the review set: any of
	// MatchReasonTeamAuthored, MatchReasonReviewRequested, and one
	// MatchReasonLabelPrefix+<label> per matched watch label. May be empty for a
	// PR the ingest surfaced but none of the reasons currently identify (e.g. a
	// review-requested PR before the B2 GraphQL node populates ReviewRequestedOfMe).
	MatchReason []string `json:"match_reason,omitempty"`
	// HasConflicts is true when GitHub signals a merge conflict; a conflicting
	// team PR is also dampened out of NeedsAttention (not worth reviewing until
	// the author rebases).
	HasConflicts bool `json:"has_conflicts,omitempty"`
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
