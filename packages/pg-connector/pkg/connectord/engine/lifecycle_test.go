package engine

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// queued is one QueueGroup call a recordingQueue saw.
type queued struct {
	ID, Group string
	Class     governor.Class
	Due       time.Time
}

// recordingQueue is the Queuer the tests install.
type recordingQueue struct {
	mu   sync.Mutex
	got  []queued
	fail map[string]error // by group
}

func (q *recordingQueue) QueueGroup(_ context.Context, id, group string, class governor.Class, due time.Time) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if err := q.fail[group]; err != nil {
		return err
	}
	q.got = append(q.got, queued{id, group, class, due})
	return nil
}

func (q *recordingQueue) calls() []queued {
	q.mu.Lock()
	defer q.mu.Unlock()
	return slices.Clone(q.got)
}

// lrig is a rig with a lifecycle and a recording queue over a store file that
// can be closed and reopened.
type lrig struct {
	*rig
	path string
	q    *recordingQueue
	lc   *Lifecycle
	logs []string
	mu   sync.Mutex
}

func newLifeRig(t *testing.T) *lrig {
	t.Helper()
	l := &lrig{rig: &rig{t: t, clk: governor.NewManualClock(t0), purpose: governor.PurposeRefresh}, path: filepath.Join(t.TempDir(), "store.db")}
	l.open()
	return l
}

// open opens the store file and builds a fresh engine and lifecycle over it, the
// way a restarted process would.
func (l *lrig) open() {
	l.t.Helper()
	st, _, err := store.Open(l.path, store.Options{Synchronous: "OFF"})
	if err != nil {
		l.t.Fatal(err)
	}
	l.t.Cleanup(func() { _ = st.Close() })
	l.st, l.e, l.obs = st, newEngine(l.t, l.clk), &recorder{}
	l.e.SetObserver(l.obs)
	l.q = &recordingQueue{}
	l.lc = &Lifecycle{
		Engine: l.e, Store: st, Queue: l.q, Clock: l.clk,
		Log: func(event string, kv ...any) {
			l.mu.Lock()
			defer l.mu.Unlock()
			l.logs = append(l.logs, event)
		},
	}
}

// restart simulates a process restart: the store is closed and reopened.
func (l *lrig) restart() {
	l.t.Helper()
	l.must(l.st.Close())
	l.open()
}

// gammaPending seeds the entity with a gamma group, changes its count and so
// leaves one pending row awaiting gamma.
func (l *lrig) gammaPending() {
	l.t.Helper()
	l.seed(idOne, sample())
	l.seedGroup(idOne, groupGamma, "g1", "", "")
	l.clk.Advance(time.Minute)
	l.must(l.changePrimary(idOne, func(w *widget) { w.Count = 1; w.Open = false }))
	if got := l.awaitedGroups(); !slices.Equal(got, []string{groupGamma}) {
		l.t.Fatalf("awaited = %v, want [gamma]", got)
	}
}

// otherPending seeds a second entity and a pending row for it awaiting the given
// groups, with the given deadline.
func (l *lrig) otherPending(deadline time.Time, awaited ...string) {
	l.t.Helper()
	w := sample()
	w.Ref = "Example-Org/Thing#2"
	l.seed(idTwo, w)
	var aw []Awaiting
	for _, g := range awaited {
		aw = append(aw, Awaiting{Group: g, Since: l.clk.Now()})
	}
	fields, err := EncodeFields([]Field{{Name: "title", Before: []byte(`"a"`), After: []byte(`"b"`)}})
	l.must(err)
	awaitedRaw, err := encodeAwaited(aw)
	l.must(err)
	p := store.PendingChange{
		Kind: kindWidget, ID: idTwo, Kinds: []string{changeRetitled}, Fields: fields, Awaited: awaitedRaw,
		Origin: OriginRefresh, OriginUpdatedAt: t0, DetectedAt: l.clk.Now(), Deadline: deadline,
	}
	p.SetStamps("s1", "t1")
	l.must(l.inTx(func(tx *store.Tx) error { return tx.PutPending(context.Background(), p) }))
}

func (l *lrig) pendingIDs() []string {
	l.t.Helper()
	ps, err := l.st.ListPending(context.Background())
	l.must(err)
	var ids []string
	for _, p := range ps {
		ids = append(ids, p.ID)
	}
	return ids
}

func (l *lrig) setLastError(id, group, msg string) {
	l.t.Helper()
	l.must(l.inTx(func(tx *store.Tx) error {
		row, ok, err := tx.GetGroup(context.Background(), kindWidget, id, group)
		if err != nil || !ok {
			l.t.Fatalf("group %s: ok=%v err=%v", group, ok, err)
		}
		row.LastError = msg
		return tx.PutGroup(context.Background(), row)
	}))
}

func (l *lrig) stats() Stats {
	l.t.Helper()
	s, err := l.lc.Stats(context.Background())
	l.must(err)
	return s
}

func (l *lrig) sweep() int {
	l.t.Helper()
	n, err := l.lc.Sweep(context.Background())
	l.must(err)
	return n
}

func TestSweepAppendsAPendingRowAtItsDeadlineWithOneVersionBump(t *testing.T) {
	l := newLifeRig(t)
	l.gammaPending()

	l.clk.Advance(29 * time.Second)
	if n := l.sweep(); n != 0 || len(l.changes()) != 0 {
		t.Fatalf("sweep before the deadline completed %d rows, appended %d", n, len(l.changes()))
	}
	// Exactly at the deadline: the deadline is "not after" now, so it ends.
	l.clk.Advance(time.Second)
	if n := l.sweep(); n != 1 {
		t.Fatalf("sweep at the deadline completed %d rows, want 1", n)
	}
	rows := l.changes()
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1: %+v", len(rows), rows)
	}
	wantKinds(t, rows[0].Kinds, changeToggled)
	if rows[0].Version != 6 || rows[0].Origin != OriginRefresh || !rows[0].At.Equal(l.clk.Now()) {
		t.Errorf("row = %+v", rows[0])
	}
	if _, ok := l.pending(); ok {
		t.Error("the pending row survived")
	}
	if !slices.Equal(l.obs.settled, []string{ResultTimeout}) {
		t.Errorf("settled = %v, want [timeout]", l.obs.settled)
	}
	if n := l.sweep(); n != 0 || len(l.changes()) != 1 {
		t.Errorf("second sweep completed %d, rows %d", n, len(l.changes()))
	}
	if got := l.q.calls(); len(got) != 0 {
		t.Errorf("sweep queued groups: %+v", got)
	}
}

func TestADependentWhoseStoredRowCarriesALastErrorIsCountedFailed(t *testing.T) {
	l := newLifeRig(t)
	l.gammaPending()
	l.setLastError(idOne, groupGamma, "boom")
	l.clk.Advance(30 * time.Second)
	if n := l.sweep(); n != 1 || len(l.changes()) != 1 {
		t.Fatalf("completed %d, rows %d; a failed settle must still append", n, len(l.changes()))
	}
	if !slices.Equal(l.obs.settled, []string{ResultFailed}) {
		t.Errorf("settled = %v, want [failed]", l.obs.settled)
	}
	if s := l.stats(); s.Failures != 1 || s.Timeouts != 0 {
		t.Errorf("stats = %+v, want one failure", s)
	}
}

func TestFlushAllEndsEveryPendingRowBeforeItsDeadlineAndLeavesNone(t *testing.T) {
	l := newLifeRig(t)
	l.gammaPending()
	l.otherPending(l.clk.Now().Add(20*time.Second), groupAlpha, groupGamma)
	if got := l.pendingIDs(); len(got) != 2 {
		t.Fatalf("pending = %v, want 2", got)
	}
	n, err := l.lc.FlushAll(context.Background())
	if err != nil || n != 2 {
		t.Fatalf("FlushAll = %d, %v; want 2, nil", n, err)
	}
	if got := l.pendingIDs(); len(got) != 0 {
		t.Errorf("pending after FlushAll = %v, want none", got)
	}
	rows := l.changes()
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2: %+v", len(rows), rows)
	}
	if !slices.Equal(l.obs.settled, []string{ResultTimeout, ResultTimeout}) {
		t.Errorf("settled = %v, want two timeouts", l.obs.settled)
	}
	if n, err := l.lc.FlushAll(context.Background()); err != nil || n != 0 {
		t.Errorf("second FlushAll = %d, %v; want 0, nil", n, err)
	}
}

func TestAfterARestartRecoverEndsExpiredRowsAndQueuesTheAwaitedGroupsOfTheOthers(t *testing.T) {
	l := newLifeRig(t)
	l.gammaPending() // deadline detection + 30 s
	l.otherPending(l.clk.Now().Add(time.Minute), groupAlpha, groupGamma)
	l.clk.Advance(45 * time.Second) // the first expired, the second not

	l.restart()
	if got := l.pendingIDs(); !slices.Equal(got, []string{idOne, idTwo}) {
		t.Fatalf("the pending rows did not survive the restart: %v", got)
	}
	queuedN, completed, err := l.lc.Recover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if queuedN != 2 || completed != 1 {
		t.Errorf("Recover = queued %d, completed %d; want 2, 1", queuedN, completed)
	}
	want := []queued{
		{idTwo, groupAlpha, governor.ClassInvalidated, l.clk.Now()},
		{idTwo, groupGamma, governor.ClassInvalidated, l.clk.Now()},
	}
	if got := l.q.calls(); !slices.Equal(got, want) {
		t.Errorf("queued = %+v, want %+v", got, want)
	}
	rows := l.changes()
	if len(rows) != 1 || rows[0].ID != refOne {
		t.Errorf("rows = %+v, want one for %s", rows, refOne)
	}
	if got := l.pendingIDs(); !slices.Equal(got, []string{idTwo}) {
		t.Errorf("pending = %v, want [%s]", got, idTwo)
	}
	if s := l.stats(); s.Pending != 1 || s.Timeouts != 1 {
		t.Errorf("stats = %+v, want pending 1 and one timeout", s)
	}
}

func TestRecoverKeepsGoingAfterAQueueErrorAndNeedsAQueue(t *testing.T) {
	l := newLifeRig(t)
	l.otherPending(l.clk.Now().Add(time.Minute), groupAlpha, groupGamma)
	l.restart()
	boom := errors.New("queue is down")
	l.q.fail = map[string]error{groupAlpha: boom}
	queuedN, completed, err := l.lc.Recover(context.Background())
	if !errors.Is(err, boom) || completed != 0 || queuedN != 1 {
		t.Errorf("Recover = queued %d, completed %d, err %v; want 1, 0 and the queue error", queuedN, completed, err)
	}
	l.lc.Queue = nil
	if _, _, err := l.lc.Recover(context.Background()); err == nil {
		t.Error("Recover without a queue reported nothing")
	}
}

func TestARowEndedByAnotherPathBetweenTheListAndTheTransactionIsSkippedWithoutError(t *testing.T) {
	l := newLifeRig(t)
	l.gammaPending()
	l.clk.Advance(time.Minute)
	listed, ok := l.pending()
	if !ok {
		t.Fatal("no pending row")
	}
	l.must(l.inTx(func(tx *store.Tx) error {
		return l.e.Complete(context.Background(), tx, listed, ResultSettled)
	}))
	done, err := l.lc.end(context.Background(), listed, l.clk.Now(), true)
	if err != nil || done {
		t.Errorf("end = %v, %v; want false, nil", done, err)
	}
	if len(l.changes()) != 1 || l.version(idOne) != 6 {
		t.Errorf("the skipped row appended again: %d rows, version %d", len(l.changes()), l.version(idOne))
	}
	if s := l.stats(); s.Timeouts != 0 {
		t.Errorf("a skipped row was counted: %+v", s)
	}
}

func TestARowRecreatedWithALaterDeadlineIsNotEndedByAStaleListing(t *testing.T) {
	l := newLifeRig(t)
	l.gammaPending()
	stale, _ := l.pending()
	l.clk.Advance(time.Minute)
	l.must(l.inTx(func(tx *store.Tx) error {
		return l.e.Complete(context.Background(), tx, stale, ResultSettled)
	}))
	// A fresh detection creates a new pending row with a later deadline.
	l.must(l.changePrimary(idOne, func(w *widget) { w.Count = 2 }))
	if fresh, ok := l.pending(); !ok || !fresh.Deadline.After(l.clk.Now()) {
		t.Fatalf("fresh = %+v, %v", fresh, ok)
	}
	done, err := l.lc.end(context.Background(), stale, l.clk.Now(), true)
	if err != nil || done {
		t.Errorf("end = %v, %v; want false, nil", done, err)
	}
	if _, ok := l.pending(); !ok {
		t.Error("the fresh pending row was ended by a stale listing")
	}
}

func TestStatsCountsTheLastHourOnlyAndAnEventExactlyOneHourOldStillCounts(t *testing.T) {
	l := newLifeRig(t)
	l.gammaPending()
	l.clk.Advance(30 * time.Second)
	l.sweep() // a timeout at T

	l.clk.Advance(30 * time.Minute)
	l.must(l.changePrimary(idOne, func(w *widget) { w.Count = 2 }))
	l.setLastError(idOne, groupGamma, "boom")
	l.clk.Advance(30 * time.Second)
	l.sweep() // a failure 30 minutes after the timeout
	if s := l.stats(); s.Timeouts != 1 || s.Failures != 1 || s.Pending != 0 {
		t.Fatalf("stats = %+v, want one timeout and one failure", s)
	}
	l.clk.Advance(29*time.Minute + 30*time.Second) // the timeout is exactly one hour old
	if s := l.stats(); s.Timeouts != 1 || s.Failures != 1 {
		t.Errorf("at exactly one hour: stats = %+v, want both still counted", s)
	}
	l.clk.Advance(time.Nanosecond)
	if s := l.stats(); s.Timeouts != 0 || s.Failures != 1 {
		t.Errorf("one nanosecond later: stats = %+v, want the timeout gone", s)
	}
	l.clk.Advance(time.Hour)
	if s := l.stats(); s.Timeouts != 0 || s.Failures != 0 {
		t.Errorf("two hours later: stats = %+v, want zero", s)
	}
}

func TestRunSweepsOnItsTickAndLogsASweepErrorAndGoesOn(t *testing.T) {
	l := newLifeRig(t)
	l.gammaPending()
	l.clk.Advance(time.Minute)
	l.lc.Interval = 5 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { l.lc.Run(ctx); close(done) }()
	deadline := time.After(10 * time.Second)
	for len(l.pendingIDs()) != 0 {
		select {
		case <-deadline:
			cancel()
			<-done
			t.Fatal("Run did not end the expired row")
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	<-done
	if rows := l.changes(); len(rows) != 1 || rows[0].Version != 6 {
		t.Errorf("rows = %+v, want one at version 6", rows)
	}

	// Every sweep now fails: Run logs it and goes on.
	l.must(l.st.Close())
	ctx, cancel = context.WithCancel(context.Background())
	done = make(chan struct{})
	go func() { l.lc.Run(ctx); close(done) }()
	for {
		l.mu.Lock()
		n := len(l.logs)
		l.mu.Unlock()
		if n >= 2 {
			break
		}
		select {
		case <-time.After(5 * time.Millisecond):
		case <-time.After(10 * time.Second):
			cancel()
			<-done
			t.Fatal("no sweep errors were logged")
		}
	}
	cancel()
	<-done
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.logs[len(l.logs)-1] != "settle_sweep_error" {
		t.Errorf("logged %q, want settle_sweep_error", l.logs)
	}
}

func TestTheZeroValuesOfClockIntervalAndLogAreUsable(t *testing.T) {
	l := newLifeRig(t)
	lc := &Lifecycle{Engine: l.e, Store: l.st}
	if lc.now().IsZero() {
		t.Error("the zero clock did not read the system clock")
	}
	lc.log("anything", "k", "v") // a nil Log discards
	if n, err := lc.Sweep(context.Background()); err != nil || n != 0 {
		t.Errorf("Sweep = %d, %v", n, err)
	}
}
