// changes_refresher.go: the refresher mode of "changes" for pr and issue
// (INV-CACHE-6, INV-CACHE-7). When the registry's cache_refresh_after is set,
// a changes call does not list every matching entity in full; it
//
//  1. reads membership with the ids-only list (the cheap call),
//  2. fetches a full entity (the backend's show) only for a member that is new
//     to the ledger or whose detail cache entry is missing or older than
//     refresh_after,
//  3. confirms every removal with one read before it is reported, and
//  4. leaves writing the fetched entities to the cache until the response has
//     been written and flushed (commitCacheWrites, below).
//
// It feeds Ledger.Refresh through the same listFn seam the ordinary path
// uses: Refresh already classifies added/changed from the entities it is
// given and removed from present_ids alone, so a refresh pass that returns
// only some entities is exactly the "cursor-scoped subset" case Refresh was
// written for. The ledger, consumer cursors and tombstones are untouched in
// meaning.
//
// The refresher fetches with the backend's own show, one call per entity,
// because no batched fetch-by-ids op exists yet. That is why it is opt-in:
// the per-pass spend is one show per new or aged member.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// refreshPass is one backend's refresher state for one changes call.
type refreshPass struct {
	// fetched are the detail entities this pass read, to be written to the
	// cache at detail level after the response is flushed.
	fetched []json.RawMessage
	// confirmed maps an id that left the membership to the entity its
	// confirmation read returned (for example a merged PR), so the removed
	// row can carry it.
	confirmed map[string]json.RawMessage
	// incomplete is true when a fetch or confirmation failed, so the pass
	// cannot be called a whole-query answer (INV-CACHE-6, INV-LEDGER-FRESH-3).
	incomplete bool
}

// refresherTypes are the entity types the refresher policy applies to.
func refresherType(entityType string) bool {
	return entityType == "pr" || entityType == "issue"
}

// refresherEnabled reports whether backend runs in refresher mode for
// entityType, and the refresh_after it uses. It is off unless
// cache_refresh_after is set, and off for a type or backend opted out of the
// cache (INV-CACHE-8).
func refresherEnabled(ctx context.Context, reg *Registry, entityType, backend string) (time.Duration, bool) {
	if !refresherType(entityType) {
		return 0, false
	}
	after := resolveCacheRefreshAfter(reg)
	if after <= 0 {
		return 0, false
	}
	if enabled, err := cacheEnabled(ctx, reg, entityType, backend); err != nil || !enabled {
		return 0, false
	}
	return after, true
}

// showEntity reads one entity with the backend's show op.
func showEntity(ctx context.Context, reg *Registry, backend, id string) (json.RawMessage, error) {
	resp, err := invokeOne(ctx, reg, backend, "show", map[string]string{"id": id})
	if err != nil {
		return nil, err
	}
	return resp.Result, nil
}

// refresherListFn builds the Ledger.Refresh listFn for the refresher mode. l
// is the ledger being refreshed (its index decides which members are new and
// which ids left); pass collects what the call fetched. *lastTruncated is set
// from the membership answer only: a pass that merely failed to fetch some
// entity is flagged on pass.incomplete, not as a truncated membership, so it
// does not suppress removals the membership itself supports.
func refresherListFn(ctx context.Context, reg *Registry, entityType, backend, query string, l *Ledger, after time.Duration, pass *refreshPass, lastTruncated *bool) func(json.RawMessage) ([]json.RawMessage, []string, json.RawMessage, bool, error) {
	return func(json.RawMessage) ([]json.RawMessage, []string, json.RawMessage, bool, error) {
		resp, err := invokeOne(ctx, reg, backend, "list", map[string]any{"query": query, "cursor": nil, "ids_only": true})
		if err != nil {
			return nil, nil, nil, false, err
		}
		var membership rawListResult
		if err := scriptout.Decode(resp.Result, &membership); err != nil {
			return nil, nil, nil, false, err
		}
		*lastTruncated = membership.Truncated

		cache, err := loadCache(CacheKey{Type: entityType, Backend: backend})
		if err != nil {
			cache = newEmptyCache() // a cache fault must not stop a refresh; everything reads as aged
		}
		now := time.Now()
		present := make(map[string]bool, len(membership.PresentIDs))
		ids := append([]string(nil), membership.PresentIDs...)
		sort.Strings(ids)

		// Stage 2: new or aged members.
		var entities []json.RawMessage
		fetchOK := true
		for _, id := range ids {
			present[id] = true
			entry, inLedger := l.Entries[id]
			tracked := inLedger && entry.RemovedAtVersion == nil
			content, _, young := cache.GetDetail(id, after, now)
			switch {
			case young && tracked:
				continue // within refresh_after: nothing to ask the origin
			case young:
				entities = append(entities, content) // new to the ledger, already held
				continue
			}
			if !fetchOK {
				pass.incomplete = true // origin went away mid-pass; the rest wait
				continue
			}
			raw, err := showEntity(ctx, reg, backend, id)
			if err != nil {
				pass.incomplete = true
				if errors.Is(err, scriptout.ErrUnavailable) {
					fetchOK = false
				}
				continue
			}
			entities = append(entities, raw)
			pass.fetched = append(pass.fetched, raw)
		}

		// Stage 3: removal confirmation. A truncated membership names no
		// removal, so there is nothing to confirm.
		presentOut := append([]string(nil), ids...)
		if !membership.Truncated {
			var gone []string
			for id, entry := range l.Entries {
				if entry.RemovedAtVersion == nil && !present[id] {
					gone = append(gone, id)
				}
			}
			sort.Strings(gone)
			for _, id := range gone {
				raw, err := showEntity(ctx, reg, backend, id)
				switch {
				case err == nil:
					if pass.confirmed == nil {
						pass.confirmed = map[string]json.RawMessage{}
					}
					pass.confirmed[id] = raw
				case errors.Is(err, scriptout.ErrNotFound):
					// The origin confirms it is gone.
				default:
					// Cannot confirm: withhold the removal by keeping the id
					// present, so the next call confirms it again.
					presentOut = append(presentOut, id)
					pass.incomplete = true
				}
			}
		}
		return entities, presentOut, nil, membership.Truncated, nil
	}
}

// commitCacheWrites is the post-flush cache write of a changes call
// (INV-CACHE-6). In the refresher mode it writes every entity the pass
// fetched, and every removal-confirmation entity, at detail level; otherwise
// it writes each entity the call listed at summary level. It runs before
// commitCacheTombstones so a confirmed removal's content is in the cache when
// its tombstone is set. Best-effort throughout, like every cache write.
func commitCacheWrites(ctx context.Context, reg *Registry, entityType string, results []changesBackendResult) {
	if !refresherType(entityType) {
		return
	}
	for _, r := range results {
		if r.skipAdvance || r.ledger == nil {
			continue
		}
		var raws []json.RawMessage
		level := CacheLevelSummary
		if r.pass != nil {
			level = CacheLevelDetail
			raws = append(raws, r.pass.fetched...)
			ids := make([]string, 0, len(r.pass.confirmed))
			for id := range r.pass.confirmed {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			for _, id := range ids {
				raws = append(raws, r.pass.confirmed[id])
			}
		} else {
			raws = r.listed
		}
		writes := make([]cacheWrite, 0, len(raws))
		for _, raw := range raws {
			if w, ok := cacheWriteFor(raw); ok {
				writes = append(writes, w)
			}
		}
		putEntitiesCacheLevel(ctx, reg, entityType, r.backend, writes, level)
	}
}
