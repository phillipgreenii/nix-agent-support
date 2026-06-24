package sync

import (
	"context"
	"encoding/json"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
)

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
		Author: pr.Author, URL: pr.URL, Draft: pr.Draft,
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
