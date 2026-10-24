package prread

import (
	"context"
	"fmt"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/changes"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/registry"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// Watcher applies the watch option of a direct read: the PR the read names
// becomes watched, which makes the daemon append change rows for it. It is two
// steps because of when the daemon can name the PR:
//
//   - WatchKnown runs BEFORE the read, for a PR whose reference the alias table
//     already knows. A change the read's own refresh then finds is a row of a
//     watched entity, and a known PR gets one added row so its consumer
//     hydrates it (under a registration that emits for watched entities only).
//   - WatchSeen runs AFTER the read, for a PR the daemon had never seen: the
//     read has just stored it, and the watch only sets the flag. Its first
//     fetch appended no row, so the watch appends none either; the PR's next
//     change is its first row.
//
// A read without the option never reaches a Watcher and never clears a watch.
type Watcher interface {
	// WatchKnown watches the PR with the wire reference id when the daemon
	// already knows it and reports whether it did.
	WatchKnown(ctx context.Context, id string) (known bool, err error)
	// WatchSeen watches the PR the read just stored: node is its entity key and
	// id the wire reference the caller used.
	WatchSeen(ctx context.Context, node, id string) error
}

// WatchEngine is the change engine as the watch option uses it. *changes.Engine
// satisfies it.
type WatchEngine interface {
	WatchOne(ctx context.Context, st *store.Store, w changes.WatchRequest) (changes.WatchResult, error)
}

var _ WatchEngine = (*changes.Engine)(nil)

// AliasLookup is the alias table as the watch option uses it. *entity.Identity
// satisfies it.
type AliasLookup interface {
	NodeID(ctx context.Context, ref entity.Ref) (nodeID string, canonical entity.Ref, ok bool, err error)
}

var _ AliasLookup = (*entity.Identity)(nil)

// NewWatcher returns the Watcher over the change engine, its store and the alias
// table.
func NewWatcher(eng WatchEngine, st *store.Store, aliases AliasLookup) Watcher {
	return &storeWatcher{eng: eng, st: st, aliases: aliases}
}

type storeWatcher struct {
	eng     WatchEngine
	st      *store.Store
	aliases AliasLookup
}

func (w *storeWatcher) WatchKnown(ctx context.Context, id string) (bool, error) {
	ref, err := entity.ParseRef(id)
	if err != nil {
		return false, err
	}
	node, canonical, ok, err := w.aliases.NodeID(ctx, ref)
	if err != nil || !ok {
		return false, err
	}
	_, err = w.eng.WatchOne(ctx, w.st, changes.WatchRequest{
		ID: node, Ref: canonical.String(), Via: registry.ViaDirect, Mode: changes.WatchLate,
	})
	if err != nil {
		return false, fmt.Errorf("watch %s: %w", id, err)
	}
	return true, nil
}

func (w *storeWatcher) WatchSeen(ctx context.Context, node, id string) error {
	_, err := w.eng.WatchOne(ctx, w.st, changes.WatchRequest{
		ID: node, Ref: id, Via: registry.ViaDirect, Mode: changes.WatchBaseline,
	})
	if err != nil {
		return fmt.Errorf("watch %s: %w", id, err)
	}
	return nil
}
