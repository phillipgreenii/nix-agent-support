package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var curNow = time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)

// seedChanges appends n records of entityType, one per entity, stamped at.
func seedChanges(t *testing.T, s *Store, entityType string, n int, at time.Time) []ChangeRecord {
	t.Helper()
	for i := 0; i < n; i++ {
		e := Entity{
			Repo: clRepo, EntityType: entityType, EntityID: fmt.Sprintf("%s-%d-%d", entityType, at.Unix(), i),
			Facts: "f", AsOf: formatTime(at), ContentHash: "h",
		}
		if _, err := s.WriteEntityWithLog(e, 0, []string{"created"}, "sync", formatTime(at)); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	recs, err := s.ListChangesAfter(entityType, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	return recs
}

func seqs(recs []ChangeRecord) []int64 {
	var out []int64
	for _, r := range recs {
		out = append(out, r.Seq)
	}
	return out
}

func last(recs []ChangeRecord) int64 { return recs[len(recs)-1].Seq }

func TestCursorAdvancesOnlyAfterFlush(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	seedChanges(t, s, "pr", 3, curNow)

	first, err := s.ReadChanges("c1", "pr", 0, curNow)
	if err != nil || len(first) != 3 {
		t.Fatalf("first read = (%d, %v)", len(first), err)
	}
	// No advance yet: a second read returns the same records.
	again, err := s.ReadChanges("c1", "pr", 0, curNow)
	if err != nil || len(again) != 3 {
		t.Fatalf("read without advance = (%d, %v), want 3 again", len(again), err)
	}
	if err := s.AdvanceCursor("c1", "pr", last(first)); err != nil {
		t.Fatal(err)
	}
	after, err := s.ReadChanges("c1", "pr", 0, curNow)
	if err != nil || len(after) != 0 {
		t.Fatalf("read after advance = (%d, %v), want 0", len(after), err)
	}
}

func TestAdvanceCursorOnlyForward(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	recs := seedChanges(t, s, "pr", 3, curNow)
	if err := s.RegisterConsumer("c1", "pr", curNow); err != nil {
		t.Fatal(err)
	}
	if err := s.AdvanceCursor("c1", "pr", recs[2].Seq); err != nil {
		t.Fatal(err)
	}
	if err := s.AdvanceCursor("c1", "pr", recs[0].Seq); err != nil {
		t.Fatalf("backward advance err = %v, want silent no-op", err)
	}
	cs, _ := s.ListConsumers()
	if len(cs) != 1 || cs[0].Cursor != recs[2].Seq {
		t.Fatalf("cursor = %+v, want %d", cs, recs[2].Seq)
	}
	if err := s.AdvanceCursor("ghost", "pr", 1); !errors.Is(err, ErrConsumerNotFound) {
		t.Fatalf("advance unregistered err = %v, want ErrConsumerNotFound", err)
	}
}

func TestPeekReadsWithoutAdvancingOrRegistering(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	seedChanges(t, s, "pr", 2, curNow)

	got, err := s.PeekChanges("cached", "pr", 0)
	if err != nil || len(got) != 2 {
		t.Fatalf("peek = (%d, %v)", len(got), err)
	}
	if cs, _ := s.ListConsumers(); len(cs) != 0 {
		t.Fatalf("peek registered a consumer: %+v", cs)
	}
	// A registered consumer's peek leaves cursor and seen_at alone.
	if _, err := s.ReadChanges("c1", "pr", 0, curNow); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PeekChanges("c1", "pr", 1); err != nil {
		t.Fatal(err)
	}
	cs, _ := s.ListConsumers()
	if len(cs) != 1 || cs[0].Cursor != 0 || cs[0].SeenAt != formatTime(curNow) {
		t.Fatalf("after peek consumer = %+v", cs)
	}
}

func TestMultiConsumerDeliveryAndLimitPaging(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	all := seedChanges(t, s, "pr", 5, curNow)
	seedChanges(t, s, "issue", 2, curNow)

	// Consumer a pages through 5 records, 2 at a time.
	var got []int64
	for i := 0; i < 5; i++ {
		page, err := s.ReadChanges("a", "pr", 2, curNow)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		got = append(got, seqs(page)...)
		if err := s.AdvanceCursor("a", "pr", last(page)); err != nil {
			t.Fatal(err)
		}
	}
	if fmt.Sprint(got) != fmt.Sprint(seqs(all)) {
		t.Fatalf("paged seqs = %v, want %v", got, seqs(all))
	}
	// Consumer b of the same type is independent and still sees everything.
	b, err := s.ReadChanges("b", "pr", 0, curNow)
	if err != nil || len(b) != 5 {
		t.Fatalf("b read = (%d, %v), want 5", len(b), err)
	}
	// The same name on another type has its own cursor and its own log slice.
	iss, err := s.ReadChanges("a", "issue", 0, curNow)
	if err != nil || len(iss) != 2 {
		t.Fatalf("a/issue read = (%d, %v), want 2", len(iss), err)
	}
}

func TestResetReplaysFromStart(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	recs := seedChanges(t, s, "pr", 3, curNow)
	if _, err := s.ReadChanges("c1", "pr", 0, curNow); err != nil {
		t.Fatal(err)
	}
	if err := s.AdvanceCursor("c1", "pr", last(recs)); err != nil {
		t.Fatal(err)
	}
	if err := s.ResetConsumer("c1", "pr"); err != nil {
		t.Fatal(err)
	}
	replay, err := s.ReadChanges("c1", "pr", 0, curNow)
	if err != nil || fmt.Sprint(seqs(replay)) != fmt.Sprint(seqs(recs)) {
		t.Fatalf("replay = (%v, %v), want %v", seqs(replay), err, seqs(recs))
	}
	if err := s.ResetConsumer("ghost", "pr"); !errors.Is(err, ErrConsumerNotFound) {
		t.Fatalf("reset unregistered err = %v", err)
	}
}

func TestListAndForgetConsumers(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	for _, c := range [][2]string{{"b", "pr"}, {"a", "pr"}, {"a", "issue"}} {
		if err := s.RegisterConsumer(c[0], c[1], curNow); err != nil {
			t.Fatal(err)
		}
	}
	cs, err := s.ListConsumers()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, c := range cs {
		names = append(names, c.Type+"/"+c.Name)
	}
	if strings.Join(names, ",") != "issue/a,pr/a,pr/b" {
		t.Fatalf("list order = %v", names)
	}
	if err := s.ForgetConsumer("a", "pr"); err != nil {
		t.Fatal(err)
	}
	if err := s.ForgetConsumer("a", "pr"); err != nil {
		t.Fatalf("forget absent err = %v, want nil", err)
	}
	cs, _ = s.ListConsumers()
	if len(cs) != 2 {
		t.Fatalf("after forget = %+v", cs)
	}
}

func TestOldSchemaRefusesCursorAPI(t *testing.T) {
	s := OpenForTest(t)
	if _, err := s.ReadChanges("c", "pr", 0, curNow); !errors.Is(err, ErrOldSchema) {
		t.Fatalf("ReadChanges on old schema err = %v", err)
	}
	if _, err := s.PruneChangeLog(curNow, 0, 0); !errors.Is(err, ErrOldSchema) {
		t.Fatalf("PruneChangeLog on old schema err = %v", err)
	}
}

func logSeqs(t *testing.T, s *Store, entityType string) []int64 {
	t.Helper()
	recs, err := s.ListChangesAfter(entityType, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	return seqs(recs)
}

func TestPruneWaitsForSlowestConsumer(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	old := curNow.Add(-30 * 24 * time.Hour)
	recs := seedChanges(t, s, "pr", 4, old)
	fresh := seedChanges(t, s, "pr", 1, curNow) // includes the 4 old ones
	_ = fresh

	// fast has consumed everything; slow only the first old record.
	for _, name := range []string{"fast", "slow"} {
		if _, err := s.ReadChanges(name, "pr", 0, curNow); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.AdvanceCursor("fast", "pr", recs[len(recs)-1].Seq+1); err != nil {
		t.Fatal(err)
	}
	if err := s.AdvanceCursor("slow", "pr", recs[0].Seq); err != nil {
		t.Fatal(err)
	}

	n, err := s.PruneChangeLog(curNow, 14*24*time.Hour, 7*24*time.Hour)
	if err != nil || n != 1 {
		t.Fatalf("prune = (%d, %v), want 1 (only what slow passed)", n, err)
	}
	// The three old rows slow has not consumed, and the fresh row, remain.
	if got := len(logSeqs(t, s, "pr")); got != 4 {
		t.Fatalf("remaining = %d, want 4", got)
	}
	// Once slow catches up, the remaining old rows go; the fresh one stays
	// because it is younger than retention.
	if err := s.AdvanceCursor("slow", "pr", recs[len(recs)-1].Seq+1); err != nil {
		t.Fatal(err)
	}
	if n, err := s.PruneChangeLog(curNow, 0, 0); err != nil || n != 3 {
		t.Fatalf("second prune = (%d, %v), want 3", n, err)
	}
	if got := len(logSeqs(t, s, "pr")); got != 1 {
		t.Fatalf("remaining = %d, want the 1 fresh row", got)
	}
}

func TestStaleConsumerExcludedFromPruneHorizon(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	old := curNow.Add(-30 * 24 * time.Hour)
	seedChanges(t, s, "pr", 3, old)

	// "stuck" never advanced and was last seen 10 days ago (> 7d stale).
	if err := s.RegisterConsumer("stuck", "pr", curNow.Add(-10*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if n, err := s.PruneChangeLog(curNow, 0, 0); err != nil || n != 3 {
		t.Fatalf("prune with stale consumer = (%d, %v), want 3", n, err)
	}

	// A NULL seen_at counts as stale as well.
	seedChanges(t, s, "pr", 2, old.Add(time.Hour))
	if _, err := s.sql.Exec(`INSERT INTO consumer (name, type, cursor, seen_at) VALUES ('nullseen', 'pr', 0, NULL)`); err != nil {
		t.Fatal(err)
	}
	if n, err := s.PruneChangeLog(curNow, 0, 0); err != nil || n != 2 {
		t.Fatalf("prune with NULL seen_at = (%d, %v), want 2", n, err)
	}

	// A consumer seen 1 day ago is NOT stale and holds rows back.
	seedChanges(t, s, "pr", 2, old.Add(2*time.Hour))
	if err := s.RegisterConsumer("live", "pr", curNow.Add(-24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if n, err := s.PruneChangeLog(curNow, 0, 0); err != nil || n != 0 {
		t.Fatalf("prune with live consumer at cursor 0 = (%d, %v), want 0", n, err)
	}
}

func TestPruneHorizonIsPerType(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	old := curNow.Add(-30 * 24 * time.Hour)
	seedChanges(t, s, "pr", 2, old)
	seedChanges(t, s, "issue", 2, old)
	// A live consumer of "pr" at cursor 0 must not hold back "issue" rows.
	if err := s.RegisterConsumer("prc", "pr", curNow); err != nil {
		t.Fatal(err)
	}
	n, err := s.PruneChangeLog(curNow, 0, 0)
	if err != nil || n != 2 {
		t.Fatalf("prune = (%d, %v), want 2 (the issue rows)", n, err)
	}
	if len(logSeqs(t, s, "pr")) != 2 || len(logSeqs(t, s, "issue")) != 0 {
		t.Fatalf("pr=%v issue=%v", logSeqs(t, s, "pr"), logSeqs(t, s, "issue"))
	}
}

func TestPruneKeepsRowsWithinRetention(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	seedChanges(t, s, "pr", 2, curNow.Add(-24*time.Hour))
	if n, err := s.PruneChangeLog(curNow, 0, 0); err != nil || n != 0 {
		t.Fatalf("prune = (%d, %v), want 0 within retention", n, err)
	}
	if n, err := s.PruneChangeLog(curNow, time.Hour, 0); err != nil || n != 2 {
		t.Fatalf("prune with 1h retention = (%d, %v), want 2", n, err)
	}
}

func TestConcurrentChangesSameConsumerSerialize(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	seedChanges(t, s, "pr", 4, curNow)
	s.SetConsumerLockerOptions(LockerOptions{LockDir: t.TempDir(), Timeout: 10 * time.Second})

	var mu sync.Mutex
	var delivered [][]int64
	var inCritical atomic.Int32
	var overlapped atomic.Bool
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			unlock, err := s.LockConsumer("pr", "c1")
			if err != nil {
				t.Errorf("LockConsumer: %v", err)
				return
			}
			defer unlock()
			if inCritical.Add(1) > 1 {
				overlapped.Store(true)
			}
			recs, err := s.ReadChanges("c1", "pr", 2, curNow)
			if err != nil {
				t.Errorf("read: %v", err)
			}
			time.Sleep(100 * time.Millisecond) // "flush"
			if len(recs) > 0 {
				if err := s.AdvanceCursor("c1", "pr", last(recs)); err != nil {
					t.Errorf("advance: %v", err)
				}
			}
			mu.Lock()
			delivered = append(delivered, seqs(recs))
			mu.Unlock()
			inCritical.Add(-1)
		}()
	}
	wg.Wait()
	if overlapped.Load() {
		t.Fatal("two holders of the same (type, consumer) lock overlapped")
	}
	seen := map[int64]bool{}
	for _, d := range delivered {
		if len(d) != 2 {
			t.Fatalf("delivered = %v, want two pages of 2", delivered)
		}
		for _, q := range d {
			if seen[q] {
				t.Fatalf("seq %d delivered twice: %v", q, delivered)
			}
			seen[q] = true
		}
	}
	if len(seen) != 4 {
		t.Fatalf("delivered = %v, want all 4 seqs", delivered)
	}
}

func TestLockConsumerDifferentKeysDoNotContend(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	s.SetConsumerLockerOptions(LockerOptions{LockDir: t.TempDir(), Timeout: 300 * time.Millisecond})
	u1, err := s.LockConsumer("pr", "a")
	if err != nil {
		t.Fatal(err)
	}
	defer u1()
	for _, k := range [][2]string{{"pr", "b"}, {"issue", "a"}} {
		u, err := s.LockConsumer(k[0], k[1])
		if err != nil {
			t.Fatalf("Lock(%v) while another key held: %v", k, err)
		}
		u()
	}
	if _, err := s.LockConsumer("pr", "a"); !errors.Is(err, ErrLockTimeout) {
		t.Fatalf("same key err = %v, want ErrLockTimeout", err)
	}
}

// --- fault-injecting driver -------------------------------------------------

var errInjected = errors.New("injected fault")

// faultState decides which statements fail. A statement fails when failOn is
// non-empty and the statement text contains it.
type faultState struct {
	mu     sync.Mutex
	failOn string
}

func (f *faultState) set(s string) { f.mu.Lock(); f.failOn = s; f.mu.Unlock() }
func (f *faultState) check(q string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failOn != "" && strings.Contains(q, f.failOn) {
		return errInjected
	}
	return nil
}

type faultDriver struct {
	inner driver.Driver
	state *faultState
}

func (d faultDriver) Open(name string) (driver.Conn, error) {
	c, err := d.inner.Open(name)
	if err != nil {
		return nil, err
	}
	return &faultConn{Conn: c, state: d.state}, nil
}

type faultConn struct {
	driver.Conn
	state *faultState
}

func (c *faultConn) Prepare(q string) (driver.Stmt, error) {
	if err := c.state.check(q); err != nil {
		return nil, err
	}
	return c.Conn.Prepare(q)
}

func (c *faultConn) PrepareContext(ctx context.Context, q string) (driver.Stmt, error) {
	if err := c.state.check(q); err != nil {
		return nil, err
	}
	if p, ok := c.Conn.(driver.ConnPrepareContext); ok {
		return p.PrepareContext(ctx, q)
	}
	return c.Conn.Prepare(q)
}

func (c *faultConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	if err := c.state.check(q); err != nil {
		return nil, err
	}
	if e, ok := c.Conn.(driver.ExecerContext); ok {
		return e.ExecContext(ctx, q, args)
	}
	return nil, driver.ErrSkip
}

func (c *faultConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if err := c.state.check(q); err != nil {
		return nil, err
	}
	if e, ok := c.Conn.(driver.QueryerContext); ok {
		return e.QueryContext(ctx, q, args)
	}
	return nil, driver.ErrSkip
}

func (c *faultConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if b, ok := c.Conn.(driver.ConnBeginTx); ok {
		return b.BeginTx(ctx, opts)
	}
	return c.Conn.Begin() //nolint:staticcheck // fallback for drivers without BeginTx
}

var faultDriverSeq atomic.Int32

// openFaulty reopens s's database file through the fault-injecting driver.
func openFaulty(t *testing.T, s *Store) (*Store, *faultState) {
	t.Helper()
	probe, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	inner := probe.Driver()
	_ = probe.Close()

	state := &faultState{}
	name := fmt.Sprintf("faultsqlite-%d", faultDriverSeq.Add(1))
	sql.Register(name, faultDriver{inner: inner, state: state})
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(%d)", s.path, busyTimeoutMillis)
	db, err := sql.Open(name, dsn)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return &Store{sql: db, path: s.path}, state
}

func TestCrashBetweenFlushAndAdvanceYieldsDuplicateNotLoss(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	recs := seedChanges(t, s, "pr", 3, curNow)

	fs, fault := openFaulty(t, s)

	// Run 1: read, flush (output recorded), then the advance fails mid-sequence.
	var output []int64
	got, err := fs.ReadChanges("c1", "pr", 0, curNow)
	if err != nil {
		t.Fatal(err)
	}
	output = append(output, seqs(got)...) // the flush
	fault.set("UPDATE consumer")
	if err := fs.AdvanceCursor("c1", "pr", last(got)); !errors.Is(err, errInjected) {
		t.Fatalf("advance err = %v, want injected fault", err)
	}
	fault.set("")

	// Run 2 (the "restart"): a fresh read re-delivers the same records.
	got2, err := fs.ReadChanges("c1", "pr", 0, curNow)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(seqs(got2)) != fmt.Sprint(seqs(recs)) {
		t.Fatalf("re-read = %v, want duplicate delivery of %v", seqs(got2), seqs(recs))
	}
	output = append(output, seqs(got2)...)
	if err := fs.AdvanceCursor("c1", "pr", last(got2)); err != nil {
		t.Fatal(err)
	}

	// Nothing lost: every seq appears; duplicates are expected.
	count := map[int64]int{}
	for _, q := range output {
		count[q]++
	}
	for _, r := range recs {
		if count[r.Seq] < 1 {
			t.Fatalf("seq %d lost; output = %v", r.Seq, output)
		}
	}
	if count[recs[0].Seq] != 2 {
		t.Fatalf("expected a duplicate of seq %d; output = %v", recs[0].Seq, output)
	}
	if rest, _ := fs.ReadChanges("c1", "pr", 0, curNow); len(rest) != 0 {
		t.Fatalf("after confirmed advance still got %v", seqs(rest))
	}
}
