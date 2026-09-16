package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// seedCache saves c for key directly, bypassing the CLI/dispatch layer, so
// a test can establish precise pre-call cache state without first driving
// a real backend call to build it up — mirrors changes_test.go's own
// seedLedger.
func seedCache(t *testing.T, key CacheKey, c *Cache) {
	t.Helper()
	if err := saveCache(key, c); err != nil {
		t.Fatalf("seed cache %+v: %v", key, err)
	}
}

// TestDispatchShowWithCache_LiveSuccessThenServedStaleOnUnavailable covers
// this packet's own Validation items (a) and (b) together: a live success
// (a) Puts the returned entity into the cache (mirroring what
// newPrShowCmd/newIssueShowCmd do on their own success path), and a
// SUBSEQUENT call against the same backend now answering
// scriptout.ErrUnavailable (b) is served from that cache with stale: true
// and the ORIGINAL live as_of, exit-worthy as a nil error (this docket's
// own Binding decision: "a cache-served show/list result MUST exit 0").
func TestDispatchShowWithCache_LiveSuccessThenServedStaleOnUnavailable(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	liveAsOf := time.Now().UTC().Format(time.RFC3339)
	writeOpAwareFakeBackend(t, "backend-dsc-live", map[string]string{
		"show": fmt.Sprintf(`{"protocolVersion":1,"schemaVersion":1,"result":{"id":"pr-1","as_of":%q,"stale":false}}`, liveAsOf),
	}, `{"protocolVersion":1,"schemaVersions":{"pr":1},"ops":["capabilities","show"]}`)

	reg, err := parseRegistry([]byte("connector:\n  pr:\n    - backend-dsc-live\n"), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}
	ctx := context.Background()

	// (a) Live success.
	resp, backend, fromCache, err := dispatchShowWithCache(ctx, reg, "pr", "pr-1", "")
	if err != nil {
		t.Fatalf("dispatchShowWithCache (live): %v", err)
	}
	if fromCache {
		t.Fatal("first call: fromCache = true, want false (this is the live read)")
	}
	if backend != "backend-dsc-live" {
		t.Fatalf("backend = %q, want backend-dsc-live", backend)
	}
	// Mirror what newPrShowCmd/newIssueShowCmd do on a live success.
	putLiveEntity(ctx, reg, "pr", backend, resp.Result)

	// Re-point the SAME registered backend name at a script now answering
	// unavailable for "show" (a later writeOpAwareFakeBackend prepends a
	// new dir ahead of the first on $PATH, so exec.LookPath finds it
	// first — the same trick this package's own tests already rely on).
	writeOpAwareFakeBackend(t, "backend-dsc-live", map[string]string{
		"show": `{"protocolVersion":1,"schemaVersion":1,"error":{"code":"unavailable","message":"backend down"}}`,
	}, `{"protocolVersion":1,"schemaVersions":{"pr":1},"ops":["capabilities","show"]}`)

	// (b) Subsequent unavailable, served from cache.
	resp2, backend2, fromCache2, err2 := dispatchShowWithCache(ctx, reg, "pr", "pr-1", "")
	if err2 != nil {
		t.Fatalf("dispatchShowWithCache (fallback): %v, want nil (cache-served MUST exit 0)", err2)
	}
	if !fromCache2 {
		t.Fatal("second call: fromCache = false, want true (this is the cache-served fallback)")
	}
	if backend2 != "backend-dsc-live" {
		t.Fatalf("backend2 = %q, want backend-dsc-live", backend2)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(resp2.Result, &fields); err != nil {
		t.Fatalf("decode fallback result: %v", err)
	}
	if string(fields["stale"]) != "true" {
		t.Fatalf(`fallback result["stale"] = %s, want true`, fields["stale"])
	}
	gotAsOf := ""
	if err := json.Unmarshal(fields["as_of"], &gotAsOf); err != nil {
		t.Fatalf("decode fallback as_of: %v", err)
	}
	if gotAsOf != liveAsOf {
		t.Fatalf("fallback as_of = %q, want the ORIGINAL live as_of %q", gotAsOf, liveAsOf)
	}
}

// TestDispatchShowWithCache_OptedOutTypeFallsThroughToRawUnavailable
// covers Validation item (c): an opted-out type/backend still reports the
// raw unavailable unchanged, even when a matching cache entry exists —
// proving the opt-out, not merely an empty cache, is what keeps this
// path's error unchanged.
func TestDispatchShowWithCache_OptedOutTypeFallsThroughToRawUnavailable(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	writeOpAwareFakeBackend(t, "backend-dsc-optout", map[string]string{
		"show": `{"protocolVersion":1,"schemaVersion":1,"error":{"code":"unavailable","message":"backend down"}}`,
	}, `{"protocolVersion":1,"schemaVersions":{"pr":1},"ops":["capabilities","show"]}`)

	reg, err := parseRegistry([]byte("connector:\n  pr:\n    - backend-dsc-optout\nstate:\n  cache_disabled_types: \"pr\"\n"), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}

	// Pre-seed a cache entry that WOULD satisfy the fallback if caching
	// were enabled for pr.
	seedCache(t, CacheKey{Type: "pr", Backend: "backend-dsc-optout"}, &Cache{Entries: map[string]CacheEntry{
		"pr-1": {Content: json.RawMessage(`{"id":"pr-1"}`), AsOf: time.Now(), LastAccess: time.Now()},
	}})

	resp, _, fromCache, err := dispatchShowWithCache(context.Background(), reg, "pr", "pr-1", "")
	if err == nil || !errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("dispatchShowWithCache err = %v, want the raw ErrUnavailable unchanged", err)
	}
	if fromCache {
		t.Fatal("fromCache = true, want false (type opted out of caching)")
	}
	// resp is still the backend's own decoded error envelope (Invoke's own
	// convention: a non-nil resp accompanies a non-nil error on a
	// backend-reported failure) — never nil, and never turned into a
	// synthesized cache-fallback response.
	if resp == nil || resp.Error == nil || resp.Error.Code != "unavailable" {
		t.Fatalf("resp = %+v, want the backend's own unmodified unavailable error envelope", resp)
	}
}

// TestDispatchShowWithCache_NoMatchingCacheEntryFallsThroughToRawUnavailable
// covers Validation item (c)'s sibling case named in this packet's own
// Acceptance criteria: no matching cache entry (nothing was ever Put)
// behaves exactly like today — the raw unavailable, unchanged.
func TestDispatchShowWithCache_NoMatchingCacheEntryFallsThroughToRawUnavailable(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	writeOpAwareFakeBackend(t, "backend-dsc-miss", map[string]string{
		"show": `{"protocolVersion":1,"schemaVersion":1,"error":{"code":"unavailable","message":"backend down"}}`,
	}, `{"protocolVersion":1,"schemaVersions":{"pr":1},"ops":["capabilities","show"]}`)

	reg, err := parseRegistry([]byte("connector:\n  pr:\n    - backend-dsc-miss\n"), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}

	resp, _, fromCache, err := dispatchShowWithCache(context.Background(), reg, "pr", "pr-1", "")
	if err == nil || !errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("dispatchShowWithCache err = %v, want the raw ErrUnavailable unchanged (no cache entry ever Put)", err)
	}
	if fromCache {
		t.Fatal("fromCache = true, want false (nothing was ever cached for this id)")
	}
	if resp == nil || resp.Error == nil || resp.Error.Code != "unavailable" {
		t.Fatalf("resp = %+v, want the backend's own unmodified unavailable error envelope", resp)
	}
}

// TestDispatchShowWithCache_ExpiredEntryFallsThroughToRawUnavailable
// covers the Acceptance criteria's third disabled-fallback case: a cache
// entry exists but is older than the resolved max-age, so it behaves
// exactly like a miss.
func TestDispatchShowWithCache_ExpiredEntryFallsThroughToRawUnavailable(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	writeOpAwareFakeBackend(t, "backend-dsc-expired", map[string]string{
		"show": `{"protocolVersion":1,"schemaVersion":1,"error":{"code":"unavailable","message":"backend down"}}`,
	}, `{"protocolVersion":1,"schemaVersions":{"pr":1},"ops":["capabilities","show"]}`)

	reg, err := parseRegistry([]byte("connector:\n  pr:\n    - backend-dsc-expired\n"), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}

	// Default max-age is one hour; seed an entry well past it.
	seedCache(t, CacheKey{Type: "pr", Backend: "backend-dsc-expired"}, &Cache{Entries: map[string]CacheEntry{
		"pr-1": {Content: json.RawMessage(`{"id":"pr-1"}`), AsOf: time.Now().Add(-2 * time.Hour), LastAccess: time.Now()},
	}})

	resp, _, fromCache, err := dispatchShowWithCache(context.Background(), reg, "pr", "pr-1", "")
	if err == nil || !errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("dispatchShowWithCache err = %v, want the raw ErrUnavailable unchanged (entry past max-age)", err)
	}
	if fromCache {
		t.Fatal("fromCache = true, want false (expired entry)")
	}
	if resp == nil || resp.Error == nil || resp.Error.Code != "unavailable" {
		t.Fatalf("resp = %+v, want the backend's own unmodified unavailable error envelope", resp)
	}
}
