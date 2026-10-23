package engine

import (
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// Primary is what the engine needs of an entity's stored primary-group content.
type Primary struct {
	Ref      string    // the entity's wire reference as of this content
	Stamp    string    // opaque; stored with every row in the stamp column (see store accessors)
	Stamp2   string    // opaque; stored with a pending row
	OriginAt time.Time // the origin's own update time, zero when unknown
	Gone     bool
}

// Classifier is the connector's change classifier. It is pure and store-free.
type Classifier interface {
	// Describe reads the stored primary content. A content that does not decode is an error.
	Describe(content []byte) (Primary, error)
	// Primary classifies a refreshed primary-group content change (prev is nil for a
	// first sighting, which the engine never passes) and names the groups that must be
	// re-fetched before the row settles (the awaited set).
	Primary(prev, next []byte, bodyCap int) (res Result, dependents []string, err error)
	// Group classifies one non-primary group's change.
	Group(name string, prev, next []byte, bodyCap int) (Result, error)
	// Counts reports whether a fetch of a group satisfies an awaited group of a pending
	// row (a stamped group compares the stamps it was fetched for with the pending row's).
	Counts(a Awaiting, fetched store.GroupRow, pending store.PendingChange, started time.Time) bool
}
