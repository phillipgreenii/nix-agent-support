package store

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/phillipgreenii/ccpool/internal/clock"
)

// TestTransition_concurrentProcessesNeverBusy pins bead pg2-gdpwt. Several
// processes (modelled by several Stores, each with its own connection pool, on
// one WAL database file) flip session states at once: a `ccpool reply` marking
// a session working races the `hook start`/`hook stop` of the sessions beside
// it. A read-then-write transaction begun DEFERRED (SELECT the prior state,
// then UPDATE) fails with SQLITE_BUSY_SNAPSHOT (517, "database is locked")
// whenever another connection commits between its SELECT and its UPDATE, and
// busy_timeout cannot help because retrying cannot make the stale snapshot
// current. Every write transaction must therefore take the write lock up front
// (BEGIN IMMEDIATE), where busy_timeout does apply.
func TestTransition_concurrentProcessesNeverBusy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ccpool.db")
	const (
		processes = 3
		perProc   = 2
		rounds    = 100
	)
	stores := make([]*Store, processes)
	for i := range stores {
		st, err := Open(path, clock.Real{})
		if err != nil {
			t.Fatalf("Open #%d: %v", i, err)
		}
		t.Cleanup(func() { _ = st.Close() })
		stores[i] = st
	}
	ctx := context.Background()
	for p := range stores {
		for k := 0; k < perProc; k++ {
			mustInsert(t, stores[0], fmt.Sprintf("s-%d-%d", p, k), fmt.Sprintf("csid-%d-%d", p, k))
		}
	}

	var wg sync.WaitGroup
	errs := make(chan error, processes*perProc*rounds)
	for p, st := range stores {
		for k := 0; k < perProc; k++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				id := fmt.Sprintf("s-%d-%d", p, k)
				for r := 0; r < rounds; r++ {
					to := Working
					if r%2 == 1 {
						to = Idle
					}
					if _, err := st.Transition(ctx, id, to, "", ""); err != nil {
						errs <- fmt.Errorf("Transition %s round %d: %w", id, r, err)
						return
					}
					if err := st.InsertTurn(ctx, Turn{TurnID: fmt.Sprintf("%s-t%d", id, r), ExternalID: id}); err != nil {
						errs <- fmt.Errorf("InsertTurn %s round %d: %w", id, r, err)
						return
					}
					if _, _, err := st.ResolveOldestPendingTurn(ctx, id, "/t.jsonl"); err != nil {
						errs <- fmt.Errorf("ResolveOldestPendingTurn %s round %d: %w", id, r, err)
						return
					}
				}
			}()
		}
	}
	wg.Wait()
	close(errs)
	n := 0
	for err := range errs {
		if n++; n <= 5 {
			t.Error(err)
		}
	}
	if n > 0 {
		t.Fatalf("%d concurrent writers failed", n)
	}
}
