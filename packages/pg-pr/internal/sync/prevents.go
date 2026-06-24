package sync

import (
	"context"
	"encoding/json"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
)

// prPayload builds the enriched bridge payload for an observed PR.
func (e *Engine) prPayload(repo string, pr api.PR, ownership string) store.PRPayload {
	return store.PRPayload{
		Repo: repo, Number: pr.Number, Title: pr.Title, Ownership: ownership,
		Merged: pr.Merged, State: stateForPR(pr), Branch: pr.Branch, Base: pr.Base,
		Author: pr.Author, URL: pr.URL, Draft: pr.Draft,
		LastSyncedAt: e.deps.Now().UTC().Format(time.RFC3339),
	}
}

// emitPREvent enqueues a pr.* lifecycle event in its own committed transaction.
// No-op when the store is nil (test/legacy configs without event projection).
func (e *Engine) emitPREvent(ctx context.Context, eventType, repo string, pr api.PR, ownership string) error {
	if e.deps.Store == nil {
		return nil
	}
	payload, err := json.Marshal(e.prPayload(repo, pr, ownership))
	if err != nil {
		return err
	}
	return e.deps.Store.InTx(ctx, func(tx *store.Tx) error {
		return tx.EnqueueEvent(eventType, payload)
	})
}

// emitPRClosed enqueues pr.closed (or pr.merged when merged) for a stored PR row
// that is no longer observed upstream.
//
//nolint:unused // called by Phase B/C wiring (not yet wired)
func (e *Engine) emitPRClosed(ctx context.Context, row store.PullRequest, merged bool) error {
	if e.deps.Store == nil {
		return nil
	}
	eventType := store.EventPRClosed
	if merged {
		eventType = store.EventPRMerged
	}
	payload, err := json.Marshal(store.PRPayload{
		Repo: row.Repo, Number: row.Number, Ownership: row.Ownership, Merged: merged,
	})
	if err != nil {
		return err
	}
	return e.deps.Store.InTx(ctx, func(tx *store.Tx) error {
		return tx.EnqueueEvent(eventType, payload)
	})
}
