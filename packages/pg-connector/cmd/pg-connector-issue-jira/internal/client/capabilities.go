package client

import (
	"maps"

	jira "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// Vocabulary keys the client declares, compiled in. cache_opt_out keeps the
// umbrella's own cache and ledger diff off for this backend; owns_changes says
// the backend owns its change feed; queries names every configured query and
// whether it is watched (the backend side of INV-ICD-14(a)).
const (
	VocabCacheOptOut = "cache_opt_out"
	VocabOwnsChanges = "owns_changes"
	VocabQueries     = "queries"
	VocabState       = "state"
	VocabPriority    = "priority"
	VocabActivity    = "activity_kinds"
)

// QueryInfo is one entry of the queries vocabulary object.
type QueryInfo struct {
	Watched bool `json:"watched"`
}

// Capabilities is the capabilities answer, built from compiled-in values and
// the loaded config alone; it never contacts the daemon, so an outage can never
// be mistaken for a backend that opted out of the umbrella's cache. cfg may be
// nil, in which case no query is listed.
func Capabilities(cfg *config.Config, build string) scriptout.CapabilitiesResponse {
	queries := map[string]QueryInfo{}
	if cfg != nil {
		for name, def := range cfg.Queries {
			queries[name] = QueryInfo{Watched: def.Watched}
		}
	}
	vocab := map[string]any{
		VocabState:       jira.Vocabulary,
		VocabPriority:    jira.PriorityVocabulary,
		VocabActivity:    jira.ActivityKinds,
		VocabCacheOptOut: true,
		VocabOwnsChanges: true,
		VocabQueries:     queries,
	}
	versions := map[string]int{
		"issue":    schema.IssueSchemaVersion,
		"search":   schema.SearchSchemaVersion,
		"activity": schema.ActivitySchemaVersion,
	}
	out := scriptout.CapabilitiesResponse{
		ProtocolVersion: scriptout.ProtocolVersion,
		SchemaVersions:  maps.Clone(versions),
		Ops:             OpNames(),
		Vocabulary:      vocab,
		Version:         build,
	}
	return out
}
