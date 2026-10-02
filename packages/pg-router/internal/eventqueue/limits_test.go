package eventqueue

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// Tests for the log-size limit enforcement (bead pg2-5d3ui, limits.go): the soft
// step (emittersHalted, NOT a gate), the hard limit (classified admission
// refusal that leaves accept/evict/gate records appendable), the unwritable
// detection and its volatile halt, and the wedge-freedom of all of it.

// --- doubles --------------------------------------------------------------

// rejectRecorder is a recordingObserver that also implements RejectObserver.
type rejectRecorder struct {
	recordingObserver
	mu      sync.Mutex
	rejects []string // "type/reason"
}

func (o *rejectRecorder) OnEnqueueRejected(evtType, reason string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.rejects = append(o.rejects, evtType+"/"+reason)
}

// failEveryAppendStore fails EVERY append (and batch) until healed, counting the
// attempts. It can also act as a Prober.
type failEveryAppendStore struct {
	inner Store
	mu    sync.Mutex
	err   error
	calls int
	// probeOK, when non-nil, makes the double a Prober that succeeds iff it
	// returns true.
	probeOK func() bool
}

func newFailEveryAppendStore() *failEveryAppendStore {
	return &failEveryAppendStore{inner: NewMemStore(), err: syscall.ENOSPC}
}

func (s *failEveryAppendStore) heal() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = nil
}

func (s *failEveryAppendStore) Append(r Record) error {
	s.mu.Lock()
	s.calls++
	err := s.err
	s.mu.Unlock()
	if err != nil {
		return err
	}
	return s.inner.Append(r)
}

func (s *failEveryAppendStore) AppendBatch(rs []Record) error {
	s.mu.Lock()
	s.calls++
	err := s.err
	s.mu.Unlock()
	if err != nil {
		return err
	}
	return s.inner.AppendBatch(rs)
}
func (s *failEveryAppendStore) Replay() ([]Record, error) { return s.inner.Replay() }
func (s *failEveryAppendStore) Close() error              { return s.inner.Close() }
func (s *failEveryAppendStore) attempts() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

type probingFailStore struct{ *failEveryAppendStore }

func (p probingFailStore) Probe() error {
	if p.probeOK != nil && p.probeOK() {
		return nil
	}
	return syscall.ENOSPC
}

// probingStore makes any Store a Prober whose probe always fails (the disk is
// still full).
type probingStore struct{ Store }

func (probingStore) Probe() error { return syscall.ENOSPC }

// failAfterBytesStore persists like a MemStore until its cumulative encoded size
// would exceed limit, then fails every further append: a disk that fills up.
type failAfterBytesStore struct {
	inner *MemStore
	limit int64
	full  bool
}

func (s *failAfterBytesStore) fits(rs ...Record) bool {
	var n int64
	for _, r := range rs {
		n += encodedLen(r)
	}
	return !s.full && s.inner.LogSize()+n <= s.limit
}

func (s *failAfterBytesStore) Append(r Record) error {
	if !s.fits(r) {
		s.full = true
		return syscall.ENOSPC
	}
	return s.inner.Append(r)
}

func (s *failAfterBytesStore) AppendBatch(rs []Record) error {
	if !s.fits(rs...) {
		s.full = true
		return syscall.ENOSPC
	}
	return s.inner.AppendBatch(rs)
}
func (s *failAfterBytesStore) Replay() ([]Record, error) { return s.inner.Replay() }
func (s *failAfterBytesStore) Close() error              { return nil }

func newLimitQueue(t *testing.T, store Store, soft, hard int64, extra ...Option) (*Queue, *mockClock) {
	t.Helper()
	clk := newClock()
	opts := append([]Option{WithClock(clk.now), WithSleeper(clk.after), WithLogLimits(soft, hard)}, extra...)
	q, err := New(store, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return q, clk
}

// drainToIdle runs the queue to idle (every event offered and retired).
func drainToIdle(t *testing.T, q *Queue) {
	t.Helper()
	if err := q.RunUntilIdle(context.Background(), time.Second); err != nil {
		t.Fatal(err)
	}
}

// --- soft step ------------------------------------------------------------

func TestLimits_belowSoftDoesNothing(t *testing.T) {
	mem := NewMemStore()
	q, _ := newLimitQueue(t, mem, 900, 1000)
	mem.SetLogSize(100)
	q.EnforceLogLimits()
	st := q.LimitStatus()
	if q.EmittersHalted() || st.State != StateOK || st.EmittersHalted {
		t.Fatalf("below soft: halted=%v state=%q, want ok", q.EmittersHalted(), st.State)
	}
	if _, err := q.Enqueue(evt("e1", "T")); err != nil {
		t.Fatalf("Enqueue below soft: %v", err)
	}
}

// The boundary at exactly soft, and the way back: the flag is re-derived from the
// size every call, so it clears the moment the log is back at or under soft.
func TestLimits_softBoundaryAndHysteresis(t *testing.T) {
	const soft, hard = 900, 1000
	for _, tc := range []struct {
		name       string
		size       int64
		wantHalted bool
		wantState  string
	}{
		{"well below", 100, false, StateOK},
		{"one below soft", soft - 1, false, StateOK},
		{"exactly soft is not over soft", soft, false, StateOK},
		{"one over soft", soft + 1, true, StateEmittersHalted},
		{"between soft and hard", hard - 1, true, StateEmittersHalted},
		{"exactly hard", hard, true, StateLogFull},
		{"over hard", hard + 500, true, StateLogFull},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mem := NewMemStore()
			q, _ := newLimitQueue(t, mem, soft, hard)
			mem.SetLogSize(tc.size)
			q.EnforceLogLimits()
			if got := q.EmittersHalted(); got != tc.wantHalted {
				t.Errorf("EmittersHalted = %v, want %v", got, tc.wantHalted)
			}
			if got := q.LimitStatus().State; got != tc.wantState {
				t.Errorf("State = %q, want %q", got, tc.wantState)
			}
		})
	}

	// hysteresis: halted at soft+1, cleared at exactly soft.
	mem := NewMemStore()
	q, _ := newLimitQueue(t, mem, soft, hard)
	mem.SetLogSize(soft + 1)
	q.EnforceLogLimits()
	if !q.EmittersHalted() {
		t.Fatal("not halted at soft+1")
	}
	mem.SetLogSize(soft)
	q.EnforceLogLimits()
	if q.EmittersHalted() {
		t.Fatal("still halted at exactly soft; the flag must be re-derived each tick")
	}
}

// Crossing soft halts the emitters and nothing else: a listener WITHOUT any gate
// exemption keeps getting dispatched to, Enqueue is still admitted, and
// run-until-idle still drains. This is the property that makes the soft step safe:
// space can be reclaimed and no listener loses an event.
func TestLimits_softHaltsEmittersButListenersAndDrainKeepRunning(t *testing.T) {
	mem := NewMemStore()
	q, _ := newLimitQueue(t, mem, 900, 1000)
	l := newListener("h", "T") // no GateExempter: blocks on every gate TYPE
	q.Register(l)
	if _, err := q.Enqueue(evt("e1", "T")); err != nil {
		t.Fatal(err)
	}
	mem.SetLogSize(950)
	q.EnforceLogLimits()
	if !q.EmittersHalted() {
		t.Fatal("expected the emitters halted at 950 of 1000 (soft 900)")
	}
	if _, err := q.Enqueue(evt("e2", "T")); err != nil {
		t.Fatalf("a pushed/timer event must still be admitted while only soft-halted: %v", err)
	}
	if err := q.RunUntilIdle(context.Background(), time.Second); err != nil {
		t.Fatal(err)
	}
	if !equal(l.accepted, []string{"e1", "e2"}) {
		t.Fatalf("listener accepted %v while soft-halted, want [e1 e2]: dispatch must keep working", l.accepted)
	}
	if !q.EmittersHalted() {
		t.Fatal("halt must be unaffected by dispatch")
	}
}

// The halt is not a gate: no gate appears, and gate set / clear (the operator's
// pause / resume --all) neither sets nor clears it.
func TestLimits_haltIsNotAGateAndGateClearDoesNotTouchIt(t *testing.T) {
	mem := NewMemStore()
	q, _ := newLimitQueue(t, mem, 900, 1000)
	mem.SetLogSize(950)
	q.EnforceLogLimits()
	if !q.EmittersHalted() {
		t.Fatal("not halted")
	}
	if g := q.ActiveGates(); len(g) != 0 {
		t.Fatalf("soft halt appeared as gates %v; it must not be a gate", gateTypes(g))
	}
	mustSet(t, q, GateRequest{Type: "SYSTEM_PAUSE"})
	if !q.EmittersHalted() {
		t.Fatal("a gate set cleared the halt")
	}
	if _, ok, err := q.ClearGate("SYSTEM_PAUSE", "op"); err != nil || !ok {
		t.Fatalf("ClearGate: ok=%v err=%v", ok, err)
	}
	if !q.EmittersHalted() {
		t.Fatal("gate clear (resume) cleared the halt; it must be re-derived from log size only")
	}
	mem.SetLogSize(10)
	q.EnforceLogLimits()
	if q.EmittersHalted() {
		t.Fatal("halt did not clear once the log shrank")
	}
}

// Over soft, the controller compacts first; when that reclaims the space the halt
// is cleared (and was never set after the compaction landed).
func TestLimits_compactionReclaimingSpaceClearsTheHalt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.jsonl")
	fs, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fs.Close() }()
	clk := newClock()
	q, err := New(fs, WithClock(clk.now), WithSleeper(clk.after), WithLogLimits(20_000, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	l := newListener("h", "T")
	q.Register(l)
	// Dead history: events that are accepted and evicted.
	for i := 0; i < 300; i++ {
		mustEnqueue(t, q, evt("e"+strconv.Itoa(i), "T"))
	}
	drainToIdle(t, q)
	if fs.LogSize() <= 20_000 {
		t.Fatalf("setup: log is only %d bytes, want > soft 20000", fs.LogSize())
	}
	q.EnforceLogLimits() // starts the compaction; the halt is decided on this tick's size
	q.waitCompaction()
	q.EnforceLogLimits()
	if q.EmittersHalted() {
		t.Fatalf("still halted after compaction (log %d bytes); compaction reclaiming space must clear it", fs.LogSize())
	}
	if st := q.LimitStatus(); st.State != StateOK {
		t.Fatalf("State = %q, want ok", st.State)
	}
	if q.Compactions() != 1 {
		t.Fatalf("Compactions = %d, want 1 (the limit-driven one)", q.Compactions())
	}
}

// A live set that itself exceeds soft cannot be compacted under it: the halt
// stays, and the controller does not re-compact on every tick (thrash guard).
func TestLimits_noProgressCompactionDoesNotThrash(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.jsonl")
	fs, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fs.Close() }()
	clk := newClock()
	q, err := New(fs, WithClock(clk.now), WithSleeper(clk.after), WithLogLimits(2000, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	// Live events: retained for an hour, nobody to take them, never evicted.
	for i := 0; i < 40; i++ {
		if _, err := q.Enqueue(evtUntil("live"+strconv.Itoa(i), "T", clk.in(time.Hour))); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 5; i++ {
		q.EnforceLogLimits()
		q.waitCompaction()
		clk.advance(10 * time.Second)
	}
	if !q.EmittersHalted() {
		t.Fatalf("live state alone (%d bytes) exceeds soft 2000; expected the halt to hold", fs.LogSize())
	}
	if got := q.Compactions(); got != 1 {
		t.Fatalf("Compactions = %d after 5 ticks, want exactly 1: a no-progress compaction must not repeat every tick", got)
	}
}

// --- hard limit -----------------------------------------------------------

func TestLimits_hardRejectsEnqueueWithClassifiedReason(t *testing.T) {
	mem := NewMemStore()
	obs := &rejectRecorder{}
	q, _ := newLimitQueue(t, mem, 900, 1000, WithObserver(obs))
	mem.SetLogSize(1000)
	_, err := q.Enqueue(evt("e1", "T"))
	var full *ErrLogFull
	if !errors.As(err, &full) {
		t.Fatalf("Enqueue at the hard limit returned %v, want *ErrLogFull", err)
	}
	if full.Bytes != 1000 || full.Limit != 1000 {
		t.Fatalf("ErrLogFull{%d of %d}, want 1000 of 1000", full.Bytes, full.Limit)
	}
	for _, want := range []string{"log_full: pg-router event log is at 1000 of 1000 bytes", "the event was NOT queued", "safe to retry later", "PG_ROUTER_MAX_LOG_BYTES"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("reason %q lacks %q", err.Error(), want)
		}
	}
	if r, ok := RejectReason(err); !ok || r != ReasonLogFull {
		t.Fatalf("RejectReason = %q,%v", r, ok)
	}
	if got := q.DepthByType()["T"]; got != 0 {
		t.Fatalf("a rejected event was queued: depth %d", got)
	}
	if recs, _ := mem.Replay(); len(recs) != 0 {
		t.Fatalf("a rejected event was written: %v", recs)
	}
	if st := q.LimitStatus(); st.RejectedLogFull != 1 || st.RejectedLogUnwritable != 0 {
		t.Fatalf("reject counts = %+v", st)
	}
	if !equal(obs.rejects, []string{"T/log_full"}) {
		t.Fatalf("RejectObserver saw %v", obs.rejects)
	}
	if len(obs.enqueued) != 0 {
		t.Fatalf("OnEnqueue fired for a rejected event: %v", obs.enqueued)
	}
	// Just under the limit admits.
	mem.SetLogSize(999)
	if _, err := q.Enqueue(evt("e2", "T")); err != nil {
		t.Fatalf("Enqueue just under the hard limit: %v", err)
	}
}

// The hard limit is an ADMISSION limit only: above it, accept, evict, gate_set and
// gate_cleared records still append (using the soft-to-hard headroom), and a
// Deduped re-emit stays a no-write success.
func TestLimits_aboveHardOnlyEnqueueAdmissionIsRefused(t *testing.T) {
	mem := NewMemStore()
	q, clk := newLimitQueue(t, mem, 900, 1000)
	l := newListener("h", "T")
	q.Register(l)
	// A retained event (still in its dedup window) plus a born-expired one, both
	// enqueued while there was room.
	if _, err := q.Enqueue(evtUntil("kept", "T", clk.in(time.Hour))); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Enqueue(evt("gone", "T")); err != nil {
		t.Fatal(err)
	}
	mem.SetLogSize(5000) // far over hard
	q.EnforceLogLimits()
	if st := q.LimitStatus(); st.State != StateLogFull {
		t.Fatalf("State = %q, want log_full", st.State)
	}

	if _, err := q.Enqueue(evt("new", "T")); err == nil {
		t.Fatal("a NEW event was admitted over the hard limit")
	}
	if res, err := q.Enqueue(evtUntil("kept", "T", clk.in(time.Hour))); err != nil || res != Deduped {
		t.Fatalf("Deduped re-emit at the hard limit = (%v, %v), want (Deduped, nil)", res, err)
	}

	before, _ := mem.Replay()
	// Dispatch offers each listener its head once per pass.
	if n := q.Dispatch() + q.Dispatch(); n != 2 {
		t.Fatalf("Dispatch accepted %d over the hard limit, want 2", n)
	}
	if n := q.Expire(); n != 1 {
		t.Fatalf("Expire dropped %d, want 1 (the born-expired accepted event)", n)
	}
	if _, err := q.SetGate(GateRequest{Type: "SYSTEM_PAUSE"}); err != nil {
		t.Fatalf("SetGate over the hard limit: %v", err)
	}
	if _, ok, err := q.ClearGate("SYSTEM_PAUSE", "op"); err != nil || !ok {
		t.Fatalf("ClearGate over the hard limit: ok=%v err=%v", ok, err)
	}
	after, _ := mem.Replay()
	ops := map[opKind]int{}
	for _, r := range after[len(before):] {
		ops[r.Op]++
	}
	if ops[opAccept] != 2 || ops[opEvict] != 1 || ops[opGateSet] != 1 || ops[opGateCleared] != 1 {
		t.Fatalf("records appended above the hard limit = %v, want 2 accept, 1 evict, 1 gate_set, 1 gate_cleared", ops)
	}
	if st := q.LimitStatus(); st.State != StateLogFull {
		t.Fatalf("those writes must not mark the log unwritable; State = %q", st.State)
	}
}

// --- unwritable -----------------------------------------------------------

func TestLimits_firstAppendErrorMarksUnwritableAndRejects(t *testing.T) {
	store := newFailEveryAppendStore()
	store.probeOK = func() bool { return false } // a Prober whose probe keeps failing
	obs := &rejectRecorder{}
	q, _ := newLimitQueue(t, probingFailStore{store}, 900, 1000, WithObserver(obs))

	_, err := q.Enqueue(evt("e1", "T"))
	var unw *ErrLogUnwritable
	if !errors.As(err, &unw) {
		t.Fatalf("Enqueue with a failing store returned %v, want *ErrLogUnwritable", err)
	}
	if !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("ErrLogUnwritable must wrap the cause: %v", err)
	}
	if !strings.HasPrefix(err.Error(), "log_unwritable: ") || !strings.Contains(err.Error(), "the event was NOT queued") {
		t.Fatalf("reason = %q", err.Error())
	}
	if q.DepthByType()["T"] != 0 {
		t.Fatal("the event whose write failed was queued in memory")
	}
	calls := store.attempts()
	// The mark is in memory: further events are rejected WITHOUT another write.
	if _, err := q.Enqueue(evt("e2", "T")); !errors.As(err, &unw) {
		t.Fatalf("second Enqueue = %v, want *ErrLogUnwritable", err)
	}
	if store.attempts() != calls {
		t.Fatalf("rejection depended on the log being writable: %d extra write attempts", store.attempts()-calls)
	}
	// The halt is volatile and does not need any record written.
	q.EnforceLogLimits()
	st := q.LimitStatus()
	if st.State != StateLogUnwritable || !st.EmittersHalted || !q.EmittersHalted() {
		t.Fatalf("status = %+v, want log_unwritable with emitters halted", st)
	}
	if st.Detail == "" {
		t.Fatal("no detail for the operator")
	}
	if st.RejectedLogUnwritable != 2 || st.RejectedLogFull != 0 {
		t.Fatalf("reject counts = %+v", st)
	}
	if !equal(obs.rejects, []string{"T/log_unwritable", "T/log_unwritable"}) {
		t.Fatalf("RejectObserver saw %v", obs.rejects)
	}
}

// No wedge: with a log that cannot be written at all the rest of the queue
// machinery keeps running, and recovery comes from the probe.
func TestLimits_unwritableDoesNotWedgeAndRecoversViaProbe(t *testing.T) {
	store := newFailEveryAppendStore()
	healed := false
	store.probeOK = func() bool { return healed }
	q, clk := newLimitQueue(t, probingFailStore{store}, 900, 1000)
	l := newListener("h", "T")
	q.Register(l)
	if _, err := q.Enqueue(evt("e1", "T")); err == nil {
		t.Fatal("expected the first write to fail")
	}
	q.EnforceLogLimits()
	if !q.EmittersHalted() {
		t.Fatal("not halted while unwritable")
	}

	// Dispatch / Expire / Idle / RunUntilIdle all return; none panics or blocks.
	done := make(chan struct{})
	go func() {
		defer close(done)
		q.Dispatch()
		q.Expire()
		q.Idle()
		_ = q.RunUntilIdle(context.Background(), time.Second)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the queue wedged on an unwritable log")
	}
	if _, err := q.SetGate(GateRequest{Type: "SYSTEM_PAUSE"}); err == nil {
		t.Fatal("SetGate reported success although nothing could be written")
	}

	// A failed probe keeps it unwritable, and is not retried before probeInterval.
	q.EnforceLogLimits()
	if !q.EmittersHalted() || q.LimitStatus().State != StateLogUnwritable {
		t.Fatal("recovered although the probe fails")
	}
	healed = true
	store.heal()
	q.EnforceLogLimits()
	if q.LimitStatus().State != StateLogUnwritable {
		t.Fatal("probe ran again before probeInterval elapsed")
	}
	clk.advance(probeInterval + time.Second)
	q.EnforceLogLimits()
	if st := q.LimitStatus(); st.State != StateOK || q.EmittersHalted() {
		t.Fatalf("after the probe succeeded: %+v halted=%v, want ok", st, q.EmittersHalted())
	}
	if _, err := q.Enqueue(evt("e2", "T")); err != nil {
		t.Fatalf("Enqueue after recovery: %v", err)
	}
}

// A store with no Prober is recovered half-open: the mark is dropped on the next
// tick, the next Enqueue tries the real write, and a failure re-marks it.
func TestLimits_halfOpenRecoveryWithoutProber(t *testing.T) {
	store := newFailEveryAppendStore()
	q, _ := newLimitQueue(t, store, 900, 1000)
	if _, err := q.Enqueue(evt("e1", "T")); err == nil {
		t.Fatal("expected failure")
	}
	q.EnforceLogLimits() // half-open: lets the next enqueue try; then re-marks as the halted flag is re-derived below
	if _, err := q.Enqueue(evt("e2", "T")); err == nil {
		t.Fatal("expected the retried write to fail again")
	}
	if q.LimitStatus().State != StateLogUnwritable {
		t.Fatalf("State = %q after a failed half-open retry, want log_unwritable", q.LimitStatus().State)
	}
	store.heal()
	q.EnforceLogLimits()
	if _, err := q.Enqueue(evt("e3", "T")); err != nil {
		t.Fatalf("Enqueue after the disk healed: %v", err)
	}
	if q.LimitStatus().State != StateOK {
		t.Fatalf("State = %q", q.LimitStatus().State)
	}
}

// A disk that fills part-way: writes succeed until N bytes, then every append
// fails. Already-queued events keep dispatching; new ones are rejected.
func TestLimits_failAfterNBytes(t *testing.T) {
	mem := NewMemStore()
	store := &failAfterBytesStore{inner: mem, limit: 600}
	q, _ := newLimitQueue(t, probingStore{store}, 0, 0)
	l := newListener("h", "T")
	q.Register(l)
	admitted := 0
	var firstErr error
	for i := 0; i < 20; i++ {
		_, err := q.Enqueue(evtUntil("e"+strconv.Itoa(i), "T", time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)))
		if err != nil {
			firstErr = err
			break
		}
		admitted++
	}
	if admitted == 0 || admitted == 20 {
		t.Fatalf("admitted %d of 20; the fake disk should fill part-way", admitted)
	}
	var unw *ErrLogUnwritable
	if !errors.As(firstErr, &unw) {
		t.Fatalf("first failure = %v, want *ErrLogUnwritable", firstErr)
	}
	q.EnforceLogLimits()
	if !q.EmittersHalted() {
		t.Fatal("emitters not halted on a full disk")
	}
	drainToIdle(t, q) // accept appends fail too; delivery is unaffected
	if got := len(l.accepted); got != admitted {
		t.Fatalf("listener accepted %d", got)
	}
}

// An encode failure is a fault in the event, not in the storage: it must not mark
// the log unwritable or reject later events.
func TestLimits_encodeErrorDoesNotMarkUnwritable(t *testing.T) {
	fs, err := NewFileStore(filepath.Join(t.TempDir(), "queue.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fs.Close() }()
	q, _ := newLimitQueue(t, fs, 0, 0)
	bad := Event{ID: "nan", Type: "T", Payload: map[string]any{"v": math.NaN()}}
	_, err = q.Enqueue(bad)
	var enc *EncodeError
	if !errors.As(err, &enc) {
		t.Fatalf("Enqueue(NaN payload) = %v, want *EncodeError", err)
	}
	if _, ok := RejectReason(err); ok {
		t.Fatal("an encode error must not be classified as an admission refusal")
	}
	if q.LimitStatus().State != StateOK {
		t.Fatalf("State = %q after an encode error", q.LimitStatus().State)
	}
	if _, err := q.Enqueue(evt("fine", "T")); err != nil {
		t.Fatalf("a later good event was refused: %v", err)
	}
}

// --- restart --------------------------------------------------------------

// A restart over a log that is only large because of dead history: startup
// compaction runs BEFORE the limits are evaluated, so the first tick sees the
// compacted size and nothing is halted or rejected.
func TestLimits_restartCompactsBeforeEvaluatingLimits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.jsonl")
	fs, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	clk := newClock()
	q, err := New(fs, WithClock(clk.now), WithSleeper(clk.after))
	if err != nil {
		t.Fatal(err)
	}
	q.Register(newListener("h", "T"))
	for i := 0; i < 300; i++ {
		mustEnqueue(t, q, evt("e"+strconv.Itoa(i), "T"))
	}
	drainToIdle(t, q)
	big := fs.LogSize()
	_ = fs.Close()
	const hard = 10_000
	if big <= hard {
		t.Fatalf("setup: log %d bytes is not over the %d hard limit", big, hard)
	}

	fs2, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fs2.Close() }()
	q2, err := New(fs2, WithClock(clk.now), WithCompaction(0, true), WithLogLimits(hard*9/10, hard))
	if err != nil {
		t.Fatal(err)
	}
	q2.EnforceLogLimits()
	if st := q2.LimitStatus(); st.State != StateOK || q2.EmittersHalted() {
		t.Fatalf("restart over %d bytes of dead history: %+v halted=%v, want ok (startup compaction first)", big, st, q2.EmittersHalted())
	}
	if _, err := q2.Enqueue(evt("after", "T")); err != nil {
		t.Fatalf("Enqueue after a compacting restart: %v", err)
	}
}

// A restart over a log whose LIVE state is still over soft: the halt is re-derived
// (not persisted, so nothing stale can survive either way).
func TestLimits_restartWhileSoftHaltedRederivesTheHalt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.jsonl")
	clk := newClock()
	open := func() (*Queue, *FileStore) {
		fs, err := NewFileStore(path)
		if err != nil {
			t.Fatal(err)
		}
		q, err := New(fs, WithClock(clk.now), WithCompaction(0, true), WithLogLimits(3000, 1<<20))
		if err != nil {
			t.Fatal(err)
		}
		return q, fs
	}
	q, fs := open()
	for i := 0; i < 40; i++ {
		if _, err := q.Enqueue(evtUntil("live"+strconv.Itoa(i), "T", clk.in(time.Hour))); err != nil {
			t.Fatal(err)
		}
	}
	q.EnforceLogLimits()
	q.waitCompaction()
	if !q.EmittersHalted() {
		t.Fatalf("setup: live state of %d bytes should be over soft 3000", fs.LogSize())
	}
	_ = fs.Close()

	q2, fs2 := open()
	defer func() { _ = fs2.Close() }()
	if q2.EmittersHalted() {
		t.Fatal("the halt is volatile: a fresh process starts un-halted until its first tick")
	}
	q2.EnforceLogLimits()
	q2.waitCompaction()
	if !q2.EmittersHalted() {
		t.Fatal("live state is still over soft after a restart; the halt must be re-derived")
	}
}

// A restart while the disk is still unwritable: the first failed append re-derives
// the state (nothing was persisted).
func TestLimits_restartWhileUnwritableRederivesOnFirstFailedAppend(t *testing.T) {
	store := newFailEveryAppendStore()
	q, _ := newLimitQueue(t, store, 900, 1000)
	if _, err := q.Enqueue(evt("e1", "T")); err == nil {
		t.Fatal("expected failure")
	}
	// "Restart": a brand-new queue over the same still-failing store.
	q2, _ := newLimitQueue(t, store, 900, 1000)
	if q2.LimitStatus().State != StateOK {
		t.Fatalf("a fresh process starts ok; got %q", q2.LimitStatus().State)
	}
	if _, err := q2.Enqueue(evt("e2", "T")); err == nil {
		t.Fatal("expected failure")
	}
	if q2.LimitStatus().State != StateLogUnwritable {
		t.Fatalf("State = %q, want log_unwritable after the first failed append", q2.LimitStatus().State)
	}
}

// --- FileStore hardening ---------------------------------------------------

func TestFileStore_shortWriteIsRolledBackAndNothingIsLost(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.jsonl")
	fs, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fs.Close() }()
	good1 := Record{Op: opEnqueue, EventID: "a", Type: "T"}
	good2 := Record{Op: opEnqueue, EventID: "b", Type: "T"}
	if err := fs.Append(good1); err != nil {
		t.Fatal(err)
	}
	sizeBefore := fs.LogSize()

	fs.writeFn = func(f *os.File, b []byte) (int, error) {
		n, _ := f.Write(b[:len(b)/2]) // a short write, then the disk is full
		return n, syscall.ENOSPC
	}
	if err := fs.Append(Record{Op: opEnqueue, EventID: "torn", Type: "T"}); !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("Append = %v, want ENOSPC", err)
	}
	if got := fs.LogSize(); got != sizeBefore {
		t.Fatalf("LogSize = %d after a failed write, want it rolled back to %d", got, sizeBefore)
	}
	fs.writeFn = nil
	if err := fs.Append(good2); err != nil {
		t.Fatal(err)
	}
	recs, err := fs.Replay()
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 || recs[0].EventID != "a" || recs[1].EventID != "b" {
		t.Fatalf("Replay = %+v, want exactly [a b]: the record after the failure must not fuse onto a partial line", recs)
	}
	info, _ := os.Stat(path)
	if info.Size() != fs.LogSize() {
		t.Fatalf("file is %d bytes but LogSize says %d", info.Size(), fs.LogSize())
	}
}

// A failed write whose rollback fails too poisons the store; the recovery probe
// heals it by compacting.
func TestFileStore_poisonedStoreIsHealedByProbe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.jsonl")
	fs, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fs.Close() }()
	if err := fs.Append(Record{Op: opEnqueue, EventID: "a", Type: "T"}); err != nil {
		t.Fatal(err)
	}
	// Break the descriptor under the store: the write AND the rollback fail.
	_ = fs.f.Close()
	if err := fs.Append(Record{Op: opEnqueue, EventID: "x", Type: "T"}); err == nil {
		t.Fatal("expected the append on a broken descriptor to fail")
	}
	if err := fs.Append(Record{Op: opEnqueue, EventID: "y", Type: "T"}); err == nil {
		t.Fatal("a poisoned store must refuse appends")
	}
	if err := fs.Probe(); err != nil {
		t.Fatalf("Probe on a poisoned store: %v (it should heal by compacting)", err)
	}
	if err := fs.Append(Record{Op: opEnqueue, EventID: "b", Type: "T"}); err != nil {
		t.Fatalf("Append after healing: %v", err)
	}
	recs, _ := fs.Replay()
	if len(recs) != 2 || recs[0].EventID != "a" || recs[1].EventID != "b" {
		t.Fatalf("Replay = %+v, want [a b]", recs)
	}
}

func TestFileStore_probeLeavesNoFileBehindAndFailsInAMissingDir(t *testing.T) {
	dir := t.TempDir()
	fs, err := NewFileStore(filepath.Join(dir, "queue.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fs.Close() }()
	if err := fs.Probe(); err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if _, err := os.Stat(fs.path + probeSuffix); !os.IsNotExist(err) {
		t.Fatalf("probe file left behind: %v", err)
	}
	fs.path = filepath.Join(dir, "gone", "queue.jsonl")
	if err := fs.Probe(); err == nil {
		t.Fatal("Probe succeeded although the directory does not exist")
	}
}

// --- value helpers ----------------------------------------------------------

func TestLimitStatus_percent(t *testing.T) {
	for _, tc := range []struct {
		bytes, hard int64
		want        float64
	}{
		{0, 100, 0},
		{50, 100, 50},
		{100, 100, 100},
		{150, 100, 150},
		{10, 0, 0}, // no limit set: no percent
	} {
		if got := (LimitStatus{Bytes: tc.bytes, HardBytes: tc.hard}).Percent(); got != tc.want {
			t.Errorf("Percent(%d of %d) = %v, want %v", tc.bytes, tc.hard, got, tc.want)
		}
	}
}

func TestRejectReason(t *testing.T) {
	if _, ok := RejectReason(errors.New("boom")); ok {
		t.Fatal("a plain error is not an admission refusal")
	}
	if r, ok := RejectReason(&ErrLogUnwritable{}); !ok || r != ReasonLogUnwritable {
		t.Fatalf("RejectReason(unwritable) = %q,%v", r, ok)
	}
	if msg := (&ErrLogUnwritable{}).Error(); !strings.HasPrefix(msg, "log_unwritable: ") {
		t.Fatalf("zero-cause message = %q", msg)
	}
}

// Without WithLogLimits nothing changes: no halt, no rejection, however large the
// log.
func TestLimits_disabledWithoutOption(t *testing.T) {
	mem := NewMemStore()
	q, err := New(mem)
	if err != nil {
		t.Fatal(err)
	}
	mem.SetLogSize(1 << 40)
	q.EnforceLogLimits()
	if q.EmittersHalted() || q.LimitStatus().State != StateOK {
		t.Fatal("limits were enforced although none were configured")
	}
	if _, err := q.Enqueue(evt("e1", "T")); err != nil {
		t.Fatal(err)
	}
}

// Racing Enqueue, the controller, dispatch and a store that fails and heals must
// be clean under -race and must never wedge or lose an admitted event.
func TestLimits_concurrentEnqueueControllerAndFlakyStore(t *testing.T) {
	store := newFailEveryAppendStore()
	store.err = nil
	store.probeOK = func() bool {
		store.mu.Lock()
		defer store.mu.Unlock()
		return store.err == nil
	}
	q, clk := newLimitQueue(t, probingFailStore{store}, 900, 1<<20)
	l := newListener("h", "T")
	q.Register(l)

	var wg sync.WaitGroup
	stop := make(chan struct{})
	var admitted, refused int64
	var mu sync.Mutex
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 60; i++ {
				_, err := q.Enqueue(evtUntil("e"+strconv.Itoa(w)+"-"+strconv.Itoa(i), "T", time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)))
				mu.Lock()
				if err != nil {
					if _, ok := RejectReason(err); !ok {
						t.Errorf("unclassified error: %v", err)
					}
					refused++
				} else {
					admitted++
				}
				mu.Unlock()
			}
		}(w)
	}
	wg.Add(1)
	go func() { // the disk flaps
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			store.mu.Lock()
			if i%2 == 0 {
				store.err = syscall.ENOSPC
			} else {
				store.err = nil
			}
			store.mu.Unlock()
			time.Sleep(time.Millisecond)
		}
	}()
	wg.Add(1)
	go func() { // the drive loop
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			q.EnforceLogLimits()
			q.Dispatch()
			q.Expire()
			_ = q.LimitStatus()
			_ = q.EmittersHalted()
			time.Sleep(time.Millisecond)
		}
	}()

	// Let the enqueuers finish (they are the bounded workers), then stop the rest.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			mu.Lock()
			n := admitted + refused
			mu.Unlock()
			if n >= 240 {
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("enqueuers wedged")
	}
	close(stop)
	wg.Wait()

	store.heal()
	clk.advance(probeInterval + time.Second) // past any back-off after a failed probe
	q.EnforceLogLimits()
	if _, err := q.Enqueue(evt("final", "T")); err != nil {
		t.Fatalf("Enqueue after the disk healed: %v", err)
	}
	// every admitted event is in the queue
	mu.Lock()
	a := admitted
	mu.Unlock()
	if got := q.DepthByType()["T"]; int64(got) < a {
		t.Fatalf("queue holds %d events but %d were admitted", got, a)
	}
}
