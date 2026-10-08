package gather

import (
	"context"
	"encoding/json"
	"fmt"
)

// ReviewPendingFact is the pending-review half of the PR review state: the
// pg-connector `pr review pending` result (contract 9.1a) carried verbatim,
// or the reason the lookup failed. Exactly one of Result and Error is set. A
// failed lookup is never recorded as "no pending review".
type ReviewPendingFact struct {
	// Result is the 9.1a `result` object: {"pending":false,...} or
	// {"pending":true,"head_sha":...,"review":{...}}.
	Result json.RawMessage `json:"result,omitempty"`
	// Error says why the lookup produced no answer.
	Error string `json:"error,omitempty"`
}

// gatherReviewState adds the pending-review state of one PR to f. It is the
// pg-desk read of the pg2-kftf9.12 record: the pending-review lookup is
// `pg-connector pr review pending`, run once and stored verbatim; there is no
// lookup of its own here.
//
// The read never degrades the hydration. A degraded hydration writes nothing
// (the previous snapshot stands), which would keep showing a previous
// "current" or "none" as if it were fresh. A failure is recorded in the facts
// instead, so the stored state says "unknown" with the reason.
//
// The read is read-only: this posts, deletes, submits and creates nothing.
func (g *Gatherer) gatherReviewState(ctx context.Context, entityID string, f *Facts) {
	f.ReviewPending = g.lookupPendingReview(ctx, entityID)
}

// lookupPendingReview runs `pr review pending <id>`. A not_found answer, any
// other failure and a result that is not a 9.1a record are all failures.
func (g *Gatherer) lookupPendingReview(ctx context.Context, entityID string) *ReviewPendingFact {
	raw, notFound, err := g.targetedCall(ctx, []string{"pr", "review", "pending", entityID}, nil)
	switch {
	case notFound:
		return &ReviewPendingFact{Error: fmt.Sprintf("pr review pending %s: pg-connector reports not_found", entityID)}
	case err != nil:
		return &ReviewPendingFact{Error: err.Error()}
	}
	var probe struct {
		Pending *bool `json:"pending"`
	}
	if jerr := json.Unmarshal(raw, &probe); jerr != nil || probe.Pending == nil {
		return &ReviewPendingFact{Error: fmt.Sprintf("pr review pending %s: the result is not a pending-review record", entityID)}
	}
	return &ReviewPendingFact{Result: raw}
}
