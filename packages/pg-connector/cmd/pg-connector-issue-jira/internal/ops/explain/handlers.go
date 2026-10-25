package explain

import (
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/summary"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/groupkit"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/scheduler"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// OpName is the wire name of the op.
const OpName = "explain"

// Ops is the set of ops this package serves.
var Ops = []string{OpName}

// Deps is what the handler needs. There is deliberately no origin: explain
// reads the store and the scheduler only.
type Deps struct {
	Store     *store.Store
	Scheduler *scheduler.Scheduler
	// KeepAlive is the retention rule that says whether an issue is still
	// refreshed in the background.
	KeepAlive summary.KeepAlive
	// Rules is the freshness table the verdicts are given by.
	Rules groupkit.Table
	// Clock is the time source; nil means the system clock.
	Clock governor.Clock
}

// Handlers returns the explain handler, keyed by op name (exactly Ops), stamped
// with schema.IssueSchemaVersion. It panics when deps has no Store or
// Scheduler: that is a wiring mistake.
func Handlers(deps Deps) map[string]scriptout.OpHandler {
	if deps.Store == nil || deps.Scheduler == nil {
		panic("explain: handlers need a store and the scheduler")
	}
	if deps.Clock == nil {
		deps.Clock = governor.SystemClock{}
	}
	h := &handler{deps: deps}
	return map[string]scriptout.OpHandler{
		OpName: {SchemaVersion: schema.IssueSchemaVersion, Handle: h.handle},
	}
}
