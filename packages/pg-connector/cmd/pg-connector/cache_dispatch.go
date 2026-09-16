// cache_dispatch.go: the shared cache-fallback/cache-write helpers that
// wire this docket's sibling "cache engine" packet's Cache/CacheKey Go API
// (cache.go, bead pg2-2j5ac.42.1) into pr/issue show and list
// [design: docket design field, "Design elements", "The deferred entity
// cache" and "Freshness semantics once the cache lands"; design of record
// section 5.6]. Neither dispatch.go nor cache.go is this packet's own file
// to grow unboundedly, so this dispatch/fallback logic lives here instead;
// changes.go's own read-only substitution and post-flush tombstone commit
// stay in changes.go itself (that file's own Files entry), since they are
// small, changes.go-specific edits rather than a shared helper.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// ensureCacheDirExists creates the cache directory if it does not exist
// yet. loadCache (cache.go, out of this packet's own scope) flocks a
// sibling lock file that it expects to be able to CREATE, which requires
// the parent directory to already exist — true once ANY saveCache call
// has ever run (saveCache's own os.MkdirAll), but not yet true on a fresh
// host before any cache has ever been written. This mirrors changes.go's
// own identical ensureLedgerDirExists fix for ledger.go's analogous
// precondition gap; every one of this file's own loadCache call sites
// establishes it first, keeping the fix entirely on this packet's side of
// the seam rather than touching cache.go's own locking code (this
// docket's sibling packet's file).
func ensureCacheDirExists() error {
	dir, err := cacheDir()
	if err != nil {
		return err
	}
	return os.MkdirAll(dir, 0o755)
}

// cacheTombstoneRetention is the tombstone-retention duration passed to
// every Evict call this docket's packets make (this file's own Put path,
// and changes.go's own post-flush commit step) — the design cites no
// state: override for this figure (unlike max-age's "default one hour"),
// so a plain constant is used everywhere it is needed, kept in this one
// place so the two call sites cannot drift apart.
const cacheTombstoneRetention = 7 * 24 * time.Hour

// cacheFallbackReason is the sources[] row Reason a "list" fan-out reports
// for a backend served from the cache fallback — deliberately never
// claiming "ok"/succeeded for that row (this docket's own Binding
// decision: "do not claim 'ok' for a fallback-served backend").
const cacheFallbackReason = "served from cache: backend unavailable"

// noConsumersTracked is the consumersPassed function cache.go's own
// Evict doc comment names for "a caller with no ledger data to check":
// show/list's own Put path (unlike changes.go's newChangesCmd RunE, which
// builds a real one from its own already-loaded *Ledger) has no ledger in
// scope, so it relies on the cacheTombstoneRetention clock alone.
func noConsumersTracked(string) bool { return false }

// dispatchShowWithCache mirrors DispatchTargeted's own try-each policy
// over resolveBackends/pinBackend (dispatch.go) for the "show" op, but on
// a per-backend scriptout.ErrUnavailable result, tries a cache fallback
// (tryCacheFallback below) before moving on (pinned case) or trying the
// next backend (fan-out/try-each case) — on a cache hit, returning a
// synthesized *scriptout.Response with a nil error, exactly as if the
// backend itself had answered. On a cache miss, opted-out type/backend,
// or any OTHER error, it falls through to today's existing behavior
// unchanged (the real error/response, keeping Stale: false on any live
// read).
//
// Return values: resp/err are the same shape DispatchTargeted itself
// returns; backend is the backend that produced resp (live or
// cache-served) — needed by the caller (newPrShowCmd/newIssueShowCmd) to
// know which backend's cache to Put a live success into; fromCache
// reports whether resp was synthesized from the cache rather than a live
// backend answer, so the caller never Puts a cache-served read back into
// its own cache (nothing new to store) [design: docket design field,
// "Produces" — "track this via a second return value or a sentinel
// field, your choice"].
func dispatchShowWithCache(ctx context.Context, reg *Registry, entityType, id, pinned string) (resp *scriptout.Response, backend string, fromCache bool, err error) {
	backends, err := resolveBackends(reg, entityType)
	if err != nil {
		return nil, "", false, err
	}
	args := map[string]string{"id": id}

	if pinned != "" {
		b, pinErr := pinBackend(entityType, backends, pinned)
		if pinErr != nil {
			return nil, "", false, pinErr
		}
		r, callErr := invokeOne(ctx, reg, b, "show", args)
		if callErr != nil && errors.Is(callErr, scriptout.ErrUnavailable) {
			if cached, ok := tryCacheFallback(ctx, reg, entityType, b, id); ok {
				return cached, b, true, nil
			}
		}
		return r, b, false, callErr
	}

	var r *scriptout.Response
	var callErr error
	var lastBackend string
	for _, b := range backends {
		lastBackend = b
		r, callErr = invokeOne(ctx, reg, b, "show", args)
		if callErr == nil {
			return r, b, false, nil
		}
		if !errors.Is(callErr, scriptout.ErrNotFound) {
			// Success, or any error other than not_found: short-circuit —
			// never swallowed to fall through to the next backend — except
			// ErrUnavailable gets one cache-fallback attempt first.
			if errors.Is(callErr, scriptout.ErrUnavailable) {
				if cached, ok := tryCacheFallback(ctx, reg, entityType, b, id); ok {
					return cached, b, true, nil
				}
			}
			return r, b, false, callErr
		}
		// not_found: try the next registered backend.
	}
	return r, lastBackend, false, callErr
}

// tryCacheFallback checks cacheEnabled for (entityType, backend); on
// disabled or any error, returns (nil, false) unconditionally — the
// caller falls through to reporting the real error/response unchanged.
// On a within-max-age, non-tombstoned cache hit for id, it marks the
// entry accessed, best-effort saves the cache back, and returns a
// synthesized *scriptout.Response whose Result is the cached content with
// "as_of"/"stale" overridden (markStale below) — ok=true. Any other
// outcome (cache-load error, or Get reporting no hit) is (nil, false).
func tryCacheFallback(ctx context.Context, reg *Registry, entityType, backend, id string) (*scriptout.Response, bool) {
	enabled, err := cacheEnabled(ctx, reg, entityType, backend)
	if err != nil || !enabled {
		return nil, false
	}
	if err := ensureCacheDirExists(); err != nil {
		return nil, false
	}
	key := CacheKey{Type: entityType, Backend: backend}
	c, err := loadCache(key)
	if err != nil {
		return nil, false
	}
	now := time.Now()
	content, asOf, ok := c.Get(id, resolveCacheMaxAge(reg), now)
	if !ok {
		return nil, false
	}
	c.MarkAccessed(id, now)
	if saveErr := saveCache(key, c); saveErr != nil {
		// Best-effort persistence of the MarkAccessed bump — the entity
		// this call already has decoded in hand is served regardless,
		// mirroring this docket's existing Evict/saveCache error
		// tolerance elsewhere (e.g. Evict's own swallowed deleteLedger
		// error).
		_ = saveErr
	}
	stale, err := markStale(content, asOf)
	if err != nil {
		return nil, false
	}
	return &scriptout.Response{ProtocolVersion: scriptout.ProtocolVersion, Result: stale}, true
}

// cacheFallbackEntities is tryCacheFallback's list-shaped counterpart
// (fanOutPRList/fanOutIssueList's own list-fan-out helper): "list has no
// single id" [design: docket design field, "Produces"], so on a backend
// answering scriptout.ErrUnavailable, this falls back to EVERY live
// (non-tombstoned), within-max-age cache entry currently cached for
// (entityType, backend), each re-marshaled via markStale, in
// deterministic (sorted-by-id) order. Returns (nil, nil, false) on any
// opt-out, cache-load error, or zero live matching entries — the caller
// falls through to reporting the real error unchanged in every one of
// those cases, exactly like tryCacheFallback's own (nil, false)
// convention.
func cacheFallbackEntities(ctx context.Context, reg *Registry, entityType, backend string) ([]json.RawMessage, []string, bool) {
	enabled, err := cacheEnabled(ctx, reg, entityType, backend)
	if err != nil || !enabled {
		return nil, nil, false
	}
	if err := ensureCacheDirExists(); err != nil {
		return nil, nil, false
	}
	key := CacheKey{Type: entityType, Backend: backend}
	c, err := loadCache(key)
	if err != nil {
		return nil, nil, false
	}

	allIDs := make([]string, 0, len(c.Entries))
	for id := range c.Entries {
		allIDs = append(allIDs, id)
	}
	// Deterministic order, independent of Go's randomized map iteration
	// order (mirrors ledger.go's Refresh own convention for its returned
	// changes slice).
	sort.Strings(allIDs)

	maxAge := resolveCacheMaxAge(reg)
	now := time.Now()
	var entries []json.RawMessage
	var ids []string
	for _, id := range allIDs {
		content, asOf, ok := c.Get(id, maxAge, now)
		if !ok {
			continue
		}
		stale, err := markStale(content, asOf)
		if err != nil {
			continue
		}
		c.MarkAccessed(id, now)
		entries = append(entries, stale)
		ids = append(ids, id)
	}
	if len(entries) == 0 {
		return nil, nil, false
	}
	if saveErr := saveCache(key, c); saveErr != nil {
		// Best-effort persistence, same tolerance as tryCacheFallback.
		_ = saveErr
	}
	return entries, ids, true
}

// markStale re-marshals content with "as_of" set to asOf (RFC3339, UTC)
// and "stale" set to true — the same decode/set-keys/re-marshal technique
// ledger.go's own canonicalHash already uses (there, to EXCLUDE two keys
// before hashing; here, to OVERRIDE two keys before returning), cited as
// the pattern to follow per this docket's own Contract, not copied
// verbatim since the fields being set differ.
func markStale(content json.RawMessage, asOf time.Time) (json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(content, &fields); err != nil {
		return nil, fmt.Errorf("cache_dispatch: decode cached entity: %w", err)
	}
	asOfJSON, err := json.Marshal(asOf.UTC().Format(time.RFC3339))
	if err != nil {
		return nil, fmt.Errorf("cache_dispatch: encode as_of: %w", err)
	}
	fields["as_of"] = asOfJSON
	fields["stale"] = json.RawMessage("true")
	data, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("cache_dispatch: encode cached entity: %w", err)
	}
	return data, nil
}

// liveEntityMeta is the minimal shape putLiveEntity needs to extract from
// a live-fetched entity's own raw JSON — every schema entity type carries
// both an "id" (entityID's own doc comment, ledger.go) and an "as_of"
// (this docket's design) field, so this stays entity-type-agnostic
// exactly like ledger.go's own entityID/idOnlyEntity helpers.
type liveEntityMeta struct {
	ID   string `json:"id"`
	AsOf string `json:"as_of"`
}

// putLiveEntity writes one live-fetched entity's own raw content into
// (entityType, backend)'s cache, via putEntityCache below. A malformed
// content (missing id/as_of, or an unparsable as_of) is silently
// skipped — this is a best-effort cache write on the caller's own success
// path, never something that may turn a successful live read into a
// reported failure [design: docket design field, "Binding decisions"].
func putLiveEntity(ctx context.Context, reg *Registry, entityType, backend string, content json.RawMessage) {
	if backend == "" {
		return
	}
	var meta liveEntityMeta
	if err := scriptout.Decode(content, &meta); err != nil || meta.ID == "" {
		return
	}
	asOf, err := time.Parse(time.RFC3339, meta.AsOf)
	if err != nil {
		return
	}
	putEntityCache(ctx, reg, entityType, backend, meta.ID, content, asOf)
}

// putEntityCache is show/list's own live-success cache write: Put the
// entity, then Evict(resolveCacheSizeCap(reg), cacheTombstoneRetention,
// noConsumersTracked, now), then saveCache — see cacheTombstoneRetention's
// own doc comment for why this path has no consumersPassed of its own.
// Gated by cacheEnabled FIRST: a type/backend opted out of caching must
// never have anything Put for it at all, preserving cache.go's own
// documented invariant that changes.go's mergeChanges plain Get relies on
// ("nothing was ever Put for it, so Get naturally reports no entry")
// [design: docket design field, "Produces", mergeChanges's own Contract
// note]. Errors are swallowed throughout (best-effort persistence,
// mirroring this docket's existing Evict/saveCache error tolerance
// elsewhere) — the caller's own live read already succeeded and must not
// be turned into a reported failure by a cache-write problem [design:
// docket design field, "Binding decisions", "Put/Evict/saveCache on the
// live-success path MUST happen only after the response has already been
// computed for the caller"].
func putEntityCache(ctx context.Context, reg *Registry, entityType, backend, id string, content json.RawMessage, asOf time.Time) {
	enabled, err := cacheEnabled(ctx, reg, entityType, backend)
	if err != nil || !enabled {
		return
	}
	if err := ensureCacheDirExists(); err != nil {
		return
	}
	key := CacheKey{Type: entityType, Backend: backend}
	c, err := loadCache(key)
	if err != nil {
		return
	}
	now := time.Now()
	c.Put(id, content, asOf, now)
	c.Evict(resolveCacheSizeCap(reg), cacheTombstoneRetention, noConsumersTracked, now)
	_ = saveCache(key, c)
}
