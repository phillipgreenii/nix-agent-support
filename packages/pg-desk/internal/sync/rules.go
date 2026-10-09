package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/interpret"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// runContext carries one Sync call's resolved state across the anchor,
// feedback-cycle, and review-request rules below — the SAME primitive
// bootstrap adoption and every later steady-state decision reuse (design
// section 7.5's own "do not write two separate code paths" instruction):
// every rule below resolves its bead id from the ledger first, then from
// this run's adoption match, before ever deciding to create anything.
type runContext struct {
	syncer *Syncer

	mode     string
	repo     string
	entityID string
	prNumber int
	pr       prShowFields
	prOK     bool
	interp   interpret.Interpretation
	headSHA  string
	now      string

	// workBeads is Facts.WorkBeads, kept so handleClosure can find every
	// open child of the anchor (type-blind), not only the classified ones.
	workBeads json.RawMessage

	ledgerAnchor, ledgerCycle, ledgerReview store.LedgerEntry

	anchorID, cycleID, reviewID             string
	anchorNeedsBackfill                     bool
	anchorEntity, cycleEntity, reviewEntity *workBeadEntity
}

func (rc *runContext) loadLedger() error {
	var err error
	rc.ledgerAnchor, _, err = rc.syncer.store.GetLedger(rc.repo, "pr", rc.entityID, KindAnchor)
	if err != nil {
		return fmt.Errorf("sync: read anchor ledger row: %w", err)
	}
	rc.ledgerCycle, _, err = rc.syncer.store.GetLedger(rc.repo, "pr", rc.entityID, KindFeedbackCycle)
	if err != nil {
		return fmt.Errorf("sync: read feedback-cycle ledger row: %w", err)
	}
	rc.ledgerReview, _, err = rc.syncer.store.GetLedger(rc.repo, "pr", rc.entityID, KindReviewRequest)
	if err != nil {
		return fmt.Errorf("sync: read review-request ledger row: %w", err)
	}
	return nil
}

// mergeAdoption resolves each kind's bead id: the ledger's cached id wins
// (design section 7.5: "the ledger is a cache of bead ids"); on a ledger
// miss, this run's own adoption match (from Facts.WorkBeads) supplies it —
// this is the crash-safety rule ("a crash between issue create and the
// ledger write must not mint a duplicate on the next run: consult the
// ledger, then the work-beads gathered in stage 1, before creating
// anything").
func (rc *runContext) mergeAdoption(a adoption) {
	rc.anchorEntity = a.Anchor
	rc.anchorNeedsBackfill = a.AnchorNeedsMetadataBackfill
	rc.cycleEntity = a.Cycle
	rc.reviewEntity = a.Review

	rc.anchorID = rc.ledgerAnchor.BeadID
	if rc.anchorID == "" && a.Anchor != nil {
		rc.anchorID = a.Anchor.ID
	}
	rc.cycleID = rc.ledgerCycle.BeadID
	if rc.cycleID == "" && a.Cycle != nil {
		rc.cycleID = a.Cycle.ID
	}
	rc.reviewID = rc.ledgerReview.BeadID
	if rc.reviewID == "" && a.Review != nil {
		rc.reviewID = a.Review.ID
	}
}

// areaLabels is the area label set derived from config (area_labels) for
// this PR's title and branch (bead pg2-lvoye); nil when none is configured or
// none matches.
func (rc *runContext) areaLabels() []string {
	return rc.syncer.cfg.AreaLabelsFor(rc.pr.Title, rc.pr.Branch)
}

// childAreaLabels is the area label set a review-pr / process-feedback child
// carries: the labels derived for the PR, plus any area-vocabulary label the
// parent merge-request bead already carries (so a label an operator or agent
// put on the anchor flows to its children too). Sorted, de-duplicated.
func (rc *runContext) childAreaLabels() []string {
	set := map[string]bool{}
	for _, l := range rc.areaLabels() {
		set[l] = true
	}
	if rc.anchorEntity != nil {
		for _, l := range rc.syncer.cfg.AreaVocabulary() {
			if hasLabel(rc.anchorEntity.Labels, l) {
				set[l] = true
			}
		}
	}
	return setToSorted(set)
}

// missingLabels returns the entries of want absent from have, sorted. Sync
// only ever ADDS area labels, so a label an operator or agent removed or
// added by hand is never fought over.
func missingLabels(want, have []string) []string {
	var out []string
	for _, l := range want {
		if !hasLabel(have, l) {
			out = append(out, l)
		}
	}
	return sortedCopy(out)
}

func setToSorted(set map[string]bool) []string {
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (rc *runContext) upsertLedger(kind, beadID, contentHash, lastReviewedHeadSHA string) error {
	return rc.syncer.store.UpsertLedger(store.LedgerEntry{
		Repo: rc.repo, EntityType: "pr", EntityID: rc.entityID, Kind: kind,
		BeadID:                beadID,
		LastSyncedContentHash: contentHash,
		LastSyncedAt:          rc.now,
		LastReviewedHeadSHA:   lastReviewedHeadSHA,
	})
}

// upsertReviewLedger writes the review-request row with explicit settle
// state (the pending head and when it was first seen). Every other ledger
// write clears the settle state, which is right: a created, reopened or
// closed review request has no pending head.
func (rc *runContext) upsertReviewLedger(beadID, contentHash, lastReviewedHeadSHA, firstSeenHeadSHA, firstSeenHeadAt string) error {
	return rc.syncer.store.UpsertLedger(store.LedgerEntry{
		Repo: rc.repo, EntityType: "pr", EntityID: rc.entityID, Kind: KindReviewRequest,
		BeadID:                beadID,
		LastSyncedContentHash: contentHash,
		LastSyncedAt:          rc.now,
		LastReviewedHeadSHA:   lastReviewedHeadSHA,
		FirstSeenHeadSHA:      firstSeenHeadSHA,
		FirstSeenHeadAt:       firstSeenHeadAt,
	})
}

// handleClosure closes the anchor's open children and then the anchor (children first, bead pg2-mhz7b) — BOTH cycle types,
// the process-feedback cycle and the review-pr request alike — on a
// CONFIRMED closure (design section 7.5's Anchor rule: "closed, with its
// open cycles, only on a CONFIRMED closure"; the phase-10 packet's own
// acceptance criterion is explicit that this covers both: "the anchor is
// created only after the first cycle or review request ... and confirmed
// closure closes the anchor and its cycles") — never because the PR merely
// left a query (closureFromFacts, sync.go, only ever reports isClosure=true
// from a real `pr show` re-read).
//
// The cascade is type-blind, like pg-pr's own cascade-close
// (packages/pg-pr/internal/beadsbridge/bridge.go's CascadeCloseMergeRequest,
// which closed every direct child via ListChildrenOfPR): after the anchor and
// the two ledger-tracked cycle beads, EVERY other open parent-child
// dependent of the anchor found in Facts.WorkBeads is closed too (pg2-kftf9.7:
// improvised children such as "Human: unblock ..." beads were left open
// after merge). Unlike pg-pr this cannot enumerate children itself; it is
// limited to what the work-beads query returned, and only reaches children
// filed with --parent <anchor>. Feedback's own grandchildren are not
// walked. History: pg2-ryexi added the review-pr request to the cascade
// (it went stale at ~8x process-feedback's rate when only the feedback
// cycle closed here).
func (rc *runContext) handleClosure(ctx context.Context, reason string) error {
	if rc.anchorID == "" {
		return nil // no anchor ever existed for this PR — nothing to close
	}
	// Children first, the anchor LAST (bead pg2-mhz7b): bd >= 1.3.1 refuses
	// to close a parent while any child is open ("cannot close X: N open
	// child issue(s); close children first"), so closing the anchor first
	// failed every confirmed closure after the 2026-10-08 bd upgrade, froze
	// the anchor's ledger last_synced_at (the oldest-anchor-check-age gauge
	// reached 37318s) and re-stamped closed_at on every retry.
	//
	// A closure failure fails the run so pg-router retries it (pg2-kftf9.1),
	// so this MUST be safe to re-enter after a PARTIAL failure: each
	// ledger-tracked child skips itself if already sentinel-closed
	// (closeCascadedChild), an already-closed open child no longer appears in
	// the work-beads read, and the anchor's own transition runs only while its
	// ledger row is not yet sentinel-closed.
	if err := rc.closeCascadedChild(ctx, KindFeedbackCycle, "feedback cycle", rc.cycleID, rc.ledgerCycle); err != nil {
		return err
	}
	if err := rc.closeCascadedChild(ctx, KindReviewRequest, "review request", rc.reviewID, rc.ledgerReview); err != nil {
		return err
	}
	for _, id := range openChildrenOf(rc.workBeads, rc.anchorID) {
		if id == rc.cycleID || id == rc.reviewID {
			continue // already closed above, with its ledger row
		}
		if rc.mode == ModeApply {
			if err := rc.syncer.client.Transition(ctx, id, "closed"); err != nil {
				return fmt.Errorf("sync: close anchor child %s: %w", id, err)
			}
		}
	}
	if rc.ledgerAnchor.LastSyncedContentHash == closedSentinel {
		return nil
	}
	if rc.mode == ModeApply {
		// Stamp the terminal state BEFORE the close so a closed anchor
		// never keeps its stale open/draft metadata (pg2-kftf9.3). It is
		// stamped only once the children are closed, so a child failure
		// does not rewrite the anchor (and bump its updated_at) per retry.
		state := reason
		if state != "merged" {
			state = "closed" // "closed" and "gone" (PR vanished) both read as closed
		}
		if err := rc.syncer.client.Update(ctx, rc.anchorID, updateInput{Metadata: map[string]string{
			"state": state, "draft": "false", "closed_at": rc.now, "last_checked_at": rc.now,
		}}); err != nil {
			return fmt.Errorf("sync: stamp closed state on anchor %s: %w", rc.anchorID, err)
		}
		if err := rc.syncer.client.Transition(ctx, rc.anchorID, "closed"); err != nil {
			return fmt.Errorf("sync: close anchor %s: %w", rc.anchorID, err)
		}
	}
	return rc.upsertLedger(KindAnchor, rc.anchorID, closedSentinel, "")
}

// closeCascadedChild closes one open cycle-type child bead of the anchor
// (a process-feedback cycle or a review-pr request) as part of handleClosure's
// cascade, and records the closure in that kind's own ledger row. An empty
// beadID means that child never existed for this PR — nothing to close.
func (rc *runContext) closeCascadedChild(ctx context.Context, kind, label, beadID string, ledger store.LedgerEntry) error {
	if beadID == "" || ledger.LastSyncedContentHash == closedSentinel {
		return nil // never existed, or already closed by an earlier (possibly partially failed) run
	}
	if rc.mode == ModeApply {
		if err := rc.syncer.client.Transition(ctx, beadID, "closed"); err != nil {
			return fmt.Errorf("sync: close %s %s: %w", label, beadID, err)
		}
	}
	return rc.upsertLedger(kind, beadID, closedSentinel, "")
}

// reconcile applies the Anchor/Feedback-cycle/Review-request rules for an
// open (non-closed) PR — design section 7.5.
func (rc *runContext) reconcile(ctx context.Context) error {
	actsAsMine := rc.interp.Ownership == string(interpret.OwnershipMine) || rc.interp.Ownership == string(interpret.OwnershipCoOwned)
	coOwned := rc.interp.Ownership == string(interpret.OwnershipCoOwned)

	unaddressed := unaddressedCommentIDs(rc.interp.Dispositions)
	needsCycle := len(unaddressed) > 0
	// Review request rule, ported verbatim from pg-router's ACL (design
	// section 7.5): every mine/co-owned PR (draft included), and every
	// non-draft team PR.
	needsReview := actsAsMine || (rc.interp.Ownership == string(interpret.OwnershipTeam) && !rc.pr.Draft)
	// Anchor rule: created lazily, only once a cycle or review request
	// first needs a parent (D8) — never eagerly.
	anchorNeeded := needsCycle || needsReview

	if rc.anchorNeedsBackfill && rc.anchorID != "" && rc.mode == ModeApply {
		if err := rc.syncer.client.Update(ctx, rc.anchorID, updateInput{
			Metadata: map[string]string{"repo": rc.repo, "pr_number": strconv.Itoa(rc.prNumber)},
		}); err != nil {
			return fmt.Errorf("sync: backfill anchor metadata %s: %w", rc.anchorID, err)
		}
	}

	// An anchor that already exists is refreshed on EVERY check even when no
	// cycle/review currently needs it, so its state/draft metadata cannot
	// drift from GitHub (pg2-kftf9.3).
	if anchorNeeded || rc.anchorID != "" {
		if err := rc.ensureAnchor(ctx, coOwned, actsAsMine); err != nil {
			return err
		}
	}
	if needsCycle {
		if err := rc.ensureCycle(ctx, unaddressed, actsAsMine); err != nil {
			return err
		}
	}
	if needsReview {
		if err := rc.ensureReviewRequest(ctx); err != nil {
			return err
		}
	}
	return nil
}

// anchorHashInput is the anchor write's own field content, hashed for the
// ledger's last_synced_content_hash column (see sync.go's package doc).
// last_synced_at is deliberately excluded: it changes every run regardless
// of real content change, which would defeat the "no-op when nothing
// changed" diff-before-write this rule otherwise gets for free.
type anchorHashInput struct {
	State, Branch, Base, Author, URL string
	Draft, CoOwned                   bool
	AddLabels, RemoveLabels          []string
	Priority                         int
	SetPriority                      bool
	// AreaLabels is the full derived area set (not the delta), so the hash
	// is stable; omitted when empty so a deployment without area_labels keeps
	// its existing ledger hashes (no one-time rewrite of every anchor).
	AreaLabels []string `json:",omitempty"`
}

// anchorConflict is the conflict signal the anchor's priority nudge runs on.
// A definite read is used as-is. A read with no definite mergeability answer
// (prShowFields.conflictUnknown) HOLDS the anchor's current episode: it stays
// in conflict while the anchor still carries its pbase marker, and stays out
// otherwise, so a transient UNKNOWN between two DIRTY reads no longer clears
// and re-opens the episode (two writes each time, bead pg2-jj0ym).
//
// When the work-beads read was degraded (no anchor entity, so no labels to
// consult), the episode is held from the ledger instead (bead pg2-n6d8y): the
// ledger hash records the conflict state last applied, so the UNKNOWN read
// adopts whichever conflict value reproduces that hash. hashFor is the
// anchor's content hash for a given conflict value. With no usable ledger
// hash the read still counts as "no conflict".
func (rc *runContext) anchorConflict(curLabels []string, hashFor func(conflict bool) string) bool {
	if rc.pr.hasConflict() {
		return true
	}
	if rc.pr.conflictUnknown() {
		if rc.anchorEntity == nil && rc.anchorID != "" && rc.ledgerAnchor.LastSyncedContentHash != "" {
			switch rc.ledgerAnchor.LastSyncedContentHash {
			case hashFor(true):
				return true
			case hashFor(false):
				return false
			}
		}
		_, held := parsePbase(curLabels)
		return held
	}
	return false
}

// anchorWriteCause names why ensureAnchor is about to write an existing
// anchor bead, for the diagnostic log (bead pg2-n6d8y). The ledger stores only
// the content hash, so the cause is derived by asking whether flipping just
// the conflict episode reproduces the last recorded hash:
//   - ledger-unrecorded: no hash was recorded yet.
//   - conflict-flip: only the conflict episode differs from the last write
//     (the flapping this log exists to confirm or rule out).
//   - pr-content-change: something else differs (state, branch, base, author,
//     url, draft, co-owned, area labels).
func (rc *runContext) anchorWriteCause(conflict bool, hashFor func(conflict bool) string) string {
	recorded := rc.ledgerAnchor.LastSyncedContentHash
	switch {
	case recorded == "":
		return "ledger-unrecorded"
	case hashFor(!conflict) == recorded:
		return "conflict-flip"
	default:
		return "pr-content-change"
	}
}

// ensureAnchor implements the Anchor rule (design section 7.5): exactly one
// per (repo, number), created lazily, with the conflict-priority nudge
// (priority.go) applied via `issue update`.
func (rc *runContext) ensureAnchor(ctx context.Context, coOwned, actsAsMine bool) error {
	curPriority := bdDefaultPriority
	var curLabels []string
	if rc.anchorEntity != nil {
		if p, ok := parsePriority(rc.anchorEntity.Priority); ok {
			curPriority = p
		}
		curLabels = rc.anchorEntity.Labels
	}
	area := rc.areaLabels()
	// The hash covers the DESIRED state, never the delta against the anchor's
	// CURRENT labels (bead pg2-jj0ym). The live delta is empty once applied,
	// so hashing it made every applied nudge look like a content change on
	// the next run and cost a second, identical write per conflict
	// transition; each write bumps updated_at, which the issue changes feed
	// echoes as a desk-issue run. The stateless delta below is a pure
	// function of (actsAsMine, conflict) and is empty when there is no
	// conflict, so a quiet anchor keeps the hash it always had.
	hashFor := func(conflict bool) string {
		hashAdd, hashRemove, hashPriority, hashSetPriority := priorityDelta(bdDefaultPriority, nil, actsAsMine, conflict)
		return contentHash(anchorHashInput{
			State: rc.pr.State, Branch: rc.pr.Branch, Base: rc.pr.Base, Author: rc.pr.Author, URL: rc.pr.URL,
			Draft: rc.pr.Draft, CoOwned: coOwned,
			AddLabels: sortedCopy(hashAdd), RemoveLabels: sortedCopy(hashRemove),
			Priority: hashPriority, SetPriority: hashSetPriority,
			AreaLabels: area,
		})
	}
	conflict := rc.anchorConflict(curLabels, hashFor)
	addLabels, removeLabels, priority, setPriority := priorityDelta(curPriority, curLabels, actsAsMine, conflict)
	hash := hashFor(conflict)

	metadata := map[string]string{
		"repo": rc.repo, "pr_number": strconv.Itoa(rc.prNumber),
		"state": rc.pr.State, "branch": rc.pr.Branch, "base": rc.pr.Base,
		"author": rc.pr.Author, "url": rc.pr.URL, "draft": strconv.FormatBool(rc.pr.Draft),
		"last_synced_at": rc.now, "last_checked_at": rc.now,
	}

	if rc.anchorID == "" {
		var newID string
		if rc.mode == ModeApply {
			labels := append([]string{}, addLabels...)
			if coOwned {
				labels = append(labels, "co-owned")
			}
			labels = append(labels, area...)
			id, err := rc.syncer.client.Create(ctx, createInput{
				Title:     fmt.Sprintf("%s#%d: %s", rc.repo, rc.prNumber, rc.pr.Title),
				IssueType: "merge-request",
				Labels:    labels,
				Metadata:  metadata,
			})
			if err != nil {
				return fmt.Errorf("sync: create anchor: %w", err)
			}
			newID = id
			if setPriority {
				if err := rc.syncer.client.Update(ctx, newID, updateInput{Priority: formatPriority(priority)}); err != nil {
					return fmt.Errorf("sync: set anchor priority %s: %w", newID, err)
				}
			}
			rc.logAnchorWrite(ctx, newID, "created", conflict)
		}
		rc.anchorID = newID
		return rc.upsertLedger(KindAnchor, rc.anchorID, hash, "")
	}

	if hash == rc.ledgerAnchor.LastSyncedContentHash {
		// Content unchanged: make NO bead call at all (bead pg2-u4c1s). Any
		// anchor update — even one rewriting the same values — bumps the
		// bead's updated_at, which pg-connector's issue changes feed hashes,
		// so a per-check stamp echoed an issue.changed -> desk-issue run on
		// every desk-pr run. The check time is recorded in pg-desk's own
		// store instead: the anchor's LEDGER last_synced_at ("this row's own
		// last-touched timestamp, in every mode", sync.go), which feeds the
		// pg_desk_oldest_anchor_check_age_seconds staleness gauge. The BEAD's
		// metadata.last_synced_at / last_checked_at are written only by a
		// content write or closure. This intentionally reverses what
		// pg2-kftf9.3 / pg2-cl3ya verified ("quiet open PR advances
		// last_checked_at each sync").
		return rc.upsertLedger(KindAnchor, rc.anchorID, hash, "")
	}
	if rc.mode == ModeApply {
		var curAnchorLabels []string
		if rc.anchorEntity != nil {
			curAnchorLabels = rc.anchorEntity.Labels
		}
		upd := updateInput{
			Metadata:     metadata,
			AddLabels:    append(append([]string{}, addLabels...), missingLabels(area, curAnchorLabels)...),
			RemoveLabels: removeLabels,
		}
		if setPriority {
			upd.Priority = formatPriority(priority)
		}
		if err := rc.syncer.client.Update(ctx, rc.anchorID, upd); err != nil {
			return fmt.Errorf("sync: update anchor %s: %w", rc.anchorID, err)
		}
		rc.logAnchorWrite(ctx, rc.anchorID, rc.anchorWriteCause(conflict, hashFor), conflict)
	}
	return rc.upsertLedger(KindAnchor, rc.anchorID, hash, "")
}

// logAnchorWrite records one applied anchor bead write and its cause (bead
// pg2-n6d8y). Every anchor write bumps the bead's updated_at, which the issue
// changes feed echoes as a desk-issue run, so the cause is what attributes a
// residual echo to a conflict flip, a real PR change, or a degraded read.
func (rc *runContext) logAnchorWrite(ctx context.Context, beadID, cause string, conflict bool) {
	RecordAnchorWrite(ctx, cause)
	rc.syncer.logger().Info(
		"pg-desk sync: anchor write",
		"bead", beadID,
		"repo", rc.repo,
		"pr", rc.prNumber,
		"cause", cause,
		"conflict", conflict,
		"conflict_unknown", rc.pr.conflictUnknown(),
		"anchor_entity", rc.anchorEntity != nil,
		"mode", rc.mode,
	)
}

type cycleHashInput struct {
	Description string
	Labels      []string
}

// ensureCycle implements the Feedback-cycle rule (design section 7.5): one
// open cycle per PR with unaddressed feedback, keyed by title and
// deduplicated by the fbsum digest (fbsum.go).
func (rc *runContext) ensureCycle(ctx context.Context, unaddressed []string, actsAsMine bool) error {
	digest := fbsumDigest(unaddressed)
	description := renderCycleDescription(rc.repo, rc.prNumber, unaddressed)

	var labels []string
	if actsAsMine {
		labels = append(labels, "mine")
	}
	if digest != "" {
		labels = append(labels, fbsumLabelPrefix+digest)
	}
	area := rc.childAreaLabels()
	labels = append(labels, area...)

	hash := contentHash(cycleHashInput{Description: description, Labels: sortedCopy(labels)})

	if rc.cycleID == "" {
		var newID string
		if rc.mode == ModeApply {
			id, err := rc.syncer.client.Create(ctx, createInput{
				Title:       fmt.Sprintf("process-feedback: %s#%d", rc.repo, rc.prNumber),
				IssueType:   "task",
				Description: description,
				Labels:      labels,
				Metadata:    map[string]string{"repo": rc.repo, "pr_number": strconv.Itoa(rc.prNumber), "branch": rc.pr.Branch},
				Parent:      rc.anchorID,
			})
			if err != nil {
				return fmt.Errorf("sync: create feedback cycle: %w", err)
			}
			newID = id
		}
		rc.cycleID = newID
		return rc.upsertLedger(KindFeedbackCycle, rc.cycleID, hash, "")
	}

	if hash == rc.ledgerCycle.LastSyncedContentHash {
		return nil
	}
	if rc.mode == ModeApply {
		var removeLabels []string
		if rc.cycleEntity != nil && digest != "" {
			removeLabels = staleFbsumLabels(rc.cycleEntity.Labels, digest)
		}
		var addLabels []string
		if digest != "" && (rc.cycleEntity == nil || !hasLabel(rc.cycleEntity.Labels, fbsumLabelPrefix+digest)) {
			addLabels = append(addLabels, fbsumLabelPrefix+digest)
		}
		if actsAsMine && (rc.cycleEntity == nil || !hasLabel(rc.cycleEntity.Labels, "mine")) {
			addLabels = append(addLabels, "mine")
		}
		var curCycleLabels []string
		if rc.cycleEntity != nil {
			curCycleLabels = rc.cycleEntity.Labels
		}
		addLabels = append(addLabels, missingLabels(area, curCycleLabels)...)
		if err := rc.syncer.client.Update(ctx, rc.cycleID, updateInput{
			Metadata:     map[string]string{"repo": rc.repo, "pr_number": strconv.Itoa(rc.prNumber), "branch": rc.pr.Branch},
			AddLabels:    addLabels,
			RemoveLabels: removeLabels,
		}); err != nil {
			return fmt.Errorf("sync: update feedback cycle %s: %w", rc.cycleID, err)
		}
	}
	return rc.upsertLedger(KindFeedbackCycle, rc.cycleID, hash, "")
}

// ensureReviewRequest implements the Review-request rule, ported verbatim
// from pg-router's ACL (design section 7.5): no gate (D13) — the caller
// (reconcile) has already decided needsReview; this method only ensures
// the bead exists and reopens/refreshes it once the head advances past the
// ledger's last-requested SHA AND has then stayed put for the settle window.
//
// # What the ledger's last_reviewed_head_sha means (bead pg2-a9yhn, item 2)
//
// It is the head of the last review REQUEST sync made — written when the
// bead is created or reopened — and NOT the head of a review that completed.
// The column name is historical. That is intended, for three reasons:
//
//   - Completion is not observable here. Sync sees only the bead's status;
//     whether a review was posted, submitted, declined or skipped lives in the
//     worker and the pending review (see docs/behavior/pg-desk/sync.md,
//     "Review-request lifecycle"). A "last reviewed" value would have to be
//     inferred from a closed bead, which cannot tell a posted review from a
//     declined one.
//   - A request that is dropped without the bead being closed (a killed or
//     crashed session, a worker that hands the bead back, a blocked review
//     that is deferred) is NOT lost: the bead itself is the durable request
//     and stays open, in progress or deferred until a worker closes it, and
//     the workspace's session reaping releases a dead claim. Re-requesting the
//     same head on top of an open bead would change nothing.
//   - A worker that closes the bead WITHOUT reviewing (declined) must not be
//     asked again for the same head: the 72h trigger trace found no review of
//     an already-reviewed head, and this deterministic dedup by head SHA is the
//     property to preserve. A person who wants the head re-reviewed reopens the
//     bead, or pushes; a new head is requested as soon as it settles.
//
// # Settle window (bead pg2-a9yhn, item 1)
//
// A head that differs from the last-requested one is not acted on at once: a
// burst of pushes would otherwise start a ~16 minute review of every
// intermediate head, and a review whose head moved is refused at submit. The
// ledger row remembers the pending head and when a sync run first saw it
// (first_seen_head_sha / first_seen_head_at); the bead is reopened only once
// that head has been the PR's head for sync.review_settle_window
// (config.DefaultReviewSettleWindow). A push inside the window moves the
// pending head and restarts the timer, so a burst of N pushes yields one
// request, for the head the burst ended on. The window applies to the REOPEN
// only: the first review request of a PR is created at once (a PR's first
// head has no earlier head to supersede, and an empty-bead pending row would
// be indistinguishable from a plan-mode planned row). The timer is measured
// in sync runs' own clock, so it starts when sync first saw the head, which
// is at most one polling cadence after the push; a quiet PR is re-driven
// after the window by `pg-desk reconcile` (see SettleDueRows).
func (rc *runContext) ensureReviewRequest(ctx context.Context) error {
	metadata := map[string]string{
		"repo": rc.repo, "pr_number": strconv.Itoa(rc.prNumber),
		"branch": rc.pr.Branch, "head_sha": rc.headSHA, "ownership": rc.interp.Ownership,
	}
	hash := contentHash(metadata)

	if rc.reviewID == "" {
		var newID string
		if rc.mode == ModeApply {
			id, err := rc.syncer.client.Create(ctx, createInput{
				Title:     fmt.Sprintf("review-pr: %s#%d", rc.repo, rc.prNumber),
				IssueType: "task",
				Labels:    rc.childAreaLabels(),
				Metadata:  metadata,
				Parent:    rc.anchorID,
			})
			if err != nil {
				return fmt.Errorf("sync: create review request: %w", err)
			}
			newID = id
		}
		rc.reviewID = newID
		return rc.upsertLedger(KindReviewRequest, rc.reviewID, hash, rc.headSHA)
	}

	if rc.ledgerReview.LastReviewedHeadSHA != "" && rc.ledgerReview.LastReviewedHeadSHA == rc.headSHA {
		// The head is back at (or never left) the one already requested, so
		// any pending newer head was superseded (a force-push back). Forget
		// it: otherwise the same SHA reappearing later would inherit the old
		// timer and be treated as settled at once.
		if rc.ledgerReview.FirstSeenHeadSHA != "" || rc.ledgerReview.FirstSeenHeadAt != "" {
			return rc.upsertReviewLedger(rc.reviewID, rc.ledgerReview.LastSyncedContentHash, rc.headSHA, "", "")
		}
		return nil // head has not advanced — nothing to do
	}

	// The head has advanced past the last request: wait for it to settle.
	window := rc.syncer.cfg.ReviewSettleWindow()
	if window > 0 {
		led := rc.ledgerReview
		firstSeenAt, parsed := parseSettleTime(led.FirstSeenHeadAt)
		switch {
		case led.FirstSeenHeadSHA != rc.headSHA || !parsed:
			// A head sync has not seen before (or unreadable state): start
			// (or restart) its timer. Nothing is written to the bead.
			return rc.upsertReviewLedger(rc.reviewID, led.LastSyncedContentHash, led.LastReviewedHeadSHA, rc.headSHA, rc.now)
		case rc.nowTime().Sub(firstSeenAt) < window:
			return nil // still settling; no write, so a quiet re-run is free
		}
	}

	if rc.mode == ModeApply {
		// ONE update: status open + assignee cleared + deferral cleared +
		// metadata refreshed (pg2-1pt7r). A bare Transition("open") cannot
		// clear the assignee, so the previous reviewer's claim would survive
		// the reopen and no worker could claim the re-review (beads-lifecycle
		// B-4: anything that re-opens a closed bead MUST clear the assignee in
		// that same update). The deferral is cleared for the same reason
		// (pg2-vhs3e): the review worker releases a blocked review deferred,
		// and that deferral belongs to the OLD head. Verified on bd 1.2.2: a
		// reopen that leaves defer_until in place keeps the bead out of `bd
		// ready` until it expires, delaying review of the new head.
		var curReviewLabels []string
		if rc.reviewEntity != nil {
			curReviewLabels = rc.reviewEntity.Labels
		}
		if err := rc.syncer.client.Update(ctx, rc.reviewID, updateInput{
			AddLabels:     missingLabels(rc.childAreaLabels(), curReviewLabels),
			Status:        "open",
			ClearAssignee: true,
			ClearDefer:    true,
			Metadata:      metadata,
		}); err != nil {
			return fmt.Errorf("sync: reopen review request %s: %w", rc.reviewID, err)
		}
	}
	return rc.upsertLedger(KindReviewRequest, rc.reviewID, hash, rc.headSHA)
}

// parseSettleTime parses a ledger first_seen_head_at; ok is false for an
// empty or unreadable value.
func parseSettleTime(v string) (time.Time, bool) {
	if v == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(rfc3339, v)
	return t, err == nil
}

// nowTime is rc.now (the run's single stamped instant) as a time.Time.
func (rc *runContext) nowTime() time.Time {
	t, err := time.Parse(rfc3339, rc.now)
	if err != nil {
		return rc.syncer.clock.Now().UTC()
	}
	return t
}

// SettleDue reports whether a review-request ledger row is waiting out a
// pending head whose settle window has now elapsed, so that a sync run would
// reopen the bead. It is false for any other row, for a zero window, and for
// a row with no pending head.
func SettleDue(l store.LedgerEntry, window time.Duration, now time.Time) bool {
	if l.Kind != KindReviewRequest || l.BeadID == "" || l.FirstSeenHeadSHA == "" || window <= 0 {
		return false
	}
	at, ok := parseSettleTime(l.FirstSeenHeadAt)
	if !ok {
		return false
	}
	return now.Sub(at) >= window
}
