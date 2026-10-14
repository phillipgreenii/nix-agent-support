package store

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func sampleGroup(gen int64) GroupRow {
	return GroupRow{
		Kind: "pr", ID: "PR_1", Group: "detail", Content: []byte(`{"x":1}`), Fingerprint: "fp",
		FetchedAt: time.UnixMilli(1760000000000), Generation: gen,
	}
}

func TestInTxCommitsOnNil(t *testing.T) {
	ctx := context.Background()
	s, _ := mustOpen(t, dbPath(t), fastOpts())
	err := s.InTx(ctx, func(tx *Tx) error {
		if err := tx.PutEntity(ctx, EntityRow{Kind: "pr", ID: "PR_1", Version: 4}); err != nil {
			return err
		}
		// A read inside the transaction sees the transaction's own write.
		e, ok, err := tx.GetEntity(ctx, "pr", "PR_1")
		if err != nil || !ok || e.Version != 4 {
			t.Errorf("in-tx read = %+v ok=%v err=%v", e, ok, err)
		}
		return tx.PutGroup(ctx, sampleGroup(1))
	})
	if err != nil {
		t.Fatal(err)
	}
	if e, ok, err := s.GetEntity(ctx, "pr", "PR_1"); err != nil || !ok || e.Version != 4 {
		t.Errorf("entity after commit = %+v ok=%v err=%v", e, ok, err)
	}
	if g, ok, err := s.GetGroup(ctx, "pr", "PR_1", "detail"); err != nil || !ok || g.Generation != 1 {
		t.Errorf("group after commit = %+v ok=%v err=%v", g, ok, err)
	}
}

func TestInTxRollsBackOnErrorAndReturnsItUnwrapped(t *testing.T) {
	ctx := context.Background()
	s, _ := mustOpen(t, dbPath(t), fastOpts())
	boom := errors.New("boom")
	err := s.InTx(ctx, func(tx *Tx) error {
		if err := tx.PutEntity(ctx, EntityRow{Kind: "pr", ID: "PR_1", Version: 4}); err != nil {
			return err
		}
		if _, err := tx.AppendChange(ctx, ChangeRow{Kind: "pr", ID: "org/repo#1", At: time.UnixMilli(1)}); err != nil {
			return err
		}
		return boom
	})
	if err != boom { //nolint:errorlint // the contract is that fn's error comes back as it is
		t.Fatalf("err = %v, want the callback's own error", err)
	}
	if _, ok, _ := s.GetEntity(ctx, "pr", "PR_1"); ok {
		t.Error("the entity written before the error survived the rollback")
	}
	if head, err := s.ChangeHead(ctx); err != nil || head != 0 {
		t.Errorf("head = %d err=%v, want the rolled-back append to consume no number", head, err)
	}
}

func TestInTxRollsBackOnPanicAndThePanicPropagates(t *testing.T) {
	ctx := context.Background()
	s, _ := mustOpen(t, dbPath(t), fastOpts())
	func() {
		defer func() {
			if r := recover(); r != "kaboom" {
				t.Errorf("recovered %v, want the original panic value", r)
			}
		}()
		_ = s.InTx(ctx, func(tx *Tx) error {
			if err := tx.PutEntity(ctx, EntityRow{Kind: "pr", ID: "PR_1"}); err != nil {
				t.Error(err)
			}
			panic("kaboom")
		})
		t.Error("InTx returned after a panic in its callback")
	}()
	if _, ok, _ := s.GetEntity(ctx, "pr", "PR_1"); ok {
		t.Error("the entity written before the panic survived")
	}
	// The pooled connection was released: the store still works.
	if err := s.PutEntity(ctx, EntityRow{Kind: "pr", ID: "PR_2"}); err != nil {
		t.Fatalf("store unusable after a panicking transaction: %v", err)
	}
}

func TestTwoInTxCallsSerialiseWithoutDeadlock(t *testing.T) {
	ctx := context.Background()
	s, _ := mustOpen(t, dbPath(t), fastOpts())
	if err := s.PutEntity(ctx, EntityRow{Kind: "pr", ID: "PR_1"}); err != nil {
		t.Fatal(err)
	}
	const workers, bumps = 2, 20
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		inside  int
		overlap bool
	)
	done := make(chan struct{})
	go func() {
		for range workers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for range bumps {
					err := s.InTx(ctx, func(tx *Tx) error {
						mu.Lock()
						inside++
						if inside > 1 {
							overlap = true
						}
						mu.Unlock()
						_, err := tx.BumpEntityVersion(ctx, "pr", "PR_1")
						mu.Lock()
						inside--
						mu.Unlock()
						return err
					})
					if err != nil {
						t.Error(err)
						return
					}
				}
			}()
		}
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("two goroutines in InTx deadlocked")
	}
	if overlap {
		t.Error("two transactions were open at once")
	}
	if e, _, _ := s.GetEntity(ctx, "pr", "PR_1"); e.Version != workers*bumps {
		t.Errorf("version = %d, want %d (every bump applied once)", e.Version, workers*bumps)
	}
}

func TestPutGroupIfThenCallsThenOnlyWhenStored(t *testing.T) {
	ctx := context.Background()
	s, _ := mustOpen(t, dbPath(t), fastOpts())

	calls := 0
	then := func(tx *Tx) error {
		calls++
		_, err := tx.AppendChange(ctx, ChangeRow{Kind: "pr", ID: "org/repo#1", At: time.UnixMilli(1)})
		return err
	}
	stored, err := s.PutGroupIfThen(ctx, sampleGroup(1), 0, then)
	if err != nil || !stored || calls != 1 {
		t.Fatalf("first store: stored=%v err=%v calls=%d", stored, err, calls)
	}
	if head, _ := s.ChangeHead(ctx); head != 1 {
		t.Errorf("head = %d, want the change appended in the same transaction", head)
	}

	// A stale expectation: the generation check fails, then is not called, and
	// nothing is written.
	stale := sampleGroup(9)
	stale.Content = []byte(`{"stale":true}`)
	stored, err = s.PutGroupIfThen(ctx, stale, 0, then)
	if err != nil || stored || calls != 1 {
		t.Errorf("stale store: stored=%v err=%v calls=%d, want not stored and no call", stored, err, calls)
	}
	if g, _, _ := s.GetGroup(ctx, "pr", "PR_1", "detail"); string(g.Content) != `{"x":1}` || g.Generation != 1 {
		t.Errorf("group after a stale store = %+v", g)
	}
	if head, _ := s.ChangeHead(ctx); head != 1 {
		t.Errorf("head = %d after a stale store, want 1", head)
	}
}

func TestPutGroupIfThenRollsBackTheGroupWhenThenFails(t *testing.T) {
	ctx := context.Background()
	s, _ := mustOpen(t, dbPath(t), fastOpts())
	boom := errors.New("then failed")
	stored, err := s.PutGroupIfThen(ctx, sampleGroup(1), 0, func(tx *Tx) error {
		if _, err := tx.AppendChange(ctx, ChangeRow{Kind: "pr", ID: "org/repo#1", At: time.UnixMilli(1)}); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) || stored {
		t.Fatalf("stored=%v err=%v, want not stored and the callback's error", stored, err)
	}
	if _, ok, _ := s.GetGroup(ctx, "pr", "PR_1", "detail"); ok {
		t.Error("the group was stored although then failed")
	}
	if head, _ := s.ChangeHead(ctx); head != 0 {
		t.Errorf("head = %d, want the change row rolled back with the group", head)
	}
}

func TestTxPutGroupIfIsTheSameCompareAndSwap(t *testing.T) {
	ctx := context.Background()
	s, _ := mustOpen(t, dbPath(t), fastOpts())
	err := s.InTx(ctx, func(tx *Tx) error {
		if ok, err := tx.PutGroupIf(ctx, sampleGroup(1), 0); err != nil || !ok {
			t.Errorf("insert at 0: ok=%v err=%v", ok, err)
		}
		if ok, err := tx.PutGroupIf(ctx, sampleGroup(5), 0); err != nil || ok {
			t.Errorf("stale expectation: ok=%v err=%v, want not stored", ok, err)
		}
		if ok, err := tx.PutGroupIf(ctx, sampleGroup(2), 1); err != nil || !ok {
			t.Errorf("matching expectation: ok=%v err=%v", ok, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if g, _, _ := s.GetGroup(ctx, "pr", "PR_1", "detail"); g.Generation != 2 {
		t.Errorf("generation = %d, want 2", g.Generation)
	}
}

func TestTxQueryAccessorsAndBaselineIsNeverCleared(t *testing.T) {
	ctx := context.Background()
	s, _ := mustOpen(t, dbPath(t), fastOpts())
	base := time.UnixMilli(1760000000000).UTC()

	if err := s.InTx(ctx, func(tx *Tx) error {
		if err := tx.PutQuery(ctx, QueryRow{Key: "q1", Configured: true, IDs: []string{"A"}, BaselineAt: base}); err != nil {
			return err
		}
		return tx.PutQuery(ctx, QueryRow{Key: "q2"})
	}); err != nil {
		t.Fatal(err)
	}

	// A PutQuery with a zero BaselineAt keeps the stored baseline, through the
	// Store and through a Tx, and still replaces every other column.
	if err := s.PutQuery(ctx, QueryRow{Key: "q1", Configured: true, IDs: []string{"A", "B"}}); err != nil {
		t.Fatal(err)
	}
	err := s.InTx(ctx, func(tx *Tx) error {
		q, ok, err := tx.GetQuery(ctx, "q1")
		if err != nil || !ok || !q.BaselineAt.Equal(base) || len(q.IDs) != 2 {
			t.Errorf("q1 = %+v ok=%v err=%v, want ids replaced and baseline kept", q, ok, err)
		}
		if err := tx.PutQuery(ctx, QueryRow{Key: "q1", Configured: true}); err != nil {
			return err
		}
		all, err := tx.ListQueries(ctx, false)
		if err != nil || len(all) != 2 || all[0].Key != "q1" {
			t.Errorf("ListQueries = %+v err=%v", all, err)
		}
		cfg, err := tx.ListQueries(ctx, true)
		if err != nil || len(cfg) != 1 || cfg[0].Key != "q1" {
			t.Errorf("ListQueries(configured) = %+v err=%v", cfg, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if q, _, _ := s.GetQuery(ctx, "q1"); !q.BaselineAt.Equal(base) {
		t.Errorf("baseline = %v after zero-baseline puts, want %v", q.BaselineAt, base)
	}
	// A non-zero BaselineAt replaces it.
	later := base.Add(time.Hour)
	if err := s.PutQuery(ctx, QueryRow{Key: "q1", BaselineAt: later}); err != nil {
		t.Fatal(err)
	}
	if q, _, _ := s.GetQuery(ctx, "q1"); !q.BaselineAt.Equal(later) {
		t.Errorf("baseline = %v, want %v", q.BaselineAt, later)
	}
	if q, _, _ := s.GetQuery(ctx, "q2"); !q.BaselineAt.IsZero() {
		t.Errorf("a query never given a baseline reads %v, want the zero time", q.BaselineAt)
	}
}

func TestBumpEntityVersionChangesOnlyVersion(t *testing.T) {
	ctx := context.Background()
	s, _ := mustOpen(t, dbPath(t), fastOpts())
	want := EntityRow{
		Kind: "pr", ID: "PR_1", Version: 7, LastAccess: time.UnixMilli(1760000000000).UTC(),
		KeepAlive: true, LastKnownMergeable: "MERGEABLE", TombstonedAt: time.UnixMilli(1760000009000).UTC(),
	}
	if err := s.PutEntity(ctx, want); err != nil {
		t.Fatal(err)
	}
	if err := s.InTx(ctx, func(tx *Tx) error {
		v, err := tx.BumpEntityVersion(ctx, "pr", "PR_1")
		if err != nil || v != 8 {
			t.Errorf("bump = %d err=%v, want 8", v, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	got, _, _ := s.GetEntity(ctx, "pr", "PR_1")
	want.Version = 8
	if got != want {
		t.Errorf("entity = %+v, want only the version changed: %+v", got, want)
	}
}

func TestBumpEntityVersionOfAMissingEntityIsAnError(t *testing.T) {
	ctx := context.Background()
	s, _ := mustOpen(t, dbPath(t), fastOpts())
	err := s.InTx(ctx, func(tx *Tx) error {
		_, err := tx.BumpEntityVersion(ctx, "pr", "PR_missing")
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "no such entity") {
		t.Fatalf("err = %v, want a no-such-entity error", err)
	}
	if _, ok, _ := s.GetEntity(ctx, "pr", "PR_missing"); ok {
		t.Error("a bump created the row")
	}
}

func TestTxEnsureEntityCreatesDefaultsAndLeavesExistingRowsAlone(t *testing.T) {
	ctx := context.Background()
	s, _ := mustOpen(t, dbPath(t), fastOpts())
	existing := EntityRow{
		Kind: "pr", ID: "PR_2", Version: 5, LastAccess: time.UnixMilli(1760000000000).UTC(), KeepAlive: true,
		LastKnownMergeable: "CONFLICTING", TombstonedAt: time.UnixMilli(1760000001000).UTC(),
	}
	if err := s.PutEntity(ctx, existing); err != nil {
		t.Fatal(err)
	}
	if err := s.InTx(ctx, func(tx *Tx) error {
		if err := tx.EnsureEntity(ctx, "pr", "PR_1"); err != nil {
			return err
		}
		return tx.EnsureEntity(ctx, "pr", "PR_2")
	}); err != nil {
		t.Fatal(err)
	}
	created, ok, err := s.GetEntity(ctx, "pr", "PR_1")
	if err != nil || !ok {
		t.Fatalf("created = %+v ok=%v err=%v", created, ok, err)
	}
	if created.Version != 0 || !created.LastAccess.IsZero() || created.KeepAlive || created.LastKnownMergeable != "" ||
		!created.TombstonedAt.IsZero() {
		t.Errorf("created row = %+v, want version 0, no last access, keep_alive 0, no tombstone", created)
	}
	if got, _, _ := s.GetEntity(ctx, "pr", "PR_2"); got != existing {
		t.Errorf("existing row = %+v, want it untouched: %+v", got, existing)
	}
	// The first bump of a row EnsureEntity created returns 1.
	if err := s.InTx(ctx, func(tx *Tx) error {
		v, err := tx.BumpEntityVersion(ctx, "pr", "PR_1")
		if v != 1 {
			t.Errorf("first bump = %d, want 1", v)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSetKeepAliveChangesOnlyKeepAlive(t *testing.T) {
	ctx := context.Background()
	s, _ := mustOpen(t, dbPath(t), fastOpts())
	want := EntityRow{
		Kind: "pr", ID: "PR_1", Version: 7, LastAccess: time.UnixMilli(1760000000000).UTC(),
		KeepAlive: true, LastKnownMergeable: "MERGEABLE", TombstonedAt: time.UnixMilli(1760000009000).UTC(),
	}
	if err := s.PutEntity(ctx, want); err != nil {
		t.Fatal(err)
	}
	if err := s.SetKeepAlive(ctx, "pr", "PR_1", false); err != nil {
		t.Fatal(err)
	}
	got, _, _ := s.GetEntity(ctx, "pr", "PR_1")
	want.KeepAlive = false
	if got != want {
		t.Errorf("entity = %+v, want only keep_alive changed: %+v", got, want)
	}
	if err := s.SetKeepAlive(ctx, "pr", "PR_1", true); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := s.GetEntity(ctx, "pr", "PR_1"); !got.KeepAlive || got.Version != 7 {
		t.Errorf("entity = %+v, want keep_alive back on and version kept", got)
	}
	// An absent row stays absent.
	if err := s.SetKeepAlive(ctx, "pr", "PR_none", true); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.GetEntity(ctx, "pr", "PR_none"); ok {
		t.Error("SetKeepAlive created a row")
	}
}
