package gather

import (
	"context"
	"encoding/json"
	"fmt"
)

// openIDsWireOut is the subset of pg-connector's `<type> list --ids-only`
// outcome this package decodes: with --ids-only the entities[] array is empty
// by design and present_ids carries every matched id.
type openIDsWireOut struct {
	PresentIDs []string `json:"present_ids"`
}

// ListOpenIDs runs the cheap, uncharged `pg-connector <type> list --query
// <query> --ids-only --output json` through the package's single exec
// chokepoint and returns the ids the query currently lists (bead pg2-hpakl:
// reconcile uses the union over the watched queries as the "still open" set,
// so it re-reads only anchors whose PR is absent from it). It classifies the
// call by pg-connector's fan-out exit scheme: 0 ok, 2 partial (the partial id
// list is returned: an id a healthy backend listed is open whatever the
// degraded ones did), anything else, a failure to start pg-connector, or an
// undecodable response is an error. A caller MUST NOT read an id's ABSENCE as
// "left the open set" on this result alone; absence only means "not shown to
// be open", which is the safe direction for a re-read filter.
func (g *Gatherer) ListOpenIDs(ctx context.Context, entityType, query string) ([]string, error) {
	if entityType == "" || query == "" {
		return nil, fmt.Errorf("gather: list open ids: type and query are required")
	}
	var env []string
	if entityType == "issue" {
		env = g.issueBeadsDirEnv()
	}
	args := []string{entityType, "list", "--query", query, "--ids-only", "--output", "json"}
	raw, err := g.fanOutCall(ctx, args, env)
	if err != nil {
		return nil, err
	}
	var wire openIDsWireOut
	if err := json.Unmarshal(raw, &wire); err != nil {
		return nil, fmt.Errorf("decode pg-connector %v stdout: %w", args, err)
	}
	return wire.PresentIDs, nil
}
