package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/agentregistry"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/cirollup"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/feedbackclassify"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/ownership"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/telemetry"
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
	own := ownership.Classify(ownership.Engagement{
		Self: self, PRAuthor: pr.Author, CommitAuthors: enriched.CommitAuthors,
	})
	mine := own.ActsAsMine()

	// ExcludedCIChecks was removed outright (operator ruling on pg2-dw73b,
	// 2026-08-24); its replacement, RepoConfig.CheckInterpreters, is not
	// yet wired into the rollup — that lands with
	// pg2-4dz88.2.4/pg2-4dz88.2.6. Until then this Excluder claims nothing,
	// matching the "uninterpreted checks count in CI health" safe default
	// the check-interpreter generalization itself requires.
	ciExcl := cirollup.NewExcluder(nil)

	// UpsertPR once, outside the per-feedback transactions, so we capture
	// prID before the item loop. This is idempotent with the authoritative
	// UpsertPR the Sync per-PR loop already performs for every observed PR;
	// reusing prToStoreRow keeps both writes equivalent (last-writer-wins;
	// both derive state/ownership from stateForPR — last_synced_at may differ
	// since each call invokes Now() separately) so the second write never
	// meaningfully clobbers the first. It is still required here because
	// ingestFeedbackToStore is also called directly (e.g. the full-chain
	// integration test) without a prior upsert.
	prID, err := e.deps.Store.UpsertPR(ctx, e.prToStoreRow(repo, pr, own.String()))
	if err != nil {
		return fmt.Errorf("ingest: upsert pr %s#%d: %w", repo, pr.Number, err)
	}

	// Record revision timeline and attach CI + review marker.
	rev, _, err := e.deps.Store.RecordRevision(ctx, prID, pr.HeadSHA, pr.BaseSHA)
	if err != nil {
		return fmt.Errorf("ingest: record revision %s#%d: %w", repo, pr.Number, err)
	}
	if err := e.deps.Store.SetRevisionCI(ctx, rev.ID, ciRollupFromSync(enriched.CIRuns, e.deps.Now, ciExcl)); err != nil {
		return fmt.Errorf("ingest: set revision ci %s#%d: %w", repo, pr.Number, err)
	}
	for _, rv := range mySubmittedReviews(enriched.Reviews, self) {
		// Record the self observation as a per-approver row (pg2-4dz88.1.5).
		// This is THE read path as of pg2-4dz88.1.9; the legacy write-only
		// my_review_state/reviewed_at columns (and this loop's former write
		// to them) were dropped in schema v12 (pg2-tgrip) since a DISMISSED
		// review could never be represented in that single slot anyway
		// (pg2-4dz88.1.7) — recordApproval handles that case via
		// SetDismissedApproval regardless of rv.Dismissed.
		if err := e.recordApproval(ctx, prID, rv); err != nil {
			return fmt.Errorf("ingest: set approval (self) %s#%d: %w", repo, pr.Number, err)
		}
	}
	// Record teammate (non-self) approvals per revision so the attention predicate
	// (pg2-4c5i.13) is store-derived. This runs on the daemon's enriched path per
	// tick, keeping the marker current. The viewer's own approval is excluded by
	// othersApprovedReviews (X3), so it can never masquerade as a teammate's.
	for _, rv := range othersApprovedReviews(enriched.Reviews, self) {
		// Record this teammate's approval as its own per-approver row
		// (pg2-4dz88.1.5), keyed by their real login. The legacy write-only
		// others_approved/others_approved_at columns (and this loop's former
		// write to them) were dropped in schema v12 (pg2-tgrip): that single
		// OR'd boolean could never express staleness or per-approver identity
		// (pg2-4dz88.1.7).
		if err := e.recordApproval(ctx, prID, rv); err != nil {
			return fmt.Errorf("ingest: set approval (teammate %s) %s#%d: %w", rv.Approver, repo, pr.Number, err)
		}
	}
	// Record teammate CHANGES_REQUESTED reviews as their OWN per-approver row
	// too (pg2-4dz88.1.8) — a teammate explicitly asking for changes is now
	// representable, distinct from both an absent record and that same
	// approver's own APPROVED/STALE state. Deliberately NOT wired into the
	// others-approved loop above: asking for changes does not put the PR
	// "off the hook".
	for _, rv := range othersChangesRequestedReviews(enriched.Reviews, self) {
		if err := e.deps.Store.SetApproval(ctx, prID, rv.Approver, rv.CommitSHA, rv.State, rv.SubmittedAt); err != nil {
			return fmt.Errorf("ingest: set approval (teammate changes-requested %s) %s#%d: %w", rv.Approver, repo, pr.Number, err)
		}
	}
	// Bot-COMMENT-derived verdict approvals (pg2-4dz88.1.6) — a FOURTH,
	// PARALLEL per-approver source into the SAME pr_approval table as the
	// three GitHub-REVIEW-based sources above, this one resolved from an
	// allowlisted bot's own comment body via the config-declared verdict
	// grammar rather than a GitHub review object. See botVerdictApprovals's
	// doc (internal/sync/approver.go) for the allowlist-gating decision and
	// the latest-wins/fallback/tiebreak rules, and approverApprovalState's
	// doc for the (findings, authority) -> store-state mapping.
	//
	// A bad verdict-generation regex is defensive-guarded exactly like the
	// agentregistry.New failure above: log to stderr and skip this source
	// for the cycle rather than aborting the whole ingest — the broken
	// generation was already rejected at config-validate time, but we don't
	// let a config bug here block the other three per-approver sources or
	// the rest of ingestion.
	if clf, err := buildVerdictClassifier(e.cfg().VerdictGenerations); err != nil {
		fmt.Fprintf(os.Stderr, "pg-pr: ingest: building verdict classifier failed, skipping bot verdict approvals: %v\n", err)
	} else {
		allowlist := approverAllowlistSet(e.cfg().ApproverAllowlist)
		approvals, pending := botVerdictApprovals(enriched.Comments, allowlist, clf)
		for _, bv := range approvals {
			state := approverApprovalState(bv.Result)
			if err := e.deps.Store.SetApproval(ctx, prID, bv.Approver, pr.HeadSHA, state, bv.ObservedAt); err != nil {
				return fmt.Errorf("ingest: set approval (bot verdict %s) %s#%d: %w", bv.Approver, repo, pr.Number, err)
			}
		}
		// Unmatched verdict marker signal (pg2-4dz88.1.11 / INV-APPROVAL-5):
		// a Pending result means an allowlisted approver's winning comment
		// carried a configured generation's BodyMarker but no generation's
		// grammar resolved it — the same class of silent failure the old
		// approval_regex mechanism had (a marker matched nothing and nothing
		// reported it), now one layer down in the new parser. Counter is
		// repo-labeled only (cardinality rule, internal/telemetry/metrics.go's
		// header); PR number/login/generation context goes to the log line
		// instead, following ingest.go's own fmt.Fprintf(os.Stderr, ...)
		// convention used elsewhere in this function.
		//
		// Operator response: when this counter rises, re-derive the verdict
		// grammar (internal/config.Config.VerdictGenerations) against a fresh
		// sample of the bot's real comments — the shipped grammar no longer
		// matches its current output format.
		if len(pending) > 0 {
			gens := e.cfg().VerdictGenerations
			genIDs := make([]string, len(gens))
			for i, g := range gens {
				genIDs[i] = g.ID
			}
			for _, p := range pending {
				telemetry.VerdictPendingTotal.WithLabelValues(repo).Inc()
				fmt.Fprintf(os.Stderr,
					"pg-pr: ingest: unmatched verdict marker: repo=%s pr=%d approver=%s observed_at=%s configured_generations=%d generation_ids=%v\n",
					repo, pr.Number, p.Approver, p.ObservedAt, len(gens), genIDs)
			}
		}
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
			_, err := tx.UpsertFeedback(f)
			return err
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

		// Representative comment: the oldest non-ours comment in the thread
		// (the feedback we must address). Falls back to items[0] in the
		// degenerate case where every comment is ours — but the allOurs guard
		// above already skips that, so a non-ours comment is guaranteed here.
		rep := items[0]
		for _, item := range items {
			if !item.author.IsOurs {
				rep = item
				break
			}
		}
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
				// PostedAt comes from the comment's GraphQL createdAt
				// (RFC3339); empty when the provider supplies none.
				// ListMessages orders by posted_at, then id as a tiebreaker.
				PostedAt: item.comment.CreatedAt,
			})
		}

		if err := e.deps.Store.InTx(ctx, func(tx *store.Tx) error {
			fbID, err := tx.UpsertFeedback(f)
			if err != nil {
				return err
			}
			return tx.ReplaceMessages(fbID, msgs)
		}); err != nil {
			// A single thread failure must NOT abort the rest.
			fmt.Fprintf(os.Stderr, "pg-pr: ingest: upsert thread %s: %v (continuing)\n", tid, err)
			continue
		}
	}

	// --- CI failures ---
	for _, r := range enriched.CIRuns {
		// Route through the shared cirollup classifier (pg2-qs46b) so ingest
		// agrees with every other "is CI failed?" decision site: excluded
		// checks (e.g. policy-bot) create no feedback, and the full Failed
		// taxonomy (error/cancelled/timed_out/action_required/...) is covered,
		// not just the literal "failure" conclusion.
		if cirollup.Classify(r, ciExcl) != cirollup.Failed {
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
			_, err := tx.UpsertFeedback(f)
			return err
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

	return e.emitFeedbackEvent(ctx, repo, pr, prID, mine)
}

// emitFeedbackEvent enqueues ONE feedback.created event for the PR, carrying the
// summary of what still needs processing — and enqueues NOTHING when nothing
// does (pg2-onq1e).
//
// Two deliberate changes from the per-row emission it replaces:
//
//   - ONE event per PR per tick instead of one per upserted row. The payload was
//     already identical for every row, so N events meant N identical
//     create-if-absent projections (each a full `bd list` scan) for one PR.
//   - Emitted AFTER every row is committed and staleness reconciled, so the
//     summary is derived from the FINAL state of the tick rather than guessed
//     mid-loop. The outbox is at-least-once and the projection is idempotent, so
//     the event no longer needs to share a transaction with a row write: a crash
//     between the row and the event simply re-derives both on the next tick, the
//     same self-healing the attention projector relies on.
//
// The suppression is the fix for the "empty bead on re-sync" defect: the PR
// author's own comments — including agent replies posted under their login,
// since pg-pr posts as the user — are not feedback needing processing, so a
// tick whose only new activity was the agent's own replies now surfaces zero
// unaddressed items and emits nothing.
func (e *Engine) emitFeedbackEvent(ctx context.Context, repo string, pr api.PR, prID int64, mine bool) error {
	sum, err := e.deps.Store.UnaddressedFeedback(ctx, prID, pr.Author)
	if err != nil {
		return fmt.Errorf("ingest: summarise unaddressed feedback %s#%d: %w", repo, pr.Number, err)
	}
	if sum.Unaddressed == 0 {
		return nil // nothing to process ⇒ no event ⇒ no process-feedback bead
	}
	payloadBytes, err := json.Marshal(store.FeedbackPayload{
		Repo: repo, Number: pr.Number, Mine: mine, Summary: sum,
	})
	if err != nil {
		return fmt.Errorf("ingest: marshal feedback payload: %w", err)
	}
	if err := e.deps.Store.InTx(ctx, func(tx *store.Tx) error {
		return tx.EnqueueEvent(store.EventFeedbackCreated, payloadBytes)
	}); err != nil {
		return fmt.Errorf("ingest: enqueue feedback.created %s#%d: %w", repo, pr.Number, err)
	}
	return nil
}
