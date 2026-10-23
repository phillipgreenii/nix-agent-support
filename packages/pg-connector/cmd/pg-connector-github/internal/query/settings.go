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
	def, ok := s.cfg.PR.Queries[name]
	return def.Expr, ok
}

// Watched reports the configured query's watched flag: a property of the
// configuration, not of the store. A name the configuration does not define is
// not watched.
func (s configSettings) Watched(name string) bool { return s.cfg.PR.Queries[name].Watched }

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
