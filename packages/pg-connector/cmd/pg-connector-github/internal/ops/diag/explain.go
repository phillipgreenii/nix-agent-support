package diag

import (
	"fmt"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ops/explain"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// ExplainHandlers returns the explain handler of each instance, keyed by
// instance and then op: the Phase 2 explain handler with the freshness rules and
// the job table attached, so each group carries its status, reason,
// stamps_match and, for runs, the active and failing verdicts and the cached job
// count, and (when deps.Changes is set) the pending settle row and the last
// change rows. The assembly registers these in place of explain.Register (the router
// refuses a duplicate registration). The handlers read the store and the
// scheduler's queue positions only and call no origin. It panics when deps has
// no Store or Queue: that is a wiring mistake.
func ExplainHandlers(deps Deps) map[string]map[string]scriptout.OpHandler {
	cfg := explain.Config{Store: deps.Store, Queue: deps.Queue, Clock: deps.Clock, Rules: deps.Rules, Jobs: deps.Jobs, Changes: deps.Changes}
	out := map[string]map[string]scriptout.OpHandler{}
	for instance, version := range map[string]int{"pr": schema.PRSchemaVersion, "ci": schema.CISchemaVersion} {
		h, err := explain.Handler(cfg, instance, version)
		if err != nil {
			panic(fmt.Sprintf("diag: %v", err))
		}
		out[instance] = map[string]scriptout.OpHandler{OpExplain: h}
	}
	return out
}
