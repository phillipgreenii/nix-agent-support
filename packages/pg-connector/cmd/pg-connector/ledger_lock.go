package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
)

// ledgerCycleWait bounds how long a writer waits for another writer of the
// same ledger key. A holder is at most one backend call (about 45 s) plus its
// own save, so twice that is generous; past it the waiter reports an error
// rather than hanging.
const ledgerCycleWait = 2 * time.Minute

// The ledger has two locks. The short data lock (lockPath) is taken inside
// loadLedger and saveLedger and protects one read or one write. The cycle lock
// (ledgerCyclePath) is taken by every WRITER of a key for its whole
// load, modify, save cycle, so two polls of one key can no longer both load
// the same snapshot and then overwrite each other's whole file (a backward
// cursor, a lost --reset).
//
// Why a lock across the cycle rather than a version counter checked at save:
// a writer that loses a version check has already paid for its backend call
// and its snapshot is stale in a way that cannot be merged (the list result,
// the evictions and the consumer cursors are all derived from the old
// snapshot), so the only safe answer is to discard it and degrade the poll or
// to repeat the backend call. Holding the lock instead makes the second poll
// wait and then start from the first poll's saved state, so it does one
// cheap incremental refresh. The cost is that a second poll of the SAME key
// waits behind a backend call of up to about 45 s (bounded by ledgerCycleWait
// and the caller's context). Readers are not affected: a --cached poll,
// ledger show and the cache fallback take only the short data lock, never the
// cycle lock. A lock file is per key, so polls of different keys still run
// concurrently.
//
// Lock order is cycle lock then data lock, never the reverse, and the cycle
// lock is not reentrant: code that already holds it MUST call loadLedger and
// saveLedger directly, not updateLedger.
func ledgerCyclePath(path string) string { return path + ".cycle.lock" }

// acquireLedgerCycle takes key's cycle lock, waiting until ctx ends or
// ledgerCycleWait passes, and returns its release func.
func acquireLedgerCycle(ctx context.Context, key LedgerKey) (func(), error) {
	path, err := ledgerPath(key)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("ledger: create ledger dir: %w", err)
	}
	fl := flock.New(ledgerCyclePath(path))
	wctx, cancel := context.WithTimeout(ctx, ledgerCycleWait)
	defer cancel()
	locked, err := fl.TryLockContext(wctx, 10*time.Millisecond)
	if err != nil || !locked {
		if err == nil {
			err = wctx.Err()
		}
		return nil, fmt.Errorf("ledger: wait for writer of %s: %w", path, err)
	}
	return func() { _ = fl.Unlock() }, nil
}

// updateLedger is a short read-modify-write of key's ledger under the cycle
// lock: it loads the CURRENT file (adopting a legacy one), applies fn, and
// saves when fn reports a change. Use it for a write that happens outside a
// polling cycle (the post-flush cursor advance, the forwarded-call stamp).
func updateLedger(ctx context.Context, key LedgerKey, previousNames []string, fn func(*Ledger) bool) error {
	release, err := acquireLedgerCycle(ctx, key)
	if err != nil {
		return err
	}
	defer release()
	l, err := loadLedgerAdopting(key, previousNames)
	if err != nil {
		return err
	}
	if !fn(l) {
		return nil
	}
	return saveLedger(key, l)
}

// advanceConsumerTo moves consumerID's cursor forward to version, never
// backward, and stamps LastSeen. A poll reports changes up to the version it
// saw; a later poll of the same key may already have advanced the cursor
// past it, and moving it back would replay changes the consumer has seen.
func (l *Ledger) advanceConsumerTo(consumerID string, version int64, now time.Time) {
	if l.Consumers == nil {
		l.Consumers = map[string]ConsumerState{}
	}
	cs := l.Consumers[consumerID]
	if version > cs.Cursor {
		cs.Cursor = version
	}
	cs.LastSeen = now
	l.Consumers[consumerID] = cs
}
