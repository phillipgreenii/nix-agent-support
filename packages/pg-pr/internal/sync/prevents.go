package sync

import (
	"context"
	"encoding/json"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/ownership"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/snapshot"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
)

// draftReviewFinder is the optional bd capability the attention projector needs
// to read the "draft review ready" signal (the draft-review bead CLOSED by
// pg2-4c5i.36). The real *beads.Client satisfies it; test fakes may inject it.
// Segregated (Interface Segregation) from the slim sync.BeadClient so the
// projector degrades gracefully when the capability is absent (signal=false).
type draftReviewFinder interface {
	FindDraftReviewForPR(ctx context.Context, repo string, number int) (id string, closed bool, found bool, err error)
}

// prToStoreRow maps an observed PR + ownership into the authoritative
// store.PullRequest row. Written for EVERY observed PR (regardless of
// enrichment) so Task 8's close-detection (store.ListOpenPRs) can later find
// PRs that disappeared upstream.
func (e *Engine) prToStoreRow(repo string, pr api.PR, ownership string) store.PullRequest {
	return store.PullRequest{
		Repo:         repo,
		Number:       pr.Number,
		Ownership:    ownership,
		Author:       pr.Author,
		State:        stateForPR(pr),
		Branch:       pr.Branch,
		Base:         pr.Base,
		URL:          pr.URL,
		HeadSHA:      pr.HeadSHA,
		LastSyncedAt: e.deps.Now().UTC().Format(time.RFC3339),
	}
}

// prPayload builds the enriched bridge payload for an observed PR.
func (e *Engine) prPayload(repo string, pr api.PR, ownership string) store.PRPayload {
	return store.PRPayload{
		Repo: repo, Number: pr.Number, Title: pr.Title, Ownership: ownership,
		Merged: pr.Merged, State: stateForPR(pr), Branch: pr.Branch, Base: pr.Base,
		Author: pr.Author, URL: pr.URL, Draft: pr.Draft, HasConflict: pr.HasConflict(),
		LastSyncedAt: e.deps.Now().UTC().Format(time.RFC3339),
	}
}

// emitPREvent atomically writes the authoritative pull_request row AND enqueues
// the pr.* lifecycle event in a SINGLE transaction, so the state change and the
// event that announces it commit (or roll back) together. If the enqueue fails,
// the row write is rolled back too, so the next observation re-emits it — no
// lost event (pg2-4c5i.17). No-op when the store is nil (test/legacy configs
// without event projection).
func (e *Engine) emitPREvent(ctx context.Context, eventType, repo string, pr api.PR, ownership string) error {
	if e.deps.Store == nil {
		return nil
	}
	payload, err := json.Marshal(e.prPayload(repo, pr, ownership))
	if err != nil {
		return err
	}
	row := e.prToStoreRow(repo, pr, ownership)
	return e.deps.Store.InTx(ctx, func(tx *store.Tx) error {
		if _, err := tx.UpsertPR(row); err != nil {
			return err
		}
		return tx.EnqueueEvent(eventType, payload)
	})
}

// emitAttention re-derives the teammate-attention verdict for a PR from
// PERSISTED store facts (its revision timeline + the draft-review-bead-closed
// signal) plus the live hasConflict signal, through the SHARED
// snapshot.NeedsAttention predicate, and emits a pr.attention event carrying
// that verdict. Called once per tick from refreshPR for team AND co-owned
// PRs.
//
// own gates the verdict: a non-TEAM PR (co-owned) is never a review target for
// me, so its attention bead must not stay open — emitAttention short-circuits
// to Need=false without consulting the revision timeline, which idempotently
// CLOSES any attention bead a prior team-owned tick opened (a team->co-owned
// transition, e.g. I pushed a commit onto a teammate's PR). Only the TEAM path
// below runs the real predicate — hasConflict (the caller's pr.HasConflict(),
// with GitHub's merge-state overlaid via overlayMergeState so the daemon REST
// path carries it) dampens that verdict to Need=false when the PR conflicts
// (pg2-tsgkj).
//
// Because it re-derives + re-emits from facts EVERY tick (never a one-shot
// transition), a dropped fire-once pr.attention event self-heals on the next
// tick (design §2.7, R1/D3). The bridge's projectAttentionBead ensures (need) or
// closes (!need) the attention bead idempotently.
//
// It uses the SAME predicate + SAME store inputs the dashboard builder feeds
// buildTeamRow, so the dashboard NeedsAttention signal and the open-attention-
// bead set can never diverge (D4/R4). No-op when the store is nil.
func (e *Engine) emitAttention(ctx context.Context, bdc BeadClient, repo string, number int, prID int64, own ownership.Ownership, hasConflict bool) error {
	if e.deps.Store == nil {
		return nil
	}
	// A co-owned (or, in principle, mine) PR is never a review target for me —
	// force Need=false so the bridge closes any open attention bead. Only a
	// genuine TEAM PR runs the revision-timeline predicate below.
	if own != ownership.Team {
		payload, err := json.Marshal(store.AttentionPayload{Repo: repo, Number: number, Need: false, Reason: ""})
		if err != nil {
			return err
		}
		return e.deps.Store.InTx(ctx, func(tx *store.Tx) error {
			return tx.EnqueueEvent(store.EventPRAttention, payload)
		})
	}
	revs, err := e.deps.Store.ListRevisions(ctx, prID)
	if err != nil {
		return err
	}
	// "Draft review ready" = the draft-review bead was CLOSED by .36. Read it via
	// the optional finder capability; absent capability degrades to "not ready".
	draftReviewClosed := false
	if finder, ok := bdc.(draftReviewFinder); ok {
		_, closed, found, ferr := finder.FindDraftReviewForPR(ctx, repo, number)
		if ferr != nil {
			return ferr
		}
		draftReviewClosed = found && closed
	}
	need, reason := snapshot.NeedsAttention(revs, draftReviewClosed, hasConflict)
	payload, err := json.Marshal(store.AttentionPayload{
		Repo: repo, Number: number, Need: need, Reason: reason,
	})
	if err != nil {
		return err
	}
	return e.deps.Store.InTx(ctx, func(tx *store.Tx) error {
		return tx.EnqueueEvent(store.EventPRAttention, payload)
	})
}

// emitPRClosed atomically marks the stored PR row closed (or merged) AND
// enqueues pr.closed (or pr.merged) in a SINGLE transaction. Combining the
// state mutation with the enqueue closes the lost-event window from #5: if the
// enqueue fails, the close is rolled back and ListOpenPRs re-detects the PR next
// tick, so the close-detection path can no longer permanently drop a pr.closed
// event (pg2-4c5i.17). No-op when the store is nil.
func (e *Engine) emitPRClosed(ctx context.Context, row store.PullRequest, merged bool) error {
	if e.deps.Store == nil {
		return nil
	}
	eventType := store.EventPRClosed
	row.State = "closed"
	if merged {
		eventType = store.EventPRMerged
		row.State = "merged"
	}
	payload, err := json.Marshal(store.PRPayload{
		Repo: row.Repo, Number: row.Number, Ownership: row.Ownership, Merged: merged,
	})
	if err != nil {
		return err
	}
	return e.deps.Store.InTx(ctx, func(tx *store.Tx) error {
		if _, err := tx.UpsertPR(row); err != nil {
			return err
		}
		return tx.EnqueueEvent(eventType, payload)
	})
}
