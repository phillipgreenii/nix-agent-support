// nodes_by_ids.go: the batched fetch of SUMMARY-level pull requests by GraphQL
// node id (bead pg2-12yur), the call behind the pr show_many op.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-pr-github/internal/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-pr-github/internal/eventlog"
)

// nodesBatchedChunkSize is how many node ids one nodesByIDsQuery document
// names: the largest size that costs 1 GraphQL point for
// pullRequestSummaryFragment (75 to 100 ids cost 2). It is the spec's measured
// "refresh by ids" row (bead pg2-cw6b3.1 spike, 2026-10-06), an observed
// property of GitHub's cost formula for this exact field set and not a
// documented constant. Re-measure with rateLimit(dryRun: true) before changing
// it or the fragment; TestNodesByIDsQuery_PinnedFieldSet fails on any change to
// the document so that re-measurement is not skipped.
const nodesBatchedChunkSize = 74

// nodesByIDsQuery is the batched fetch-by-ids document: the same summary
// fragment as searchBatchedQuery, selected through nodes(ids: [...]). The ids
// are written into the document (not passed as a variable) after nodeIDPattern
// has vetted each one, so the document needs no array-variable support from
// `gh api graphql`. A node that is not a PullRequest decodes with every field
// at its zero value (Number 0), exactly like a search node.
//
// rateLimit { cost remaining resetAt } is selected for the same reason as in
// the search document: the reserve is judged from the answer's own reading,
// and the cost is added to the call's event.
var nodesByIDsQuery = fmt.Sprintf(`
query {
  rateLimit { cost remaining resetAt }
  nodes(ids: [%%s]) {
    %s
  }
}
`, pullRequestSummaryFragment)

// nodeIDPattern is what a GraphQL node id may look like: URL-safe base64
// characters, as GitHub's opaque ids ("PR_kwDO...") are. An id outside it is
// rejected before it can reach the document text.
var nodeIDPattern = regexp.MustCompile(`^[A-Za-z0-9_=+/-]{1,200}$`)

// ValidNodeID reports whether id may be sent in a nodes(ids:) document.
func ValidNodeID(id string) bool { return nodeIDPattern.MatchString(id) }

// gqlNodeError is one entry of a GraphQL response's errors array.
type gqlNodeError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
	Path    []any  `json:"path"`
}

// nodeLookupFailure reports whether e is the per-node "this id resolves to
// nothing" answer: the one error GitHub attaches to a null element of nodes
// for a deleted or inaccessible node. Anything else is a real failure.
func (e gqlNodeError) nodeLookupFailure() bool {
	if len(e.Path) == 0 || e.Path[0] != "nodes" {
		return false
	}
	return e.Type == "NOT_FOUND" || strings.Contains(strings.ToLower(e.Message), "could not resolve to a node")
}

// ghNodesResponse is nodesByIDsQuery's response envelope. Nodes is
// position-aligned with the requested ids; a null element is a node the origin
// could not resolve.
type ghNodesResponse struct {
	Data struct {
		RateLimit *struct {
			Cost      int    `json:"cost"`
			Remaining int    `json:"remaining"`
			ResetAt   string `json:"resetAt"`
		} `json:"rateLimit"`
		Nodes []*ghBatchedSearchNode `json:"nodes"`
	} `json:"data"`
	Errors []gqlNodeError `json:"errors"`
}

// SummariesByNodeIDs fetches the summary-level PR of every node id in nodeIDs
// with nodesByIDsQuery, nodesBatchedChunkSize ids per call, and returns the
// ones the origin resolved to a pull request keyed by node id. An id that
// resolves to nothing (a deleted or inaccessible node), or to something that
// is not a pull request, is simply absent from the map: a null node is a
// per-id answer, never a failure of the call. gh reports such a node as a
// non-zero exit that still prints the response, so a response whose only
// errors are per-node lookup failures is honoured as a partial answer.
//
// Any other failure (a transport or auth error, a rate refusal, a malformed or
// misaligned response, a GraphQL error that is not a per-node lookup failure)
// returns an error and NO result, even when earlier chunks succeeded, so a
// caller never sees a partial map without knowing it: the whole call failed,
// and the caller chunks its own request to bound what one failure costs.
//
// gate, when non-nil, judges each chunk's rateLimit reading before that
// chunk's results are used and before the next chunk is requested, exactly as
// SearchPRsEnrichedGated does; a response without a reading is an error when
// gate is non-nil (fail closed). Each chunk's cost is added to the call's
// event.
//
// A node id outside ValidNodeID is an error (callers vet their input; this is
// the last line before the document text), and a duplicate id is requested
// once.
func (p *Provider) SummariesByNodeIDs(ctx context.Context, nodeIDs []string, gate RateGate) (map[string]api.PR, error) {
	seen := make(map[string]bool, len(nodeIDs))
	ids := make([]string, 0, len(nodeIDs))
	for _, id := range nodeIDs {
		if !ValidNodeID(id) {
			return nil, fmt.Errorf("github: node id %q is not a valid GraphQL node id", id)
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}

	out := make(map[string]api.PR, len(ids))
	for start := 0; start < len(ids); start += nodesBatchedChunkSize {
		end := start + nodesBatchedChunkSize
		if end > len(ids) {
			end = len(ids)
		}
		chunk := ids[start:end]
		quoted := make([]string, len(chunk))
		for i, id := range chunk {
			quoted[i] = `"` + id + `"`
		}
		doc := fmt.Sprintf(nodesByIDsQuery, strings.Join(quoted, ", "))
		raw, runErr := p.runRead(ctx, readOpts{}, "api", "graphql", "-F", "query="+doc)

		var resp ghNodesResponse
		parsed := len(bytes.TrimSpace(raw)) > 0 && json.Unmarshal(raw, &resp) == nil
		if runErr != nil && (!parsed || !onlyNodeLookupFailures(resp.Errors)) {
			return nil, runErr
		}
		if !parsed {
			return nil, errors.New("github: parse nodes graphql response: not a JSON document")
		}
		if runErr == nil && len(resp.Errors) > 0 && !onlyNodeLookupFailures(resp.Errors) {
			return nil, fmt.Errorf("github: nodes graphql response carried errors: %s", resp.Errors[0].Message)
		}
		if len(resp.Data.Nodes) != len(chunk) {
			return nil, fmt.Errorf("github: nodes graphql response answered %d of %d requested ids", len(resp.Data.Nodes), len(chunk))
		}
		if rl := resp.Data.RateLimit; rl != nil {
			eventlog.AddGraphQLCost(ctx, rl.Cost)
		}
		if gate != nil {
			rl := resp.Data.RateLimit
			if rl == nil {
				return nil, errors.New("github: nodes graphql response carried no rateLimit reading")
			}
			if gerr := gate(RateLimit{Remaining: rl.Remaining, ResetAt: rl.ResetAt}); gerr != nil {
				return nil, gerr
			}
		}
		for i, n := range resp.Data.Nodes {
			if n == nil || n.Number == 0 {
				continue // null, or a node that is not a pull request
			}
			pr := n.toAPI()
			if pr.NodeID == "" {
				pr.NodeID = chunk[i]
			}
			out[chunk[i]] = pr
		}
	}
	return out, nil
}

// onlyNodeLookupFailures reports whether errs is non-empty and every entry is
// a per-node lookup failure.
func onlyNodeLookupFailures(errs []gqlNodeError) bool {
	if len(errs) == 0 {
		return false
	}
	for _, e := range errs {
		if !e.nodeLookupFailure() {
			return false
		}
	}
	return true
}
