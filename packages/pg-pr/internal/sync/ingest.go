package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/agentregistry"
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

	// Build a classify registry from the configured agent entries.
	// On a bad regex, log to stderr and fall back to an empty registry so
	// ingestion still runs (the broken entry was already rejected at startup,
	// but we are defensive here).
	var reg feedbackclassify.Registry
	if ar, err := agentregistry.New(e.cfg().Agents); err != nil {
		fmt.Fprintf(os.Stderr, "pg-pr: ingest: building agent registry failed, falling back to empty classify registry: %v\n", err)
		reg = feedbackclassify.NewRegistry(nil)
	} else {
		reg = ar.ToClassifyRegistry()
	}
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
	//
	// Split enriched.Comments into two groups:
	//   1. Top-level (pr-comments): Path == "" — one feedback row per comment.
	//   2. Inline thread (code-comment-thread): Path != "" — one feedback row
	//      per unique ThreadID, with all per-thread comments stored as
	//      code_comment_message rows.
	//
	// Group inline comments by ThreadID, preserving the order they appear in
	// the slice (which matches GraphQL creation order within each thread).
	type threadGroup struct {
		comments []api.Comment
	}
	threadOrder := []string{} // thread ids in first-seen order
	threads := map[string]*threadGroup{}

	for _, c := range enriched.Comments {
		if c.Path == "" {
			// Top-level pr-comment — handle inline below after grouping.
			continue
		}
		tid := c.ThreadID
		if tid == "" {
			// Shouldn't happen for path-bearing comments, but be defensive.
			// Treat each as its own pseudo-thread keyed by comment id.
			tid = "comment-" + c.ID
		}
		if _, exists := threads[tid]; !exists {
			threadOrder = append(threadOrder, tid)
			threads[tid] = &threadGroup{}
		}
		threads[tid].comments = append(threads[tid].comments, c)
	}

	// Ingest top-level (pr-comments) — unchanged from prior behaviour.
	for _, c := range enriched.Comments {
		if c.Path != "" {
			continue // handled in the thread loop below
		}

		typename := ""
		if strings.HasSuffix(c.Author, "[bot]") {
			typename = "Bot"
		}
		a := reg.Classify(c.Author, typename, c.Body, self)
		if a.IsOurs {
			continue
		}

		fp := feedbackclassify.Fingerprint("pr-comments", feedbackclassify.FPParts{
			Body:       c.Body,
			ExternalID: c.ID,
		})
		f := store.Feedback{
			PRID:             prID,
			Kind:             "pr-comments",
			ExternalID:       c.ID,
			Fingerprint:      fp,
			Body:             c.Body,
			AuthorLogin:      c.Author,
			AuthorKind:       a.Kind,
			AgentName:        a.AgentName,
			IsOurs:           false,
			AuthorRole:       c.AuthorRole,
			Severity:         a.DefaultSeverity,
			ManagedUpstream:  a.ManagedUpstream,
			FirstSeenHeadSHA: pr.HeadSHA,
		}
		if err := e.deps.Store.InTx(ctx, func(tx *store.Tx) error {
			if _, err := tx.UpsertFeedback(f); err != nil {
				return err
			}
			return tx.EnqueueEvent(store.EventFeedbackCreated, payloadBytes)
		}); err != nil {
			fmt.Fprintf(os.Stderr, "pg-pr: ingest: upsert comment %s: %v (continuing)\n", c.ID, err)
			continue
		}
	}

	// Ingest inline thread groups — one feedback row per thread.
	for _, tid := range threadOrder {
		grp := threads[tid]
		if len(grp.comments) == 0 {
			continue
		}

		// Classify all comments in the thread. Skip the thread entirely only
		// if ALL comments are ours.
		type classified struct {
			comment api.Comment
			author  feedbackclassify.Author
		}
		var items []classified
		allOurs := true
		for _, c := range grp.comments {
			typename := ""
			if strings.HasSuffix(c.Author, "[bot]") {
				typename = "Bot"
			}
			a := reg.Classify(c.Author, typename, c.Body, self)
			items = append(items, classified{c, a})
			if !a.IsOurs {
				allOurs = false
			}
		}
		if allOurs {
			continue
		}

		// Representative comment: first in the thread (oldest).
		rep := items[0]
		repC := rep.comment
		repA := rep.author

		fp := feedbackclassify.Fingerprint("code-comment-thread", feedbackclassify.FPParts{
			ThreadID: tid,
			File:     repC.Path,
			Body:     repC.Body,
		})

		f := store.Feedback{
			PRID:            prID,
			Kind:            "code-comment-thread",
			ExternalID:      tid, // thread id, not comment id
			Fingerprint:     fp,
			Body:            repC.Body,
			AuthorLogin:     repC.Author,
			AuthorKind:      repA.Kind,
			AgentName:       repA.AgentName,
			IsOurs:          false,
			AuthorRole:      repC.AuthorRole,
			Severity:        repA.DefaultSeverity,
			ManagedUpstream: repA.ManagedUpstream,
			File:            repC.Path,
			Line:            repC.Line,
			SubjectSHA:      repC.OriginalCommitOID,
			IsOutdated:      repC.ThreadIsOutdated,
			IsMinimized:     repC.IsMinimized,
			MinimizedReason: repC.MinimizedReason,
		}

		// Build messages from all non-ours comments (the agent's context).
		var msgs []store.Message
		for _, item := range items {
			if item.author.IsOurs {
				continue
			}
			msgs = append(msgs, store.Message{
				ExternalID:  item.comment.ID,
				AuthorLogin: item.comment.Author,
				AuthorKind:  item.author.Kind,
				AgentName:   item.author.AgentName,
				IsOurs:      false,
				AuthorRole:  item.comment.AuthorRole,
				Body:        item.comment.Body,
				PostedAt:    item.comment.OriginalCommitOID, // best proxy for ordering; actual timestamp not in api.Comment
			})
		}

		if err := e.deps.Store.InTx(ctx, func(tx *store.Tx) error {
			fbID, err := tx.UpsertFeedback(f)
			if err != nil {
				return err
			}
			if err := tx.ReplaceMessages(fbID, msgs); err != nil {
				return err
			}
			return tx.EnqueueEvent(store.EventFeedbackCreated, payloadBytes)
		}); err != nil {
			// A single thread failure must NOT abort the rest.
			fmt.Fprintf(os.Stderr, "pg-pr: ingest: upsert thread %s: %v (continuing)\n", tid, err)
			continue
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
			// A single CI run failure must NOT abort the rest (including
			// ReconcileStaleness). Record the error and continue.
			fmt.Fprintf(os.Stderr, "pg-pr: ingest: upsert ci run %s: %v (continuing)\n", r.ID, err)
			continue
		}
	}

	// Reconcile stale CI-failure rows: mark any ci-failure row whose
	// subject_sha differs from the current PR head as superseded.
	if err := e.deps.Store.ReconcileStaleness(ctx, prID, pr.HeadSHA); err != nil {
		return fmt.Errorf("ingest: reconcile staleness pr=%d: %w", prID, err)
	}

	return nil
}
