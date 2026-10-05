// search.go: Backend's search.Provider implementation — execs `pa-monitor
// search <query>` and maps matches onto schema.SearchResult.
package internal

import (
	"context"
	"encoding/json"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/search"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

var _ search.Provider = (*Backend)(nil)

type searchMatchJSON struct {
	SessionID string `json:"session_id"`
	Role      string `json:"role"`
	Line      int    `json:"line"`
	Snippet   string `json:"snippet"`
	Timestamp string `json:"timestamp,omitempty"`
}

type searchJSONDoc struct {
	Matches []searchMatchJSON `json:"matches"`
}

// Search ignores fields — pa-monitor's search subcommand has no
// attribute-selection concept; a well-behaved Provider silently ignores an
// unsupported requested attribute (pkg/provider/search.Provider's own
// documented freedom boundary). The time bound rides the request config, not
// the shared Provider signature (the 2026-09-18 search time-bound design,
// bead pg2-ttk9t): the umbrella merges search_since/search_before (RFC3339)
// onto this backend's config for a bounded call, and they are forwarded to
// `pa-monitor search --since/--before`. Absent keys leave the call
// unbounded; a malformed value is invalid_argument.
func (b *Backend) Search(ctx context.Context, query string, _ []string) ([]schema.SearchResult, error) {
	rng, rngErr := scriptout.SearchRangeFromContext(ctx)
	if rngErr != nil {
		return nil, rngErr
	}
	raw, err := b.runner.Search(ctx, query, "", rng)
	if err != nil {
		return nil, classifyPaMonitorError(err)
	}
	var doc searchJSONDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, classifyPaMonitorError(err)
	}
	var results []schema.SearchResult
	for _, m := range doc.Matches {
		attrs := map[string]any{"role": m.Role, "line": m.Line}
		if m.Timestamp != "" {
			attrs["timestamp"] = m.Timestamp
		}
		results = append(results, schema.SearchResult{
			Type: "agentsession", ID: m.SessionID, Title: m.Snippet, Source: "pa-monitor",
			Attributes: attrs,
		})
	}
	return results, nil
}
