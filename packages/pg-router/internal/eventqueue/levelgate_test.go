package eventqueue

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/internal/levels"
)

// diskScale is the generic scale the admission tests use.
var diskScale = levels.Scale{Levels: []string{"PLENTY", "OK", "LOW", "CRITICAL"}, Unknown: "PLENTY"}

func testRegistry() levels.Registry {
	return levels.Registry{"disk": diskScale, "net": {Levels: []string{"UP", "DOWN"}, Unknown: "UP"}}
}

// reqDiskOK is "disk >= OK".
func reqDiskOK() levels.Requirement {
	return levels.Requirement{Clauses: []levels.Clause{{Index: 0, Text: "disk >= OK", Resource: "disk", Allowed: []string{"PLENTY", "OK"}}}}
}

// testLevels is a mutable level source.
type testLevels struct {
	mu  sync.Mutex
	now func() time.Time
	cur map[string]string
}

func newTestLevels(now func() time.Time) *testLevels {
	return &testLevels{now: now, cur: map[string]string{}}
}

func (l *testLevels) set(resource, level string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cur[resource] = level
}

func (l *testLevels) snapshot() levels.Snapshot {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := levels.Snapshot{Now: l.now(), Levels: map[string]levels.Level{}}
	for r, v := range l.cur {
		s.Levels[r] = levels.Level{Value: v, State: levels.StateHeld, Source: "sensor", Owner: "s", Since: l.now()}
	}
	return s
}

// recordingLevelObserver captures LevelObserver callbacks.
type recordingLevelObserver struct {
	mu        sync.Mutex
	blocked   []string // "participant/work/resource"
	kinds     []string // why each block failed: level, unknown or stale
	pausedAt  []string // "role/resource/level"
	resumedAt []string // "role/resource/level"
	drops     []string // "evtType/role/resource"
	paused    []string // "role/resource"
	resumed   []string
	legacy    []string // "type/owner"
	legacyAt  []time.Time
	onBlock   func()
}

func (o *recordingLevelObserver) OnRequirementBlocked(p, k, r, why string) {
	o.mu.Lock()
	o.blocked = append(o.blocked, p+"/"+k+"/"+r)
	o.kinds = append(o.kinds, why)
	cb := o.onBlock
	o.mu.Unlock()
	if cb != nil {
		cb()
	}
}

func (o *recordingLevelObserver) OnRequirementDrop(e, role, r string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.drops = append(o.drops, e+"/"+role+"/"+r)
}

func (o *recordingLevelObserver) OnRolePaused(role, r, level string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.paused = append(o.paused, role+"/"+r)
	o.pausedAt = append(o.pausedAt, role+"/"+r+"/"+level)
}

func (o *recordingLevelObserver) OnRoleResumed(role, r, level string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.resumed = append(o.resumed, role)
	o.resumedAt = append(o.resumedAt, role+"/"+r+"/"+level)
}

func (o *recordingLevelObserver) OnLegacyGateDropped(t, owner string, exp time.Time) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.legacy = append(o.legacy, t+"/"+owner)
	o.legacyAt = append(o.legacyAt, exp)
}

func newLevelQueue(t *testing.T, store Store, c *mockClock, extra ...Option) (*Queue, *testLevels, *recordingLevelObserver) {
	t.Helper()
	src := newTestLevels(c.now)
	obs := &recordingLevelObserver{}
	opts := append([]Option{WithClock(c.now), WithLevelSource(src.snapshot), WithRegistry(testRegistry()), WithLevelObserver(obs)}, extra...)
	q, err := New(store, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return q, src, obs
}

func TestLevel_NoLevelSourceBlocksNothing(t *testing.T) {
	c := newClock()
	q, err := New(NewMemStore(), WithClock(c.now))
	if err != nil {
		t.Fatal(err)
	}
	l := newListener("L", "work")
	q.Register(l)
	q.RegisterListener("L", reqDiskOK())
	if err := q.CheckPull("L"); err != nil {
		t.Fatalf("no level source: pull = %v, want nil", err)
	}
	if _, b := q.EmitterBlockedBy("src"); b {
		t.Fatal("no level source: emitter must not be blocked")
	}
	if _, err := q.Enqueue(Event{ID: "e1", Type: "work", ExpiresAt: c.in(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	q.Dispatch()
	if !slices.Equal(l.accepted, []string{"e1"}) {
		t.Fatalf("accepted %v, want [e1]", l.accepted)
	}
}

func TestLevel_BlockedListenerIsNotDispatchedUnblockedStillIs(t *testing.T) {
	c := newClock()
	q, src, obs := newLevelQueue(t, NewMemStore(), c)
	blocked := newListener("blocked", "work")
	free := newListener("free", "work")
	q.Register(blocked)
	q.Register(free)
	q.RegisterListener("blocked", reqDiskOK())
	src.set("disk", "LOW")
	if _, err := q.Enqueue(Event{ID: "e1", Type: "work", ExpiresAt: c.in(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	q.Dispatch()
	if len(blocked.offered) != 0 {
		t.Fatalf("a level-blocked listener was offered %v", blocked.offered)
	}
	if !slices.Equal(free.accepted, []string{"e1"}) {
		t.Fatalf("a listener with no requirement accepted %v, want [e1]", free.accepted)
	}
	if !slices.Contains(obs.blocked, "blocked/dispatch/disk") || !slices.Equal(obs.paused, []string{"blocked/disk"}) {
		t.Fatalf("blocked=%v paused=%v, want blocked/dispatch/disk and one pause", obs.blocked, obs.paused)
	}
	if !slices.Contains(obs.kinds, "level") || !slices.Equal(obs.pausedAt, []string{"blocked/disk/LOW"}) {
		t.Fatalf("kinds=%v pausedAt=%v, want a block of kind level and a pause at disk/LOW", obs.kinds, obs.pausedAt)
	}
	q.Dispatch()
	if len(obs.paused) != 1 {
		t.Fatalf("paused = %v: a pause is reported on the transition only", obs.paused)
	}
	// The event stayed QUEUED: when the level recovers it is delivered (ADM-3).
	src.set("disk", "OK")
	q.Dispatch()
	if !slices.Equal(obs.resumedAt, []string{"blocked/disk/OK"}) {
		t.Fatalf("resumedAt = %v, want the blocking resource and the level it is at now", obs.resumedAt)
	}
	if !slices.Equal(blocked.accepted, []string{"e1"}) || !slices.Equal(obs.resumed, []string{"blocked"}) {
		t.Fatalf("after recovery accepted=%v resumed=%v, want [e1] and [blocked]", blocked.accepted, obs.resumed)
	}
}

// ADM-7: an event whose final attempt falls due while the role is blocked goes
// away for it, and the drop carries the role and the resource.
func TestLevel_FinalAttemptLostWhileBlockedAndCounted(t *testing.T) {
	c := newClock()
	q, src, obs := newLevelQueue(t, NewMemStore(), c)
	blocked := newListener("blocked", "work")
	free := newListener("free", "work")
	q.Register(blocked)
	q.Register(free)
	q.RegisterListener("blocked", reqDiskOK())
	src.set("disk", "CRITICAL")
	// The default event is BORN EXPIRED: its first attempt is its last.
	if _, err := q.Enqueue(Event{ID: "e1", Type: "work"}); err != nil {
		t.Fatal(err)
	}
	q.Dispatch()
	if len(blocked.offered) != 0 {
		t.Fatalf("blocked listener offered %v", blocked.offered)
	}
	if !slices.Equal(free.accepted, []string{"e1"}) {
		t.Fatalf("unblocked listener accepted %v", free.accepted)
	}
	if !slices.Equal(obs.drops, []string{"work/blocked/disk"}) {
		t.Fatalf("drops = %v, want one drop naming the event type, role and resource", obs.drops)
	}
	if dropped := q.Expire(); dropped != 1 {
		t.Fatalf("Expire dropped %d, want 1", dropped)
	}
	src.set("disk", "PLENTY")
	q.Dispatch()
	if len(blocked.offered) != 0 || len(obs.drops) != 1 {
		t.Fatalf("a dropped event must not reappear or be counted twice: offered=%v drops=%v", blocked.offered, obs.drops)
	}
}

type midOfferListener struct {
	*fakeListener
	onOffer func()
}

func (m *midOfferListener) Offer(o Offering) OfferResult {
	if m.onOffer != nil {
		m.onOffer()
		m.onOffer = nil
	}
	return m.fakeListener.Offer(o)
}

// ADM-1: a level change never touches work in flight; pushes are still accepted.
func TestLevel_PushStillAcceptedAndInFlightUnaffected(t *testing.T) {
	c := newClock()
	st := NewMemStore()
	q, src, _ := newLevelQueue(t, st, c)
	l := &midOfferListener{fakeListener: newListener("L", "work"), onOffer: func() { src.set("disk", "CRITICAL") }}
	q.Register(l)
	q.RegisterListener("L", reqDiskOK())
	if _, err := q.Enqueue(Event{ID: "e1", Type: "work", ExpiresAt: c.in(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if accepted := q.Dispatch(); accepted != 1 {
		t.Fatalf("Dispatch accepted %d, want 1: an ack under a level change is still processed", accepted)
	}
	recs, _ := st.Replay()
	var sawAccept bool
	for _, r := range recs {
		if r.Op == opAccept && r.EventID == "e1" {
			sawAccept = true
		}
	}
	if !sawAccept {
		t.Fatal("the acceptance was not persisted")
	}
	res, err := q.Enqueue(Event{ID: "e2", Type: "work", ExpiresAt: c.in(time.Hour)})
	if err != nil || res != Enqueued {
		t.Fatalf("push while blocked = (%v, %v), want Enqueued", res, err)
	}
	q.Dispatch()
	if len(l.accepted) != 1 {
		t.Fatalf("blocked listener accepted %v; the pushed event must wait", l.accepted)
	}
}

func TestLevel_CheckPullRejectsBlockedListenerNamingTheResource(t *testing.T) {
	c := newClock()
	q, src, obs := newLevelQueue(t, NewMemStore(), c)
	q.RegisterListener("L", reqDiskOK())
	if err := q.CheckPull("L"); err != nil {
		t.Fatalf("level ok: pull = %v, want nil", err)
	}
	src.set("disk", "LOW")
	err := q.CheckPull("L")
	var lb *LevelBlockedError
	if !asLevelBlocked(err, &lb) || lb.Participant != "L" || firstResource(lb.Result) != "disk" || lb.Result.Failing[0].Level != "LOW" {
		t.Fatalf("pull = %v, want a LevelBlockedError naming disk at LOW", err)
	}
	if !strings.Contains(err.Error(), "disk") || !strings.Contains(err.Error(), "LOW") {
		t.Fatalf("error text %q must name the resource and level", err)
	}
	if err := q.CheckPull("unregistered"); err != nil {
		t.Fatalf("a listener without a requirement is admitted, got %v", err)
	}
	if !slices.Contains(obs.blocked, "L/pull/disk") {
		t.Fatalf("blocked = %v, want L/pull/disk", obs.blocked)
	}
}

func asLevelBlocked(err error, target **LevelBlockedError) bool {
	if lb, ok := err.(*LevelBlockedError); ok {
		*target = lb
		return lb.Is(ErrLevelBlocked)
	}
	return false
}

// 2.3: an emitter is polled when ANY enabled consumer could run.
func TestLevel_EmitterBlockedByAnyConsumer(t *testing.T) {
	c := newClock()
	q, src, obs := newLevelQueue(t, NewMemStore(), c)
	netUp := levels.Requirement{Clauses: []levels.Clause{{Resource: "net", Text: "net >= UP", Allowed: []string{"UP"}}}}
	q.RegisterEmitter("src", []levels.Consumer{{Req: reqDiskOK(), Enabled: true}, {Req: netUp, Enabled: true}})
	q.RegisterEmitter("off", []levels.Consumer{{Req: reqDiskOK(), Enabled: false}})
	q.RegisterEmitter("plain", []levels.Consumer{{Enabled: true}})
	if _, b := q.EmitterBlockedBy("src"); b {
		t.Fatal("both consumers admitted: not blocked")
	}
	src.set("disk", "LOW")
	if _, b := q.EmitterBlockedBy("src"); b {
		t.Fatal("the net consumer could still run: the emitter is polled (any consumer)")
	}
	src.set("net", "DOWN")
	if r, b := q.EmitterBlockedBy("src"); !b || r != "disk" {
		t.Fatalf("EmitterBlockedBy = (%q, %v), want (disk, true) when no consumer could run", r, b)
	}
	if _, b := q.EmitterBlockedBy("off"); b {
		t.Fatal("only disabled consumers: not blocked here (over-poll, never under-poll)")
	}
	if _, b := q.EmitterBlockedBy("plain"); b {
		t.Fatal("a consumer with no requirement always runs")
	}
	if _, b := q.EmitterBlockedBy("unknown-emitter"); b {
		t.Fatal("an emitter with no registered consumers is not blocked")
	}
	if !slices.Contains(obs.blocked, "src/poll/disk") {
		t.Fatalf("blocked = %v, want src/poll/disk", obs.blocked)
	}
}

// ADM-3: a retry (decline before expiry) passes through admission again.
func TestLevel_DeclineAndRetryReenterAdmission(t *testing.T) {
	c := newClock()
	q, src, _ := newLevelQueue(t, NewMemStore(), c)
	l := newListener("L", "work")
	l.busyRemaining["e1"] = 1
	q.Register(l)
	q.RegisterListener("L", reqDiskOK())
	if _, err := q.Enqueue(Event{ID: "e1", Type: "work", ExpiresAt: c.in(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	q.Dispatch() // declined once
	if len(l.offered) != 1 {
		t.Fatalf("offered %v, want one offer", l.offered)
	}
	src.set("disk", "CRITICAL")
	c.advance(10 * time.Minute) // past any retry backoff
	q.Dispatch()
	if len(l.offered) != 1 {
		t.Fatalf("the retry was offered while blocked: %v", l.offered)
	}
	src.set("disk", "OK")
	q.Dispatch()
	if !slices.Equal(l.accepted, []string{"e1"}) {
		t.Fatalf("after recovery accepted %v, want [e1]", l.accepted)
	}
}

// ADM-3: the shutdown re-offer (Kick's chain) is also a site that honors levels.
func TestLevel_ChainNextHonorsLevels(t *testing.T) {
	c := newClock()
	q, src, _ := newLevelQueue(t, NewMemStore(), c)
	l := newListener("L", "work")
	q.Register(l)
	q.RegisterListener("L", reqDiskOK())
	for _, id := range []string{"e1", "e2"} {
		if _, err := q.Enqueue(Event{ID: id, Type: "work", ExpiresAt: c.in(time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	ls := q.listeners[0]
	q.mu.Lock()
	if _, ok := q.chainNextLocked(ls, "dsp-x", c.now()); !ok {
		q.mu.Unlock()
		t.Fatal("admitted: chain must mint the next offer")
	}
	delete(q.inFlight, "L")
	src.set("disk", "CRITICAL")
	_, ok := q.chainNextLocked(ls, "dsp-y", c.now())
	q.mu.Unlock()
	if ok {
		t.Fatal("blocked: chain must not mint an offer")
	}
}

// ADM-2: the start stagger.
func staggerQueue(t *testing.T, ids ...string) (*Queue, *testLevels, *mockClock, map[string]*fakeListener) {
	t.Helper()
	c := newClock()
	q, src, _ := newLevelQueue(t, NewMemStore(), c, WithStartStagger(30*time.Second))
	ls := map[string]*fakeListener{}
	for _, id := range ids {
		l := newListener(id, "work")
		ls[id] = l
		q.Register(l)
		// Every registration has a clause on disk, which is below best.
		q.RegisterListener(id, reqDiskOK())
	}
	src.set("disk", "OK")
	return q, src, c, ls
}

func offeredIDs(ls map[string]*fakeListener) []string {
	var out []string
	for id, l := range ls {
		if len(l.offered) > 0 {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

func TestLevel_StaggerSpacesStartsOnTheQueueClock(t *testing.T) {
	q, _, c, ls := staggerQueue(t, "a", "b", "c")
	if _, err := q.Enqueue(Event{ID: "e1", Type: "work", ExpiresAt: c.in(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	// The rotation offset is the start time in Unix seconds modulo the count.
	offset := int(q.adm.startedAt.Unix() % 3)
	order := []string{"a", "b", "c"}
	order = append(order[offset:], order[:offset]...)
	q.Dispatch()
	if got := offeredIDs(ls); !slices.Equal(got, []string{order[0]}) {
		t.Fatalf("at start only the first of the rotated order %v may start, got %v", order, got)
	}
	// Each listener has its own FIFO cursor: all three would take e1 once released.
	c.advance(30 * time.Second)
	q.Dispatch()
	if got := offeredIDs(ls); len(got) != 2 {
		t.Fatalf("after one interval two registrations have started, got %v", got)
	}
	c.advance(30 * time.Second)
	q.Dispatch()
	if got := offeredIDs(ls); len(got) != 3 {
		t.Fatalf("after two intervals all have started, got %v", got)
	}
}

func TestLevel_StaggerExplainsAsItsOwnReasonNotARequirementBlock(t *testing.T) {
	q, _, c, ls := staggerQueue(t, "a", "b")
	offset := int(q.adm.startedAt.Unix() % 2)
	late := []string{"a", "b"}[(1+offset)%2]
	if _, err := q.Enqueue(Event{ID: "e1", Type: "work", ExpiresAt: c.in(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	res := q.Explain(late)
	if !res.Admit || res.Reason != ReasonStartStagger || len(res.Failing) != 0 {
		t.Fatalf("Explain = %+v, want admit with reason %q and no failing clause", res, ReasonStartStagger)
	}
	q.Dispatch()
	if len(ls[late].offered) != 0 {
		t.Fatalf("%s was offered inside its stagger slot", late)
	}
	c.advance(30 * time.Second)
	if res := q.Explain(late); res.Reason != "" {
		t.Fatalf("after its slot the reason must clear, got %q", res.Reason)
	}
}

func TestLevel_StaggerOneOfferPerListenerThenFree(t *testing.T) {
	q, _, c, ls := staggerQueue(t, "a", "b")
	for _, id := range []string{"e1", "e2", "e3"} {
		if _, err := q.Enqueue(Event{ID: id, Type: "work", ExpiresAt: c.in(time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	c.advance(30 * time.Second) // both slots open
	q.Dispatch()
	q.Dispatch()
	q.Dispatch()
	for id, l := range ls {
		if len(l.accepted) != 3 {
			t.Fatalf("%s accepted %v: once started a registration is no longer delayed", id, l.accepted)
		}
	}
}

func TestLevel_StaggerDoesNotDelayRegistrationWithoutNonBestClause(t *testing.T) {
	q, src, c, ls := staggerQueue(t, "a", "b", "c")
	src.set("disk", "PLENTY") // every resource at best
	if _, err := q.Enqueue(Event{ID: "e1", Type: "work", ExpiresAt: c.in(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	q.Dispatch()
	if got := offeredIDs(ls); len(got) != 3 {
		t.Fatalf("no clause on a non-best resource: nobody is staggered, got %v", got)
	}
	// And a registration with no clause at all is never delayed.
	q2, src2, c2, _ := staggerQueue(t, "x")
	free := newListener("free", "work")
	q2.Register(free)
	src2.set("disk", "OK")
	if _, err := q2.Enqueue(Event{ID: "e1", Type: "work", ExpiresAt: c2.in(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	q2.Dispatch()
	if len(free.offered) != 1 {
		t.Fatal("a registration without a requirement was delayed by the stagger")
	}
}

func TestLevel_StaggerNeverAppliesToAFinalAttemptOffer(t *testing.T) {
	q, _, _, ls := staggerQueue(t, "a", "b", "c")
	// BORN EXPIRED: every offer is a final attempt.
	if _, err := q.Enqueue(Event{ID: "e1", Type: "work"}); err != nil {
		t.Fatal(err)
	}
	q.Dispatch()
	if got := offeredIDs(ls); len(got) != 3 {
		t.Fatalf("a final-attempt offer must not be staggered; offered by %v", got)
	}
}

func TestLevel_StaggerInChainNextOpensNoEmptyWindow(t *testing.T) {
	q, _, c, _ := staggerQueue(t, "a", "b")
	for _, id := range []string{"e1", "e2"} {
		if _, err := q.Enqueue(Event{ID: id, Type: "work", ExpiresAt: c.in(time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	offset := int(q.adm.startedAt.Unix() % 2)
	late := q.listeners[(1+offset)%2]
	q.mu.Lock()
	defer q.mu.Unlock()
	if _, ok := q.chainNextLocked(late, "dsp-1", c.now()); ok {
		t.Fatal("the stagger must hold the late registration in chainNextLocked too")
	}
	if _, busy := q.inFlight[late.l.ID()]; busy {
		t.Fatal("a staggered chain must not leave an in-flight slot behind")
	}
	if len(q.custody) != 0 {
		t.Fatalf("a staggered chain must mint no offer, custody = %v", q.custody)
	}
}

func TestLevel_StaggerIsNotPersisted(t *testing.T) {
	st := NewMemStore()
	c := newClock()
	q, src, _ := newLevelQueue(t, st, c, WithStartStagger(30*time.Second))
	for _, id := range []string{"a", "b"} {
		q.Register(newListener(id, "work"))
		q.RegisterListener(id, reqDiskOK())
	}
	src.set("disk", "OK")
	if _, err := q.Enqueue(Event{ID: "e1", Type: "work", ExpiresAt: c.in(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	q.Dispatch()
	recs, _ := st.Replay()
	for _, r := range recs {
		if strings.Contains(string(r.Op), "stagger") {
			t.Fatalf("a stagger record was persisted: %+v", r)
		}
	}
}

// Legacy gate records: ignored on replay, dropped by compaction.
func legacyGateLog(now time.Time) []Record {
	return []Record{
		{Op: opGateSet, GateType: "SYSTEM_PAUSE", Owner: "operator", At: now.Add(-time.Hour)},
		{Op: opGateSet, GateType: "BEAD_SERVER_DOWN", Owner: "bd", At: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour)},
		{Op: opGateSet, GateType: "LAPSED", Owner: "x", At: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-time.Hour)},
		{Op: opGateSet, GateType: "CLEARED", Owner: "x", At: now.Add(-2 * time.Hour)},
		{Op: opGateCleared, GateType: "CLEARED", Owner: "op", At: now.Add(-time.Hour)},
		{Op: opGateSet, GateType: "EXPIRED", Owner: "x", At: now.Add(-2 * time.Hour), ExpiresAt: now.Add(time.Hour)},
		{Op: opGateExpired, GateType: "EXPIRED", At: now.Add(-time.Minute)},
	}
}

func TestLevel_LegacyGateRecordsAreIgnoredOnReplay(t *testing.T) {
	c := newClock()
	st := NewMemStore()
	for _, r := range legacyGateLog(time.Now()) {
		if _, err := st.Append(r); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.Append(Record{Op: opEnqueue, EventID: "e1", Type: "work", At: c.now(), ExpiresAt: c.in(time.Hour), EnqueuedAt: c.now()}); err != nil {
		t.Fatal(err)
	}
	q, _, obs := newLevelQueue(t, st, c)
	q.Register(newListener("L", "work"))
	if q.DepthByType()["work"] != 1 {
		t.Fatalf("depth = %v, want the event restored beside the ignored gate records", q.DepthByType())
	}
	if len(obs.legacy) != 0 {
		t.Fatalf("replay reported %v; only the compaction pass reports a dropped gate", obs.legacy)
	}
}

func fileStoreWithLegacyGates(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "queue.jsonl")
	fs, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range legacyGateLog(time.Now()) {
		if _, err := fs.Append(r); err != nil {
			t.Fatal(err)
		}
	}
	if err := fs.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func logOps(t *testing.T, path string) map[opKind]int {
	t.Helper()
	fs, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fs.Close() }()
	recs, err := fs.Replay()
	if err != nil {
		t.Fatal(err)
	}
	ops := map[opKind]int{}
	for _, r := range recs {
		ops[r.Op]++
	}
	return ops
}

// Startup compaction runs BEFORE replay: the event comes from the compaction
// pass, once per ACTIVE legacy gate, and the gate records are gone afterward.
func TestLevel_StartupCompactionDropsActiveLegacyGatesAndReportsEach(t *testing.T) {
	path := fileStoreWithLegacyGates(t)
	fs, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fs.Close() }()
	obs := &recordingLevelObserver{}
	if _, err := New(fs, WithLevelObserver(obs), WithCompaction(1<<30, true)); err != nil {
		t.Fatal(err)
	}
	if want := []string{"BEAD_SERVER_DOWN/bd", "SYSTEM_PAUSE/operator"}; !slices.Equal(obs.legacy, want) {
		t.Fatalf("dropped-gate events = %v, want one per ACTIVE gate %v (not the lapsed, cleared or expired ones)", obs.legacy, want)
	}
	if !obs.legacyAt[0].After(time.Now()) || !obs.legacyAt[1].IsZero() {
		t.Fatalf("expiries = %v, want the leased gate's expiry and zero for the no-expiry gate", obs.legacyAt)
	}
	_ = fs.Close()
	ops := logOps(t, path)
	if ops[opGateSet]+ops[opGateCleared]+ops[opGateExpired] != 0 {
		t.Fatalf("legacy gate records survived compaction: %v", ops)
	}
}

// Compaction on start is optional: with it off the event comes from the next
// compaction that runs, never from replay, and never twice.
func TestLevel_LegacyGateReportedByLaterCompactionWhenStartupOff(t *testing.T) {
	path := fileStoreWithLegacyGates(t)
	fs, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fs.Close() }()
	obs := &recordingLevelObserver{}
	q, err := New(fs, WithLevelObserver(obs), WithCompaction(1<<30, false))
	if err != nil {
		t.Fatal(err)
	}
	if len(obs.legacy) != 0 {
		t.Fatalf("replay reported %v", obs.legacy)
	}
	if _, err := q.CompactNow(); err != nil {
		t.Fatal(err)
	}
	if len(obs.legacy) != 2 {
		t.Fatalf("compaction reported %v, want 2 active gates", obs.legacy)
	}
	if _, err := q.CompactNow(); err != nil {
		t.Fatal(err)
	}
	if len(obs.legacy) != 2 {
		t.Fatalf("a second compaction re-reported: %v", obs.legacy)
	}
}

// Level set, replay and dispatch together under -race; an observer may call
// back into the queue without deadlock.
func TestLevel_ConcurrentSetReplayDispatchAndObserverReentry(t *testing.T) {
	c := newClock()
	q, src, obs := newLevelQueue(t, NewMemStore(), c)
	obs.onBlock = func() { _ = q.DepthByType(); _ = q.CheckPull("again") }
	q.Register(newListener("L", "work"))
	q.RegisterListener("L", reqDiskOK())
	q.RegisterListener("again", reqDiskOK())
	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(3)
	go func() {
		defer wg.Done()
		levelsCycle := []string{"PLENTY", "LOW", "OK", "CRITICAL"}
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
				src.set("disk", levelsCycle[i%4])
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			q.Dispatch()
			_ = q.CheckPull("L")
			_, _ = q.EmitterBlockedBy("src")
			_ = q.Explain("L")
		}
		close(stop)
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			q.RegisterEmitter("src", []levels.Consumer{{Req: reqDiskOK(), Enabled: true}})
			q.RegisterListener("L", reqDiskOK())
			_, _ = q.Enqueue(Event{ID: "e" + string(rune('a'+i%26)) + string(rune('a'+i/26)), Type: "work", ExpiresAt: c.in(time.Hour)})
		}
	}()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("deadlock: concurrent level changes, registration and dispatch did not finish")
	}
}

// The evaluation (levels.Decide) is called from exactly the admission sites:
// levelAdmitLocked (snapshotLocked, chainNextLocked, Explain), CheckPull and
// EmitterBlockedBy. A fifth site makes this test fail.
func TestLevel_EvaluationIsCalledOnlyFromTheAdmissionSites(t *testing.T) {
	fset := token.NewFileSet()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	callers := map[string][]string{} // function -> callees of interest it contains
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(fset, f, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range file.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				switch fn := call.Fun.(type) {
				case *ast.SelectorExpr:
					if x, ok := fn.X.(*ast.Ident); ok && x.Name == "levels" && fn.Sel.Name == "Decide" {
						callers[fd.Name.Name] = append(callers[fd.Name.Name], "Decide")
					}
					if fn.Sel.Name == "levelAdmitLocked" {
						callers[fd.Name.Name] = append(callers[fd.Name.Name], "levelAdmitLocked")
					}
				}
				return true
			})
		}
	}
	var decide, admit []string
	for fn, callees := range callers {
		for _, c := range callees {
			switch c {
			case "Decide":
				decide = append(decide, fn)
			case "levelAdmitLocked":
				admit = append(admit, fn)
			}
		}
	}
	sort.Strings(decide)
	sort.Strings(admit)
	if want := []string{"CheckPull", "EmitterBlockedBy", "levelAdmitLocked"}; !slices.Equal(decide, want) {
		t.Fatalf("levels.Decide is called from %v, want exactly %v", decide, want)
	}
	if want := []string{"Explain", "chainNextLocked", "snapshotLocked"}; !slices.Equal(admit, want) {
		t.Fatalf("levelAdmitLocked is called from %v, want exactly %v", admit, want)
	}
}

func TestLevel_PausedRolesListsOnlyBlockedListenersWithoutSignals(t *testing.T) {
	c := newClock()
	q, src, obs := newLevelQueue(t, NewMemStore(), c)
	q.Register(newListener("blocked", "work"))
	q.Register(newListener("free", "work"))
	q.RegisterListener("blocked", reqDiskOK())
	if got := q.PausedRoles(); len(got) != 0 {
		t.Fatalf("PausedRoles with no reading = %v, want none", got)
	}
	src.set("disk", "LOW")
	got := q.PausedRoles()
	if len(got) != 1 || got[0].Role != "blocked" || len(got[0].Result.Failing) != 1 || got[0].Result.Failing[0].ClauseText != "disk >= OK" {
		t.Fatalf("PausedRoles = %+v, want blocked by disk >= OK", got)
	}
	if len(obs.paused) != 0 || len(obs.blocked) != 0 {
		t.Fatalf("PausedRoles must be read-only; paused=%v blocked=%v", obs.paused, obs.blocked)
	}
}

func TestLevel_IdleExceptPausedIgnoresWorkOnlyABlockedListenerCouldTake(t *testing.T) {
	c := newClock()
	q, src, _ := newLevelQueue(t, NewMemStore(), c)
	q.Register(newListener("blocked", "work"))
	q.RegisterListener("blocked", reqDiskOK())
	src.set("disk", "LOW")
	if _, err := q.Enqueue(Event{ID: "e1", Type: "work", ExpiresAt: c.in(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if q.Idle() {
		t.Fatal("precondition: plain Idle is held open by the blocked listener's event")
	}
	if !q.IdleExceptPaused() {
		t.Fatal("IdleExceptPaused must not wait on an event only a blocked listener could take")
	}
	src.set("disk", "OK")
	if q.IdleExceptPaused() {
		t.Fatal("once the role is admitted its event is deliverable work again")
	}
}

func TestLevel_IdleExceptPausedStillWaitsForAnAdmittedListener(t *testing.T) {
	c := newClock()
	q, src, _ := newLevelQueue(t, NewMemStore(), c)
	q.Register(newListener("blocked", "work"))
	q.Register(newListener("free", "work"))
	q.RegisterListener("blocked", reqDiskOK())
	src.set("disk", "LOW")
	if _, err := q.Enqueue(Event{ID: "e1", Type: "work", ExpiresAt: c.in(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if q.IdleExceptPaused() {
		t.Fatal("a free listener still owes the event; the queue is not idle")
	}
	q.Dispatch()
	if !q.IdleExceptPaused() {
		t.Fatal("after the free listener accepted, only the blocked one remains: idle")
	}
}
