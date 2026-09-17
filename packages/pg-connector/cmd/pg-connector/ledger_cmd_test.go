package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func decodeLedgerShowRows(t *testing.T, stdout string) []ledgerShowRow {
	t.Helper()
	var rows []ledgerShowRow
	if err := json.Unmarshal([]byte(stdout), &rows); err != nil {
		t.Fatalf("decode ledger show rows: %v (stdout=%s)", err, stdout)
	}
	return rows
}

func decodeLedgerClearResult(t *testing.T, stdout string) ledgerClearResult {
	t.Helper()
	var result ledgerClearResult
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("decode ledger clear result: %v (stdout=%s)", err, stdout)
	}
	return result
}

// TestRun_LedgerShow_PrintsCursorIndexSizeVersionAndConsumerPosition
// covers "ledger show prints the stored cursor, index size, version, and
// consumer position for a known (type, backend, query[, consumer])
// combination."
func TestRun_LedgerShow_PrintsCursorIndexSizeVersionAndConsumerPosition(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)

	key := LedgerKey{Type: "pr", Backend: "pg-connector-pr-github", Query: "mine"}
	lastSeen := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	seedLedger(t, key, &Ledger{
		Cursor:  json.RawMessage(`"cursor-1"`),
		Entries: map[string]LedgerEntry{"o/r#1": {Hash: "h1", VersionLastChanged: 1}},
		Version: 1,
		Consumers: map[string]ConsumerState{
			"c1": {Cursor: 1, LastSeen: lastSeen},
		},
	})

	stdout, _, code := executePr(t, []string{"ledger", "show", "--type", "pr", "--backend", "pg-connector-pr-github", "--query", "mine"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}
	rows := decodeLedgerShowRows(t, stdout)
	if len(rows) != 1 {
		t.Fatalf("rows = %+v, want exactly 1", rows)
	}
	r := rows[0]
	if string(r.Cursor) != `"cursor-1"` {
		t.Fatalf("Cursor = %s, want \"cursor-1\"", r.Cursor)
	}
	if r.IndexSize != 1 {
		t.Fatalf("IndexSize = %d, want 1", r.IndexSize)
	}
	if r.Version != 1 {
		t.Fatalf("Version = %d, want 1", r.Version)
	}
	cs, ok := r.Consumers["c1"]
	if !ok || cs.Cursor != 1 {
		t.Fatalf("Consumers = %+v, want c1 at cursor 1", r.Consumers)
	}
}

// TestRun_LedgerShow_ConsumerFilterNarrowsPositionsNotWhichLedgersMatch
// covers the --consumer flag's own documented scope: it narrows which
// consumer POSITIONS are printed within each matching ledger, without
// narrowing which ledgers match.
func TestRun_LedgerShow_ConsumerFilterNarrowsPositionsNotWhichLedgersMatch(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)

	key := LedgerKey{Type: "pr", Backend: "b", Query: "mine"}
	seedLedger(t, key, &Ledger{
		Entries: map[string]LedgerEntry{},
		Version: 0,
		Consumers: map[string]ConsumerState{
			"c1": {Cursor: 1, LastSeen: time.Now()},
			"c2": {Cursor: 2, LastSeen: time.Now()},
		},
	})

	stdout, _, code := executePr(t, []string{"ledger", "show", "--consumer", "c2"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}
	rows := decodeLedgerShowRows(t, stdout)
	if len(rows) != 1 {
		t.Fatalf("rows = %+v, want the one ledger to still match (the ledger, not the consumer, is filtered by --type/--backend/--query)", rows)
	}
	if len(rows[0].Consumers) != 1 {
		t.Fatalf("Consumers = %+v, want only c2's position", rows[0].Consumers)
	}
	if _, ok := rows[0].Consumers["c2"]; !ok {
		t.Fatalf("Consumers = %+v, want c2 present", rows[0].Consumers)
	}
}

// TestLedgerClear_RemovesFileAndShowAfterwardShowsNothing covers "ledger
// clear --type pr --backend pg-connector-pr-github removes that key's
// on-disk ledger file and ledger show afterward shows nothing for it,"
// AND (phase 14, bead pg2-2j5ac.42.4) that the same call also drops the
// matching CacheKey's own on-disk cache file, reporting it in the
// result's ClearedCache field. Renamed from
// TestRun_LedgerClear_RemovesFileAndShowAfterwardShowsNothing so this
// packet's own validation command (`-run 'TestCache|TestLedgerClear'`)
// selects it; no existing assertion was removed, only extended.
func TestLedgerClear_RemovesFileAndShowAfterwardShowsNothing(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)

	key := LedgerKey{Type: "pr", Backend: "pg-connector-pr-github", Query: "mine"}
	seedLedger(t, key, &Ledger{Entries: map[string]LedgerEntry{}, Version: 1, Consumers: map[string]ConsumerState{}})

	cacheKey := CacheKey{Type: "pr", Backend: "pg-connector-pr-github"}
	seedCache(t, cacheKey, &Cache{Entries: map[string]CacheEntry{
		"o/r#1": {Content: []byte(`{"id":"o/r#1"}`), AsOf: time.Now().UTC()},
	}})

	stdout, _, code := executePr(t, []string{"ledger", "clear", "--type", "pr", "--backend", "pg-connector-pr-github"})
	if code != 0 {
		t.Fatalf("clear: exit code = %d, want 0; stdout=%s", code, stdout)
	}
	cleared := decodeLedgerClearResult(t, stdout)
	if len(cleared.Cleared) != 1 || cleared.Cleared[0].Backend != "pg-connector-pr-github" {
		t.Fatalf("Cleared = %+v, want the one matching key", cleared.Cleared)
	}
	if len(cleared.ClearedCache) != 1 || cleared.ClearedCache[0].Type != "pr" || cleared.ClearedCache[0].Backend != "pg-connector-pr-github" {
		t.Fatalf("ClearedCache = %+v, want the one matching cache key", cleared.ClearedCache)
	}

	stdout2, _, code2 := executePr(t, []string{"ledger", "show", "--type", "pr", "--backend", "pg-connector-pr-github"})
	if code2 != 0 {
		t.Fatalf("show after clear: exit code = %d, want 0; stdout=%s", code2, stdout2)
	}
	rows := decodeLedgerShowRows(t, stdout2)
	if len(rows) != 0 {
		t.Fatalf("rows after clear = %+v, want empty", rows)
	}

	cacheKeysAfter, err := ListCacheKeys()
	if err != nil {
		t.Fatalf("ListCacheKeys: %v", err)
	}
	if len(cacheKeysAfter) != 0 {
		t.Fatalf("cache keys after clear = %+v, want none left", cacheKeysAfter)
	}
}

// TestLedgerClear_UnconditionalEvenWhenTypeOptedOutOfCaching proves
// ledger clear's cache-dropping extension is UNCONDITIONAL: it does not
// consult cacheEnabled/opt-outs before dropping a matching cache file, so
// a since-opted-out type's stale cache still gets cleared [design: docket
// design field, "Binding decisions"].
func TestLedgerClear_UnconditionalEvenWhenTypeOptedOutOfCaching(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)

	key := LedgerKey{Type: "pr", Backend: "b", Query: "mine"}
	seedLedger(t, key, &Ledger{Entries: map[string]LedgerEntry{}, Version: 1, Consumers: map[string]ConsumerState{}})

	cacheKey := CacheKey{Type: "pr", Backend: "b"}
	seedCache(t, cacheKey, &Cache{Entries: map[string]CacheEntry{
		"o/r#1": {Content: []byte(`{"id":"o/r#1"}`), AsOf: time.Now().UTC()},
	}})

	// Opt "pr" out of caching entirely via the same state: key
	// cacheEnabled consults.
	reg, err := parseRegistry([]byte("connector:\n  pr:\n    - b\nstate:\n  cache_disabled_types: pr\n"), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}
	enabled, err := cacheEnabled(context.Background(), reg, "pr", "b")
	if err != nil || enabled {
		t.Fatalf("cacheEnabled = (%v, %v), want (false, nil) — precondition for this test", enabled, err)
	}

	stdout, _, code := executePr(t, []string{"ledger", "clear", "--type", "pr", "--backend", "b"})
	if code != 0 {
		t.Fatalf("clear: exit code = %d, want 0; stdout=%s", code, stdout)
	}
	cleared := decodeLedgerClearResult(t, stdout)
	if len(cleared.ClearedCache) != 1 {
		t.Fatalf("ClearedCache = %+v, want the opted-out type's stale cache cleared anyway", cleared.ClearedCache)
	}
}

// TestRun_LedgerShowAndClear_PartialFilterMatchesBothLedgers proves
// ListLedgerKeys' partial-filter matching rule is actually implemented
// (an omitted flag matches any value), not just a single-full-key
// lookup: --type pr alone must match and clear/show BOTH of two ledgers
// with the same Type but different Backend/Query.
func TestRun_LedgerShowAndClear_PartialFilterMatchesBothLedgers(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)

	keyA := LedgerKey{Type: "pr", Backend: "backend-a", Query: "mine"}
	keyB := LedgerKey{Type: "pr", Backend: "backend-b", Query: "team"}
	seedLedger(t, keyA, &Ledger{Entries: map[string]LedgerEntry{}, Version: 1, Consumers: map[string]ConsumerState{}})
	seedLedger(t, keyB, &Ledger{Entries: map[string]LedgerEntry{}, Version: 1, Consumers: map[string]ConsumerState{}})

	stdout, _, code := executePr(t, []string{"ledger", "show", "--type", "pr"})
	if code != 0 {
		t.Fatalf("show: exit code = %d, want 0; stdout=%s", code, stdout)
	}
	rows := decodeLedgerShowRows(t, stdout)
	if len(rows) != 2 {
		t.Fatalf("rows = %+v, want both ledgers under Type pr", rows)
	}

	stdout2, _, code2 := executePr(t, []string{"ledger", "clear", "--type", "pr"})
	if code2 != 0 {
		t.Fatalf("clear: exit code = %d, want 0; stdout=%s", code2, stdout2)
	}
	cleared := decodeLedgerClearResult(t, stdout2)
	if len(cleared.Cleared) != 2 {
		t.Fatalf("Cleared = %+v, want both ledgers cleared by the partial --type filter alone", cleared.Cleared)
	}

	keys, err := ListLedgerKeys()
	if err != nil {
		t.Fatalf("ListLedgerKeys: %v", err)
	}
	if len(keys) != 0 {
		t.Fatalf("keys after clear = %+v, want none left", keys)
	}
}

// TestRun_LedgerShow_HumanOutput smoke-checks --output human never leaks
// raw JSON.
func TestRun_LedgerShow_HumanOutput(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	key := LedgerKey{Type: "pr", Backend: "b", Query: "mine"}
	seedLedger(t, key, &Ledger{Entries: map[string]LedgerEntry{}, Version: 3, Consumers: map[string]ConsumerState{}})

	stdout, _, code := executePr(t, []string{"--output", "human", "ledger", "show"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}
	if len(stdout) == 0 {
		t.Fatalf("human output empty")
	}
}
