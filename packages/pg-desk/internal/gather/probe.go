package gather

import (
	"context"
	"fmt"
)

// ProbeQuery reports whether pg-connector recognizes query as a watched
// query of entityType, WITHOUT any side effect: it execs the read-only
// listing verb `pg-connector <type> list --query <q> --ids-only --output
// json` through this package's single exec chokepoint (run). The listing
// verb touches no change ledger and no consumer cursor, and exits non-zero
// (invalid_argument) when every backend answers query_not_recognized, so an
// unknown query genuinely fails — unlike `changes --cached`, which skips the
// backend refresh and would still look healthy [design: 9.10, 11].
//
// The call is classified by the fan-out exit scheme shared with fanOutCall:
// exit 0 or 2 pass (2 is partial degradation of some backend, which says
// nothing about the query name); any other outcome, including a failure to
// start pg-connector, is an error.
func (g *Gatherer) ProbeQuery(ctx context.Context, entityType, query string) error {
	if entityType == "" || query == "" {
		return fmt.Errorf("gather: probe query: type and query are required")
	}
	var env []string
	if entityType == "issue" {
		env = g.issueBeadsDirEnv()
	}
	_, err := g.fanOutCall(ctx, []string{entityType, "list", "--query", query, "--ids-only", "--output", "json"}, env)
	return err
}
