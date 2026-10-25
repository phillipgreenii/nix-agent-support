package feed

import (
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/feed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/feedop"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// View is the one view the Jira registration serves.
const View = "issue"

// The ops, as the router keys them.
const (
	OpChanges    = "changes"
	OpChangesAck = "changes_ack"
)

// Ops lists the ops Handlers returns, for the assembly's drift guard.
var Ops = []string{OpChanges, OpChangesAck}

// Deps is what the handlers need.
type Deps struct {
	Feed     *feed.Feed
	Store    *store.Store
	Renderer feedop.Renderer
	// Instance is the configured instance name, written to sources[].backend and
	// changes[].source.
	Instance string
	// Clock is the time source; nil means the system clock.
	Clock governor.Clock
}

// Handlers returns exactly changes and changes_ack, stamped with
// schema.IssueSchemaVersion, as the shared feedop handlers for view "issue".
// The wire errors come from feedop.MapError; nothing is wrapped here.
func Handlers(d Deps) map[string]scriptout.OpHandler {
	return feedop.Handlers(feedop.Deps{
		Feed: d.Feed, Store: d.Store, View: View, Source: d.Instance,
		Renderer: d.Renderer, AuthErrors: []error{scriptout.ErrUnauthenticated},
		SchemaVersion: schema.IssueSchemaVersion, Clock: d.Clock,
	})
}
