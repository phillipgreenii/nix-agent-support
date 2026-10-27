package changes

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// issueEntityType is the entity type the backend resolver serves.
const issueEntityType = "issue"

// ErrAmbiguousBackend is returned (wrapped, naming the id) by
// ResolveIssueBackend when the persisted watch sets of two different backend
// instances both hold the same issue id: the id cannot be pinned to one
// backend. It is a doctor gate; a caller never guesses.
var ErrAmbiguousBackend = errors.New("changes: issue id is served by queries of two different backends")

// ResolveIssueBackend names the backend instance that serves issue id, and
// whether that instance owns its changes. It does no I/O: it reads only the
// store and the config.
//
// A non-NULL backend column wins: the column value is the instance, and owning
// is true when it is one of cfg.OwningInstances("issue"). For a NULL column
// (or no row yet), a non-owning watched query whose persisted watch set holds
// the id gives that query's instance with owning=false, and ids held by sets of
// two different instances yield ErrAmbiguousBackend. An id in no set falls back
// to cfg.OwningInstanceForID, which gives the single owning instance with
// owning=true when the id matches ticket_patterns; otherwise the answer is "",
// false, nil and the caller keeps today's unpinned read.
func ResolveIssueBackend(cfg *config.Config, st *store.Store, id string) (instance string, owning bool, err error) {
	if cfg == nil || st == nil {
		return "", false, errors.New("changes: resolve issue backend: config and store are required")
	}
	repo := ""
	if len(cfg.Repos) > 0 {
		repo = cfg.Repos[0].Remote
	}
	row, found, err := st.GetEntity(repo, issueEntityType, id)
	if err != nil {
		return "", false, fmt.Errorf("changes: resolve issue backend %s: %w", id, err)
	}
	if found && row.Backend != "" {
		return row.Backend, slices.Contains(cfg.OwningInstances(issueEntityType), row.Backend), nil
	}

	held := map[string]bool{}
	for _, q := range cfg.NonOwningWatchQueries(issueEntityType) {
		b, ok := cfg.WatchQueryBackend(issueEntityType, q)
		if !ok || b.Instance == "" {
			continue
		}
		if loadWatchSet(st, issueEntityType, q)[id] {
			held[b.Instance] = true
		}
	}
	switch len(held) {
	case 0:
	case 1:
		for inst := range held {
			return inst, false, nil
		}
	default:
		insts := make([]string, 0, len(held))
		for inst := range held {
			insts = append(insts, inst)
		}
		sort.Strings(insts)
		return "", false, fmt.Errorf("%w: %s is held by %v", ErrAmbiguousBackend, id, insts)
	}

	if inst, ok := cfg.OwningInstanceForID(issueEntityType, id); ok {
		return inst, true, nil
	}
	return "", false, nil
}

// ProbeOwning asks each owning instance in cfg.OwningInstances("issue") order
// whether it holds issue id (a --backend-pinned daemon read, never a call to
// another backend) and returns the first that does. It stops at the FIRST
// error, including the daemon being unavailable, and returns it: an id is
// concluded to be held by no owning instance only when every instance
// answered not held (held=false, err=nil).
func ProbeOwning(ctx context.Context, g *gather.Gatherer, cfg *config.Config, id string) (instance string, held bool, err error) {
	for _, inst := range cfg.OwningInstances(issueEntityType) {
		ok, err := g.HoldsIssue(ctx, id, inst)
		if err != nil {
			return "", false, fmt.Errorf("changes: probe %s for %s: %w", inst, id, err)
		}
		if ok {
			return inst, true, nil
		}
	}
	return "", false, nil
}
