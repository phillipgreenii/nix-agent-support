package feedop

import (
	"context"
	"encoding/json"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// Renderer builds the wire entity of a delivered row. Both methods return JSON
// for the connector's own schema type; the shared handler never decodes it.
type Renderer interface {
	// Row renders the entity as it is stored now with the row's OWN version and
	// stamp set.
	Row(ctx context.Context, row store.ChangeRow) (json.RawMessage, error)
	// Replay renders the entity of a reset replay entry from its wire reference.
	Replay(ctx context.Context, ref string) (json.RawMessage, error)
}
