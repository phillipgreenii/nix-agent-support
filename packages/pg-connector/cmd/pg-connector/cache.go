// cache.go: pg-connector's umbrella-owned entity cache engine (phase 14) —
// a self-contained internal store, its eviction rules, and its two
// opt-out checks [design: docket design field, "Design elements", "The
// deferred entity cache"; design of record section 5.6]. This file has NO
// CLI surface of its own — mirroring how ledger.go shipped in phase 8 —
// it produces a Go API that a later packet wires into show/list/changes
// and a cache verb group.
//
// On-disk layout (freedom boundary — same class as ledger.go's own "not
// decided by the design beyond flock + temp-file-and-rename"): one JSON
// file per CacheKey under $XDG_STATE_HOME/pg-connector/cache/ (the same
// XDG resolution ledgerDir already uses, a sibling directory to
// ledger/), named "<type>__<backend>.json" — the same double-underscore
// convention ledgerFileName uses (ledgerKeySeparator, reused here
// unchanged), with two fields instead of three: no query, since the
// cache is "one file per type and backend", not per query [design:
// section 5.6]. Concurrency reuses the exact same flock-on-sibling-
// lock-file (lockPath) plus temp-file-and-rename write ledger.go's own
// loadLedger/saveLedger already use, rather than inventing a second
// concurrency primitive.
//
// The cache holds full entity copies, not just hashes (unlike the
// ledger): it exists precisely so a later packet's list/show can answer
// from local state when a backend answers unavailable [design: section
// 5.6]. The umbrella caching its own copy of what a stateless backend
// already returned does not weaken D3 (statelessness) — the backend
// itself gains no store; only the umbrella does.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// CacheKey identifies one persisted cache file: one per (type, backend) —
// no query, unlike LedgerKey (design: section 5.6, "one file per type and
// backend").
type CacheKey struct{ Type, Backend string }

// CacheEntry is one entity's cached state.
type CacheEntry struct {
	Content    json.RawMessage `json:"content"`
	AsOf       time.Time       `json:"as_of"`
	LastAccess time.Time       `json:"last_access"`
	RemovedAt  *time.Time      `json:"removed_at,omitempty"` // nil unless tombstoned
}

// Cache is the full persisted state for one (type, backend). Entries is a
// single, entity-kind-agnostic field — exactly like Ledger.Entries, which
// already exists and already passes entity_store_test.go's
// TestNoCrossConnectorEntityStore — never split into a per-entity-kind map
// field (no PRs/Issues/etc. map fields here).
type Cache struct {
	Entries map[string]CacheEntry `json:"entries"`
}

// cacheDirName is the sibling directory to ledgerDirName ("ledger") under
// the same ledgerSubdir root; cacheFileName reuses ledgerKeySeparator
// directly (same "__" convention, no reason to redefine it).
const cacheDirName = "cache"

// cacheDir resolves the directory every cache file lives under, mirroring
// ledgerDir's own XDG_STATE_HOME resolution (falling back to
// ~/.local/state/pg-connector/cache).
func cacheDir() (string, error) {
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, ledgerSubdir, cacheDirName), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cache: resolve home directory: %w", err)
	}
	return filepath.Join(home, ".local", "state", ledgerSubdir, cacheDirName), nil
}

// cacheFileName is key's on-disk filename: "<type>__<backend>.json",
// reusing ledgerFileName's own separator constant.
func cacheFileName(key CacheKey) string {
	return strings.Join([]string{key.Type, key.Backend}, ledgerKeySeparator) + ".json"
}

// cacheKeyFromFileName decodes a filename produced by cacheFileName back
// into a CacheKey. Returns (_, false) for anything that doesn't match the
// exact "<type>__<backend>.json" shape (e.g. a stray non-cache file
// dropped into the same directory) — ListCacheKeys skips those rather
// than erroring, mirroring ledgerKeyFromFileName's own convention.
func cacheKeyFromFileName(name string) (CacheKey, bool) {
	if !strings.HasSuffix(name, ".json") {
		return CacheKey{}, false
	}
	trimmed := strings.TrimSuffix(name, ".json")
	parts := strings.Split(trimmed, ledgerKeySeparator)
	if len(parts) != 2 {
		return CacheKey{}, false
	}
	return CacheKey{Type: parts[0], Backend: parts[1]}, true
}

// cachePath resolves the on-disk path for key.
func cachePath(key CacheKey) (string, error) {
	dir, err := cacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, cacheFileName(key)), nil
}

// newEmptyCache returns a zero-value *Cache with Entries initialized
// non-nil, used by loadCache when no file exists yet.
func newEmptyCache() *Cache {
	return &Cache{Entries: map[string]CacheEntry{}}
}

// loadCache reads key's cache file, returning a zero-value *Cache
// (Entries non-nil, empty) if the file does not exist yet. lockPath is
// ledger.go's own helper (path + ".lock"), reused unchanged.
func loadCache(key CacheKey) (*Cache, error) {
	path, err := cachePath(key)
	if err != nil {
		return nil, err
	}

	fl := flock.New(lockPath(path))
	if err := fl.Lock(); err != nil {
		return nil, fmt.Errorf("cache: lock %s: %w", path, err)
	}
	defer func() { _ = fl.Unlock() }()

	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return newEmptyCache(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("cache: read %s: %w", path, err)
	}

	var c Cache
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("cache: decode %s: %w", path, err)
	}
	if c.Entries == nil {
		c.Entries = map[string]CacheEntry{}
	}
	return &c, nil
}

// saveCache writes c for key via flock-on-sibling-lock-file plus
// temp-file-and-rename, mirroring saveLedger exactly.
func saveCache(key CacheKey, c *Cache) error {
	path, err := cachePath(key)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("cache: create cache dir %s: %w", dir, err)
	}

	fl := flock.New(lockPath(path))
	if err := fl.Lock(); err != nil {
		return fmt.Errorf("cache: lock %s: %w", path, err)
	}
	defer func() { _ = fl.Unlock() }()

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("cache: encode %s: %w", path, err)
	}

	tmp, err := os.CreateTemp(dir, ".cache-*.tmp")
	if err != nil {
		return fmt.Errorf("cache: create temp file in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("cache: write temp file for %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("cache: close temp file for %s: %w", path, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("cache: rename temp file onto %s: %w", path, err)
	}
	return nil
}

// deleteCache removes key's on-disk cache file (and its lock file)
// entirely.
func deleteCache(key CacheKey) error {
	path, err := cachePath(key)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("cache: remove %s: %w", path, err)
	}
	if err := os.Remove(lockPath(path)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("cache: remove lock file for %s: %w", path, err)
	}
	return nil
}

// ListCacheKeys enumerates every persisted (type, backend) key currently
// on disk under $XDG_STATE_HOME/pg-connector/cache/, mirroring
// ListLedgerKeys' own scan-and-decode convention. An absent cache
// directory (no cache has ever been written) returns (nil, nil), not an
// error.
func ListCacheKeys() ([]CacheKey, error) {
	dir, err := cacheDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cache: list %s: %w", dir, err)
	}
	var out []CacheKey
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasSuffix(e.Name(), ".lock") {
			continue
		}
		key, ok := cacheKeyFromFileName(e.Name())
		if !ok {
			continue
		}
		out = append(out, key)
	}
	return out, nil
}

// Get returns the entry's Content/AsOf and ok=true only when an entry
// exists, is NOT tombstoned (RemovedAt == nil), and now.Sub(AsOf) <=
// maxAge. It does NOT update LastAccess as a side effect of loading the
// struct from disk -- callers that intend to serve the entry (a real
// cache hit used to answer a request) call MarkAccessed separately so a
// read-only inspector (e.g. a future "cache show" verb) never perturbs
// LRU ordering just by looking.
func (c *Cache) Get(id string, maxAge time.Duration, now time.Time) (content json.RawMessage, asOf time.Time, ok bool) {
	entry, exists := c.Entries[id]
	if !exists || entry.RemovedAt != nil {
		return nil, time.Time{}, false
	}
	if now.Sub(entry.AsOf) > maxAge {
		return nil, time.Time{}, false
	}
	return entry.Content, entry.AsOf, true
}

// MarkAccessed updates id's LastAccess to now. No-op if id is absent.
func (c *Cache) MarkAccessed(id string, now time.Time) {
	entry, ok := c.Entries[id]
	if !ok {
		return
	}
	entry.LastAccess = now
	c.Entries[id] = entry
}

// Put stores/overwrites a live entity's content and as-of time, clearing
// any prior tombstone on that id (a re-added entity is no longer removed)
// and setting LastAccess to now. Put never evicts on its own -- eviction
// is Evict's job, called separately, mirroring Ledger.Refresh/Evict's own
// separation of concerns.
func (c *Cache) Put(id string, content json.RawMessage, asOf, now time.Time) {
	if c.Entries == nil {
		c.Entries = map[string]CacheEntry{}
	}
	c.Entries[id] = CacheEntry{Content: content, AsOf: asOf, LastAccess: now}
}

// Remove tombstones id: Content and AsOf are left exactly as they were
// (this is what lets a `removed` change carry the last content -- design:
// "a `removed` change carries the last content"), RemovedAt is set to
// now. No-op if id is absent (nothing to tombstone). IDEMPOTENT: if id is
// ALREADY tombstoned (RemovedAt != nil), this call is a no-op -- it does
// NOT reset RemovedAt to a later time. This is load-bearing: a later
// packet's own `changes` verb may report the SAME removed id to more than
// one lagging consumer across more than one call (design section 5.3's
// catch-up semantics), and each such report calling Remove again MUST NOT
// restart the tombstone-retention clock (Evict's rule 2, below) -- doing
// so would silently extend real-world retention past the seven days the
// design promises, for as long as any consumer keeps catching up on that
// id within the window.
func (c *Cache) Remove(id string, now time.Time) {
	entry, ok := c.Entries[id]
	if !ok {
		return
	}
	if entry.RemovedAt != nil {
		return
	}
	entry.RemovedAt = &now
	c.Entries[id] = entry
}

// Evict enforces both cache-retention rules in one pass:
//  1. LRU size cap: while len(non-tombstoned entries) > sizeCap, drop the
//     LIVE (RemovedAt == nil) entry with the oldest LastAccess. Tombstones
//     are exempt from the size cap -- their own retention is rule 2 below,
//     never LRU (binding decision: the design ties tombstone retention
//     only to the stated dual condition, so subjecting tombstones to LRU
//     too would drop a not-yet-expired tombstone early with no citation
//     supporting it).
//  2. Tombstone retention: drop a tombstoned entry once EITHER
//     now.Sub(*RemovedAt) > tombstoneRetention (default 7 days) OR
//     consumersPassed(id) reports true. consumersPassed is supplied by
//     the CALLER (e.g. changes.go's own fan-out, which already has this
//     (type, backend)'s ledger(s) loaded) so this file stays decoupled
//     from ledger.go's specifics; a caller with no ledger data to check
//     (e.g. a bare cache-inspection verb) may pass `func(string) bool {
//     return false }` to rely on the 7-day clock alone.
func (c *Cache) Evict(sizeCap int, tombstoneRetention time.Duration, consumersPassed func(id string) bool, now time.Time) {
	type liveEntry struct {
		id         string
		lastAccess time.Time
	}
	var live []liveEntry
	for id, entry := range c.Entries {
		if entry.RemovedAt == nil {
			live = append(live, liveEntry{id: id, lastAccess: entry.LastAccess})
		}
	}
	if len(live) > sizeCap {
		sort.Slice(live, func(i, j int) bool { return live[i].lastAccess.Before(live[j].lastAccess) })
		overflow := len(live) - sizeCap
		if overflow > len(live) {
			overflow = len(live)
		}
		for i := 0; i < overflow; i++ {
			delete(c.Entries, live[i].id)
		}
	}

	for id, entry := range c.Entries {
		if entry.RemovedAt == nil {
			continue
		}
		if now.Sub(*entry.RemovedAt) > tombstoneRetention || consumersPassed(id) {
			delete(c.Entries, id)
		}
	}
}

// Cache opt-out key names (binding decision -- documented here, in this
// packet's closeout comment, and in docs/behavior/invariants.md, per the
// packet's own instruction to name and document a chosen key):
//   - cacheStateOptOutTypesKey: a state: key (Registry.StateValue) holding
//     a comma-separated list of opted-out type names, mirroring
//     resolveConsumerPruneAfter's existing single-scalar-string
//     convention (ledger.go) rather than inventing a new state: value
//     shape.
//   - cacheVocabularyOptOutKey: a capabilities.vocabulary key a backend
//     sets to signal it opts its own (type, backend) pairs out of
//     caching. Presence of the key, with a bool value of true (or any
//     non-bool value at all -- a backend that bothered to set the key
//     is read as opting out), signals opt-out; ABSENCE of the key, or of
//     the whole Vocabulary map, means enabled.
//   - cacheStateMaxAgeKey / cacheStateSizeCapKey: state: keys for
//     resolveCacheMaxAge/resolveCacheSizeCap below.
const (
	cacheStateOptOutTypesKey = "cache_disabled_types"
	cacheVocabularyOptOutKey = "cache_opt_out"
	cacheStateMaxAgeKey      = "cache_max_age"
	cacheStateSizeCapKey     = "cache_size_cap"
)

// cacheEnabled reports whether caching applies to entityType/backend,
// checking BOTH opt-outs the design names -- "default-on per type with an
// explicit opt-out per type in `state:` and per backend through a
// capabilities flag" (design of record section 5.6).
//
// Per-backend check: this packet's own Contract named invokeOne(ctx, reg,
// backend, "capabilities", nil) decoded via scriptout.Decode/
// CapabilitiesResponse as the mechanism. That combination cannot actually
// work: scriptout.Invoke (which invokeOne wraps) always reports "protocol
// violation: response has neither result nor error" for a capabilities
// call, because the capabilities op's wire response is a bespoke
// top-level shape with no nested "result" key at all -- see
// pkg/scriptout/exec.go's own CapabilitiesResponse/InvokeCapabilities doc
// comments ("the one op whose response Invoke's normal Response envelope
// cannot decode") and serve.go's serveLoop, which writes the handler's
// CapabilitiesResponse directly rather than wrapping it in Response{}.
// Every existing caller of the capabilities op in this module
// (config_validate.go, search.go) already calls
// scriptout.InvokeCapabilities(ctx, binary) for exactly this reason, so
// this function reuses that same, already-correct helper instead of the
// named-but-nonfunctional invokeOne route. AddCapabilities (capabilities.go)
// builds each backend's CapabilitiesResponse once, at dispatch-table
// construction time, from a value that never reads the per-request config
// context -- so InvokeCapabilities' lack of a config parameter loses
// nothing InvokeOne's config-threading would have provided here.
//
// The error return is never non-nil today: a capabilities-call failure is
// swallowed and reported as (true, nil) -- FAIL OPEN -- rather than
// (true, err), so a caller cannot accidentally defeat fail-open by
// checking err first and treating it as a reason to skip/deny caching.
// The signature stays (bool, error), matching this packet's own Produces
// contract, for a future error path (e.g. a malformed Registry) that does
// not exist yet.
func cacheEnabled(ctx context.Context, reg *Registry, entityType, backend string) (bool, error) {
	if typeOptedOut(reg, entityType) {
		return false, nil
	}

	resp, err := scriptout.InvokeCapabilities(ctx, backend)
	if err != nil {
		// FAIL OPEN: an inability to ask a backend whether it opts out
		// MUST NOT itself disable fallback for that backend -- the
		// backend is already unavailable for its OWN op in exactly the
		// scenario this whole phase exists to soften, so a second failed
		// call must not compound it.
		return true, nil
	}
	if backendOptedOut(resp.Vocabulary) {
		return false, nil
	}
	return true, nil
}

// typeOptedOut reports whether entityType is named in the
// cacheStateOptOutTypesKey state: value's comma-separated list.
func typeOptedOut(reg *Registry, entityType string) bool {
	v, ok := reg.StateValue(cacheStateOptOutTypesKey)
	if !ok {
		return false
	}
	for _, t := range strings.Split(v, ",") {
		if strings.TrimSpace(t) == entityType {
			return true
		}
	}
	return false
}

// backendOptedOut reports whether vocabulary declares the
// cacheVocabularyOptOutKey opt-out flag (see this file's cache opt-out
// key names doc comment above for what counts).
func backendOptedOut(vocabulary map[string]any) bool {
	v, ok := vocabulary[cacheVocabularyOptOutKey]
	if !ok {
		return false
	}
	if b, isBool := v.(bool); isBool {
		return b
	}
	return true
}

// resolveCacheMaxAge reads the per-cache-entry max-age from state: via
// cacheStateMaxAgeKey, mirroring resolveConsumerPruneAfter's own
// "<N>d, fall back to time.ParseDuration, else default" pattern (reusing
// its exact parseDayDuration helper). Default: one hour (design:
// "max-age (default one hour)").
func resolveCacheMaxAge(reg *Registry) time.Duration {
	const def = time.Hour

	v, ok := reg.StateValue(cacheStateMaxAgeKey)
	if !ok {
		return def
	}
	if d, ok := parseDayDuration(v); ok {
		return d
	}
	if d, err := time.ParseDuration(v); err == nil {
		return d
	}
	return def
}

// resolveCacheSizeCap reads the per-backend LRU size cap from state: via
// cacheStateSizeCapKey. The design states the MECHANISM (a per-backend
// size cap with LRU eviction) but gives NO numeric default -- unlike
// max-age's stated "default one hour" or this same packet's own
// tombstone-retention default of seven days (Evict's own doc comment,
// above -- both are design-cited numbers), there is no design-cited
// number here. Default chosen here: 500 entries per (type, backend) pair
// -- a round, generously sized cap intended to bound state-directory
// growth without meaningfully limiting ordinary interactive use (an
// operator or query set touching hundreds of entities per backend in one
// session is already an unusual workload); documented here and in
// docs/behavior/invariants.md as a curation default, not a design
// citation, and freely revisable once telemetry (bead pg2-7kizi) shows
// real-world cache sizes. Falls back to this default when the state: key
// is absent, non-numeric, or negative.
func resolveCacheSizeCap(reg *Registry) int {
	const def = 500

	v, ok := reg.StateValue(cacheStateSizeCapKey)
	if !ok {
		return def
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 0 {
		return def
	}
	return n
}
