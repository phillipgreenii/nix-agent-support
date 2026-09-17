package sync

import (
	"context"
	"fmt"
	"strconv"

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

func (rc *runContext) upsertLedger(kind, beadID, contentHash, lastReviewedHeadSHA string) error {
	return rc.syncer.store.UpsertLedger(store.LedgerEntry{
		Repo: rc.repo, EntityType: "pr", EntityID: rc.entityID, Kind: kind,
		BeadID:                beadID,
		LastSyncedContentHash: contentHash,
		LastSyncedAt:          rc.now,
		LastReviewedHeadSHA:   lastReviewedHeadSHA,
	})
}

// handleClosure closes the anchor and its open cycle on a CONFIRMED closure
// (design section 7.5's Anchor rule) — never because the PR merely left a
// query (closureFromFacts, sync.go, only ever reports isClosure=true from a
// real `pr show` re-read). review-request beads are deliberately NOT
// touched here — the design's Anchor rule closes "the anchor, with its open
// cycles" only; a review-pr bead's own completion is somebody else's write
// (a human or reviewing agent), and sync only ever reopens one (see
// ensureReviewRequest), never closes one.
func (rc *runContext) handleClosure(ctx context.Context, reason string) error {
	_ = reason // names why (merged/closed/gone); no pinned bead/ledger field carries it (see design's Bead-shapes table)
	if rc.anchorID == "" {
		return nil // no anchor ever existed for this PR — nothing to close
	}
	if rc.ledgerAnchor.LastSyncedContentHash == closedSentinel {
		return nil // already closed by an earlier run — idempotent no-op
	}
	if rc.mode == ModeApply {
		if err := rc.syncer.client.Transition(ctx, rc.anchorID, "closed"); err != nil {
			return fmt.Errorf("sync: close anchor %s: %w", rc.anchorID, err)
		}
	}
	if err := rc.upsertLedger(KindAnchor, rc.anchorID, closedSentinel, ""); err != nil {
		return err
	}
	if rc.cycleID != "" {
		if rc.mode == ModeApply {
			if err := rc.syncer.client.Transition(ctx, rc.cycleID, "closed"); err != nil {
				return fmt.Errorf("sync: close feedback cycle %s: %w", rc.cycleID, err)
			}
		}
		if err := rc.upsertLedger(KindFeedbackCycle, rc.cycleID, closedSentinel, ""); err != nil {
			return err
		}
	}
	return nil
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

	if anchorNeeded {
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
	addLabels, removeLabels, priority, setPriority := priorityDelta(curPriority, curLabels, actsAsMine, rc.pr.hasConflict())

	hash := contentHash(anchorHashInput{
		State: rc.pr.State, Branch: rc.pr.Branch, Base: rc.pr.Base, Author: rc.pr.Author, URL: rc.pr.URL,
		Draft: rc.pr.Draft, CoOwned: coOwned,
		AddLabels: sortedCopy(addLabels), RemoveLabels: sortedCopy(removeLabels),
		Priority: priority, SetPriority: setPriority,
	})

	metadata := map[string]string{
		"repo": rc.repo, "pr_number": strconv.Itoa(rc.prNumber),
		"state": rc.pr.State, "branch": rc.pr.Branch, "base": rc.pr.Base,
		"author": rc.pr.Author, "url": rc.pr.URL, "draft": strconv.FormatBool(rc.pr.Draft),
		"last_synced_at": rc.now,
	}

	if rc.anchorID == "" {
		var newID string
		if rc.mode == ModeApply {
			labels := append([]string{}, addLabels...)
			if coOwned {
				labels = append(labels, "co-owned")
			}
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
		}
		rc.anchorID = newID
		return rc.upsertLedger(KindAnchor, rc.anchorID, hash, "")
	}

	if hash == rc.ledgerAnchor.LastSyncedContentHash {
		return nil // nothing changed since the last sync — diff-before-write
	}
	if rc.mode == ModeApply {
		upd := updateInput{Metadata: metadata, AddLabels: addLabels, RemoveLabels: removeLabels}
		if setPriority {
			upd.Priority = formatPriority(priority)
		}
		if err := rc.syncer.client.Update(ctx, rc.anchorID, upd); err != nil {
			return fmt.Errorf("sync: update anchor %s: %w", rc.anchorID, err)
		}
	}
	return rc.upsertLedger(KindAnchor, rc.anchorID, hash, "")
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
// ledger's last-reviewed SHA.
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
		return nil // head has not advanced — nothing to do
	}
	if rc.mode == ModeApply {
		if err := rc.syncer.client.Transition(ctx, rc.reviewID, "open"); err != nil {
			return fmt.Errorf("sync: reopen review request %s: %w", rc.reviewID, err)
		}
		if err := rc.syncer.client.Update(ctx, rc.reviewID, updateInput{Metadata: metadata}); err != nil {
			return fmt.Errorf("sync: refresh review request %s: %w", rc.reviewID, err)
		}
	}
	return rc.upsertLedger(KindReviewRequest, rc.reviewID, hash, rc.headSHA)
}
