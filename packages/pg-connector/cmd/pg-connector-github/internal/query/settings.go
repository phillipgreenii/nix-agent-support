package query

import (
	"sort"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/config"
	shared "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/query"
)

// configSettings is the GitHub implementation of the shared query.Settings over
// the daemon's own config (pr.queries and the TTLs).
type configSettings struct{ cfg *config.Config }

var _ shared.Settings = configSettings{}

func (s configSettings) Expr(name string) ([]string, bool) {
	e, ok := s.cfg.PR.Queries[name]
	return e, ok
}

// Watched is false until the watch packet wires it.
func (configSettings) Watched(string) bool { return false }

func (s configSettings) Names() []string {
	names := make([]string, 0, len(s.cfg.PR.Queries))
	for name := range s.cfg.PR.Queries {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// TTL is query_ttl for the configured query, adhoc_query_ttl for an ad-hoc key.
func (s configSettings) TTL(key shared.Key) time.Duration {
	if key.Adhoc() {
		return s.cfg.AdhocQueryTTL.Std()
	}
	return s.cfg.QueryTTLFor(key.Name).Std()
}
