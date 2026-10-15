// changes_refresher.go: the refresher mode of "changes" for pr and issue
// (INV-CACHE-6, INV-CACHE-7). When the registry's cache_refresh_after is set,
// a changes call does not list every matching entity in full; it
//
//  1. reads membership with the ids-only list (the cheap call),
//  2. fetches an entity only for a member that is new to the ledger or that the
//     refresher has not confirmed within refresh_after (see "Fetching" below),
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
// Fetching. A backend that registers the optional show_many op (pr on GitHub,
// bead pg2-12yur) is asked for the members it needs in chunks of
// refreshFetchChunkSize ids, one origin call and about one GraphQL point per
// chunk, and answers the SUMMARY-level entity (the field set list returns;
// comment and review bodies are caught at the next show, not here). Anything
// else (issue, a pr backend without the op) is fetched with the backend's own
// show, one call per entity, and answers the full entity. The mode is chosen
// per pass from the backend's capabilities, so an older backend keeps working.
//
// Freshness. "Not confirmed within refresh_after" is Cache.RefreshYoung, not
// Cache.GetDetail: a summary fetched by show_many never replaces or re-stamps
// a live detail entry (INV-CACHE-2), so only a refresher-specific stamp
// (CacheEntry.RefreshedAt) lets that fetch count as fresh without making the
// entity look detail-fresh to a show read-through. Without it, every aged
// member would be fetched again on every pass and batching would save nothing.
//
// Failure modes. A chunk that fails fails every id in it: those ids stay aged
// and the next call asks again, and the pass is incomplete (no refreshed_at). A
// null answer (a deleted or inaccessible node) is a per-id answer: the id is
// confirmed with one show, exactly as a pass without show_many would have
// fetched it. A failed show_many answered unavailable stops the pass's further
// fetching, like a failed show does.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// refreshFetchChunkSize is how many ids one show_many request names: the
// largest count that costs 1 GraphQL point at the GitHub backend (75 to 100
// cost 2; spec section 6.6, "Refresh by ids"). The backend splits an oversized
// request itself, but the refresher chunks first so that one failed origin call
// fails at most this many ids. It MUST equal the backend's own chunk size
// (nodesBatchedChunkSize in pg-connector-pr-github's github package); a test on
// each side pins 74.
const refreshFetchChunkSize = 74

// opShowMany is the optional pr op the refresher prefers to one show per id.
const opShowMany = "show_many"

// refreshPass is one backend's refresher state for one changes call.
type refreshPass struct {
	// fetched are the detail entities this pass read with show, to be written
	// to the cache at detail level after the response is flushed.
	fetched []json.RawMessage
	// summaries are the summary-level entities this pass read with show_many,
	// to be written to the cache at summary level, with the refresher's
	// freshness stamp, after the response is flushed. A summary never replaces
	// a live detail entry (INV-CACHE-2); the stamp is what makes it count.
	summaries []json.RawMessage
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

// backendHasShowMany reports whether backend's capabilities list the optional
// show_many op for entityType. Only pr has the op; a capabilities failure reads
// as "no" so the pass falls back to show, never to a failure.
func backendHasShowMany(ctx context.Context, reg *Registry, entityType, backend string) bool {
	if entityType != "pr" {
		return false
	}
	resp, err := reg.InvokeCapabilities(ctx, backend)
	if err != nil {
		return false
	}
	return slices.Contains(resp.Ops, opShowMany)
}

// showManyResult mirrors schema.PRShowManyResult's wire shape with the
// entities kept raw, like rawListResult.
type showManyResult struct {
	Entities []json.RawMessage `json:"entities"`
	Missing  []string          `json:"missing"`
}

// showManyChunk fetches one chunk of ids (each with its node id) with the
// backend's show_many op. It returns the entities for the ids that were asked
// for, and the ids of the chunk that came back with no entity (the backend's
// missing list, plus any id it silently dropped): those are for the caller to
// confirm with show. An entity for an id that was not asked for is discarded.
// A failed call, or an answer that cannot be decoded, is an error and fails
// every id in the chunk.
func showManyChunk(ctx context.Context, reg *Registry, backend string, chunk []string, nodeIDs map[string]string) (entities []json.RawMessage, absent []string, err error) {
	refs := make([]map[string]string, len(chunk))
	for i, id := range chunk {
		refs[i] = map[string]string{"id": id, "node_id": nodeIDs[id]}
	}
	resp, err := invokeOne(ctx, reg, backend, opShowMany, map[string]any{"refs": refs})
	if err != nil {
		return nil, nil, err
	}
	var result showManyResult
	if err := scriptout.Decode(resp.Result, &result); err != nil {
		return nil, nil, err
	}
	asked := make(map[string]bool, len(chunk))
	for _, id := range chunk {
		asked[id] = true
	}
	got := make(map[string]bool, len(chunk))
	for _, raw := range result.Entities {
		id, err := entityID(raw)
		if err != nil || !asked[id] || got[id] {
			continue
		}
		got[id] = true
		entities = append(entities, raw)
	}
	for _, id := range chunk {
		if !got[id] {
			absent = append(absent, id)
		}
	}
	return entities, absent, nil
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
		batched := backendHasShowMany(ctx, reg, entityType, backend)

		// Stage 2a: decide which members need the origin. A tracked member the
		// refresher confirmed within refresh_after is skipped (RefreshYoung, not
		// GetDetail: see the file comment). A member new to the ledger that the
		// cache already holds at detail level reuses that content, except when
		// the pass fetches summaries: detail content hashes differently from the
		// summary every other member of the pass contributes, so reusing it would
		// report a change that never happened once the entity next refreshes.
		var entities []json.RawMessage
		var need []string
		for _, id := range ids {
			present[id] = true
			entry, inLedger := l.Entries[id]
			tracked := inLedger && entry.RemovedAtVersion == nil
			if tracked && cache.RefreshYoung(id, after, now) {
				continue // within refresh_after: nothing to ask the origin
			}
			if !tracked && !batched {
				if content, _, young := cache.GetDetail(id, after, now); young {
					entities = append(entities, content) // new to the ledger, already held
					continue
				}
			}
			need = append(need, id)
		}

		// Stage 2b: fetch. With show_many, the members that came with a node id
		// go in chunks; the rest, and whatever a chunk could not answer, are
		// confirmed with one show each, the same call a pass without show_many
		// makes for every member.
		fetchOK := true
		toShow := need
		if batched && len(need) > 0 {
			toShow = nil
			var batchable []string
			for _, id := range need {
				if membership.NodeIDs[id] != "" {
					batchable = append(batchable, id)
				} else {
					toShow = append(toShow, id)
				}
			}
			opSupported := true
			for start := 0; start < len(batchable); start += refreshFetchChunkSize {
				chunk := batchable[start:min(start+refreshFetchChunkSize, len(batchable))]
				if !fetchOK {
					pass.incomplete = true // origin went away mid-pass; the rest wait
					continue
				}
				if !opSupported {
					toShow = append(toShow, chunk...) // the backend turned out to lack the op
					continue
				}
				got, absent, err := showManyChunk(ctx, reg, backend, chunk, membership.NodeIDs)
				switch {
				case err == nil:
					entities = append(entities, got...)
					pass.summaries = append(pass.summaries, got...)
					toShow = append(toShow, absent...)
				case errors.Is(err, scriptout.ErrUnknownOp):
					// Capabilities said the op exists and the backend says it does
					// not (a stale answer): fall back for this chunk and the rest.
					opSupported = false
					toShow = append(toShow, chunk...)
				default:
					// A failed chunk fails every id in it. They stay aged, so the
					// next call asks again; the pass is not a whole-query answer.
					pass.incomplete = true
					if errors.Is(err, scriptout.ErrUnavailable) {
						fetchOK = false
					}
				}
			}
			sort.Strings(toShow)
		}
		for _, id := range toShow {
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
// (INV-CACHE-6). In the refresher mode it writes every entity the pass fetched
// with show at detail level (its own as_of is its freshness), every entity it
// fetched with show_many at summary level with the refresher's freshness stamp
// (CacheEntry.RefreshedAt, the only thing that makes a summary count), and every
// removal-confirmation entity at detail level; otherwise it writes each entity
// the call listed at summary level. It runs before commitCacheTombstones
// so a confirmed removal's content is in the cache when its tombstone is set.
// Best-effort throughout, like every cache write.
func commitCacheWrites(ctx context.Context, reg *Registry, entityType string, results []changesBackendResult) {
	if !refresherType(entityType) {
		return
	}
	for _, r := range results {
		if r.skipAdvance || r.ledger == nil {
			continue
		}
		var writes []cacheWrite
		add := func(raws []json.RawMessage, level CacheLevel, refreshed bool) {
			for _, raw := range raws {
				if w, ok := cacheWriteFor(raw); ok {
					w.level, w.refreshed = level, refreshed
					writes = append(writes, w)
				}
			}
		}
		level := CacheLevelSummary
		if r.pass != nil {
			level = CacheLevelDetail
			add(r.pass.fetched, CacheLevelDetail, false)
			add(r.pass.summaries, CacheLevelSummary, true)
			ids := make([]string, 0, len(r.pass.confirmed))
			for id := range r.pass.confirmed {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			confirmed := make([]json.RawMessage, 0, len(ids))
			for _, id := range ids {
				confirmed = append(confirmed, r.pass.confirmed[id])
			}
			add(confirmed, CacheLevelDetail, false)
		} else {
			add(r.listed, CacheLevelSummary, false)
		}
		putEntitiesCacheLevel(ctx, reg, entityType, r.backend, writes, level)
	}
}
