package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"
)

// fakeEntity builds a minimal schema-shaped entity JSON body (id, a content
// field, plus the AsOf/Stale pair every real schema entity carries) for
// ledger tests. field is the only content that matters for hashing; asOf is
// varied across calls to prove canonicalHash excludes it.
func fakeEntity(id, field, asOf string, stale bool) json.RawMessage {
	data, err := json.Marshal(map[string]any{
		"id":    id,
		"field": field,
		"as_of": asOf,
		"stale": stale,
	})
	if err != nil {
		panic(err)
	}
	return data
}

// listFnReturning builds a Refresh listFn stub returning the given fixed
// results, regardless of the cursor passed in.
func listFnReturning(entities []json.RawMessage, presentIDs []string, cursorOut json.RawMessage, truncated bool, err error) func(json.RawMessage) ([]json.RawMessage, []string, json.RawMessage, bool, error) {
	return func(json.RawMessage) ([]json.RawMessage, []string, json.RawMessage, bool, error) {
		return entities, presentIDs, cursorOut, truncated, err
	}
}

func changeKinds(changes []LedgerChange) map[string]int {
	out := map[string]int{}
	for _, c := range changes {
		out[string(c.Change)]++
	}
	return out
}

func hasChangeForID(t *testing.T, changes []LedgerChange, id string, kind ChangeKind) bool {
	t.Helper()
	for _, c := range changes {
		var fields struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(c.Entity, &fields); err != nil {
			t.Fatalf("unmarshal change entity: %v", err)
		}
		if fields.ID == id && c.Change == kind {
			return true
		}
	}
	return false
}

func TestLedgerRefresh_ClassifiesAddedThenChangedThenUnchanged(t *testing.T) {
	l := newEmptyLedger()

	first := []json.RawMessage{
		fakeEntity("1", "a", "2026-01-01T00:00:00Z", false),
		fakeEntity("2", "b", "2026-01-01T00:00:00Z", false),
	}
	changes, err := l.Refresh(listFnReturning(first, []string{"1", "2"}, json.RawMessage(`"cursor-1"`), false, nil))
	if err != nil {
		t.Fatalf("first Refresh: %v", err)
	}
	if len(changes) != 2 {
		t.Fatalf("first Refresh: got %d changes, want 2: %+v", len(changes), changes)
	}
	if kinds := changeKinds(changes); kinds["added"] != 2 {
		t.Fatalf("first Refresh: got kinds %+v, want 2 added", kinds)
	}
	if l.Version != 1 {
		t.Fatalf("first Refresh: l.Version = %d, want 1", l.Version)
	}

	// Second call: id "1" changes content; id "2" repeats the identical
	// content under a bumped AsOf (and flipped Stale, for good measure) --
	// canonicalHash must exclude both, so "2" must report zero changes.
	seenCursor := false
	second := []json.RawMessage{
		fakeEntity("1", "a-changed", "2026-01-02T00:00:00Z", false),
		fakeEntity("2", "b", "2026-01-03T00:00:00Z", true),
	}
	changes, err = l.Refresh(func(cursor json.RawMessage) ([]json.RawMessage, []string, json.RawMessage, bool, error) {
		if string(cursor) != `"cursor-1"` {
			t.Fatalf("second Refresh: listFn got cursor %s, want the stored cursor from the first call", cursor)
		}
		seenCursor = true
		return second, []string{"1", "2"}, json.RawMessage(`"cursor-2"`), false, nil
	})
	if err != nil {
		t.Fatalf("second Refresh: %v", err)
	}
	if !seenCursor {
		t.Fatal("second Refresh: listFn was never called with the stored cursor")
	}
	if len(changes) != 1 {
		t.Fatalf("second Refresh: got %d changes, want exactly 1 (id 2's bumped-AsOf-only content must report zero changes): %+v", len(changes), changes)
	}
	if !hasChangeForID(t, changes, "1", ChangeChanged) {
		t.Fatalf("second Refresh: expected id 1 classified changed, got %+v", changes)
	}
	if l.Version != 2 {
		t.Fatalf("second Refresh: l.Version = %d, want 2", l.Version)
	}
}

func TestLedgerRefresh_RemovalDerivedFromPresentIDsNotEntities(t *testing.T) {
	l := newEmptyLedger()

	seed := []json.RawMessage{
		fakeEntity("1", "a", "2026-01-01T00:00:00Z", false),
		fakeEntity("2", "b", "2026-01-01T00:00:00Z", false),
	}
	if _, err := l.Refresh(listFnReturning(seed, []string{"1", "2"}, json.RawMessage(`"c1"`), false, nil)); err != nil {
		t.Fatalf("seed Refresh: %v", err)
	}

	// entities is a strict SUBSET (only id "1" returned, simulating a
	// cursor-scoped incremental response), but presentIDs still names both
	// previously-indexed ids -- the full current match set -- and
	// truncated is false. Removal must NOT be derived from the smaller
	// entities set.
	incremental := []json.RawMessage{fakeEntity("1", "a", "2026-01-02T00:00:00Z", false)}
	changes, err := l.Refresh(listFnReturning(incremental, []string{"1", "2"}, json.RawMessage(`"c2"`), false, nil))
	if err != nil {
		t.Fatalf("incremental Refresh: %v", err)
	}
	if len(changes) != 0 {
		t.Fatalf("incremental Refresh: got %d changes, want 0 (id 2 must not be derived removed from the entities subset): %+v", len(changes), changes)
	}
	if entry := l.Entries["2"]; entry.RemovedAtVersion != nil {
		t.Fatalf("incremental Refresh: id 2's LedgerEntry was marked removed, want untouched: %+v", entry)
	}
}

func TestLedgerRefresh_TruncatedSuppressesRemoval(t *testing.T) {
	seeded := func() *Ledger {
		l := newEmptyLedger()
		seed := []json.RawMessage{
			fakeEntity("1", "a", "2026-01-01T00:00:00Z", false),
			fakeEntity("2", "b", "2026-01-01T00:00:00Z", false),
		}
		if _, err := l.Refresh(listFnReturning(seed, []string{"1", "2"}, json.RawMessage(`"c1"`), false, nil)); err != nil {
			t.Fatalf("seed Refresh: %v", err)
		}
		return l
	}

	t.Run("truncated=true suppresses removal", func(t *testing.T) {
		l := seeded()
		before := l.Entries["2"]

		// id "2" is absent from presentIDs, but truncated=true: it MUST
		// NOT be classified removed, and its LedgerEntry MUST be left
		// exactly as it was.
		changes, err := l.Refresh(listFnReturning(nil, []string{"1"}, json.RawMessage(`"c2"`), true, nil))
		if err != nil {
			t.Fatalf("truncated Refresh: %v", err)
		}
		if hasChangeForID(t, changes, "2", ChangeRemoved) {
			t.Fatalf("truncated Refresh incorrectly classified id 2 removed: %+v", changes)
		}
		after := l.Entries["2"]
		if after != before {
			t.Fatalf("truncated Refresh must leave id 2's LedgerEntry unchanged: before=%+v after=%+v", before, after)
		}
	})

	t.Run("truncated=false on the same fixture classifies removed", func(t *testing.T) {
		// A fresh, identically-seeded ledger, this time with
		// truncated=false on the exact same "id 2 absent from presentIDs"
		// shape -- proving the truncated=true case above actually
		// exercised the suppression branch rather than passing vacuously.
		l := seeded()
		changes, err := l.Refresh(listFnReturning(nil, []string{"1"}, json.RawMessage(`"c2"`), false, nil))
		if err != nil {
			t.Fatalf("non-truncated Refresh: %v", err)
		}
		if !hasChangeForID(t, changes, "2", ChangeRemoved) {
			t.Fatalf("non-truncated Refresh: expected id 2 classified removed, got %+v", changes)
		}
		entry := l.Entries["2"]
		if entry.RemovedAtVersion == nil {
			t.Fatalf("non-truncated Refresh: id 2's RemovedAtVersion is still nil, want set")
		}
	})
}

func TestLedgerRefresh_LeavesFileUntouchedOnError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	key := LedgerKey{Type: "pr", Backend: "pg-connector-fake-backend", Query: "q"}

	l := newEmptyLedger()
	seed := []json.RawMessage{fakeEntity("1", "a", "2026-01-01T00:00:00Z", false)}
	if _, err := l.Refresh(listFnReturning(seed, []string{"1"}, json.RawMessage(`"c1"`), false, nil)); err != nil {
		t.Fatalf("seed Refresh: %v", err)
	}
	if err := saveLedger(key, l); err != nil {
		t.Fatalf("saveLedger: %v", err)
	}

	path, err := ledgerPath(key)
	if err != nil {
		t.Fatalf("ledgerPath: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read ledger file before failed refresh: %v", err)
	}

	reloaded, err := loadLedger(key)
	if err != nil {
		t.Fatalf("loadLedger: %v", err)
	}
	wantVersion, wantCursor := reloaded.Version, string(reloaded.Cursor)

	backendErr := fmt.Errorf("backend unavailable")
	_, err = reloaded.Refresh(func(json.RawMessage) ([]json.RawMessage, []string, json.RawMessage, bool, error) {
		return nil, nil, nil, false, backendErr
	})
	if err == nil {
		t.Fatal("Refresh with a failing listFn returned no error")
	}

	if reloaded.Version != wantVersion || string(reloaded.Cursor) != wantCursor {
		t.Fatalf("Refresh mutated the in-memory ledger on error: version=%d cursor=%s", reloaded.Version, reloaded.Cursor)
	}

	// Per the contract, the caller must not call saveLedger after a failed
	// Refresh -- confirm no such save happened by re-reading the file
	// directly (this test never calls saveLedger again after the seed).
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read ledger file after failed refresh: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("ledger file changed across a failed Refresh:\nbefore=%s\nafter=%s", before, after)
	}
}

func TestSaveLoadLedger_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	key := LedgerKey{Type: "pr", Backend: "pg-connector-fake-backend", Query: "q"}

	removedAt := int64(3)
	l := &Ledger{
		Cursor: json.RawMessage(`"abc"`),
		Entries: map[string]LedgerEntry{
			"1": {Hash: "h1", VersionLastChanged: 2},
			"2": {Hash: "h2", VersionLastChanged: 3, RemovedAtVersion: &removedAt},
		},
		Version: 3,
		Consumers: map[string]ConsumerState{
			"c1": {Cursor: 2, LastSeen: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		},
	}

	if err := saveLedger(key, l); err != nil {
		t.Fatalf("saveLedger: %v", err)
	}

	loaded, err := loadLedger(key)
	if err != nil {
		t.Fatalf("loadLedger: %v", err)
	}

	if loaded.Version != l.Version {
		t.Fatalf("Version: got %d, want %d", loaded.Version, l.Version)
	}
	if string(loaded.Cursor) != string(l.Cursor) {
		t.Fatalf("Cursor: got %s, want %s", loaded.Cursor, l.Cursor)
	}
	if len(loaded.Entries) != len(l.Entries) {
		t.Fatalf("Entries: got %+v, want %+v", loaded.Entries, l.Entries)
	}
	for id, want := range l.Entries {
		got, ok := loaded.Entries[id]
		if !ok {
			t.Fatalf("Entries[%q] missing after round trip", id)
		}
		if got.Hash != want.Hash || got.VersionLastChanged != want.VersionLastChanged {
			t.Fatalf("Entries[%q]: got %+v, want %+v", id, got, want)
		}
		if (got.RemovedAtVersion == nil) != (want.RemovedAtVersion == nil) {
			t.Fatalf("Entries[%q].RemovedAtVersion nilness mismatch: got %v, want %v", id, got.RemovedAtVersion, want.RemovedAtVersion)
		}
		if got.RemovedAtVersion != nil && *got.RemovedAtVersion != *want.RemovedAtVersion {
			t.Fatalf("Entries[%q].RemovedAtVersion: got %d, want %d", id, *got.RemovedAtVersion, *want.RemovedAtVersion)
		}
	}
	gotConsumer, ok := loaded.Consumers["c1"]
	if !ok {
		t.Fatal("Consumers[\"c1\"] missing after round trip")
	}
	wantConsumer := l.Consumers["c1"]
	if gotConsumer.Cursor != wantConsumer.Cursor || !gotConsumer.LastSeen.Equal(wantConsumer.LastSeen) {
		t.Fatalf("Consumers[\"c1\"]: got %+v, want %+v", gotConsumer, wantConsumer)
	}
}

func TestSaveLoadLedger_ConcurrentRefreshesDoNotCorruptFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	key := LedgerKey{Type: "pr", Backend: "pg-connector-fake-backend", Query: "q"}

	if err := saveLedger(key, newEmptyLedger()); err != nil {
		t.Fatalf("seed saveLedger: %v", err)
	}

	const iterations = 20
	var wg sync.WaitGroup
	worker := func(tag string) {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			l, err := loadLedger(key)
			if err != nil {
				t.Errorf("%s: loadLedger: %v", tag, err)
				return
			}
			entity := fakeEntity(tag, fmt.Sprintf("v%d", i), "2026-01-01T00:00:00Z", false)
			if _, err := l.Refresh(listFnReturning([]json.RawMessage{entity}, []string{tag}, json.RawMessage(`"c"`), false, nil)); err != nil {
				t.Errorf("%s: Refresh: %v", tag, err)
				return
			}
			if err := saveLedger(key, l); err != nil {
				t.Errorf("%s: saveLedger: %v", tag, err)
				return
			}
		}
	}

	wg.Add(2)
	go worker("goroutine-a")
	go worker("goroutine-b")
	wg.Wait()

	path, err := ledgerPath(key)
	if err != nil {
		t.Fatalf("ledgerPath: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read final ledger file: %v", err)
	}
	var final Ledger
	if err := json.Unmarshal(data, &final); err != nil {
		t.Fatalf("final ledger file is not valid JSON: %v\ncontent: %s", err, data)
	}
}

func TestDeleteLedger_RemovesFileEntirely(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	key := LedgerKey{Type: "pr", Backend: "pg-connector-fake-backend", Query: "q"}

	l := newEmptyLedger()
	l.Entries["1"] = LedgerEntry{Hash: "h1", VersionLastChanged: 1}
	l.Version = 1
	if err := saveLedger(key, l); err != nil {
		t.Fatalf("saveLedger: %v", err)
	}

	if err := deleteLedger(key); err != nil {
		t.Fatalf("deleteLedger: %v", err)
	}

	reloaded, err := loadLedger(key)
	if err != nil {
		t.Fatalf("loadLedger after delete: %v", err)
	}
	if len(reloaded.Entries) != 0 || len(reloaded.Consumers) != 0 || reloaded.Version != 0 {
		t.Fatalf("loadLedger after deleteLedger returned non-zero-value ledger: %+v", reloaded)
	}
}

func keySet(keys []LedgerKey) map[LedgerKey]bool {
	out := make(map[LedgerKey]bool, len(keys))
	for _, k := range keys {
		out[k] = true
	}
	return out
}

func TestListLedgerKeys_RoundTrips(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)

	keys := []LedgerKey{
		{Type: "pr", Backend: "pg-connector-pr-github", Query: "pr-mine"},
		{Type: "pr", Backend: "pg-connector-pr-github", Query: "pr-team"},
		{Type: "issue", Backend: "pg-connector-issue-beads", Query: "default"},
	}
	for _, k := range keys {
		if err := saveLedger(k, newEmptyLedger()); err != nil {
			t.Fatalf("saveLedger(%+v): %v", k, err)
		}
	}

	got, err := ListLedgerKeys()
	if err != nil {
		t.Fatalf("ListLedgerKeys: %v", err)
	}
	if want, gotSet := keySet(keys), keySet(got); len(gotSet) != len(want) {
		t.Fatalf("ListLedgerKeys = %+v, want exactly %+v", got, keys)
	} else {
		for k := range want {
			if !gotSet[k] {
				t.Fatalf("ListLedgerKeys missing %+v; got %+v", k, got)
			}
		}
	}

	if err := deleteLedger(keys[0]); err != nil {
		t.Fatalf("deleteLedger(%+v): %v", keys[0], err)
	}
	got, err = ListLedgerKeys()
	if err != nil {
		t.Fatalf("ListLedgerKeys after delete: %v", err)
	}
	gotSet := keySet(got)
	if gotSet[keys[0]] {
		t.Fatalf("ListLedgerKeys still returned deleted key %+v: %+v", keys[0], got)
	}
	for _, k := range keys[1:] {
		if !gotSet[k] {
			t.Fatalf("ListLedgerKeys missing surviving key %+v: %+v", k, got)
		}
	}
}

func TestLedgerEvict_Rule1_RequiresEveryConsumerPastRemoval(t *testing.T) {
	removedAt := int64(5)
	l := &Ledger{
		Entries: map[string]LedgerEntry{
			"x": {Hash: "h", VersionLastChanged: 5, RemovedAtVersion: &removedAt},
		},
		Consumers: map[string]ConsumerState{
			"a": {Cursor: 5, LastSeen: time.Now()},
			"b": {Cursor: 3, LastSeen: time.Now()},
		},
	}

	l.Evict(LedgerKey{}, time.Now(), 30*24*time.Hour, false)
	if _, ok := l.Entries["x"]; !ok {
		t.Fatal("removed entry dropped before every consumer's cursor passed RemovedAtVersion")
	}

	l.Consumers["b"] = ConsumerState{Cursor: 5, LastSeen: time.Now()}
	l.Evict(LedgerKey{}, time.Now(), 30*24*time.Hour, false)
	if _, ok := l.Entries["x"]; ok {
		t.Fatal("removed entry survived after every consumer's cursor passed RemovedAtVersion")
	}
}

func TestLedgerEvict_Rule2_DropsStaleConsumer(t *testing.T) {
	now := time.Now()
	l := &Ledger{
		Entries: map[string]LedgerEntry{},
		Consumers: map[string]ConsumerState{
			"fresh": {Cursor: 1, LastSeen: now.Add(-time.Hour)},
			"stale": {Cursor: 1, LastSeen: now.Add(-31 * 24 * time.Hour)},
		},
	}

	l.Evict(LedgerKey{}, now, 30*24*time.Hour, false)

	if _, ok := l.Consumers["stale"]; ok {
		t.Fatal("stale consumer was not pruned")
	}
	if _, ok := l.Consumers["fresh"]; !ok {
		t.Fatal("fresh consumer was incorrectly pruned")
	}
}

func TestLedgerEvict_Rule3_DropsWholeLedgerOnQueryNotRecognized(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	key := LedgerKey{Type: "pr", Backend: "pg-connector-fake-backend", Query: "gone"}

	l := newEmptyLedger()
	l.Entries["1"] = LedgerEntry{Hash: "h", VersionLastChanged: 1}
	l.Consumers["c"] = ConsumerState{Cursor: 1, LastSeen: time.Now()}
	l.Version = 1
	l.Cursor = json.RawMessage(`"c1"`)
	if err := saveLedger(key, l); err != nil {
		t.Fatalf("saveLedger: %v", err)
	}

	l.Evict(key, time.Now(), 30*24*time.Hour, true)

	if len(l.Entries) != 0 || len(l.Consumers) != 0 || l.Version != 0 || l.Cursor != nil {
		t.Fatalf("Evict rule 3 did not clear the in-memory ledger: %+v", l)
	}

	keys, err := ListLedgerKeys()
	if err != nil {
		t.Fatalf("ListLedgerKeys: %v", err)
	}
	for _, k := range keys {
		if k == key {
			t.Fatalf("Evict rule 3 did not delete the on-disk ledger file for %+v; ListLedgerKeys still returns it: %+v", key, keys)
		}
	}
}

func TestResolveConsumerPruneAfter_DefaultsWhenAbsentOrUnparsable(t *testing.T) {
	if got := resolveConsumerPruneAfter(nil); got != 30*24*time.Hour {
		t.Fatalf("nil registry: got %v, want 30d", got)
	}

	absent, err := parseRegistry([]byte(`connector: {}`), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry(absent state): %v", err)
	}
	if got := resolveConsumerPruneAfter(absent); got != 30*24*time.Hour {
		t.Fatalf("absent state key: got %v, want 30d", got)
	}

	unparsable, err := parseRegistry([]byte(`
connector: {}
state:
  consumer_prune_after: "not-a-duration"
`), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry(unparsable): %v", err)
	}
	if got := resolveConsumerPruneAfter(unparsable); got != 30*24*time.Hour {
		t.Fatalf("unparsable state value: got %v, want 30d", got)
	}
}

func TestResolveConsumerPruneAfter_ParsesDeployedDayForm(t *testing.T) {
	// The literal string currently deployed in
	// phillipg-nix-ziprecruiter's machine config.
	reg, err := parseRegistry([]byte(`
connector: {}
state:
  consumer_prune_after: "30d"
`), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}
	got := resolveConsumerPruneAfter(reg)
	want := 30 * 24 * time.Hour
	if got != want {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestResolveConsumerPruneAfter_AcceptsGoDurationFallback(t *testing.T) {
	reg, err := parseRegistry([]byte(`
connector: {}
state:
  consumer_prune_after: "720h"
`), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}
	got := resolveConsumerPruneAfter(reg)
	want := 720 * time.Hour
	if got != want {
		t.Fatalf("got %v, want %v", got, want)
	}
}
