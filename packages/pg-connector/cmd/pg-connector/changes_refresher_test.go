package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// The changes refresher (INV-CACHE-6, INV-CACHE-7): membership plus a fetch of
// only the new or aged members, removal confirmation, post-flush detail
// writes, and the freshness stamp of a partial pass.

// refresherEnv sets up a scripted issue backend and a registry file with the
// given state: lines, isolating the ledger and cache under a temp dir.
func refresherEnv(t *testing.T, name, state string) *scriptedBackend {
	t.Helper()
	b := newScriptedBackend(t, name)
	dir := t.TempDir()
	cfg := dir + "/config.yaml"
	yaml := "connector:\n  issue:\n    - " + name + "\n"
	if state != "" {
		yaml += "state:\n" + state
	}
	if err := os.WriteFile(cfg, []byte(yaml), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("PG_PR_CONFIG", cfg)
	t.Setenv("XDG_STATE_HOME", dir)
	return b
}

func runChanges(t *testing.T, args ...string) changesWire {
	t.Helper()
	full := append([]string{"issue", "changes", "--query", "mine", "--consumer", "c1"}, args...)
	stdout, stderr, code := executePr(t, full)
	if code != 0 {
		t.Fatalf("changes exit = %d; stdout=%s stderr=%s", code, stdout, stderr)
	}
	return decodeChangesWire(t, stdout)
}

func loadTestCache(t *testing.T, backend string) *Cache {
	t.Helper()
	c, err := loadCache(CacheKey{Type: "issue", Backend: backend})
	if err != nil {
		t.Fatalf("loadCache: %v", err)
	}
	return c
}

func TestChangesRefresher_FetchesOnlyNewOrAgedMembers(t *testing.T) {
	b := refresherEnv(t, "backend-rf-aged", "  cache_refresh_after: 2m\n")
	now := time.Now()
	for _, id := range []string{"pr-1", "pr-2"} {
		b.setEntity(t, id, "t1", now)
	}
	b.setMembership(t, "pr-1", "pr-2")

	// First pass: both are new.
	w := runChanges(t)
	if kinds := changeKindsFor(w.Changes); kinds["added"] != 2 {
		t.Fatalf("first pass kinds = %v, want 2 added: %+v", kinds, w.Changes)
	}
	if n := b.calls(t, "show"); n != 2 {
		t.Fatalf("first pass shows = %d, want 2", n)
	}
	c := loadTestCache(t, b.name)
	for _, id := range []string{"pr-1", "pr-2"} {
		if e := c.Entries[id]; e.level() != CacheLevelDetail {
			t.Fatalf("cache entry %s level = %q, want detail (written after the flush)", id, e.Level)
		}
	}

	// Second pass: both are young, so no show, no change, and the id-only
	// membership is the only origin call.
	b.setEntity(t, "pr-2", "CHANGED", now) // changes upstream, but its entry is young
	w = runChanges(t)
	if len(w.Changes) != 0 {
		t.Fatalf("second pass changes = %+v, want none (members are within refresh_after)", w.Changes)
	}
	if n := b.calls(t, "show"); n != 2 {
		t.Fatalf("second pass shows = %d total, want still 2", n)
	}

	// Age pr-2's entry past refresh_after: it is re-fetched and reported changed.
	c = loadTestCache(t, b.name)
	e := c.Entries["pr-2"]
	e.AsOf = now.Add(-10 * time.Minute)
	c.Entries["pr-2"] = e
	seedCache(t, CacheKey{Type: "issue", Backend: b.name}, c)
	w = runChanges(t)
	if kinds := changeKindsFor(w.Changes); kinds["changed"] != 1 || len(w.Changes) != 1 {
		t.Fatalf("third pass = %+v, want exactly one changed", w.Changes)
	}
	if n := b.calls(t, "show"); n != 3 {
		t.Fatalf("third pass shows = %d total, want 3 (only the aged one)", n)
	}
}

func TestChangesRefresher_OffByDefaultUsesTheFullList(t *testing.T) {
	b := refresherEnv(t, "backend-rf-off", "")
	b.set(t, "list-full.json", fmt.Sprintf(`{"protocolVersion":1,"schemaVersion":1,"result":{"entities":[{"id":"pr-1","title":"t","state":"open","as_of":%q,"stale":false}],"present_ids":["pr-1"],"cursor":null,"truncated":false}}`, time.Now().UTC().Format(time.RFC3339)))

	w := runChanges(t)
	if kinds := changeKindsFor(w.Changes); kinds["added"] != 1 {
		t.Fatalf("kinds = %v, want 1 added", kinds)
	}
	if n := b.calls(t, "show"); n != 0 {
		t.Fatalf("shows = %d, want 0 (refresher is opt-in)", n)
	}
	// The ordinary path writes the listed entity at summary level, post-flush.
	if e := loadTestCache(t, b.name).Entries["pr-1"]; e.level() != CacheLevelSummary || len(e.Content) == 0 {
		t.Fatalf("cache entry = %+v, want a summary-level entry", e)
	}
}

func TestChangesRefresher_RemovalIsConfirmedByAReadAndCarriesTheConfirmedContent(t *testing.T) {
	b := refresherEnv(t, "backend-rf-remove", "  cache_refresh_after: 2m\n")
	now := time.Now()
	b.setEntity(t, "pr-1", "open", now)
	b.setEntity(t, "pr-2", "open", now)
	b.setMembership(t, "pr-1", "pr-2")
	runChanges(t)

	// pr-2 leaves the query and now reads as merged.
	b.setMembership(t, "pr-1")
	b.set(t, "show-pr-2.json", fmt.Sprintf(`{"protocolVersion":1,"schemaVersion":1,"result":{"id":"pr-2","title":"open","state":"merged","as_of":%q,"stale":false}}`, now.UTC().Format(time.RFC3339)))
	w := runChanges(t)
	if len(w.Changes) != 1 || w.Changes[0].Change != ChangeRemoved {
		t.Fatalf("changes = %+v, want one removed", w.Changes)
	}
	var e struct{ State string }
	if err := json.Unmarshal(w.Changes[0].Entity, &e); err != nil || e.State != "merged" {
		t.Fatalf("removed entity = %s, want the confirmed merged content", w.Changes[0].Entity)
	}
}

func TestChangesRefresher_NotFoundConfirmsTheRemovalWithLastContent(t *testing.T) {
	b := refresherEnv(t, "backend-rf-gone", "  cache_refresh_after: 2m\n")
	now := time.Now()
	b.setEntity(t, "pr-1", "open", now)
	b.setMembership(t, "pr-1")
	runChanges(t)

	b.setMembership(t)
	if err := os.Remove(b.dir + "/show-pr-1.json"); err != nil {
		t.Fatalf("remove entity: %v", err)
	}
	w := runChanges(t)
	if len(w.Changes) != 1 || w.Changes[0].Change != ChangeRemoved {
		t.Fatalf("changes = %+v, want one removed", w.Changes)
	}
	var e struct{ Title string }
	if err := json.Unmarshal(w.Changes[0].Entity, &e); err != nil || e.Title != "open" {
		t.Fatalf("removed entity = %s, want the last cached content", w.Changes[0].Entity)
	}
}

func TestChangesRefresher_AFailedConfirmationWithholdsTheRemoval(t *testing.T) {
	b := refresherEnv(t, "backend-rf-withhold", "  cache_refresh_after: 2m\n")
	now := time.Now()
	b.setEntity(t, "pr-1", "open", now)
	b.setMembership(t, "pr-1")
	runChanges(t)

	b.setMembership(t)
	b.set(t, "show-error", "unavailable")
	w := runChanges(t)
	if len(w.Changes) != 0 {
		t.Fatalf("changes = %+v, want the removal withheld while the confirmation read fails", w.Changes)
	}
	if !w.Sources[0].Truncated {
		t.Fatalf("source row = %+v, want truncated (the pass was incomplete)", w.Sources[0])
	}
	// The failed pass must not stamp refreshed_at (INV-CACHE-6); the earlier
	// whole-query pass did.
	key := LedgerKey{Type: "issue", Backend: b.name, Query: "mine"}
	l, err := loadLedger(key)
	if err != nil {
		t.Fatalf("loadLedger: %v", err)
	}
	if l.LastError == nil || l.LastError.Code != ledgerErrorTruncated {
		t.Fatalf("LastError = %+v, want code truncated", l.LastError)
	}
	if l.Entries["pr-1"].RemovedAtVersion != nil {
		t.Fatal("the withheld removal was tombstoned in the ledger")
	}

	// Once the confirmation can be made, the next call reports it.
	if err := os.Remove(b.dir + "/show-error"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(b.dir + "/show-pr-1.json"); err != nil {
		t.Fatal(err)
	}
	w = runChanges(t)
	if len(w.Changes) != 1 || w.Changes[0].Change != ChangeRemoved {
		t.Fatalf("retry changes = %+v, want one removed", w.Changes)
	}
}

func TestChangesRefresher_APartialPassRecordsNoRefreshedAt(t *testing.T) {
	b := refresherEnv(t, "backend-rf-partial", "  cache_refresh_after: 2m\n")
	b.setMembership(t, "pr-1")
	b.set(t, "show-error", "unavailable") // the new member cannot be fetched

	w := runChanges(t)
	if len(w.Changes) != 0 {
		t.Fatalf("changes = %+v, want none", w.Changes)
	}
	l, err := loadLedger(LedgerKey{Type: "issue", Backend: b.name, Query: "mine"})
	if err != nil {
		t.Fatalf("loadLedger: %v", err)
	}
	if l.RefreshedAt != nil {
		t.Fatalf("RefreshedAt = %v, want nil for a pass that failed to fetch a member", l.RefreshedAt)
	}
	if l.LastError == nil || l.LastError.Code != ledgerErrorTruncated {
		t.Fatalf("LastError = %+v, want truncated", l.LastError)
	}
}

func TestChangesRefresher_AWholePassStampsRefreshedAtAndCachedReadDoesNot(t *testing.T) {
	b := refresherEnv(t, "backend-rf-stamp", "  cache_refresh_after: 2m\n")
	b.setEntity(t, "pr-1", "t", time.Now())
	b.setMembership(t, "pr-1")
	runChanges(t)
	key := LedgerKey{Type: "issue", Backend: b.name, Query: "mine"}
	l, _ := loadLedger(key)
	if l.RefreshedAt == nil || l.LastError != nil {
		t.Fatalf("after a whole pass RefreshedAt=%v LastError=%+v, want stamped and no error", l.RefreshedAt, l.LastError)
	}
	first := *l.RefreshedAt

	before := b.calls(t, "list")
	runChanges(t, "--cached")
	if b.calls(t, "list") != before {
		t.Fatal("--cached called the backend")
	}
	l, _ = loadLedger(key)
	if !l.RefreshedAt.Equal(first) {
		t.Fatalf("a --cached read moved RefreshedAt from %v to %v", first, *l.RefreshedAt)
	}
}

func TestChangesRefresher_TypeOptOutKeepsTheOrdinaryPath(t *testing.T) {
	b := refresherEnv(t, "backend-rf-optout", "  cache_refresh_after: 2m\n  cache_disabled_types: issue\n")
	b.set(t, "list-full.json", `{"protocolVersion":1,"schemaVersion":1,"result":{"entities":[{"id":"pr-1","title":"t","state":"open"}],"present_ids":["pr-1"],"cursor":null,"truncated":false}}`)
	w := runChanges(t)
	if kinds := changeKindsFor(w.Changes); kinds["added"] != 1 {
		t.Fatalf("kinds = %v, want 1 added", kinds)
	}
	if n := b.calls(t, "show"); n != 0 {
		t.Fatalf("shows = %d, want 0 (an opted-out type never runs the refresher)", n)
	}
}

func TestRefresherEnabled_RequiresPositiveRefreshAfterAndAnIssueType(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	newScriptedBackend(t, "backend-rf-enabled")
	ctx := context.Background()
	cases := []struct {
		state string
		typ   string
		want  bool
	}{
		{"", "issue", false},
		{"  cache_refresh_after: 2m\n", "issue", true},
		{"  cache_refresh_after: 2m\n", "pr", false}, // the umbrella no longer refreshes pr
		{"  cache_refresh_after: 2m\n", "calendar", false},
		{"  cache_refresh_after: off\n", "issue", false},
		{"  cache_refresh_after: nonsense\n", "issue", false},
	}
	for _, tc := range cases {
		reg := registryFor(t, "backend-rf-enabled", tc.state)
		after, got := refresherEnabled(ctx, reg, tc.typ, "backend-rf-enabled")
		if got != tc.want {
			t.Errorf("refresherEnabled(state=%q, type=%s) = %t, want %t", tc.state, tc.typ, got, tc.want)
		}
		if !got && after != 0 {
			t.Errorf("refresherEnabled(state=%q, type=%s) after = %v, want 0 when disabled", tc.state, tc.typ, after)
		}
	}
}

// --- Cache: the refresher stamp -------------------------------------------

func TestCacheRefreshYoung_StampSemantics(t *testing.T) {
	now := time.Now()
	after := 2 * time.Minute
	old, fresh := now.Add(-time.Hour), now.Add(-time.Second)
	cases := []struct {
		name  string
		entry CacheEntry
		want  bool
	}{
		{"detail within after", CacheEntry{Level: CacheLevelDetail, AsOf: fresh}, true},
		{"detail aged", CacheEntry{Level: CacheLevelDetail, AsOf: old}, false},
		{"detail aged but stamped fresh", CacheEntry{Level: CacheLevelDetail, AsOf: old, RefreshedAt: &fresh}, true},
		{"summary fresh as_of but never stamped", CacheEntry{Level: CacheLevelSummary, AsOf: fresh}, false},
		{"summary stamped fresh", CacheEntry{Level: CacheLevelSummary, AsOf: old, RefreshedAt: &fresh}, true},
		{"summary stamp aged", CacheEntry{Level: CacheLevelSummary, AsOf: fresh, RefreshedAt: &old}, false},
		{"legacy entry (no level, no stamp)", CacheEntry{AsOf: fresh}, false},
		{"tombstoned", CacheEntry{Level: CacheLevelDetail, AsOf: fresh, RemovedAt: &fresh}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &Cache{Entries: map[string]CacheEntry{"a": tc.entry}}
			if got := c.RefreshYoung("a", after, now); got != tc.want {
				t.Fatalf("RefreshYoung = %v, want %v", got, tc.want)
			}
		})
	}
	if newEmptyCache().RefreshYoung("absent", after, now) {
		t.Fatal("an absent entry is never young")
	}
}

// PutRefreshed stamps without changing what PutLevel decides, never moves a
// stamp backwards, and the stamp survives a later content replacement and a JSON
// round trip.
func TestCachePutRefreshed(t *testing.T) {
	c := newEmptyCache()
	t0 := time.Now().Add(-time.Hour).Truncate(time.Second)
	t1 := t0.Add(30 * time.Minute)

	c.PutLevel("a", json.RawMessage(`{"id":"a","detail":true}`), t0, t0, CacheLevelDetail)
	c.PutRefreshed("a", json.RawMessage(`{"id":"a","summary":true}`), t1, t1, CacheLevelSummary)
	e := c.Entries["a"]
	if e.level() != CacheLevelDetail || !strings.Contains(string(e.Content), `"detail":true`) || !e.AsOf.Equal(t0) {
		t.Fatalf("a refreshed summary changed a live detail entry: %+v", e)
	}
	if e.RefreshedAt == nil || !e.RefreshedAt.Equal(t1) {
		t.Fatalf("RefreshedAt = %v, want %v", e.RefreshedAt, t1)
	}

	c.PutRefreshed("a", json.RawMessage(`{"id":"a","summary":true}`), t0, t1, CacheLevelSummary)
	if !c.Entries["a"].RefreshedAt.Equal(t1) {
		t.Fatalf("an older write moved the stamp backwards to %v", c.Entries["a"].RefreshedAt)
	}

	// A plain list write replaces a summary entry's content and keeps the stamp.
	c.PutRefreshed("s", json.RawMessage(`{"id":"s","v":1}`), t1, t1, CacheLevelSummary)
	c.PutLevel("s", json.RawMessage(`{"id":"s","v":2}`), t1.Add(time.Minute), t1.Add(time.Minute), CacheLevelSummary)
	if e := c.Entries["s"]; !strings.Contains(string(e.Content), `"v":2`) || e.RefreshedAt == nil || !e.RefreshedAt.Equal(t1) {
		t.Fatalf("entry = %+v, want new content and the old stamp kept", e)
	}
	// A tombstone does not carry its stamp into the entity's next life.
	c.Remove("s", t1)
	c.PutLevel("s", json.RawMessage(`{"id":"s","v":3}`), t1, t1, CacheLevelSummary)
	if c.Entries["s"].RefreshedAt != nil {
		t.Fatalf("a re-added entity carried the stamp of its tombstoned life: %+v", c.Entries["s"])
	}

	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var back Cache
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if got := back.Entries["a"].RefreshedAt; got == nil || !got.Equal(t1) {
		t.Fatalf("stamp lost across a round trip: %v", got)
	}
	// An entry with no stamp omits the key, so existing cache files are unchanged.
	if strings.Contains(string(mustMarshal(t, CacheEntry{AsOf: t0})), "refreshed_at") {
		t.Fatal("an unstamped entry must omit refreshed_at")
	}
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
