package gather

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// ReviewEscalationQuery is the named pg-connector issue query that lists
// every OPEN pending-review escalation bead (in every non-closed state, human
// labelled beads included). The deployment defines it in the issue backend's
// queries; it is the same name pg-router-review-escalator lists.
const ReviewEscalationQuery = "pending-review-escalations"

// Metadata keys of an escalation bead, owned by pg-router-review-escalator
// and restated here as literals (the exec-not-import discipline of this
// package): the dedupe key, the head a per-PR bead was raised for, and the
// PRs a roll-up bead covers.
const (
	escalationKeyKey  = "review_escalation_key" // "pr:<owner/repo>#<n>" or "rollup:<reason>"
	escalationKeyHead = "review_escalation_head"
	escalationKeyPRs  = "review_escalation_prs" // ";"-joined "<owner/repo>#<n>"
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

// ReviewEscalationRef is one open escalation bead covering the PR.
type ReviewEscalationRef struct {
	// ID is the escalation bead's id.
	ID string `json:"id"`
	// Kind is "pr" (a per-PR bead) or "rollup" (a systemic roll-up bead
	// whose review_escalation_prs names the PR).
	Kind string `json:"kind"`
	// Head is the head the escalation was raised for; "" when it recorded none.
	Head string `json:"head,omitempty"`
}

// ReviewEscalationsFact is the escalation half of the PR review state: the
// open escalation beads covering the PR, or the reason the query failed.
// Open is empty only when the query succeeded and found none; a failure sets
// Error, never an empty Open.
type ReviewEscalationsFact struct {
	Open  []ReviewEscalationRef `json:"open"`
	Error string                `json:"error,omitempty"`
}

// gatherReviewState adds the pending-review state of one PR to f. It is the
// pg-desk read of the pg2-kftf9.12 record: the pending-review lookup is
// `pg-connector pr review pending`, run once and stored verbatim; there is no
// lookup of its own here. It also lists the open escalation beads covering
// the PR.
//
// Neither read degrades the hydration. A degraded hydration writes nothing
// (the previous snapshot stands), which would keep showing a previous
// "current" or "none" as if it were fresh. Each failure is recorded in the
// facts instead, so the stored state says "unknown" with the reason.
//
// Both reads are read-only: this posts, deletes, submits and creates nothing.
func (g *Gatherer) gatherReviewState(ctx context.Context, entityID string, f *Facts) {
	f.ReviewPending = g.lookupPendingReview(ctx, entityID)
	f.ReviewEscalations = g.lookupReviewEscalations(ctx, entityID, f)
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

// lookupReviewEscalations lists the open escalation beads and keeps those
// covering this PR. Only a fully successful list is trusted: a degraded
// fan-out (exit 2) could be missing the very bead that covers the PR, so it
// is a failure, the same rule pg-router-review-escalator applies before it
// would create a duplicate.
func (g *Gatherer) lookupReviewEscalations(ctx context.Context, entityID string, f *Facts) *ReviewEscalationsFact {
	res, err := g.run(ctx, []string{"issue", "list", "--query", ReviewEscalationQuery}, g.issueBeadsDirEnv())
	if err != nil {
		return &ReviewEscalationsFact{Open: []ReviewEscalationRef{}, Error: err.Error()}
	}
	if res.exitCode != 0 {
		return &ReviewEscalationsFact{Open: []ReviewEscalationRef{}, Error: fmt.Sprintf(
			"issue list --query %s: exit %d: %s", ReviewEscalationQuery, res.exitCode, wireErrorMessage(res.stdout),
		)}
	}
	var list struct {
		Entities []struct {
			ID       string         `json:"id"`
			Metadata map[string]any `json:"metadata"`
		} `json:"entities"`
	}
	if jerr := json.Unmarshal(res.stdout, &list); jerr != nil {
		return &ReviewEscalationsFact{Open: []ReviewEscalationRef{}, Error: fmt.Sprintf(
			"issue list --query %s: not a list response: %v", ReviewEscalationQuery, jerr,
		)}
	}
	ids := []string{entityID}
	if show, derr := decodePRShow(f.PRShow); derr == nil && show.Repo != "" && show.Number > 0 {
		ids = append(ids, fmt.Sprintf("%s#%d", show.Repo, show.Number))
	}
	out := ReviewEscalationsFact{Open: []ReviewEscalationRef{}}
	for _, e := range list.Entities {
		key := metadataString(e.Metadata, escalationKeyKey)
		switch {
		case strings.HasPrefix(key, "pr:"):
			if matchesAnyPR(ids, strings.TrimPrefix(key, "pr:")) {
				out.Open = append(out.Open, ReviewEscalationRef{ID: e.ID, Kind: "pr", Head: metadataString(e.Metadata, escalationKeyHead)})
			}
		case strings.HasPrefix(key, "rollup:"):
			for _, pr := range strings.Split(metadataString(e.Metadata, escalationKeyPRs), ";") {
				if matchesAnyPR(ids, pr) {
					out.Open = append(out.Open, ReviewEscalationRef{ID: e.ID, Kind: "rollup"})
					break
				}
			}
		}
	}
	return &out
}

// matchesAnyPR reports whether candidate names one of the PR's ids; a PR id
// is compared case-insensitively because the repository path is.
func matchesAnyPR(ids []string, candidate string) bool {
	candidate = strings.TrimSpace(candidate)
	if candidate == "" {
		return false
	}
	for _, id := range ids {
		if strings.EqualFold(id, candidate) {
			return true
		}
	}
	return false
}

// metadataString reads a bead metadata value as a string; a whole number is
// rendered without a fraction (pg-connector decodes unquoted numbers as
// float64).
func metadataString(md map[string]any, key string) string {
	switch v := md[key].(type) {
	case string:
		return v
	case float64:
		return fmt.Sprintf("%.0f", v)
	default:
		return ""
	}
}
