package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestCacheGet_AbsentTombstonedAndStale(t *testing.T) {
	now := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)
	tombstoned := now.Add(-time.Minute)
	c := &Cache{Entries: map[string]CacheEntry{
		"tombstoned": {Content: json.RawMessage(`{"id":"tombstoned"}`), AsOf: now, RemovedAt: &tombstoned},
		"stale":      {Content: json.RawMessage(`{"id":"stale"}`), AsOf: now.Add(-2 * time.Hour)},
		"live":       {Content: json.RawMessage(`{"id":"live"}`), AsOf: now.Add(-time.Minute)},
	}}

	if _, _, ok := c.Get("absent", time.Hour, now); ok {
		t.Fatal("Get(absent) = ok=true, want false")
	}
	if _, _, ok := c.Get("tombstoned", time.Hour, now); ok {
		t.Fatal("Get(tombstoned) = ok=true, want false")
	}
	if _, _, ok := c.Get("stale", time.Hour, now); ok {
		t.Fatal("Get(stale) = ok=true, want false (AsOf older than maxAge)")
	}
	content, asOf, ok := c.Get("live", time.Hour, now)
	if !ok {
		t.Fatal("Get(live) = ok=false, want true")
	}
	if string(content) != `{"id":"live"}` {
		t.Fatalf("Get(live) content = %s, want the stored content", content)
	}
	if want := now.Add(-time.Minute); !asOf.Equal(want) {
		t.Fatalf("Get(live) asOf = %v, want %v", asOf, want)
	}
}

func TestCachePut_StoresAndClearsTombstone(t *testing.T) {
	now := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)
	removedAt := now.Add(-time.Hour)
	c := &Cache{Entries: map[string]CacheEntry{
		"1": {Content: json.RawMessage(`{"old":true}`), AsOf: now.Add(-2 * time.Hour), RemovedAt: &removedAt},
	}}

	asOf := now.Add(-time.Minute)
	c.Put("1", json.RawMessage(`{"new":true}`), asOf, now)

	entry := c.Entries["1"]
	if string(entry.Content) != `{"new":true}` {
		t.Fatalf("Put content = %s, want the new content", entry.Content)
	}
	if !entry.AsOf.Equal(asOf) {
		t.Fatalf("Put AsOf = %v, want %v", entry.AsOf, asOf)
	}
	if !entry.LastAccess.Equal(now) {
		t.Fatalf("Put LastAccess = %v, want now (%v)", entry.LastAccess, now)
	}
	if entry.RemovedAt != nil {
		t.Fatalf("Put did not clear the prior tombstone: RemovedAt = %v, want nil", entry.RemovedAt)
	}
}

func TestCacheRemove_SetsRemovedAtWithoutClearingContent(t *testing.T) {
	now := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)
	asOf := now.Add(-time.Minute)
	c := &Cache{Entries: map[string]CacheEntry{
		"1": {Content: json.RawMessage(`{"id":"1"}`), AsOf: asOf, LastAccess: asOf},
	}}

	c.Remove("1", now)

	entry := c.Entries["1"]
	if string(entry.Content) != `{"id":"1"}` {
		t.Fatalf("Remove cleared Content: got %s", entry.Content)
	}
	if !entry.AsOf.Equal(asOf) {
		t.Fatalf("Remove changed AsOf: got %v, want %v", entry.AsOf, asOf)
	}
	if entry.RemovedAt == nil || !entry.RemovedAt.Equal(now) {
		t.Fatalf("Remove did not set RemovedAt to now: got %v, want %v", entry.RemovedAt, now)
	}

	// No-op on an absent id: must not create an entry.
	c.Remove("absent", now)
	if _, ok := c.Entries["absent"]; ok {
		t.Fatal("Remove created an entry for an absent id")
	}
}

func TestCacheRemove_Idempotent(t *testing.T) {
	first := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)
	later := first.Add(time.Hour)
	c := &Cache{Entries: map[string]CacheEntry{
		"1": {Content: json.RawMessage(`{"id":"1"}`)},
	}}

	c.Remove("1", first)
	c.Remove("1", later)

	entry := c.Entries["1"]
	if entry.RemovedAt == nil || !entry.RemovedAt.Equal(first) {
		t.Fatalf("second Remove call restarted the tombstone clock: RemovedAt = %v, want %v (the FIRST call's time)", entry.RemovedAt, first)
	}
}

func TestCacheMarkAccessed_UpdatesLastAccessOrNoopIfAbsent(t *testing.T) {
	now := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)
	c := &Cache{Entries: map[string]CacheEntry{
		"1": {LastAccess: now.Add(-time.Hour)},
	}}

	c.MarkAccessed("1", now)
	if !c.Entries["1"].LastAccess.Equal(now) {
		t.Fatalf("MarkAccessed did not update LastAccess: got %v, want %v", c.Entries["1"].LastAccess, now)
	}

	c.MarkAccessed("absent", now)
	if _, ok := c.Entries["absent"]; ok {
		t.Fatal("MarkAccessed created an entry for an absent id")
	}
}

func TestCacheEvict_LRUDropsOnlyLiveEntriesOverSizeCap(t *testing.T) {
	now := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)
	removedAt := now.Add(-time.Minute)
	c := &Cache{Entries: map[string]CacheEntry{
		"oldest": {LastAccess: now.Add(-3 * time.Hour)},
		"middle": {LastAccess: now.Add(-2 * time.Hour)},
		"newest": {LastAccess: now.Add(-time.Hour)},
		// Older than every live entry, but tombstoned -- must be exempt
		// from the LRU pass entirely.
		"tombstone": {RemovedAt: &removedAt, LastAccess: now.Add(-100 * time.Hour)},
	}}

	c.Evict(2, 7*24*time.Hour, func(string) bool { return false }, now)

	if _, ok := c.Entries["oldest"]; ok {
		t.Fatal("Evict did not drop the oldest live entry over the size cap")
	}
	if _, ok := c.Entries["middle"]; !ok {
		t.Fatal("Evict dropped a live entry that should have survived under the size cap")
	}
	if _, ok := c.Entries["newest"]; !ok {
		t.Fatal("Evict dropped the most-recently-accessed live entry")
	}
	if _, ok := c.Entries["tombstone"]; !ok {
		t.Fatal("Evict's LRU pass dropped a tombstone -- tombstones must be exempt from the size cap")
	}
}

func TestCacheEvict_TombstoneRetentionEitherConditionDrops(t *testing.T) {
	now := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)
	expired := now.Add(-8 * 24 * time.Hour)
	recentButPassed := now.Add(-time.Hour)
	c := &Cache{Entries: map[string]CacheEntry{
		"expired-by-time":     {RemovedAt: &expired},
		"passed-by-consumers": {RemovedAt: &recentButPassed},
	}}

	c.Evict(100, 7*24*time.Hour, func(id string) bool { return id == "passed-by-consumers" }, now)

	if _, ok := c.Entries["expired-by-time"]; ok {
		t.Fatal("Evict did not drop a tombstone past the retention window")
	}
	if _, ok := c.Entries["passed-by-consumers"]; ok {
		t.Fatal("Evict did not drop a tombstone consumersPassed reported true for")
	}
}

func TestCacheEvict_TombstoneSurvivesWhenNeitherConditionHolds(t *testing.T) {
	now := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)
	recent := now.Add(-time.Hour)
	c := &Cache{Entries: map[string]CacheEntry{
		"1": {RemovedAt: &recent},
	}}

	c.Evict(100, 7*24*time.Hour, func(string) bool { return false }, now)

	if _, ok := c.Entries["1"]; !ok {
		t.Fatal("Evict dropped a tombstone whose retention has not elapsed and consumersPassed reported false")
	}
}

func TestCacheSaveLoad_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	key := CacheKey{Type: "pr", Backend: "pg-connector-fake-backend"}

	removedAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c := &Cache{
		Entries: map[string]CacheEntry{
			"1": {
				Content:    json.RawMessage(`{"id":"1"}`),
				AsOf:       time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
				LastAccess: time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC),
			},
			"2": {Content: json.RawMessage(`{"id":"2"}`), RemovedAt: &removedAt},
		},
	}

	if err := saveCache(key, c); err != nil {
		t.Fatalf("saveCache: %v", err)
	}

	loaded, err := loadCache(key)
	if err != nil {
		t.Fatalf("loadCache: %v", err)
	}
	if len(loaded.Entries) != 2 {
		t.Fatalf("Entries: got %+v, want 2 entries", loaded.Entries)
	}
	got1 := loaded.Entries["1"]
	want1 := c.Entries["1"]
	// Content is compared by decoded value, not raw bytes: MarshalIndent
	// re-indents an embedded json.RawMessage's own bytes (compact-then-
	// indent over the whole document), so a round trip through
	// saveCache/loadCache is only guaranteed to preserve the JSON VALUE,
	// never the original byte-for-byte whitespace.
	var gotVal, wantVal any
	if err := json.Unmarshal(got1.Content, &gotVal); err != nil {
		t.Fatalf("decode Entries[1].Content after round trip: %v", err)
	}
	if err := json.Unmarshal(want1.Content, &wantVal); err != nil {
		t.Fatalf("decode want Entries[1].Content: %v", err)
	}
	if !reflect.DeepEqual(gotVal, wantVal) {
		t.Fatalf("Entries[1].Content: got %v, want %v", gotVal, wantVal)
	}
	if !got1.AsOf.Equal(want1.AsOf) || !got1.LastAccess.Equal(want1.LastAccess) {
		t.Fatalf("Entries[1]: got %+v, want %+v", got1, want1)
	}
	got2 := loaded.Entries["2"]
	if got2.RemovedAt == nil || !got2.RemovedAt.Equal(removedAt) {
		t.Fatalf("Entries[2].RemovedAt: got %v, want %v", got2.RemovedAt, removedAt)
	}
}

func TestCacheSaveLoad_ConcurrentAccessDoesNotCorruptFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	key := CacheKey{Type: "pr", Backend: "pg-connector-fake-backend"}

	if err := saveCache(key, newEmptyCache()); err != nil {
		t.Fatalf("seed saveCache: %v", err)
	}

	const iterations = 20
	var wg sync.WaitGroup
	worker := func(tag string) {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			c, err := loadCache(key)
			if err != nil {
				t.Errorf("%s: loadCache: %v", tag, err)
				return
			}
			id := fmt.Sprintf("%s-%d", tag, i)
			c.Put(id, json.RawMessage(fmt.Sprintf(`{"id":%q}`, id)), time.Now(), time.Now())
			if err := saveCache(key, c); err != nil {
				t.Errorf("%s: saveCache: %v", tag, err)
				return
			}
		}
	}

	wg.Add(2)
	go worker("goroutine-a")
	go worker("goroutine-b")
	wg.Wait()

	path, err := cachePath(key)
	if err != nil {
		t.Fatalf("cachePath: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read final cache file: %v", err)
	}
	var final Cache
	if err := json.Unmarshal(data, &final); err != nil {
		t.Fatalf("final cache file is not valid JSON: %v\ncontent: %s", err, data)
	}
}

func TestCacheListKeys_RoundTrips(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)

	keys := []CacheKey{
		{Type: "pr", Backend: "pg-connector-pr-github"},
		{Type: "issue", Backend: "pg-connector-issue-beads"},
	}
	for _, k := range keys {
		if err := saveCache(k, newEmptyCache()); err != nil {
			t.Fatalf("saveCache(%+v): %v", k, err)
		}
	}

	got, err := ListCacheKeys()
	if err != nil {
		t.Fatalf("ListCacheKeys: %v", err)
	}
	gotSet := map[CacheKey]bool{}
	for _, k := range got {
		gotSet[k] = true
	}
	for _, k := range keys {
		if !gotSet[k] {
			t.Fatalf("ListCacheKeys missing %+v; got %+v", k, got)
		}
	}

	if err := deleteCache(keys[0]); err != nil {
		t.Fatalf("deleteCache(%+v): %v", keys[0], err)
	}
	got, err = ListCacheKeys()
	if err != nil {
		t.Fatalf("ListCacheKeys after delete: %v", err)
	}
	for _, k := range got {
		if k == keys[0] {
			t.Fatalf("ListCacheKeys still returned deleted key %+v: %+v", keys[0], got)
		}
	}
}

func TestCacheDelete_RemovesFileEntirely(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	key := CacheKey{Type: "pr", Backend: "pg-connector-fake-backend"}

	c := newEmptyCache()
	c.Put("1", json.RawMessage(`{"id":"1"}`), time.Now(), time.Now())
	if err := saveCache(key, c); err != nil {
		t.Fatalf("saveCache: %v", err)
	}

	if err := deleteCache(key); err != nil {
		t.Fatalf("deleteCache: %v", err)
	}

	reloaded, err := loadCache(key)
	if err != nil {
		t.Fatalf("loadCache after delete: %v", err)
	}
	if len(reloaded.Entries) != 0 {
		t.Fatalf("loadCache after deleteCache returned non-empty cache: %+v", reloaded)
	}
}

func TestCacheEnabled_DefaultsTrueWhenNoOptOut(t *testing.T) {
	writeFakeBackend(t, "pg-connector-fake-backend", `{"protocolVersion":1,"schemaVersions":{"pr":1},"ops":["capabilities"]}`)

	reg, err := parseRegistry([]byte(`connector: {}`), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}

	enabled, err := cacheEnabled(context.Background(), reg, "pr", "pg-connector-fake-backend")
	if err != nil {
		t.Fatalf("cacheEnabled: %v", err)
	}
	if !enabled {
		t.Fatal("cacheEnabled = false, want true (default-on, no opt-out)")
	}
}

func TestCacheEnabled_FalseWhenTypeOptedOutInState(t *testing.T) {
	writeFakeBackend(t, "pg-connector-fake-backend", `{"protocolVersion":1,"schemaVersions":{"pr":1},"ops":["capabilities"]}`)

	reg, err := parseRegistry([]byte(`
connector: {}
state:
  cache_disabled_types: "pr,issue"
`), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}

	enabled, err := cacheEnabled(context.Background(), reg, "pr", "pg-connector-fake-backend")
	if err != nil {
		t.Fatalf("cacheEnabled: %v", err)
	}
	if enabled {
		t.Fatal("cacheEnabled = true, want false (type named in cache_disabled_types)")
	}

	// A type NOT named in the list is unaffected by the same state: value.
	enabled, err = cacheEnabled(context.Background(), reg, "ci", "pg-connector-fake-backend")
	if err != nil {
		t.Fatalf("cacheEnabled: %v", err)
	}
	if !enabled {
		t.Fatal("cacheEnabled = false for a type not named in cache_disabled_types, want true")
	}
}

func TestCacheEnabled_FalseWhenBackendCapabilitiesVocabularyOptsOut(t *testing.T) {
	writeFakeBackend(t, "pg-connector-fake-backend", `{"protocolVersion":1,"schemaVersions":{"pr":1},"ops":["capabilities"],"vocabulary":{"cache_opt_out":true}}`)

	reg, err := parseRegistry([]byte(`connector: {}`), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}

	enabled, err := cacheEnabled(context.Background(), reg, "pr", "pg-connector-fake-backend")
	if err != nil {
		t.Fatalf("cacheEnabled: %v", err)
	}
	if enabled {
		t.Fatal("cacheEnabled = true, want false (backend capabilities vocabulary opts out)")
	}
}

func TestCacheEnabled_FailsOpenOnCapabilitiesError(t *testing.T) {
	reg, err := parseRegistry([]byte(`connector: {}`), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}

	// No backend by this name exists anywhere on $PATH, so the
	// capabilities call itself fails (exec lookup error) rather than
	// returning a well-formed response.
	enabled, err := cacheEnabled(context.Background(), reg, "pr", "pg-connector-nonexistent-backend-pg2-2j5ac-42-1")
	if err != nil {
		t.Fatalf("cacheEnabled returned an error instead of failing open: %v", err)
	}
	if !enabled {
		t.Fatal("cacheEnabled = false on a capabilities-call error, want true (fail open)")
	}
}

func TestCacheMaxAge_DefaultsAndParsesConfiguredValue(t *testing.T) {
	if got := resolveCacheMaxAge(nil); got != time.Hour {
		t.Fatalf("nil registry: got %v, want 1h", got)
	}

	absent, err := parseRegistry([]byte(`connector: {}`), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry(absent): %v", err)
	}
	if got := resolveCacheMaxAge(absent); got != time.Hour {
		t.Fatalf("absent state key: got %v, want 1h", got)
	}

	dayForm, err := parseRegistry([]byte(`
connector: {}
state:
  cache_max_age: "1d"
`), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry(day form): %v", err)
	}
	if got, want := resolveCacheMaxAge(dayForm), 24*time.Hour; got != want {
		t.Fatalf("day-suffix form: got %v, want %v", got, want)
	}

	goDuration, err := parseRegistry([]byte(`
connector: {}
state:
  cache_max_age: "30m"
`), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry(go duration): %v", err)
	}
	if got, want := resolveCacheMaxAge(goDuration), 30*time.Minute; got != want {
		t.Fatalf("go-duration form: got %v, want %v", got, want)
	}

	unparsable, err := parseRegistry([]byte(`
connector: {}
state:
  cache_max_age: "not-a-duration"
`), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry(unparsable): %v", err)
	}
	if got := resolveCacheMaxAge(unparsable); got != time.Hour {
		t.Fatalf("unparsable state value: got %v, want 1h default", got)
	}
}

func TestCacheSizeCap_DefaultsAndParsesConfiguredValue(t *testing.T) {
	if got := resolveCacheSizeCap(nil); got != 500 {
		t.Fatalf("nil registry: got %d, want 500", got)
	}

	absent, err := parseRegistry([]byte(`connector: {}`), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry(absent): %v", err)
	}
	if got := resolveCacheSizeCap(absent); got != 500 {
		t.Fatalf("absent state key: got %d, want 500", got)
	}

	configured, err := parseRegistry([]byte(`
connector: {}
state:
  cache_size_cap: "50"
`), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry(configured): %v", err)
	}
	if got := resolveCacheSizeCap(configured); got != 50 {
		t.Fatalf("configured state value: got %d, want 50", got)
	}

	negative, err := parseRegistry([]byte(`
connector: {}
state:
  cache_size_cap: "-1"
`), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry(negative): %v", err)
	}
	if got := resolveCacheSizeCap(negative); got != 500 {
		t.Fatalf("negative state value: got %d, want 500 default", got)
	}

	unparsable, err := parseRegistry([]byte(`
connector: {}
state:
  cache_size_cap: "not-a-number"
`), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry(unparsable): %v", err)
	}
	if got := resolveCacheSizeCap(unparsable); got != 500 {
		t.Fatalf("unparsable state value: got %d, want 500 default", got)
	}
}
