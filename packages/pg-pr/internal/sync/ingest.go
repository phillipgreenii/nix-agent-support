package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/feedbackclassify"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/vcs"
)

// ingestFeedbackToStore writes each feedback item from enriched into the
// SQLite store and enqueues a feedback.created event — without touching the
// existing bead/processing-cycle path. It is a no-op when enriched is nil
// (the daemon always supplies enriched; tests that don't set Deps.Store skip
// this path entirely via the caller's guard).
//
// Only called from processFeedback when e.deps.Store != nil.
func (e *Engine) ingestFeedbackToStore(ctx context.Context, repo string, pr api.PR, enriched *vcs.EnrichedPR) error {
	if enriched == nil {
		// Simplest correct choice: only ingest when enriched is present.
		// The daemon always supplies it; the fallback REST path is for edge
		// cases and does not need store ingestion in this phase.
		return nil
	}

	reg := feedbackclassify.NewRegistry(nil)
	self := e.cfg().SelfLogin
	mine := e.isSelfAuthored(pr.Author)

	// UpsertPR once, outside the per-feedback transactions, so we capture
	// prID before the item loop.
	ownership := "team"
	if mine {
		ownership = "mine"
	}
	prState := pr.State
	if prState == "" {
		prState = "open"
	}
	prID, err := e.deps.Store.UpsertPR(ctx, store.PullRequest{
		Repo:      repo,
		Number:    pr.Number,
		Ownership: ownership,
		Author:    pr.Author,
		State:     prState,
		Branch:    pr.Branch,
		Base:      pr.Base,
		URL:       pr.URL,
		HeadSHA:   pr.HeadSHA,
	})
	if err != nil {
		return fmt.Errorf("ingest: upsert pr %s#%d: %w", repo, pr.Number, err)
	}

	// Encode the shared event payload (identical for every item in this PR).
	payloadBytes, err := json.Marshal(struct {
		Repo   string `json:"repo"`
		Number int    `json:"number"`
		Mine   bool   `json:"mine"`
	}{Repo: repo, Number: pr.Number, Mine: mine})
	if err != nil {
		return fmt.Errorf("ingest: marshal feedback payload: %w", err)
	}

	// --- Comments ---
	for _, c := range enriched.Comments {
		// api.Comment has no __typename field; derive "Bot" typename from the
		// [bot] login suffix (GitHub's canonical bot indicator) so Classify
		// can use the Bot/Mannequin typename path when the bot is not in the
		// registry.
		typename := ""
		if strings.HasSuffix(c.Author, "[bot]") {
			typename = "Bot"
		}
		a := reg.Classify(c.Author, typename, c.Body, self)
		if a.IsOurs {
			// Skip pg-pr's own marker'd replies — do not re-ingest.
			continue
		}

		// Inline (diff/code) comment vs top-level PR comment.
		kind := "pr-comments"
		if c.Path != "" || c.Line > 0 || c.ThreadID != "" {
			kind = "code-comment-thread"
		}

		fp := feedbackclassify.Fingerprint(kind, feedbackclassify.FPParts{
			File:       c.Path,
			Line:       c.Line,
			Body:       c.Body,
			ExternalID: c.ID,
		})

		f := store.Feedback{
			PRID:            prID,
			Kind:            kind,
			ExternalID:      c.ID,
			Fingerprint:     fp,
			Body:            c.Body,
			AuthorLogin:     c.Author,
			AuthorKind:      a.Kind,
			AgentName:       a.AgentName,
			IsOurs:          false,
			AuthorRole:      c.AuthorRole,
			Severity:        a.DefaultSeverity,
			ManagedUpstream: a.ManagedUpstream,
			File:            c.Path,
			Line:            c.Line,
			SubjectSHA:      c.OriginalCommitOID,
			IsOutdated:      c.ThreadIsOutdated,
			IsMinimized:     c.IsMinimized,
			MinimizedReason: c.MinimizedReason,
		}

		if err := e.deps.Store.InTx(ctx, func(tx *store.Tx) error {
			if _, err := tx.UpsertFeedback(f); err != nil {
				return err
			}
			return tx.EnqueueEvent(store.EventFeedbackCreated, payloadBytes)
		}); err != nil {
			return fmt.Errorf("ingest: upsert comment %s: %w", c.ID, err)
		}
	}

	// --- CI failures ---
	for _, r := range enriched.CIRuns {
		// Only ingest failures (matches the existing processFeedback notion
		// of which runs count — skip non-failure conclusions).
		if r.Conclusion != "failure" {
			continue
		}

		kind := "ci-failure"
		fp := feedbackclassify.Fingerprint(kind, feedbackclassify.FPParts{
			CheckName:  r.Name,
			SubjectSHA: r.HeadSHA,
		})

		f := store.Feedback{
			PRID:        prID,
			Kind:        kind,
			ExternalID:  r.ID,
			Fingerprint: fp,
			Body:        fmt.Sprintf("%s run %q concluded with %q (%s)", r.Provider, r.Name, r.Conclusion, r.URL),
			CheckName:   r.Name,
			Conclusion:  r.Conclusion,
			Link:        r.URL,
			RunID:       r.ID,
			SubjectSHA:  r.HeadSHA,
		}

		if err := e.deps.Store.InTx(ctx, func(tx *store.Tx) error {
			if _, err := tx.UpsertFeedback(f); err != nil {
				return err
			}
			return tx.EnqueueEvent(store.EventFeedbackCreated, payloadBytes)
		}); err != nil {
			return fmt.Errorf("ingest: upsert ci run %s: %w", r.ID, err)
		}
	}

	// Reconcile stale CI-failure rows: mark any ci-failure row whose
	// subject_sha differs from the current PR head as superseded.
	if err := e.deps.Store.ReconcileStaleness(ctx, prID, pr.HeadSHA); err != nil {
		return fmt.Errorf("ingest: reconcile staleness pr=%d: %w", prID, err)
	}

	return nil
}
