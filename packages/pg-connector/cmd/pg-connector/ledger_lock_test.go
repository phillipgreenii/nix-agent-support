package main

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofrs/flock"
)

func lockTestKey() LedgerKey {
	return LedgerKey{Type: "pr", Backend: "gh", Query: "mine"}
}

// A poll that read the ledger before another poll saved must not, when it
// commits its cursor advance afterwards, write its old snapshot back: the
// other poll's version, entries and a higher cursor stay, and the cursor never
// moves backward.
func TestCommitAdvances_StaleSnapshotKeepsNewerStateAndNeverMovesCursorBack(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	key := lockTestKey()

	seed := newEmptyLedger()
	seed.Version = 1
	seed.Entries["a"] = LedgerEntry{}
	if err := saveLedger(key, seed); err != nil {
		t.Fatal(err)
	}

	// Poll A loaded version 1 and is still holding its response.
	stale, err := loadLedger(key)
	if err != nil {
		t.Fatal(err)
	}

	// Poll B ran a whole cycle meanwhile: version 5, a new entry, cursor 5.
	cur, err := loadLedger(key)
	if err != nil {
		t.Fatal(err)
	}
	cur.Version = 5
	cur.Entries["b"] = LedgerEntry{}
	cur.AdvanceConsumer("c", time.Now())
	if err := saveLedger(key, cur); err != nil {
		t.Fatal(err)
	}

	if err := commitAdvances([]changesBackendResult{{backend: "gh", key: key, ledger: stale}}, "c"); err != nil {
		t.Fatal(err)
	}

	got, err := loadLedger(key)
	if err != nil {
		t.Fatal(err)
	}
	if got.Consumers["c"].Cursor != 5 {
		t.Errorf("cursor = %d, want 5 (never backward)", got.Consumers["c"].Cursor)
	}
	if got.Version != 5 {
		t.Errorf("version = %d, want 5", got.Version)
	}
	if _, ok := got.Entries["b"]; !ok {
		t.Errorf("entry b from the newer poll was lost: %v", got.Entries)
	}
}

// A --reset saved by one poll must survive another poll that started before
// it: writers of one key take turns, so the later one starts from the earlier
// one's saved state instead of overwriting it with its own snapshot.
func TestLedgerWriters_SerializePerKey_ResetNotLost(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	key := lockTestKey()
	ctx := context.Background()

	seed := newEmptyLedger()
	seed.Version = 3
	seed.AdvanceConsumer("c", time.Now())
	if err := saveLedger(key, seed); err != nil {
		t.Fatal(err)
	}

	// Poll A holds the cycle lock (its backend call is "in flight").
	releaseA, err := acquireLedgerCycle(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	path, _ := ledgerPath(key)
	probe := flock.New(ledgerCyclePath(path))
	if locked, _ := probe.TryLock(); locked {
		t.Fatal("cycle lock was free while poll A held it")
	}

	var aReleased atomic.Bool
	var bSawAReleased atomic.Bool
	bDone := make(chan error, 1)
	go func() {
		// Poll B: a --reset.
		bDone <- updateLedger(ctx, key, nil, func(l *Ledger) bool {
			bSawAReleased.Store(aReleased.Load())
			l.ResetConsumer("c")
			return true
		})
	}()

	// Poll A finishes its cycle: saves a newer version, then releases.
	l, err := loadLedger(key)
	if err != nil {
		t.Fatal(err)
	}
	l.Version = 4
	l.Entries["x"] = LedgerEntry{}
	if err := saveLedger(key, l); err != nil {
		t.Fatal(err)
	}
	aReleased.Store(true)
	releaseA()

	if err := <-bDone; err != nil {
		t.Fatal(err)
	}
	if !bSawAReleased.Load() {
		t.Error("poll B ran before poll A released the key's cycle lock")
	}
	got, err := loadLedger(key)
	if err != nil {
		t.Fatal(err)
	}
	if got.Consumers["c"].Cursor != 0 {
		t.Errorf("reset lost: cursor = %d, want 0", got.Consumers["c"].Cursor)
	}
	if got.Version != 4 {
		t.Errorf("version = %d, want 4 (poll A's save kept)", got.Version)
	}
	if _, ok := got.Entries["x"]; !ok {
		t.Errorf("poll A's entry lost: %s", mustJSON(t, got))
	}
}

func TestAcquireLedgerCycle_HonorsContext(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	key := lockTestKey()
	release, err := acquireLedgerCycle(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := acquireLedgerCycle(ctx, key); err == nil {
		t.Fatal("second acquire succeeded while the lock was held")
	}
}

// A cache tombstone written by one poll must survive an access bump or a Put
// that another caller read before the tombstone was written.
func TestUpdateCache_TombstoneSurvivesStaleReader(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	key := CacheKey{Type: "pr", Backend: "gh"}
	now := time.Now()

	seed := newEmptyCache()
	seed.Put("1", json.RawMessage(`{"id":"1"}`), now, now)
	seed.Put("2", json.RawMessage(`{"id":"2"}`), now, now)
	if err := saveCache(key, seed); err != nil {
		t.Fatal(err)
	}

	// A reader loads the cache, then a poll tombstones entity 1.
	if _, err := loadCache(key); err != nil {
		t.Fatal(err)
	}
	if err := updateCache(key, func(c *Cache) bool { c.Remove("1", now); return true }); err != nil {
		t.Fatal(err)
	}
	// The reader's bump goes through updateCache, on the current file.
	if err := updateCache(key, func(c *Cache) bool { c.MarkAccessed("2", now); return true }); err != nil {
		t.Fatal(err)
	}

	got, err := loadCache(key)
	if err != nil {
		t.Fatal(err)
	}
	if got.Entries["1"].RemovedAt == nil {
		t.Errorf("tombstone for entity 1 lost: %+v", got.Entries["1"])
	}
}

// Two cache writers take turns: the second reads what the first wrote.
func TestUpdateCache_WritersSerialize(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	key := CacheKey{Type: "pr", Backend: "gh"}
	now := time.Now()

	var firstDone atomic.Bool
	var secondSawFirst atomic.Bool
	started := make(chan struct{})
	secondDone := make(chan error, 1)
	firstErr := make(chan error, 1)
	go func() {
		firstErr <- updateCache(key, func(c *Cache) bool {
			close(started)
			// While this fn runs, a second writer is released and must wait.
			go func() {
				secondDone <- updateCache(key, func(c2 *Cache) bool {
					secondSawFirst.Store(firstDone.Load())
					c2.Remove("1", now)
					return true
				})
			}()
			c.Put("1", json.RawMessage(`{"id":"1"}`), now, now)
			firstDone.Store(true)
			return true
		})
	}()
	<-started
	if err := <-firstErr; err != nil {
		t.Fatal(err)
	}
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
	if !secondSawFirst.Load() {
		t.Error("second writer ran before the first finished")
	}
	got, err := loadCache(key)
	if err != nil {
		t.Fatal(err)
	}
	if got.Entries["1"].RemovedAt == nil {
		t.Errorf("tombstone lost to a concurrent Put: %+v", got.Entries["1"])
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
