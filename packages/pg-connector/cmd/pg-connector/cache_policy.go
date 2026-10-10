// cache_policy.go: the entity cache as a READ POLICY for pr and issue show —
// detail-level read-through within read_ttl, a per-entity single-flight lock,
// and the served_from / age_seconds provenance fields (INV-CACHE-2..5,
// INV-CACHE-8). cache_dispatch.go keeps the unavailable fallback and the
// live-success write; changes_refresher.go carries the changes-side half
// (INV-CACHE-6, INV-CACHE-7).
//
// The umbrella owns this policy so a backend stays stateless (INV-STATE-1)
// and Jira adopts it by configuration alone: nothing here names a backend.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// Registry state: keys for the two TTLs this policy adds.
//   - cacheStateReadTTLKey: how old a detail entry may be and still answer a
//     show without calling the backend. Default defaultCacheReadTTL; "0" or
//     "off" disables the read-through.
//   - cacheStateRefreshAfterKey: turns the changes refresher on and sets the
//     age past which a tracked entity is re-fetched. No default: the
//     refresher fetches one entity per show call (no batched fetch-by-ids op
//     exists yet), so it is opt-in.
const (
	cacheStateReadTTLKey      = "cache_read_ttl"
	cacheStateRefreshAfterKey = "cache_refresh_after"
	defaultCacheReadTTL       = 120 * time.Second
)

// servedFrom values (INV-CACHE-5).
const (
	servedFromOrigin = "origin"
	servedFromCache  = "cache"
)

// singleFlightWait bounds how long a reader waits for another reader's origin
// fetch of the same entity before giving up on the lock and fetching itself
// (INV-CACHE-4: a lock failure fails open).
const singleFlightWait = 30 * time.Second

// parseCacheDuration reads a state: duration value: "off" or any zero
// duration means disabled (0, true); otherwise "<N>d" or a Go duration.
// ok=false for an unparsable or negative value.
func parseCacheDuration(v string) (time.Duration, bool) {
	v = strings.TrimSpace(v)
	if strings.EqualFold(v, "off") {
		return 0, true
	}
	d, ok := parseDayDuration(v)
	if !ok {
		parsed, err := time.ParseDuration(v)
		if err != nil {
			return 0, false
		}
		d = parsed
	}
	if d < 0 {
		return 0, false
	}
	return d, true
}

// resolveCacheReadTTL is read_ttl: the state: value when it parses, else
// defaultCacheReadTTL. A returned 0 means the read-through is disabled.
func resolveCacheReadTTL(reg *Registry) time.Duration {
	v, ok := reg.StateValue(cacheStateReadTTLKey)
	if !ok {
		return defaultCacheReadTTL
	}
	if d, ok := parseCacheDuration(v); ok {
		return d
	}
	return defaultCacheReadTTL
}

// resolveCacheRefreshAfter is refresh_after: the state: value when it parses
// to a positive duration, else 0 (the refresher is off).
func resolveCacheRefreshAfter(reg *Registry) time.Duration {
	v, ok := reg.StateValue(cacheStateRefreshAfterKey)
	if !ok {
		return 0
	}
	if d, ok := parseCacheDuration(v); ok {
		return d
	}
	return 0
}

// queryMembers returns the live (non-tombstoned) member ids of one query's
// ledger index (INV-CACHE-3). ok is false when the ledger cannot be read or
// holds no live member, which a caller treats as "nothing to scope to".
func queryMembers(reg *Registry, entityType, backend, query string) (map[string]bool, bool) {
	if err := ensureLedgerDirExists(); err != nil {
		return nil, false
	}
	l, err := loadLedgerAdopting(LedgerKey{Type: entityType, Backend: backend, Query: query, Instance: ledgerInstanceDiscriminator(entityType)}, reg.PreviousNames(backend))
	if err != nil {
		return nil, false
	}
	members := make(map[string]bool, len(l.Entries))
	for id, entry := range l.Entries {
		if entry.RemovedAtVersion == nil {
			members[id] = true
		}
	}
	return members, len(members) > 0
}

// ageSeconds is whole seconds from asOf to now, never negative.
func ageSeconds(asOf, now time.Time) int64 {
	d := now.Sub(asOf)
	if d < 0 {
		return 0
	}
	return int64(d / time.Second)
}

// annotateServed returns raw with the two additive provenance fields
// (INV-CACHE-5) set, using the same decode/set/re-marshal technique as
// markStale. A body that is not a JSON object is returned unchanged: the
// annotation is advisory and must never turn a read into a failure.
func annotateServed(raw json.RawMessage, servedFrom string, age int64) json.RawMessage {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return raw
	}
	fields["served_from"] = json.RawMessage(fmt.Sprintf("%q", servedFrom))
	fields["age_seconds"] = json.RawMessage(fmt.Sprintf("%d", age))
	data, err := json.Marshal(fields)
	if err != nil {
		return raw
	}
	return data
}

// candidateShowBackends is the ordered backend list a show for entityType
// would try (the pinned one alone when pinned is set). It returns nil on a
// resolution error, so dispatchShowWithCache reports that error itself.
func candidateShowBackends(reg *Registry, entityType, pinned string) []string {
	backends, err := resolveBackends(reg, entityType)
	if err != nil {
		return nil
	}
	if pinned == "" {
		return backends
	}
	b, err := pinBackend(entityType, backends, pinned)
	if err != nil {
		return nil
	}
	return []string{b}
}

// showCacheHit is one read-through hit.
type showCacheHit struct {
	backend string
	content json.RawMessage
	asOf    time.Time
}

// lookupShowCache finds the first backend whose detail entry for id is within
// ttl and no older than notBefore (zero means no lower bound), and whose type
// and backend are not opted out of caching. A hit counts as an access.
func lookupShowCache(ctx context.Context, reg *Registry, entityType string, backends []string, id string, ttl time.Duration, notBefore time.Time) (showCacheHit, bool) {
	if err := ensureCacheDirExists(); err != nil {
		return showCacheHit{}, false
	}
	now := time.Now()
	for _, b := range backends {
		key := CacheKey{Type: entityType, Backend: b}
		c, err := loadCache(key)
		if err != nil {
			continue
		}
		content, asOf, ok := c.GetDetail(id, ttl, now)
		if !ok || asOf.Before(notBefore) {
			continue
		}
		if enabled, err := cacheEnabled(ctx, reg, entityType, b); err != nil || !enabled {
			continue
		}
		c.MarkAccessed(id, now)
		_ = saveCache(key, c) // best-effort LRU bump; the hit is served regardless
		return showCacheHit{backend: b, content: content, asOf: asOf}, true
	}
	return showCacheHit{}, false
}

// acquireShowFlight takes the per-entity single-flight lock (INV-CACHE-4) and
// returns its release func and whether it had to WAIT for another holder. It
// fails open: any problem creating or taking the lock within singleFlightWait
// yields a no-op release (waited false) and the caller proceeds unlocked, so
// a lock fault can never turn a read into a failure.
func acquireShowFlight(ctx context.Context, entityType, id string) (release func(), waited bool) {
	noop := func() {}
	dir, err := cacheDir()
	if err != nil {
		return noop, false
	}
	flightDir := filepath.Join(dir, "flight")
	if err := os.MkdirAll(flightDir, 0o755); err != nil {
		return noop, false
	}
	sum := sha256.Sum256([]byte(id))
	fl := flock.New(filepath.Join(flightDir, entityType+ledgerKeySeparator+hex.EncodeToString(sum[:12])+".lock"))
	if locked, err := fl.TryLock(); err == nil && locked {
		return func() { _ = fl.Unlock() }, false
	}
	lctx, cancel := context.WithTimeout(ctx, singleFlightWait)
	defer cancel()
	locked, err := fl.TryLockContext(lctx, 20*time.Millisecond)
	if err != nil || !locked {
		return noop, false
	}
	return func() { _ = fl.Unlock() }, true
}

// servedCacheResponse builds the synthesized response for a read-through hit.
func servedCacheResponse(hit showCacheHit, now time.Time) *scriptout.Response {
	return &scriptout.Response{
		ProtocolVersion: scriptout.ProtocolVersion,
		Result:          annotateServed(hit.content, servedFromCache, ageSeconds(hit.asOf, now)),
	}
}

// dispatchShow is the pr/issue show entry point: the read-through policy
// (INV-CACHE-4) wrapped around dispatchShowWithCache. Within read_ttl a
// detail entry answers with no backend call (unless fresh); otherwise one
// reader at a time per entity fetches from the origin, re-checking the cache
// once it holds the lock so readers that waited serve what the lock holder
// just wrote. A live success is written to the cache at detail level, and
// every answer carries served_from and age_seconds (INV-CACHE-5). A cache
// problem of any kind degrades to today's behavior, never to a failure.
func dispatchShow(ctx context.Context, reg *Registry, entityType, id, pinned string, fresh bool) (*scriptout.Response, error) {
	backends := candidateShowBackends(reg, entityType, pinned)
	ttl := resolveCacheReadTTL(reg)
	readThrough := ttl > 0 && len(backends) > 0 && !typeOptedOut(reg, entityType)
	start := time.Now()

	if readThrough && !fresh {
		if hit, ok := lookupShowCache(ctx, reg, entityType, backends, id, ttl, time.Time{}); ok {
			return servedCacheResponse(hit, time.Now()), nil
		}
	}

	if readThrough {
		release, waited := acquireShowFlight(ctx, entityType, id)
		defer release()
		// A reader that had to wait for the lock re-checks: the holder has
		// probably just written what this reader came for. A --fresh reader
		// accepts only an entry fetched after it started (as_of has one-second
		// resolution, so compare against the start truncated to the second).
		// A reader that took the lock at once has nothing to re-check.
		if waited {
			notBefore := time.Time{}
			if fresh {
				notBefore = start.Truncate(time.Second)
			}
			if hit, ok := lookupShowCache(ctx, reg, entityType, backends, id, ttl, notBefore); ok {
				return servedCacheResponse(hit, time.Now()), nil
			}
		}
	}

	resp, backend, fromCache, err := dispatchShowWithCache(ctx, reg, entityType, id, pinned)
	if err != nil || resp == nil {
		return resp, err
	}
	if fromCache {
		// The unavailable stale fallback: a cache answer, honestly aged.
		out := *resp
		out.Result = annotateServed(resp.Result, servedFromCache, entityAgeSeconds(resp.Result, time.Now()))
		return &out, nil
	}
	putLiveEntityLevel(ctx, reg, entityType, backend, resp.Result, CacheLevelDetail)
	out := *resp
	out.Result = annotateServed(resp.Result, servedFromOrigin, 0)
	return &out, nil
}

// entityAgeSeconds is the age of a raw entity from its own as_of; 0 when the
// as_of is absent or unparsable.
func entityAgeSeconds(raw json.RawMessage, now time.Time) int64 {
	var meta liveEntityMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return 0
	}
	asOf, err := time.Parse(time.RFC3339, meta.AsOf)
	if err != nil {
		return 0
	}
	return ageSeconds(asOf, now)
}

// cacheWrite is one entity to write to a cache file.
type cacheWrite struct {
	id      string
	content json.RawMessage
	asOf    time.Time
}

// putEntitiesCacheLevel writes a batch of entities to (entityType,
// backend)'s cache at level with one load and one save, gated by cacheEnabled
// exactly like putEntityCacheLevel and, like it, best-effort: nothing here
// may turn a completed read into a reported failure.
func putEntitiesCacheLevel(ctx context.Context, reg *Registry, entityType, backend string, writes []cacheWrite, level CacheLevel) {
	if len(writes) == 0 {
		return
	}
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
	sort.SliceStable(writes, func(i, j int) bool { return writes[i].id < writes[j].id })
	for _, w := range writes {
		c.PutLevel(w.id, w.content, w.asOf, now, level)
	}
	c.Evict(resolveCacheSizeCap(reg), cacheTombstoneRetention, noConsumersTracked, now)
	_ = saveCache(key, c)
}

// cacheWriteFor builds a cacheWrite from a raw entity, reporting ok=false for
// one with no id or no parsable as_of (the same skip rule as putLiveEntity).
func cacheWriteFor(content json.RawMessage) (cacheWrite, bool) {
	var meta liveEntityMeta
	if err := scriptout.Decode(content, &meta); err != nil || meta.ID == "" {
		return cacheWrite{}, false
	}
	asOf, err := time.Parse(time.RFC3339, meta.AsOf)
	if err != nil {
		return cacheWrite{}, false
	}
	return cacheWrite{id: meta.ID, content: content, asOf: asOf}, true
}
