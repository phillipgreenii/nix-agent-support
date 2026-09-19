// search.go: Backend's search.Provider implementation — execs `pa-monitor
// search <query>` and maps matches onto schema.SearchResult.
package internal

import (
	"context"
	"encoding/json"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/search"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
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
// documented freedom boundary). It cannot pass a time bound through to
// `pa-monitor search` here: pkg/provider/search.Provider's own signature
// (query, fields — shared by every search backend, not just this one) has
// no time-bound parameter at all, so this call is always unbounded
// regardless of pa-monitor's own --since/--before support. Extending the
// shared interface is tracked separately as bead pg2-emmut.
func (b *Backend) Search(ctx context.Context, query string, _ []string) ([]schema.SearchResult, error) {
	raw, err := b.runner.Search(ctx, query, "")
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
