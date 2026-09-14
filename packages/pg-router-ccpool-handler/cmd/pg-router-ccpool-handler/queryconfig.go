package main

import (
	"encoding/json"
	"fmt"
	"os"
)

// envQueryConfig names this module's own per-process query configuration
// input — the beads-backed filters ONE `query` invocation answers with
// (docket pg2-oju6w's Task 5.8, mirroring envRoleConfig's own per-process
// role convention). An ABSENT --query-config/envQueryConfig is deliberately
// NOT an error (unlike loadRole's own empty-path rejection): it is what
// keeps this module's `query` subcommand answering the same schema-legal,
// zero-event stub reply it always has (see runQuery's own doc comment) for
// any invocation that has not opted into the real beads-backed source yet.
const envQueryConfig = "PG_ROUTER_CCPOOL_HANDLER_QUERY"

// queryFile is the on-disk JSON shape --query-config decodes: the
// beads-backed query's own label/exclude-label/title-prefix/item-type
// filters (ported from packages/pg-router/internal/query/beads.go's
// BeadsReady, moved here by Task 5.8) plus the event type this invocation
// emits.
type queryFile struct {
	// EmitType is the event `type` every produced event carries — the
	// [[query]].emits value pg-router's own config binds a role to.
	EmitType string `json:"emitType"`
	// Labels/ExcludeLabels feed `bd ready --label ... --exclude-label ...`.
	Labels        []string `json:"labels,omitempty"`
	ExcludeLabels []string `json:"excludeLabels,omitempty"`
	// TitlePrefix/ItemType are optional client-side post-filters, exactly as
	// BeadsReady.Run applied them.
	TitlePrefix string `json:"titlePrefix,omitempty"`
	ItemType    string `json:"itemType,omitempty"`
}

// loadQueryConfig reads and decodes a queryFile from path. An empty path
// returns the zero queryFile with ok=false — the caller's signal to answer
// the stub (zero-event) reply rather than running any beads query at all;
// see this file's own envQueryConfig doc comment.
func loadQueryConfig(path string) (qf queryFile, ok bool, err error) {
	if path == "" {
		return queryFile{}, false, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return queryFile{}, false, fmt.Errorf("read query config %s: %w", path, err)
	}
	if err := json.Unmarshal(data, &qf); err != nil {
		return queryFile{}, false, fmt.Errorf("decode query config %s: %w", path, err)
	}
	if qf.EmitType == "" {
		return queryFile{}, false, fmt.Errorf("query config %s: emitType is required", path)
	}
	return qf, true, nil
}
