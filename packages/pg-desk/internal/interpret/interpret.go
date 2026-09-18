// Package interpret implements pg-desk's interpret stage (stage 2 of the
// pipeline; docket pg2-2j5ac.32, Phase 9): a deterministic, injectable-clock,
// NO-LLM function from one entity's gathered Facts (packet 4) to an
// Interpretation — ownership, enrichment, base urgency, category, feedback
// dispositions, approvals, gate state, match reasons, panel placement, and
// ready-to-promote [docs/superpowers/specs/2026-09-09-pg-desk-and-connector-discovery-design.md
// "7.4 Interpret"].
//
// # Porting sources and their limits
//
// This packet's Contract names packages/pg-pr's internal/{snapshot,
// freshness,ownership,agentregistry,enrich,beadsbridge} and
// pkg/beads/{mergerequest,processingcycle,feedback}.go as porting sources.
// pg-desk's go.mod does not depend on packages/pg-pr at all (only on
// packages/pg-connector, for internal/gather's own test suite) — every
// concept below is therefore RE-IMPLEMENTED against gather.Facts' actual
// (JSON-only, stateless, single-shot) shape, not imported. Several of
// pg-pr's ported behaviors depend on data Facts simply does not carry:
//
//   - No revision/approval HISTORY: pg-pr's approval/gate-state staleness
//     (store.Approval.IsStale against a PRIOR head SHA) requires a
//     persisted per-approver history (internal/snapshot/builder.go's
//     classifyApprovals, attention.go's NeedsAttention). gather.Facts is one
//     point-in-time read with no history, and schema.PRReview carries no
//     head-SHA-at-review field — so approvals/bot-verdict here are computed
//     from the CURRENT pr_show read only, with no staleness axis. Documented
//     deviation.
//   - No agent registry: this packet's pinned Config fields (Contract
//     section) include approver_allowlist but not the full Agents/
//     AgentConfig list agentregistry.Registry classifies from. Per
//     agentregistry's own documented default ("a nil registry means no
//     agent is configured, so every approver counts as human"), this
//     package treats every reviewer as human — approvals.go never
//     distinguishes agent from human approvers.
//   - No gate-state data: pg-pr's GateState is PROJECTED from a persisted
//     revision's gate verdict, written by internal/sync/revision.go
//     (gateStateFromSync) — not a Phase 9 porting source, and not derivable
//     from schema.CIRun either (pkg/schema/ci.go deliberately does not
//     carry over api.CIRun's Description field, which
//     checkinterpret.ClassifyApprovalGate requires to parse a gate
//     fraction). GateState is therefore always "" (unknown) in Phase 9 —
//     matching INV-GATE-2 ("an unmatched/absent gate MUST read as unknown").
//   - No go-enry: enrich.detectLanguages uses the go-enry/go-enry/v2 module,
//     which is not a pg-desk go.mod dependency. Adding it would require
//     `go mod tidy` + gomod2nix regeneration, which needs network access
//     this task must not open on its own initiative. Languages are instead
//     detected via a small built-in file-extension map (urgency.go) — same
//     output shape (sorted by count desc, name asc), coarser recognition.
//   - No WIP: annotation-table WIP is a packet-8 read-time join (design
//     "Hidden and WIP are NOT interpreted"), so ready-to-promote and panel
//     placement omit the WIP clauses pg-pr's mine_panels.go carries (the
//     "wip on"/promotion-blocked-by-WIP shapes) — this package computes the
//     D15 predicate as it can see it (own PR, draft, checks green, no bot
//     disapproval, no conflict) and leaves the WIP gate to whatever joins
//     annotation at read time.
//   - No verdict-generation body parsing: internal/verdict.go (comment-body
//     verdict-marker grammar, config.VerdictGenerations' consumer in pg-pr)
//     is not a pinned porting source for this packet. Bot verdict here reads
//     GitHub's own Review.State directly for approver_allowlist logins
//     (APPROVED/CHANGES_REQUESTED), a strictly more reliable signal than
//     regex-classifying a comment body, when the reviewer acted through a
//     real GitHub review. verdict_generations remains a typed Config field
//     (as pinned) but is not consumed by this packet's own code — mirroring
//     config.go's own established "present but unused by this phase"
//     convention for Jira/TicketPatterns/AgentTrackerBackend/Sync.Mode.
//   - Category and feedback-disposition RULE SETS ("df-categorize",
//     "df-feedback") have no Go source anywhere in this repo to port from
//     (they are today's LLM/ccpool-prompt-driven classifiers in the private
//     deployment) — category.go and disposition.go are therefore NEW,
//     deterministic, config-vocabulary-driven implementations, not ports.
//
// Every one of the above is a "documented, cited deviation" the packet's own
// Validation section explicitly allows in place of byte-for-byte parity.
package interpret

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
)

// Clock supplies "now" to Interpret, injectable so tests can pin it (packet's
// own "clock is injectable ... never time.Now() called directly inside
// interpret logic" binding decision). Only Interpretation.AsOf depends on it
// — no scoring/classification rule in this package is time-dependent.
type Clock interface {
	Now() time.Time
}

// FixedClock is a Clock that always returns the same instant. Exported for
// every caller's tests (this package's own, and packet 6's).
type FixedClock time.Time

// Now implements Clock.
func (c FixedClock) Now() time.Time { return time.Time(c) }

// SystemClock is the production Clock, backed by time.Now().
type SystemClock struct{}

// Now implements Clock.
func (SystemClock) Now() time.Time { return time.Now() }

// Panel names — the five named panels the design's dashboard payload exposes
// (section 7.7's "the five named selectors": mine_act_now,
// mine_awaiting_others, mine_awaiting_other_things, team_act_now,
// team_blocked). PanelNone means the entity is not currently admitted to any
// panel (a merged PR of mine, a draft/reasonless team PR, or a "removed"
// entity) — mirroring pg-pr's silently-dropped DroppedCount branch.
const (
	PanelMineActNow              = "mine_act_now"
	PanelMineAwaitingOthers      = "mine_awaiting_others"
	PanelMineAwaitingOtherThings = "mine_awaiting_other_things"
	PanelTeamActNow              = "team_act_now"
	PanelTeamBlocked             = "team_blocked"
	PanelNone                    = ""
)

// Enrichment is the base (LLM-free) PR enrichment: kind, languages, size.
// Urgency is scored/reported separately (Urgency below) even though pg-pr's
// own enrich.Result bundles all four — the store's interpretation table (and
// this Contract) keeps "enrichment" and "urgency" as two distinct columns.
type Enrichment struct {
	Kind      string   `json:"kind"`
	Languages []string `json:"languages,omitempty"`
	Size      string   `json:"size"`
}

// Urgency is the base-urgency scoring result (labels, keywords, checks
// rollup, bugfix commits — the layered project-health/Jira signals are
// Phase 13, pg2-jpfw.5, and are never computed here).
type Urgency struct {
	Level   string   `json:"level"` // low | medium | high
	Score   int      `json:"score"`
	Reasons []string `json:"reasons,omitempty"`
}

// Disposition is one comment/thread's feedback disposition: the rule set's
// own verdict, or an operator/agent-recorded override that has won over it
// (Overridden true). See disposition.go's ApplyDispositionOverrides — this
// package's own Interpret computes only the RULE SET's base verdict; the
// override merge is a separate, exported step (see that function's doc for
// why: Interpret's pinned signature is (facts, clock, cfg) with no place to
// thread the store's annotation-table overrides through, so packet 6 — which
// alone reads the store — is expected to call ApplyDispositionOverrides
// itself, once per run, after calling Interpret).
type Disposition struct {
	CommentID  string `json:"comment_id"`
	Verdict    string `json:"verdict"` // open | will-fix | wont-fix | no-action
	Overridden bool   `json:"overridden,omitempty"`
}

// Approvals bundles approvals, bot verdict, and waiting-on-me — the design's
// own "Approvals, gate state, waiting-on-me, panel placement" grouping
// (section 7.4), minus gate state and panel (which are Interpretation's own
// top-level fields, since packet 3's store table carries them as separate
// columns).
type Approvals struct {
	// HumanApprovers is the count of distinct logins with a currently
	// APPROVED review. Every approver counts as human (see this package's
	// doc comment on the missing agent registry).
	HumanApprovers int  `json:"human_approvers"`
	HumanApproved  bool `json:"human_approved"`
	// BotVerdict is one of BotVerdictApproved/BotVerdictDisapproved/
	// BotVerdictNoDecision (approvals.go), read from approver_allowlist
	// logins' Review.State only.
	BotVerdict string `json:"bot_verdict"`
	// WaitingOnMe is true iff the anchor work bead's dependency tree
	// (Facts.Deps, `issue deps --full`) has at least one non-closed
	// dependency and every non-closed dependency carries the `human` label
	// — ported from pkg/beads.AllNonClosedHumanLabeled.
	WaitingOnMe bool `json:"waiting_on_me"`
}

// Interpretation is this package's own return type: the same set of concepts
// packet 3's interpretation table persists (design section 7.6), in
// ergonomic Go shapes for packet 6 to JSON-encode into that table's columns.
// It carries no sync_error field (this packet MUST NOT write it — packet
// 6/10's concern) and no hidden/WIP field (packet 8's read-time join).
type Interpretation struct {
	Ownership      string        `json:"ownership"`
	Enrichment     Enrichment    `json:"enrichment"`
	Urgency        Urgency       `json:"urgency"`
	Category       string        `json:"category"`
	Dispositions   []Disposition `json:"dispositions"`
	Approvals      Approvals     `json:"approvals"`
	GateState      string        `json:"gate_state"`
	MatchReasons   []string      `json:"match_reasons"`
	Panel          string        `json:"panel"`
	ReadyToPromote bool          `json:"ready_to_promote"`
	// Degraded is copied VERBATIM from Facts.Degraded (packet 4) — empty
	// means healthy, non-empty names the failing gather input.
	Degraded string `json:"degraded"`
	// AsOf is this interpretation's own as-of time (RFC3339, UTC), stamped
	// from the injected Clock — not part of the Contract's bulleted field
	// list, but required for the packet's own deterministic-fixed-clock
	// test (same input + same injected time => byte-identical output) to
	// mean anything, and mirrored by store.Interpretation.AsOf.
	AsOf string `json:"as_of"`
}

// Interpret computes Interpretation for one entity from its gathered facts.
// Pure and deterministic: the only external input is clock.Now(), used
// solely to stamp AsOf.
//
// A facts value carrying no PRShow at all (gather's removedFacts not_found
// branch — see gather.Facts.RemovedState's doc) degrades gracefully to a
// near-empty Interpretation rather than erroring: gather's own doc treats
// not_found as an expected outcome, not a hard error, and this package must
// not reintroduce a hard failure gather deliberately avoided. A non-empty
// but malformed PRShow IS an error — everything downstream needs it.
func Interpret(facts gather.Facts, clock Clock, cfg *config.Config) (Interpretation, error) {
	now := clock.Now().UTC().Format(time.RFC3339)

	if len(facts.PRShow) == 0 {
		return Interpretation{Degraded: facts.Degraded, AsOf: now}, nil
	}

	pr, err := decodePRShow(facts.PRShow)
	if err != nil {
		return Interpretation{}, fmt.Errorf("interpret: decode pr show: %w", err)
	}

	commits := decodePRCommits(facts.PRCommits)
	files := decodePRFiles(facts.PRFiles)

	var selfLogin string
	var teamMembers, watchLabels, approverAllowlist []string
	var categoryVocab map[string][]string
	var urgencyCfg *config.UrgencyConfig
	var jiraCfg *config.JiraConfig
	var checkInterpreters []config.CheckInterpreterConfig
	if cfg != nil {
		selfLogin = cfg.SelfLogin
		teamMembers = cfg.TeamMembers
		watchLabels = cfg.WatchLabels
		approverAllowlist = cfg.ApproverAllowlist
		categoryVocab = cfg.CategoryVocabulary
		urgencyCfg = cfg.Urgency
		jiraCfg = cfg.Jira
		checkInterpreters = cfg.CheckInterpreters
	}

	commitAuthors := make([]string, 0, len(commits))
	for _, c := range commits {
		commitAuthors = append(commitAuthors, c.Author)
	}
	ownership := classifyOwnership(selfLogin, pr.Author, commitAuthors)

	ci := computeCIRollup(facts.CI, checkInterpreters)

	enrichment := computeEnrichment(pr, files, commits)
	// scoreUrgencyWithHealth fully replaces the base-only computeUrgency
	// call here (docket pg2-2j5ac.40, Phase 13's own freedom-boundary
	// choice — see urgency.go's doc comment): it degrades to
	// computeUrgency's own output byte-for-byte when there is no
	// cross-referenced Jira issue for this PR (facts.JiraIssues empty) or
	// jiraCfg is nil.
	jiraIssues := decodeJiraIssues(facts.JiraIssues)
	urgency := scoreUrgencyWithHealth(pr, commits, ci, urgencyCfg, jiraIssues, jiraCfg)
	category := classifyCategory(pr, categoryVocab)
	dispositions := computeDispositions(pr)
	approvals := computeApprovals(pr, approverAllowlist)
	approvals.WaitingOnMe = computeWaitingOnMe(facts.Deps)
	matchReasons := computeMatchReasons(pr, teamMembers, watchLabels, selfLogin)
	panel := classifyPanel(ownership, pr, ci, approvals, matchReasons)
	readyToPromote := computeReadyToPromote(ownership, pr, ci, approvals)

	return Interpretation{
		Ownership:      string(ownership),
		Enrichment:     enrichment,
		Urgency:        urgency,
		Category:       category,
		Dispositions:   dispositions,
		Approvals:      approvals,
		GateState:      "", // see package doc: no gate-state data source in Phase 9
		MatchReasons:   matchReasons,
		Panel:          panel,
		ReadyToPromote: readyToPromote,
		Degraded:       facts.Degraded,
		AsOf:           now,
	}, nil
}

// --- minimal decode shapes for gather.Facts' JSON payloads -----------------
//
// Mirrors gather.go's own decision to hand-decode a minimal field subset
// rather than importing packages/pg-connector/pkg/schema (see that file's
// prShowFields doc comment) — this package is not in the "no pkg/schema
// import" D10 chokepoint gather.go describes, but staying decoupled from
// pg-connector's Go API keeps this package testable with plain literals and
// consistent with the sibling packet's own precedent.

type prComment struct {
	ID       string `json:"id"`
	Author   string `json:"author"`
	Body     string `json:"body"`
	ThreadID string `json:"thread_id,omitempty"`
	Resolved bool   `json:"resolved"`
}

type prReview struct {
	ID       string      `json:"id"`
	Author   string      `json:"author"`
	State    string      `json:"state"`
	Body     string      `json:"body,omitempty"`
	Comments []prComment `json:"comments,omitempty"`
}

type prShow struct {
	Repo             string      `json:"repo"`
	Number           int         `json:"number"`
	Title            string      `json:"title"`
	State            string      `json:"state"`
	Branch           string      `json:"branch"`
	Base             string      `json:"base"`
	Author           string      `json:"author"`
	URL              string      `json:"url"`
	Draft            bool        `json:"draft"`
	Merged           bool        `json:"merged"`
	Body             string      `json:"body,omitempty"`
	Labels           []string    `json:"labels,omitempty"`
	Additions        int         `json:"additions,omitempty"`
	Deletions        int         `json:"deletions,omitempty"`
	Mergeable        string      `json:"mergeable,omitempty"`
	MergeStateStatus string      `json:"merge_state_status,omitempty"`
	ReviewRequests   []string    `json:"review_requests,omitempty"`
	Comments         []prComment `json:"comments,omitempty"`
	Reviews          []prReview  `json:"reviews,omitempty"`
}

// allComments returns every top-level and review-thread comment on the PR —
// "every comment and thread of the PR" (design's Interpret bullet for
// feedback dispositions).
func (p prShow) allComments() []prComment {
	out := append([]prComment{}, p.Comments...)
	for _, r := range p.Reviews {
		out = append(out, r.Comments...)
	}
	return out
}

// hasConflict ports api.PR.HasConflict() exactly: either mergeability enum
// (CONFLICTING) or merge-state status (DIRTY). UNKNOWN is not a conflict.
func (p prShow) hasConflict() bool {
	return p.Mergeable == "CONFLICTING" || p.MergeStateStatus == "DIRTY"
}

func decodePRShow(raw json.RawMessage) (prShow, error) {
	var p prShow
	if err := json.Unmarshal(raw, &p); err != nil {
		return prShow{}, err
	}
	return p, nil
}

type prCommit struct {
	SHA     string `json:"sha"`
	Author  string `json:"author"`
	Message string `json:"message,omitempty"`
}

type prCommitsResult struct {
	Commits []prCommit `json:"commits"`
}

// decodePRCommits soft-fails: a malformed/absent payload degrades to no
// commits rather than erroring the whole Interpret call — Facts.Degraded
// already names any gather-side failure; this stage never re-derives a hard
// error from data gather itself treated as a soft degradation.
func decodePRCommits(raw json.RawMessage) []prCommit {
	if len(raw) == 0 {
		return nil
	}
	var r prCommitsResult
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil
	}
	return r.Commits
}

type prFile struct {
	Path string `json:"path"`
}

type prFilesResult struct {
	Files []prFile `json:"files"`
}

func decodePRFiles(raw json.RawMessage) []prFile {
	if len(raw) == 0 {
		return nil
	}
	var r prFilesResult
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil
	}
	return r.Files
}

// toSet turns a string slice into a lookup set. nil/empty input yields a nil
// map — a nil map's zero-cost membership test (`_, ok := m[x]`) always
// reports false, matching every "absent config list disables this clause"
// default this package relies on.
func toSet(items []string) map[string]struct{} {
	if len(items) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(items))
	for _, i := range items {
		out[i] = struct{}{}
	}
	return out
}

// hasLabel reports whether want is present in labels. Ported from
// pkg/beads.hasLabel.
func hasLabel(labels []string, want string) bool {
	for _, l := range labels {
		if l == want {
			return true
		}
	}
	return false
}

// sortedKeys returns the keys of m, sorted ascending — used wherever a map
// (config vocabulary) must be walked in a deterministic order.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
