package query

import (
	"sort"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/config"
	lq "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/query"
)

// settings is the Jira implementation of the shared query.Settings over the
// daemon's own config (queries and the TTLs).
type settings struct{ cfg *config.Config }

var _ lq.Settings = settings{}

// NewSettings returns the query settings over cfg. Expr(name) is the query's
// expression list (each element is raw JQL) and false for a name the config does
// not define; Watched(name) is the declared watched value (false for a name the
// config does not define); Names is the sorted configured names; TTL is
// query_ttl for a configured key (query_ttl_default for one with no entry) and
// the ad-hoc ttl, query_ttl_default, for an ad-hoc key.
func NewSettings(cfg *config.Config) lq.Settings { return settings{cfg: cfg} }

func (s settings) Expr(name string) ([]string, bool) {
	def, ok := s.cfg.Queries[name]
	if !ok {
		return nil, false
	}
	return []string(def.Expr), true
}

func (s settings) Watched(name string) bool { return s.cfg.Queries[name].Watched }

func (s settings) Names() []string {
	names := make([]string, 0, len(s.cfg.Queries))
	for name := range s.cfg.Queries {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (s settings) TTL(key lq.Key) time.Duration {
	if key.Adhoc() {
		return s.cfg.QueryTTLDefault.Std()
	}
	return s.cfg.QueryTTLFor(key.Name).Std()
}
