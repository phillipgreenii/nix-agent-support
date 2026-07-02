// Package reviewsink holds the my-PR review sink (pg2-4c5i.34): it ingests the
// findings of a produced review for one of MY PRs as feedback rows so the
// EXISTING feedback.created → process-feedback → merge-loop machinery consumes
// them. It MUST NOT touch GitHub — for my PRs the review is never posted.
//
// Findings are ingested as a distinct 'self-review' feedback kind (schema v6,
// see internal/store/migrate.go) with is_ours=1 / author_kind=agent, keyed by
// UNIQUE(pr_id, fingerprint) so re-running the reviewer at the same head is
// idempotent while a re-review at a new head SHA yields new findings.
//
// Gating (pg2-4c5i.34, Q1 — always block): each self-review row starts at
// status='new', which the merge-loop treats as unresolved/blocking exactly like
// a ci-failure row until it is dispositioned. See internal/store: the "unresolved
// blocking feedback" predicate is status NOT IN (dispositioned, resolved,
// superseded); self-review rows are included by construction.
package reviewsink

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/feedbackclassify"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/reviewstage"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
)

// selfReviewAgentName is the agent_name stamped on ingested self-review rows.
// Matches the marker-classified "ours" agent name used elsewhere (classify.go).
const selfReviewAgentName = "pg-pr"

// IngestSelfReview ingests each finding of a produced review (the my-PR sink,
// pg2-4c5i.34) as a self-review feedback row and enqueues one feedback.created
// event per new/updated row so the existing process-feedback bead + merge loop
// consume them. It performs NO GitHub write.
//
// One row is produced per non-empty draft.Body (a PR-level, fileless finding)
// and one per draft.Comments entry (an inline finding). Idempotent per
// (pr_id, fingerprint): a re-run at the same head SHA adds no rows; a re-review
// at a new result.HeadSHA yields new findings.
//
// Returns the number of findings written (each an upsert; re-ingest of an
// already-present finding still writes-through the row but is reported as 0 new).
func IngestSelfReview(ctx context.Context, st *store.DB, repo string, prNumber int, draft *reviewstage.Draft, result *reviewstage.Result) (int, error) {
	if st == nil {
		return 0, fmt.Errorf("reviewsink: nil store")
	}
	// Treat a missing Draft/Result as "review not yet produced" (idempotent no-op).
	if draft == nil || result == nil {
		return 0, nil
	}

	pr, err := st.GetPR(ctx, repo, prNumber)
	if err != nil {
		return 0, fmt.Errorf("reviewsink: get pr %s#%d: %w", repo, prNumber, err)
	}
	if pr == nil {
		return 0, fmt.Errorf("reviewsink: no PR %s#%d in store", repo, prNumber)
	}
	prID := pr.ID
	headSHA := result.HeadSHA

	// Shared feedback.created payload (identical for every finding in this PR;
	// same shape the sync ingest path uses — {repo, number, mine}).
	payload, err := json.Marshal(struct {
		Repo   string `json:"repo"`
		Number int    `json:"number"`
		Mine   bool   `json:"mine"`
	}{Repo: repo, Number: prNumber, Mine: true})
	if err != nil {
		return 0, fmt.Errorf("reviewsink: marshal feedback payload: %w", err)
	}

	// Build the finding set: an optional PR-level body finding + one per comment.
	type finding struct {
		body string
		file string
		line int
	}
	var findings []finding
	if draft.Body != "" {
		findings = append(findings, finding{body: draft.Body})
	}
	for _, c := range draft.Comments {
		findings = append(findings, finding{body: c.Body, file: c.Path, line: c.Line})
	}

	ingested := 0
	for _, fnd := range findings {
		fp := feedbackclassify.Fingerprint("self-review", feedbackclassify.FPParts{
			SubjectSHA: headSHA,
			File:       fnd.file,
			Line:       fnd.line,
			Body:       fnd.body,
		})
		f := store.Feedback{
			PRID:             prID,
			Kind:             "self-review",
			Fingerprint:      fp,
			Body:             fnd.body,
			File:             fnd.file,
			Line:             fnd.line,
			SubjectSHA:       headSHA,
			FirstSeenHeadSHA: headSHA,
			IsOurs:           true,
			AuthorKind:       "agent",
			AgentName:        selfReviewAgentName,
			AuthorLogin:      selfReviewAgentName,
		}

		// Detect whether this fingerprint already exists so we can report the
		// count of NEW findings (idempotency signal) without a second query race:
		// query inside the tx before the upsert.
		var existed bool
		err := st.InTx(ctx, func(tx *store.Tx) error {
			var cnt int
			if e := tx.QueryRow("SELECT COUNT(*) FROM feedback WHERE pr_id=? AND fingerprint=?", prID, fp).Scan(&cnt); e != nil {
				return e
			}
			existed = cnt > 0
			if _, e := tx.UpsertFeedback(f); e != nil {
				return e
			}
			// Enqueue the event only for a genuinely new finding — a re-run at the
			// same head must not spam the merge loop with duplicate cycles.
			if !existed {
				return tx.EnqueueEvent(store.EventFeedbackCreated, payload)
			}
			return nil
		})
		if err != nil {
			return ingested, fmt.Errorf("reviewsink: ingest self-review finding (pr=%d fp=%s): %w", prID, fp, err)
		}
		if !existed {
			ingested++
		}
	}
	return ingested, nil
}
