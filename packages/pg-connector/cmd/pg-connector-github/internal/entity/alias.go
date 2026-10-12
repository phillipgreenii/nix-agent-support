package entity

import (
	"context"
	"fmt"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// AliasStore is the part of the daemon store Identity uses; *store.Store
// satisfies it.
type AliasStore interface {
	PutAlias(ctx context.Context, a store.AliasRow) error
	AliasByNodeID(ctx context.Context, nodeID string) (store.AliasRow, bool, error)
	AliasByRefFold(ctx context.Context, ref string) (store.AliasRow, bool, error)
}

var _ AliasStore = (*store.Store)(nil)

// Update is the outcome of recording an alias.
type Update struct {
	NodeID string
	// Ref is the reference now recorded for NodeID.
	Ref Ref
	// Renamed is true when NodeID already had a different reference (the
	// repository was renamed or transferred, or its spelling changed); Old is
	// that earlier reference. The node id is the same: only the alias moved.
	// The change row that reports a rename is appended by the change log, not
	// here.
	Renamed bool
	Old     Ref
}

// Identity reads and updates the alias table, which maps a GraphQL node id to
// the PR's current owner/repo#n.
type Identity struct {
	store AliasStore
	clock governor.Clock
}

// NewIdentity returns an Identity over st. A nil clock means the system clock.
func NewIdentity(st AliasStore, clock governor.Clock) *Identity {
	if clock == nil {
		clock = governor.SystemClock{}
	}
	return &Identity{store: st, clock: clock}
}

// Ref returns the reference recorded for nodeID.
func (i *Identity) Ref(ctx context.Context, nodeID string) (Ref, bool, error) {
	row, ok, err := i.store.AliasByNodeID(ctx, nodeID)
	if err != nil || !ok {
		return Ref{}, false, err
	}
	ref, err := ParseRef(row.Ref)
	if err != nil {
		return Ref{}, false, fmt.Errorf("entity: alias of %q holds an unparsable reference: %w", nodeID, err)
	}
	return ref, true, nil
}

// NodeID returns the node id recorded for ref, matching owner and repository
// case-insensitively, together with the reference as recorded (GitHub's own
// spelling).
func (i *Identity) NodeID(ctx context.Context, ref Ref) (nodeID string, canonical Ref, ok bool, err error) {
	row, ok, err := i.store.AliasByRefFold(ctx, ref.String())
	if err != nil || !ok {
		return "", Ref{}, false, err
	}
	canonical, err = ParseRef(row.Ref)
	if err != nil {
		return "", Ref{}, false, fmt.Errorf("entity: alias %q holds an unparsable reference: %w", row.NodeID, err)
	}
	return row.NodeID, canonical, true, nil
}

// Put records that nodeID is ref. When nodeID already had a different
// reference the alias is updated in place (the node id does not change) and the
// Update says it was a rename. Recording the same reference again only
// refreshes seen_at.
func (i *Identity) Put(ctx context.Context, nodeID string, ref Ref) (Update, error) {
	if nodeID == "" {
		return Update{}, fmt.Errorf("entity: empty node id for %s", ref)
	}
	if ref.IsZero() {
		return Update{}, fmt.Errorf("entity: empty reference for node id %q", nodeID)
	}
	up := Update{NodeID: nodeID, Ref: ref}
	old, ok, err := i.Ref(ctx, nodeID)
	if err != nil {
		return Update{}, err
	}
	if ok && old != ref {
		up.Renamed, up.Old = true, old
	}
	err = i.store.PutAlias(ctx, store.AliasRow{NodeID: nodeID, Ref: ref.String(), SeenAt: i.now()})
	if err != nil {
		return Update{}, err
	}
	return up, nil
}

func (i *Identity) now() time.Time { return i.clock.Now() }
