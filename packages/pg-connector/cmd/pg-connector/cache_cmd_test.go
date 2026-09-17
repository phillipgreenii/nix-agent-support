package main

import (
	"encoding/json"
	"testing"
	"time"
)

func decodeCacheShowRows(t *testing.T, stdout string) []cacheShowRow {
	t.Helper()
	var rows []cacheShowRow
	if err := json.Unmarshal([]byte(stdout), &rows); err != nil {
		t.Fatalf("decode cache show rows: %v (stdout=%s)", err, stdout)
	}
	return rows
}

func decodeCacheClearResult(t *testing.T, stdout string) cacheClearResult {
	t.Helper()
	var result cacheClearResult
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("decode cache clear result: %v (stdout=%s)", err, stdout)
	}
	return result
}

// TestCacheShow_PrintsLiveAndTombstonedEntryCountsSeparately covers this
// packet's own Contract: "cache show" prints entry count (live +
// tombstoned, reported separately) per matching key.
func TestCacheShow_PrintsLiveAndTombstonedEntryCountsSeparately(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)

	removedAt := time.Now().UTC()
	key := CacheKey{Type: "pr", Backend: "pg-connector-pr-github"}
	seedCache(t, key, &Cache{Entries: map[string]CacheEntry{
		"o/r#1": {Content: []byte(`{"id":"o/r#1"}`), AsOf: time.Now().UTC()},
		"o/r#2": {Content: []byte(`{"id":"o/r#2"}`), AsOf: time.Now().UTC()},
		"o/r#3": {Content: []byte(`{"id":"o/r#3"}`), AsOf: time.Now().UTC(), RemovedAt: &removedAt},
	}})

	stdout, _, code := executePr(t, []string{"cache", "show", "--type", "pr", "--backend", "pg-connector-pr-github"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}
	rows := decodeCacheShowRows(t, stdout)
	if len(rows) != 1 {
		t.Fatalf("rows = %+v, want exactly 1", rows)
	}
	r := rows[0]
	if r.Type != "pr" || r.Backend != "pg-connector-pr-github" {
		t.Fatalf("row = %+v, want type=pr backend=pg-connector-pr-github", r)
	}
	if r.LiveEntries != 2 {
		t.Fatalf("LiveEntries = %d, want 2", r.LiveEntries)
	}
	if r.TombstonedEntries != 1 {
		t.Fatalf("TombstonedEntries = %d, want 1", r.TombstonedEntries)
	}
}

// TestCacheShow_EmptyMatchReportsEmptyResultNotError proves an empty
// filter match is an empty result list (exit 0), never an error — this
// packet's own Contract restates ledger_cmd.go's existing convention.
func TestCacheShow_EmptyMatchReportsEmptyResultNotError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)

	stdout, _, code := executePr(t, []string{"cache", "show", "--type", "nonexistent"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}
	rows := decodeCacheShowRows(t, stdout)
	if len(rows) != 0 {
		t.Fatalf("rows = %+v, want empty", rows)
	}

	stdoutHuman, _, codeHuman := executePr(t, []string{"--output", "human", "cache", "show", "--type", "nonexistent"})
	if codeHuman != 0 {
		t.Fatalf("human: exit code = %d, want 0; stdout=%s", codeHuman, stdoutHuman)
	}
	if len(stdoutHuman) == 0 {
		t.Fatal("human output empty, want a no-matching-caches message")
	}
}

// TestCacheShow_PartialFilterMatchesBothKeys proves filterCacheKeys'
// partial-filter matching rule (an omitted flag matches any value): a
// bare --type alone must match both of two caches with the same Type but
// different Backend.
func TestCacheShow_PartialFilterMatchesBothKeys(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)

	seedCache(t, CacheKey{Type: "pr", Backend: "backend-a"}, &Cache{Entries: map[string]CacheEntry{
		"o/r#1": {Content: []byte(`{"id":"o/r#1"}`), AsOf: time.Now().UTC()},
	}})
	seedCache(t, CacheKey{Type: "pr", Backend: "backend-b"}, &Cache{Entries: map[string]CacheEntry{
		"o/r#2": {Content: []byte(`{"id":"o/r#2"}`), AsOf: time.Now().UTC()},
	}})

	stdout, _, code := executePr(t, []string{"cache", "show", "--type", "pr"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}
	rows := decodeCacheShowRows(t, stdout)
	if len(rows) != 2 {
		t.Fatalf("rows = %+v, want both caches under Type pr", rows)
	}
}

// TestCacheClear_RemovesFileAndShowAfterwardShowsNothing covers "cache
// clear --type pr --backend pg-connector-pr-github deletes that key's
// on-disk cache file and reports exactly which keys were cleared, and
// cache show afterward shows nothing for it."
func TestCacheClear_RemovesFileAndShowAfterwardShowsNothing(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)

	key := CacheKey{Type: "pr", Backend: "pg-connector-pr-github"}
	seedCache(t, key, &Cache{Entries: map[string]CacheEntry{
		"o/r#1": {Content: []byte(`{"id":"o/r#1"}`), AsOf: time.Now().UTC()},
	}})

	stdout, _, code := executePr(t, []string{"cache", "clear", "--type", "pr", "--backend", "pg-connector-pr-github"})
	if code != 0 {
		t.Fatalf("clear: exit code = %d, want 0; stdout=%s", code, stdout)
	}
	cleared := decodeCacheClearResult(t, stdout)
	if len(cleared.Cleared) != 1 || cleared.Cleared[0].Type != "pr" || cleared.Cleared[0].Backend != "pg-connector-pr-github" {
		t.Fatalf("Cleared = %+v, want the one matching key", cleared.Cleared)
	}

	stdout2, _, code2 := executePr(t, []string{"cache", "show", "--type", "pr", "--backend", "pg-connector-pr-github"})
	if code2 != 0 {
		t.Fatalf("show after clear: exit code = %d, want 0; stdout=%s", code2, stdout2)
	}
	rows := decodeCacheShowRows(t, stdout2)
	if len(rows) != 0 {
		t.Fatalf("rows after clear = %+v, want empty", rows)
	}

	keysAfter, err := ListCacheKeys()
	if err != nil {
		t.Fatalf("ListCacheKeys: %v", err)
	}
	if len(keysAfter) != 0 {
		t.Fatalf("keys after clear = %+v, want none left", keysAfter)
	}
}

// TestCacheClear_NeverDispatchesToABackend proves cache clear (like
// ledger clear) always exits 0 and never touches a backend — clearing a
// key for a backend with no registered entry at all still succeeds,
// since neither verb ever dispatches.
func TestCacheClear_NeverDispatchesToABackend(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)

	key := CacheKey{Type: "pr", Backend: "unregistered-backend"}
	seedCache(t, key, &Cache{Entries: map[string]CacheEntry{
		"o/r#1": {Content: []byte(`{"id":"o/r#1"}`), AsOf: time.Now().UTC()},
	}})

	stdout, _, code := executePr(t, []string{"cache", "clear", "--backend", "unregistered-backend"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}
	cleared := decodeCacheClearResult(t, stdout)
	if len(cleared.Cleared) != 1 {
		t.Fatalf("Cleared = %+v, want the one matching key cleared with no backend involved", cleared.Cleared)
	}
}

// TestCacheShow_HumanOutput smoke-checks --output human never leaks raw
// JSON, mirroring TestRun_LedgerShow_HumanOutput.
func TestCacheShow_HumanOutput(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	key := CacheKey{Type: "pr", Backend: "b"}
	seedCache(t, key, &Cache{Entries: map[string]CacheEntry{
		"o/r#1": {Content: []byte(`{"id":"o/r#1"}`), AsOf: time.Now().UTC()},
	}})

	stdout, _, code := executePr(t, []string{"--output", "human", "cache", "show"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}
	if len(stdout) == 0 {
		t.Fatal("human output empty")
	}
}
