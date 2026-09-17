// Package sync implements pg-desk's sync stage (stage 3 of the pipeline;
// docket pg2-2j5ac.34, Phase 10): turning one PR's gathered facts and
// interpretation into agent-visible bead writes through `pg-connector
// issue`, deduplicated by keys carried IN the bead itself and cached in the
// store's ledger table [design doc "7.5 Sync"].
//
// sync.mode stays "plan" for the whole of Phase 10 (D17) — apply is
// switched on only at the Phase 11 flip — but this package implements all
// three modes (off/plan/apply) in full, since the design pins that as this
// phase's own scope, not a later one.
//
// # Adoption reuses gather's own work-beads call
//
// design section 7.5's first-run adoption sweep ("list open merge-request /
// process-feedback: / review-pr: beads via issue list --query work-beads
// ... on every run") is implemented by classifying Facts.WorkBeads — the
// SAME `issue list --query work-beads` fan-out result internal/gather's
// Gather already fetches every run (its own doc comment: "issue list
// --query work-beads ... filtered down to the entries that match this PR")
// — rather than this package issuing a second, redundant `issue list` call.
// See adoption.go.
//
// # Planned rows and the ledger's existing columns
//
// The design's freedom boundary pins the ledger table's EXISTING columns
// and the values written into them, and does not authorize a schema
// change here (this packet's own Files section does not name
// internal/store/ledger.go or internal/store/migrations.go). Since the
// ledger's primary key already partitions by `kind`, this package assigns
// each kind's own consistent meaning to the existing columns rather than
// adding new ones:
//
//   - bead_id: the real bead id once one exists (apply, or an adopted
//     pre-existing bead); empty ("", never NULL — the column is TEXT NOT
//     NULL) when sync.mode is "plan" and no bead exists yet. An empty
//     bead_id IS the "this is a planned, not-yet-applied row" signal
//     status/show render on (design section 7.7's "planned sync rows").
//   - last_synced_content_hash: a deterministic hash of the WRITE's own
//     field content (title/description/metadata/labels — excluding a
//     not-yet-resolved parent id, see rules.go), computed IDENTICALLY in
//     plan and apply mode for the same fixture. This is what makes "plan's
//     planned rows match, kind for kind, the writes a subsequent apply run
//     actually performs" (the design's own phase-10 sync-parity check)
//     mechanically checkable: two runs against the same input produce the
//     same hash regardless of which mode took the write branch.
//   - last_synced_at: this ledger row's own last-touched timestamp, in
//     every mode.
//   - last_reviewed_head_sha: the review-request kind's own pinned meaning
//     (design section 7.6), used only for kind=review-request rows;
//     unused for kind=anchor/feedback-cycle rows.
package sync

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/interpret"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// Mode names sync.mode's three values, verbatim from design section 7.5.
const (
	ModeOff   = "off"
	ModePlan  = "plan"
	ModeApply = "apply"
)

// Syncer runs the sync stage for one entity per Sync call. Not safe for
// concurrent Sync calls on the same entity — mirrors gather.Gatherer's own
// "not safe for concurrent Gather calls" contract and pipeline.Pipeline's
// own single-Gatherer-per-run convention.
type Syncer struct {
	cfg    *config.Config
	store  *store.Store
	client *issueClient
	clock  interpret.Clock
}

// Option configures a Syncer constructed by New.
type Option func(*Syncer)

// WithClock overrides the clock last_synced_at is stamped from. Production
// always uses the default (interpret.SystemClock{}); tests inject a fixed
// clock.
func WithClock(c interpret.Clock) Option {
	return func(s *Syncer) { s.clock = c }
}

// New constructs a Syncer backed by cfg and st. The caller owns st's
// lifecycle (open and Close); Syncer never closes it — mirrors
// pipeline.New's own contract for the store it is handed.
func New(cfg *config.Config, st *store.Store, opts ...Option) *Syncer {
	s := &Syncer{
		cfg:    cfg,
		store:  st,
		client: newIssueClient(cfg),
		clock:  interpret.SystemClock{},
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// mode resolves cfg.Sync.Mode, defaulting to "off" when unset (Phase 9 left
// sync.mode an unwired stub; an empty value MUST NOT be treated as "apply"
// or "plan" by accident).
func (s *Syncer) mode() string {
	if s.cfg == nil || s.cfg.Sync.Mode == "" {
		return ModeOff
	}
	return s.cfg.Sync.Mode
}

// Sync runs the sync stage for one PR entity: entityType=="pr" only — the
// pipeline (packet 6 of the Phase 9 docket, this packet's own Contract)
// gates the call to entityType=="pr" and to cfg.Sync.Mode != "off" before
// ever calling this method, but Sync repeats the mode check itself so it is
// safe to call unconditionally too.
func (s *Syncer) Sync(ctx context.Context, repo, entityID string, change gather.ChangeKind, facts gather.Facts, interp interpret.Interpretation) error {
	mode := s.mode()
	if mode == ModeOff {
		return nil
	}
	if mode != ModePlan && mode != ModeApply {
		return fmt.Errorf("sync: unknown sync.mode %q (want off, plan, or apply)", mode)
	}

	pr, prErr := decodePRShow(facts.PRShow)
	prOK := prErr == nil && len(facts.PRShow) > 0

	closeReason, isClosure := closureFromFacts(facts, pr, prOK)

	prNumber := pr.Number
	if !prOK {
		// A removed-not_found re-read carries no PRShow at all — fall back
		// to parsing the number out of entityID ("<repo>#<n>", resolvePRRef's
		// own qualified form) so the ledger lookup key is still correct.
		if n, ok := prNumberFromEntityID(entityID, repo); ok {
			prNumber = n
		}
	}

	rc := &runContext{
		syncer:   s,
		mode:     mode,
		repo:     repo,
		entityID: entityID,
		prNumber: prNumber,
		pr:       pr,
		prOK:     prOK,
		interp:   interp,
		headSHA:  facts.HeadSHA,
		now:      s.clock.Now().UTC().Format(rfc3339),
	}

	adopted := adoptFromWorkBeads(facts.WorkBeads, repo, prNumber)
	if err := rc.loadLedger(); err != nil {
		return err
	}
	rc.mergeAdoption(adopted)

	if isClosure {
		return rc.handleClosure(ctx, closeReason)
	}
	if !prOK {
		// Degraded/unknown PR state with no closure signal either — nothing
		// safe to decide; leave the ledger exactly as adoption found it.
		return nil
	}

	return rc.reconcile(ctx)
}

const rfc3339 = "2006-01-02T15:04:05Z07:00"

// closedSentinel is the last_synced_content_hash value this package writes
// for an anchor/feedback-cycle ledger row once it has been closed, so a
// later run does not keep re-issuing `issue transition ... closed` forever.
// Never a valid hex sha256 digest (those are exactly 64 lowercase hex
// chars), so it cannot collide with a real content hash.
const closedSentinel = "closed"

// closureFromFacts decides whether this run's facts confirm the PR is
// closed, per design section 7.3's removed-re-read rule and the Anchor
// rule's "CONFIRMED closure" wording: `merged` or `closed` from either the
// `--change removed` re-read (Facts.RemovedState) or an ordinary/sweep `pr
// show` read is a confirmed closure; `not_found` (the removed re-read's own
// outcome) is a closure with reason "gone"; `open` (from either source) is
// never a closure. reason is one of "merged"/"closed"/"gone".
func closureFromFacts(facts gather.Facts, pr prShowFields, prOK bool) (reason string, isClosure bool) {
	if facts.RemovedState != "" {
		switch facts.RemovedState {
		case "merged":
			return "merged", true
		case "closed":
			return "closed", true
		case "not_found":
			return "gone", true
		default: // "open"
			return "", false
		}
	}
	if !prOK {
		return "", false
	}
	if pr.Merged {
		return "merged", true
	}
	if pr.State != "" && pr.State != "open" {
		return "closed", true
	}
	return "", false
}

// prShowFields is the minimal subset of `pr show`'s result payload this
// package decodes for itself — mirroring internal/gather/gather.go's own
// prShowFields and internal/interpret/interpret.go's own prShow (each
// package hand-decodes its own minimal subset rather than importing
// pkg/schema; see gather.go's doc comment for why).
type prShowFields struct {
	Repo             string `json:"repo"`
	Number           int    `json:"number"`
	Title            string `json:"title"`
	State            string `json:"state"`
	Branch           string `json:"branch"`
	Base             string `json:"base"`
	Author           string `json:"author"`
	URL              string `json:"url"`
	Draft            bool   `json:"draft"`
	Merged           bool   `json:"merged"`
	Mergeable        string `json:"mergeable,omitempty"`
	MergeStateStatus string `json:"merge_state_status,omitempty"`
}

// hasConflict ports api.PR.HasConflict()'s exact rule (see
// internal/gather/gather.go's own hasConflict precedent, and
// packages/pg-pr/internal/sync/sync.go's original): either mergeability
// enum (CONFLICTING) or merge-state status (DIRTY). UNKNOWN is not a
// conflict.
func (p prShowFields) hasConflict() bool {
	return p.Mergeable == "CONFLICTING" || p.MergeStateStatus == "DIRTY"
}

func decodePRShow(raw json.RawMessage) (prShowFields, error) {
	var p prShowFields
	if len(raw) == 0 {
		return p, fmt.Errorf("sync: empty pr show payload")
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return p, err
	}
	return p, nil
}

// prNumberFromEntityID parses the trailing "#<n>" off entityID (the
// qualified "<repo>#<n>" form cmd/pg-desk's resolvePRRef and
// internal/gather's own prKey convention both use), returning ok=false if
// entityID does not start with repo+"#" followed by a valid integer.
func prNumberFromEntityID(entityID, repo string) (int, bool) {
	prefix := repo + "#"
	if len(entityID) <= len(prefix) || entityID[:len(prefix)] != prefix {
		return 0, false
	}
	var n int
	if _, err := fmt.Sscanf(entityID[len(prefix):], "%d", &n); err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}
