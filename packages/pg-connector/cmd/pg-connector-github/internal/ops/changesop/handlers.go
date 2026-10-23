package changesop

import (
	"context"
	"encoding/json"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/feed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ops/status"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/vcs"
	sharedfeed "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/feed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/feedop"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// The ops, as the router keys them under the pr and ci instances.
const (
	OpChanges    = "changes"
	OpChangesAck = "changes_ack"
)

// Deps is what the handlers need. The feed's own Queries is built by the
// assembly; the handlers read the stored configured query rows themselves.
type Deps struct {
	Feed    *feed.Feed
	Store   *store.Store
	Aliases *entity.Identity
	// Clock is the time source; nil means the system clock.
	Clock governor.Clock
}

// Handlers returns the handlers of the change feed, keyed by instance and then
// op: for pr the ops changes {consumer, query, cached, reset} and changes_ack
// {consumer, query, next_seq}, each stamped with schema.PRSchemaVersion, and for
// ci the ops changes and changes_ack with the same arguments, each stamped with
// schema.CISchemaVersion and acting on the key (consumer, ci, query). The ops
// themselves are the shared feedop handlers; this package supplies the two
// renderers (the PR entity and the CI entity) and the unauthenticated sentinel.
func Handlers(deps Deps) map[string]map[string]scriptout.OpHandler {
	if deps.Clock == nil {
		deps.Clock = governor.SystemClock{}
	}
	h := &handler{deps: deps}
	var shared *sharedfeed.Feed
	if deps.Feed != nil {
		shared = deps.Feed.Shared()
	}
	return map[string]map[string]scriptout.OpHandler{
		"pr": feedop.Handlers(feedop.Deps{
			Feed: shared, Store: deps.Store, View: feed.KindPR, Source: status.RegistryNamePR,
			Renderer: prRenderer{h}, AuthErrors: authErrors, SchemaVersion: schema.PRSchemaVersion, Clock: deps.Clock,
		}),
		"ci": feedop.Handlers(feedop.Deps{
			Feed: shared, Store: deps.Store, View: feed.KindCI, Source: status.RegistryNameCI,
			Renderer: ciRenderer{h}, AuthErrors: authErrors, SchemaVersion: schema.CISchemaVersion, Clock: deps.Clock,
		}),
	}
}

// authErrors are the errors that map to unauthenticated.
var authErrors = []error{vcs.ErrAuthInvalid}

type handler struct{ deps Deps }

// prRenderer renders the entity of the pr view: the summary-level PR.
type prRenderer struct{ h *handler }

func (r prRenderer) Row(ctx context.Context, row store.ChangeRow) (json.RawMessage, error) {
	pr, err := r.h.rowEntity(ctx, row.ID, row.Version, row.HeadSHA)
	if err != nil {
		return nil, err
	}
	return json.Marshal(pr)
}

func (r prRenderer) Replay(ctx context.Context, ref string) (json.RawMessage, error) {
	pr, err := r.h.currentEntity(ctx, ref)
	if err != nil {
		return nil, err
	}
	return json.Marshal(pr)
}

// ciRenderer renders the entity of the ci view: the PR's stored CI state.
type ciRenderer struct{ h *handler }

func (r ciRenderer) Row(ctx context.Context, row store.ChangeRow) (json.RawMessage, error) {
	e, err := r.h.ciRowEntity(ctx, row.ID, row.Version, row.HeadSHA)
	if err != nil {
		return nil, err
	}
	return json.Marshal(e)
}

func (r ciRenderer) Replay(ctx context.Context, ref string) (json.RawMessage, error) {
	e, err := r.h.ciCurrentEntity(ctx, ref)
	if err != nil {
		return nil, err
	}
	return json.Marshal(e)
}

var (
	_ feedop.Renderer = prRenderer{}
	_ feedop.Renderer = ciRenderer{}
)
