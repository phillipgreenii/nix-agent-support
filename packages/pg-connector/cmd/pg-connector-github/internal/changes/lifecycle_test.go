package changes

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/classify"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/summary"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

const node2 = "PR_2"

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
	path := filepath.Join(t.TempDir(), "store.db")
	l := &lrig{rig: &rig{t: t, clk: governor.NewManualClock(t0), purpose: governor.PurposeRefresh}, path: path}
	l.open()
	return l
}

// open opens the store file and builds a fresh engine and lifecycle over it,
// the way a restarted process would.
func (l *lrig) open() {
	l.t.Helper()
	st, _, err := store.Open(l.path, store.Options{Synchronous: "OFF"})
	if err != nil {
		l.t.Fatal(err)
	}
	l.t.Cleanup(func() { _ = st.Close() })
	e, err := New(Config{Clock: l.clk, BodyCap: 64})
	if err != nil {
		l.t.Fatal(err)
	}
	l.st, l.e, l.obs = st, e, &recorder{}
	e.SetObserver(l.obs)
	l.q = &recordingQueue{}
	l.lc = &Lifecycle{
		Engine: e, Store: st, Queue: l.q, Clock: l.clk,
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

// rollupPending seeds the entity with a runs group, changes its checks state
// and so leaves one pending row awaiting runs.
func (l *lrig) rollupPending() {
	l.t.Helper()
	l.seed(sample())
	l.seedGroup(groups.Runs, runsContent("success"), "h1", "b1")
	l.clk.Advance(time.Minute)
	l.must(l.changeSummary(func(s *summary.Summary) { s.ChecksState = "FAILURE" }, nil))
	if got := l.awaitedGroups(); !slices.Equal(got, []string{groups.Runs}) {
		l.t.Fatalf("awaited = %v, want [runs]", got)
	}
}

// otherPending seeds a second entity and a pending row for it awaiting the
// given groups, with the given deadline.
func (l *lrig) otherPending(deadline time.Time, awaited ...string) {
	l.t.Helper()
	s := sample()
	s.NodeID, s.Number = node2, 8
	l.seed(s)
	var aw []Awaiting
	for _, g := range awaited {
		aw = append(aw, Awaiting{Group: g, Since: l.clk.Now()})
	}
	fields, err := classify.EncodeFields([]classify.Field{{Name: "checks_state", Before: []byte(`"SUCCESS"`), After: []byte(`"FAILURE"`)}})
	l.must(err)
	awaitedRaw, err := encodeAwaited(aw)
	l.must(err)
	l.must(l.inTx(func(tx *store.Tx) error {
		return tx.PutPending(context.Background(), store.PendingChange{
			Kind: LogKindPR, ID: node2, Kinds: []string{classify.KindCIChanged}, Fields: fields, Awaited: awaitedRaw,
			HeadSHA: "h1", BaseSHA: "b1", Origin: "refresh", OriginUpdatedAt: t0,
			DetectedAt: l.clk.Now(), Deadline: deadline,
		})
	}))
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

func (l *lrig) setLastError(group, msg string) {
	l.t.Helper()
	l.must(l.inTx(func(tx *store.Tx) error {
		row, ok, err := tx.GetGroup(context.Background(), LogKindPR, node, group)
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

func TestSweepAppendsAPendingRowWhoseDeadlinePassedWithTheSummarysFieldsAndOneVersionBump(t *testing.T) {
	l := newLifeRig(t)
	l.rollupPending()
	if v := l.version(); v != 5 {
		t.Fatalf("version before = %d, want 5", v)
	}

	// Before the deadline: nothing.
	l.clk.Advance(29 * time.Second)
	if n := l.sweep(); n != 0 {
		t.Fatalf("sweep before the deadline completed %d rows", n)
	}
	if rows := l.changes(); len(rows) != 0 {
		t.Fatalf("appended before the deadline: %+v", rows)
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
	row := rows[0]
	wantKinds(t, row.Kinds, classify.KindCIChanged)
	if !slices.Equal(fieldNames(row), []string{"checks_state"}) {
		t.Errorf("fields = %v, want [checks_state]", fieldNames(row))
	}
	if row.ID != ref || row.Version != 6 || row.Origin != "refresh" || !row.At.Equal(l.clk.Now()) {
		t.Errorf("row = %+v", row)
	}
	if v := l.version(); v != 6 {
		t.Errorf("version = %d, want 6 (bumped once)", v)
	}
	if _, ok := l.pending(); ok {
		t.Error("the pending row survived")
	}
	if !slices.Equal(l.obs.settled, []string{ResultTimeout}) {
		t.Errorf("settled = %v, want [timeout]", l.obs.settled)
	}

	// A second sweep has nothing to do.
	if n := l.sweep(); n != 0 || len(l.changes()) != 1 {
		t.Errorf("second sweep completed %d, rows %d", n, len(l.changes()))
	}
	// A sweep never queues a group.
	if got := l.q.calls(); len(got) != 0 {
		t.Errorf("sweep queued groups: %+v", got)
	}
}

func TestRunSweepsOnItsTick(t *testing.T) {
	l := newLifeRig(t)
	l.rollupPending()
	l.clk.Advance(time.Minute)
	l.lc.Interval = 5 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { l.lc.Run(ctx); close(done) }()

	deadline := time.After(10 * time.Second)
	for {
		ps, err := l.st.ListPending(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(ps) == 0 {
			break
		}
		select {
		case <-deadline:
			cancel()
			<-done
			t.Fatal("Run did not end the expired row")
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after its context ended")
	}
	rows := l.changes()
	if len(rows) != 1 || rows[0].Version != 6 {
		t.Errorf("rows = %+v, want one at version 6", rows)
	}
}

func TestRunLogsASweepErrorAndGoesOn(t *testing.T) {
	l := newLifeRig(t)
	l.lc.Interval = 5 * time.Millisecond
	l.must(l.st.Close()) // every sweep now fails

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { l.lc.Run(ctx); close(done) }()
	deadline := time.After(10 * time.Second)
	for {
		l.mu.Lock()
		n := len(l.logs)
		l.mu.Unlock()
		if n >= 2 { // it went on after the first error
			break
		}
		select {
		case <-deadline:
			cancel()
			<-done
			t.Fatal("no sweep errors were logged")
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	<-done
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.logs[0] != "settle_sweep_error" {
		t.Errorf("logged %q, want settle_sweep_error", l.logs[0])
	}
}

func TestARowWhoseDependentsWereAllRefreshedIsNotTouchedBySweep(t *testing.T) {
	l := newLifeRig(t)
	l.rollupPending()
	l.clk.Advance(time.Second)
	l.must(l.refresh(groups.Runs, runsContent("failure"), "h1", "b1", l.clk.Now()))
	if rows := l.changes(); len(rows) != 1 || rows[0].Version != 6 {
		t.Fatalf("the engine did not append: %+v", rows)
	}
	settled := slices.Clone(l.obs.settled)

	l.clk.Advance(time.Hour)
	if n := l.sweep(); n != 0 {
		t.Errorf("sweep completed %d rows, want 0", n)
	}
	if rows := l.changes(); len(rows) != 1 {
		t.Errorf("rows = %d, want 1", len(rows))
	}
	if v := l.version(); v != 6 {
		t.Errorf("version = %d, want 6", v)
	}
	if !slices.Equal(l.obs.settled, settled) || !slices.Equal(settled, []string{ResultSettled}) {
		t.Errorf("settled = %v (was %v), want [settled] unchanged", l.obs.settled, settled)
	}
	if s := l.stats(); s.Timeouts != 0 || s.Failures != 0 || s.Pending != 0 {
		t.Errorf("stats = %+v, want zero", s)
	}
}

func TestASettleUnderAGovernorPauseAppendsAtTheDeadlineAndCountsATimeout(t *testing.T) {
	l := newLifeRig(t)
	l.rollupPending() // no dependent is ever refreshed: the governor holds it back
	l.clk.Advance(30 * time.Second)
	if n := l.sweep(); n != 1 {
		t.Fatalf("completed %d, want 1", n)
	}
	if rows := l.changes(); len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if !slices.Equal(l.obs.settled, []string{ResultTimeout}) {
		t.Errorf("settled = %v, want [timeout]", l.obs.settled)
	}
	if s := l.stats(); s.Timeouts != 1 || s.Failures != 0 {
		t.Errorf("stats = %+v, want one timeout", s)
	}
}

func TestADependentWhoseStoredRowCarriesALastErrorIsCountedFailed(t *testing.T) {
	l := newLifeRig(t)
	l.rollupPending()
	l.setLastError(groups.Runs, "boom")
	l.clk.Advance(30 * time.Second)
	if n := l.sweep(); n != 1 {
		t.Fatalf("completed %d, want 1", n)
	}
	if rows := l.changes(); len(rows) != 1 {
		t.Fatalf("a failed settle must still append: rows = %d", len(rows))
	}
	if !slices.Equal(l.obs.settled, []string{ResultFailed}) {
		t.Errorf("settled = %v, want [failed]", l.obs.settled)
	}
	if s := l.stats(); s.Failures != 1 || s.Timeouts != 0 {
		t.Errorf("stats = %+v, want one failure", s)
	}
}

func TestALastErrorOnAGroupThatIsNotAwaitedDoesNotMakeTheRowFailed(t *testing.T) {
	l := newLifeRig(t)
	l.rollupPending()
	l.seedGroup(groups.Files, filesContent("a"), "h1", "b1")
	l.setLastError(groups.Files, "boom")
	l.clk.Advance(30 * time.Second)
	l.sweep()
	if !slices.Equal(l.obs.settled, []string{ResultTimeout}) {
		t.Errorf("settled = %v, want [timeout]", l.obs.settled)
	}
}

func TestAGroupRefreshedAfterTheDeadlineAppendsItsOwnRowIfItsContentChanged(t *testing.T) {
	l := newLifeRig(t)
	l.rollupPending()
	l.clk.Advance(30 * time.Second)
	l.sweep()
	if rows := l.changes(); len(rows) != 1 || rows[0].Version != 6 {
		t.Fatalf("after the sweep rows = %+v", rows)
	}

	l.clk.Advance(5 * time.Second)
	l.must(l.refresh(groups.Runs, runsContent("failure"), "h1", "b1", l.clk.Now()))
	rows := l.changes()
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2: %+v", len(rows), rows)
	}
	wantKinds(t, rows[1].Kinds, classify.KindCIChanged)
	if !slices.Equal(fieldNames(rows[1]), []string{"run:1"}) || rows[1].Version != 7 {
		t.Errorf("late row = %+v (fields %v)", rows[1], fieldNames(rows[1]))
	}

	// An unchanged refresh appends nothing.
	l.clk.Advance(time.Second)
	l.must(l.refresh(groups.Runs, runsContent("failure"), "h1", "b1", l.clk.Now()))
	if rows := l.changes(); len(rows) != 2 {
		t.Errorf("an unchanged refresh appended: rows = %d", len(rows))
	}
}

func TestFlushAllAppendsEveryPendingRowBeforeItsDeadlineAndLeavesNone(t *testing.T) {
	l := newLifeRig(t)
	l.rollupPending()
	l.otherPending(l.clk.Now().Add(20*time.Second), groups.Files, groups.Runs)
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
	ids := []string{rows[0].ID, rows[1].ID}
	slices.Sort(ids)
	if !slices.Equal(ids, []string{"phillipgreenii/example#7", "phillipgreenii/example#8"}) {
		t.Errorf("row ids = %v", ids)
	}
	// Ended before the deadline without the barrier satisfied: the same rule as
	// a sweep (timeout when no awaited group carries a last_error).
	if !slices.Equal(l.obs.settled, []string{ResultTimeout, ResultTimeout}) {
		t.Errorf("settled = %v, want two timeouts", l.obs.settled)
	}
	if n, err := l.lc.FlushAll(context.Background()); err != nil || n != 0 {
		t.Errorf("second FlushAll = %d, %v; want 0, nil", n, err)
	}
}

func TestFlushAllAppliesTheFailedRuleToo(t *testing.T) {
	l := newLifeRig(t)
	l.rollupPending()
	l.setLastError(groups.Runs, "boom")
	if n, err := l.lc.FlushAll(context.Background()); err != nil || n != 1 {
		t.Fatalf("FlushAll = %d, %v", n, err)
	}
	if !slices.Equal(l.obs.settled, []string{ResultFailed}) {
		t.Errorf("settled = %v, want [failed]", l.obs.settled)
	}
}

func TestAfterARestartRecoverCompletesExpiredRowsAndQueuesTheAwaitedGroupsOfTheOthers(t *testing.T) {
	l := newLifeRig(t)
	l.rollupPending() // PR_1: deadline detection + 30 s
	l.otherPending(l.clk.Now().Add(time.Minute), groups.Files, groups.Runs)
	l.clk.Advance(45 * time.Second) // PR_1 expired, PR_2 not

	l.restart()
	if got := l.pendingIDs(); !slices.Equal(got, []string{node, node2}) {
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
		{node2, groups.Files, governor.ClassInvalidated, l.clk.Now()},
		{node2, groups.Runs, governor.ClassInvalidated, l.clk.Now()},
	}
	if got := l.q.calls(); !slices.Equal(got, want) {
		t.Errorf("queued = %+v, want %+v", got, want)
	}
	// The expired row was appended; the other was left alone, awaiting both.
	rows := l.changes()
	if len(rows) != 1 || rows[0].ID != ref {
		t.Errorf("rows = %+v, want one for %s", rows, ref)
	}
	if got := l.pendingIDs(); !slices.Equal(got, []string{node2}) {
		t.Errorf("pending = %v, want [%s]", got, node2)
	}
	ps, err := l.st.ListPending(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	aw, err := DecodeAwaited(ps[0].Awaited)
	if err != nil || len(aw) != 2 {
		t.Errorf("awaited of the kept row = %+v, %v; want files and runs", aw, err)
	}
	// Recover is not a count: the in-memory stats start from zero after a
	// restart and only the row it ended is counted.
	if s := l.stats(); s.Pending != 1 || s.Timeouts != 1 {
		t.Errorf("stats = %+v, want pending 1 and one timeout", s)
	}
}

func TestRecoverQueuesOnlyTheStillAwaitedGroupsAndKeepsGoingAfterAQueueError(t *testing.T) {
	l := newLifeRig(t)
	l.rollupPending()
	l.otherPending(l.clk.Now().Add(time.Minute), groups.Detail, groups.Files)
	l.restart()
	boom := errors.New("queue is down")
	l.q.fail = map[string]error{groups.Detail: boom}

	queuedN, completed, err := l.lc.Recover(context.Background())
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want it to wrap the queue error", err)
	}
	// PR_1 (deadline detection + 30 s) is still inside its deadline too: it
	// awaits runs.
	if completed != 0 || queuedN != 2 {
		t.Errorf("Recover = queued %d, completed %d; want 2, 0", queuedN, completed)
	}
	var got []string
	for _, c := range l.q.calls() {
		got = append(got, c.ID+"/"+c.Group)
	}
	if !slices.Equal(got, []string{node + "/" + groups.Runs, node2 + "/" + groups.Files}) {
		t.Errorf("queued = %v", got)
	}
	if rows := l.changes(); len(rows) != 0 {
		t.Errorf("Recover appended rows for pending entities: %+v", rows)
	}
}

func TestARowEndedByAnotherPathBetweenTheListAndTheTransactionIsSkippedWithoutError(t *testing.T) {
	l := newLifeRig(t)
	l.rollupPending()
	l.clk.Advance(time.Minute)
	listed, ok := l.pending()
	if !ok {
		t.Fatal("no pending row")
	}

	// Another path ends the row after the sweep listed it.
	l.must(l.inTx(func(tx *store.Tx) error {
		return l.e.Complete(context.Background(), tx, listed, ResultSettled)
	}))
	if rows := l.changes(); len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}

	done, err := l.lc.end(context.Background(), listed, l.clk.Now(), true)
	if err != nil || done {
		t.Errorf("end = %v, %v; want false, nil", done, err)
	}
	if rows := l.changes(); len(rows) != 1 {
		t.Errorf("the skipped row appended again: %d rows", len(rows))
	}
	if v := l.version(); v != 6 {
		t.Errorf("version = %d, want 6", v)
	}
	if s := l.stats(); s.Timeouts != 0 {
		t.Errorf("a skipped row was counted: %+v", s)
	}
}

func TestARowRecreatedWithALaterDeadlineIsNotEndedByAStaleListing(t *testing.T) {
	l := newLifeRig(t)
	l.rollupPending()
	stale, _ := l.pending()
	l.clk.Advance(time.Minute)
	l.must(l.inTx(func(tx *store.Tx) error {
		return l.e.Complete(context.Background(), tx, stale, ResultSettled)
	}))
	// A fresh detection creates a new pending row with a later deadline.
	l.must(l.changeSummary(func(s *summary.Summary) { s.ChecksState = "SUCCESS" }, nil))
	fresh, ok := l.pending()
	if !ok || !fresh.Deadline.After(l.clk.Now()) {
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
	l.rollupPending()
	l.clk.Advance(30 * time.Second)
	l.sweep() // a timeout at T

	// A failure 30 minutes later, on the same entity.
	l.clk.Advance(30 * time.Minute)
	l.must(l.changeSummary(func(s *summary.Summary) { s.ChecksState = "SUCCESS" }, nil))
	l.setLastError(groups.Runs, "boom")
	l.clk.Advance(30 * time.Second)
	l.sweep()
	if s := l.stats(); s.Timeouts != 1 || s.Failures != 1 || s.Pending != 0 {
		t.Fatalf("stats = %+v, want one timeout and one failure", s)
	}

	// The timeout is now exactly one hour old minus 30 s: move to exactly one
	// hour after it.
	l.clk.Advance(29*time.Minute + 30*time.Second)
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

func TestStatsReportsTheStoredPendingCountAndARestartResetsTheEventCounts(t *testing.T) {
	l := newLifeRig(t)
	l.rollupPending()
	l.otherPending(l.clk.Now().Add(time.Hour), groups.Files)
	l.clk.Advance(30 * time.Second)
	l.sweep()
	if s := l.stats(); s.Pending != 1 || s.Timeouts != 1 {
		t.Fatalf("stats = %+v, want pending 1 and one timeout", s)
	}
	l.restart()
	if s := l.stats(); s.Pending != 1 || s.Timeouts != 0 || s.Failures != 0 {
		t.Errorf("after a restart stats = %+v, want pending 1 and zero counts", s)
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
