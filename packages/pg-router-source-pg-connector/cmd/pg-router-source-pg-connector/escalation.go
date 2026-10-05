// escalation.go: the "list" verb's --exclude-escalated-query filter
// (bead pg2-vhs3e): drop a review-request item while an open
// pending-review escalation covers its PR, so a PR whose stale pending
// review needs a person costs ZERO review sessions instead of one per
// deferral window.
//
// This is the layer that owns dispatch: pg-router turns every item a
// [[query]] command stanza lists into a work item for its role, so an item
// this verb never prints is never dispatched. It is also the only place the
// decision can be made — a `bd ready` query cannot join one bead to another.
//
// The metadata contract below is owned by pg-router-review-escalator
// (packages/pg-router-review-escalator/internal/escalate: the
// review_escalation_* keys) and by pg-desk's review-request bead (the repo,
// pr_number and head_sha keys). It is restated here as string literals
// rather than imported, the same exec-not-import discipline as the
// pg-connector calls in exec.go.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Metadata keys of an escalation bead (pg-router-review-escalator).
const (
	escKeyKey  = "review_escalation_key"  // "pr:<owner/repo>#<n>" or "rollup:<reason>"
	escKeyHead = "review_escalation_head" // per-PR bead: the head the blocked submit targeted
	escKeyPRs  = "review_escalation_prs"  // roll-up bead: ";"-joined "<owner/repo>#<n>" set
)

// Metadata keys of a review-request bead (pg-desk's ensureReviewRequest).
const (
	reviewKeyRepo   = "repo"
	reviewKeyPR     = "pr_number"
	reviewKeyHeadSH = "head_sha"
)

// escalationIndex answers "is this review item covered by an open
// escalation?" from the beads one escalation query listed.
type escalationIndex struct {
	// perPR maps "<owner/repo>#<n>" to the head the PR's own escalation bead
	// recorded ("" when it recorded none).
	perPR map[string]string
	// rollup is the set of PRs named by an open roll-up bead.
	rollup map[string]bool
}

// buildEscalationIndex reads the escalation query's `list` wire response.
func buildEscalationIndex(out []byte) (escalationIndex, error) {
	var wire listWire
	if err := json.Unmarshal(out, &wire); err != nil {
		return escalationIndex{}, err
	}
	idx := escalationIndex{perPR: map[string]string{}, rollup: map[string]bool{}}
	for _, raw := range wire.Entities {
		var e listEntity
		if err := json.Unmarshal(raw, &e); err != nil {
			return escalationIndex{}, err
		}
		key := metaString(e.Metadata, escKeyKey)
		switch {
		case strings.HasPrefix(key, "pr:"):
			idx.perPR[strings.TrimPrefix(key, "pr:")] = metaString(e.Metadata, escKeyHead)
		case strings.HasPrefix(key, "rollup:"):
			for _, pr := range strings.Split(metaString(e.Metadata, escKeyPRs), ";") {
				if pr = strings.TrimSpace(pr); pr != "" {
					idx.rollup[pr] = true
				}
			}
		}
	}
	return idx, nil
}

// suppresses reports whether an open escalation covers the review item.
//
//   - A per-PR escalation covers the item while the item's head is the head
//     the escalation was raised for. A different head means the head advanced
//     since: the new head has not been reviewed, so review resumes at once. An
//     escalation that recorded no head, or an item that carries none, cannot be
//     told apart from the blocked head, so it is treated as covering.
//   - A roll-up escalation covers every PR it names regardless of head: a
//     roll-up is raised only for systemic reasons (credentials, permissions,
//     the host), which a newer head does not cure. It stops covering when the
//     roll-up closes or the PR leaves it.
//
// An item whose repo or PR number is unknown is never suppressed.
func (idx escalationIndex) suppresses(md map[string]any) bool {
	repo, num := metaString(md, reviewKeyRepo), metaString(md, reviewKeyPR)
	if repo == "" || num == "" {
		return false
	}
	pr := repo + "#" + num
	if idx.rollup[pr] {
		return true
	}
	head, ok := idx.perPR[pr]
	if !ok {
		return false
	}
	itemHead := metaString(md, reviewKeyHeadSH)
	return head == "" || itemHead == "" || head == itemHead
}

// metaString reads a metadata value as a string. pg-connector decodes
// numbers it did not quote as float64, so a whole number is rendered without
// a fraction.
func metaString(md map[string]any, key string) string {
	switch v := md[key].(type) {
	case string:
		return v
	case float64:
		return fmt.Sprintf("%.0f", v)
	case json.Number:
		return v.String()
	default:
		return ""
	}
}

// loadEscalationIndex lists the escalation query and indexes it. A failure
// is NOT fatal: it is reported on stderr and the review items are listed
// unfiltered, which is only the status quo (an extra session per deferral
// window for a blocked PR). Failing the whole listing instead would stop ALL
// review dispatch whenever the escalation query hiccups.
func loadEscalationIndex(ctx context.Context, stderr io.Writer, entityType, query, backend string, env []string) (escalationIndex, bool) {
	res, err := runPgConnector(ctx, []string{entityType, "list", "--query", query, "--backend", backend, "--output", "json"}, env)
	if err != nil || !classifyExit(res.exitCode) {
		reason := "exit " + fmt.Sprint(res.exitCode)
		if err != nil {
			reason = err.Error()
		} else if msg := strings.TrimSpace(string(res.stderr)); msg != "" {
			reason += ": " + msg
		}
		fmt.Fprintf(stderr, "warning: escalation query %q failed (%s); listing without the escalation filter\n", query, reason)
		return escalationIndex{}, false
	}
	idx, err := buildEscalationIndex(res.stdout)
	if err != nil {
		fmt.Fprintf(stderr, "warning: escalation query %q is not a list response (%v); listing without the escalation filter\n", query, err)
		return escalationIndex{}, false
	}
	return idx, true
}
