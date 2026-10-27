package changes

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/pipeline"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

const (
	feedInst     = "owning-demo"
	feedIdentity = "store-A"
)

// feedEvents is the shared, ordered log of what the fake feeder and fake
// ingester were asked, so a test can assert call order across both.
type feedEvents struct {
	mu sync.Mutex
	ev []string
}

func (f *feedEvents) add(format string, args ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ev = append(f.ev, fmt.Sprintf(format, args...))
}

func (f *feedEvents) list() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.ev...)
}

func (f *feedEvents) index(prefix string) int {
	for i, e := range f.list() {
		if strings.HasPrefix(e, prefix) {
			return i
		}
	}
	return -1
}

type listCall struct {
	Type, Query, Consumer, Backend string
	Reset                          bool
}

// fakeFeeder answers ListFeed from a script (one entry per call, the last one
// repeating) and records every call.
type fakeFeeder struct {
	ev      *feedEvents
	answers []gather.FeedResult
	listErr error
	ackErr  error
	delay   time.Duration

	mu    sync.Mutex
	lists []listCall
	acks  []int64
	// inflight is the number of polls between ListFeed and AckFeed; maxIn its
	// high-water mark.
	inflight, maxIn atomic.Int32
}

func (f *fakeFeeder) ListFeed(_ context.Context, typ, query, consumer, backend string, reset bool) (gather.FeedResult, error) {
	f.mu.Lock()
	n := len(f.lists)
	f.lists = append(f.lists, listCall{typ, query, consumer, backend, reset})
	f.mu.Unlock()
	f.ev.add("list reset=%v", reset)
	if cur := f.inflight.Add(1); cur > f.maxIn.Load() {
		f.maxIn.Store(cur)
	}
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	if f.listErr != nil {
		f.inflight.Add(-1)
		return gather.FeedResult{}, f.listErr
	}
	if len(f.answers) == 0 {
		return gather.FeedResult{}, nil
	}
	if n >= len(f.answers) {
		n = len(f.answers) - 1
	}
	return f.answers[n], nil
}

func (f *fakeFeeder) AckFeed(_ context.Context, _, query, consumer, backend string, nextSeq int64) (int64, error) {
	f.mu.Lock()
	f.acks = append(f.acks, nextSeq)
	f.mu.Unlock()
	f.ev.add("ack %d query=%q consumer=%s backend=%s", nextSeq, query, consumer, backend)
	f.inflight.Add(-1)
	if f.ackErr != nil {
		return 0, f.ackErr
	}
	return nextSeq, nil
}

// fakeIngester commits a row the way the pipeline does (entity write, change
// row with the row's own kinds, marker in the same write) against the real
// store, and records failures in the real failed-ingest table.
type fakeIngester struct {
	t      *testing.T
	st     *store.Store
	ev     *feedEvents
	failID map[string]bool

	retries atomic.Int32
	rows    []pipeline.FeedRowInput
}

func (g *fakeIngester) IngestFeedRow(_ context.Context, in pipeline.FeedRowInput) (pipeline.FeedRowResult, error) {
	g.ev.add("ingest %s seq=%d", in.Row.EntityID, in.Row.Seq)
	g.rows = append(g.rows, in)
	replay := in.Row.Seq == 0
	if !replay {
		skip, err := g.st.ShouldSkipFeedRow(in.Instance, in.StoreIdentity, in.Row.Seq, in.Row.EntityID)
		if err != nil {
			return pipeline.FeedRowResult{}, err
		}
		if skip {
			return pipeline.FeedRowResult{Skipped: true}, nil
		}
	}
	if g.failID[in.Row.EntityID] {
		err := g.st.RecordIngestFailure(store.IngestFailure{
			Instance: in.Instance, EntityID: in.Row.EntityID, Seq: in.Row.Seq,
			StoreIdentity: in.StoreIdentity, Kinds: in.Row.Kinds, LastError: "hydration failed: boom",
		})
		return pipeline.FeedRowResult{Failed: true}, err
	}
	return g.commit(in.Instance, in.Row, in.StoreIdentity, !replay)
}

func (g *fakeIngester) commit(inst string, row gather.FeedRow, identity string, marker bool) (pipeline.FeedRowResult, error) {
	e, found, err := g.st.GetEntity("o/r", "issue", row.EntityID)
	if err != nil {
		return pipeline.FeedRowResult{}, err
	}
	var ver int64
	if found {
		ver = e.Version
	}
	t := true
	opts := store.WriteOptions{Backend: &inst, Watched: &t}
	if marker {
		opts.FeedMarker = &store.FeedMarker{Instance: inst, StoreIdentity: identity, Seq: row.Seq}
	}
	origin := "pg-connector"
	kinds := row.Kinds
	if row.Seq == 0 {
		origin, kinds = "reset", []string{"added"}
	}
	nv, err := g.st.WriteEntityStateWithLogOpts(
		store.Entity{Repo: "o/r", EntityType: "issue", EntityID: row.EntityID, Facts: `{}`, AsOf: "x"},
		ver, "2026-10-01T11:59:00Z", true, nil, kinds, origin, "2026-10-01T11:59:00Z", opts,
	)
	if err != nil {
		return pipeline.FeedRowResult{}, err
	}
	return pipeline.FeedRowResult{Written: true, Version: nv}, nil
}

func (g *fakeIngester) RetryIngestFailures(_ context.Context, instance string) (int, int, error) {
	g.retries.Add(1)
	g.ev.add("retry %s", instance)
	fails, err := g.st.ListIngestFailures(instance)
	if err != nil {
		return 0, 0, err
	}
	still := 0
	for _, f := range fails {
		if g.failID[f.EntityID] {
			still++
			continue
		}
		if _, err := g.commit(f.Instance, gather.FeedRow{Seq: f.Seq, Kinds: f.Kinds, EntityID: f.EntityID}, f.StoreIdentity, false); err != nil {
			return len(fails), still, err
		}
		if err := g.st.ClearIngestFailure(f.Instance, f.EntityID, f.Seq); err != nil {
			return len(fails), still, err
		}
	}
	return len(fails), still, nil
}

func feedAnswer(identity string, next int64, rows ...gather.FeedRow) gather.FeedResult {
	n := next
	return gather.FeedResult{
		Sources: []gather.FeedSource{{Backend: feedInst, Status: "succeeded", NextSeq: &n, StoreIdentity: identity}},
		Rows:    rows,
	}
}

func frow(seq int64, id string, kinds ...string) gather.FeedRow {
	return gather.FeedRow{Seq: seq, Kinds: kinds, EntityID: id}
}

type feedFixture struct {
	t      *testing.T
	st     *store.Store
	eng    *Engine
	feeder *fakeFeeder
	ing    *fakeIngester
	hyd    *fakeHydrator
	lister *recordingLister
	ev     *feedEvents
}

// recordingLister answers each listed query with an empty complete listing and
// records the queries it was asked for.
type recordingLister struct {
	mu      sync.Mutex
	queries []string
	err     error
}

func (r *recordingLister) ListFingerprints(_ context.Context, _, query string) (gather.ListFingerprintsResult, error) {
	r.mu.Lock()
	r.queries = append(r.queries, query)
	r.mu.Unlock()
	if r.err != nil {
		return gather.ListFingerprintsResult{}, r.err
	}
	return okListing(), nil
}

// newFeedFixture wires an Engine over a real new-schema store whose issue
// config watches the given queries: a name in owning is served by feedInst
// (owns_changes), any other by the beads instance.
func newFeedFixture(t *testing.T, owning []string, listed []string, answers ...gather.FeedResult) *feedFixture {
	t.Helper()
	st := store.OpenNewSchemaForTest(t)
	st.SetConsumerLockerOptions(store.LockerOptions{LockDir: t.TempDir(), Timeout: 10 * time.Second})
	cfg := &config.Config{Repos: []config.RepoConfig{{Remote: "o/r"}}}
	cfg.Watch.Issue.Backends = map[string]config.WatchBackend{}
	for _, q := range listed {
		cfg.Watch.Issue.Queries = append(cfg.Watch.Issue.Queries, q)
		cfg.Watch.Issue.Backends[q] = config.WatchBackend{Instance: "beads"}
	}
	for _, q := range owning {
		cfg.Watch.Issue.Queries = append(cfg.Watch.Issue.Queries, q)
		cfg.Watch.Issue.Backends[q] = config.WatchBackend{Instance: feedInst, OwnsChanges: true}
	}
	ev := &feedEvents{}
	f := &feedFixture{t: t, st: st, ev: ev}
	f.feeder = &fakeFeeder{ev: ev, answers: answers}
	f.ing = &fakeIngester{t: t, st: st, ev: ev, failID: map[string]bool{}}
	f.hyd = &fakeHydrator{}
	f.lister = &recordingLister{}
	f.eng = &Engine{
		Cfg: cfg, Store: st, Lister: f.lister, Hydrator: f.hyd, Feeder: f.feeder, Ingester: f.ing,
		Now: func() time.Time { return time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC) },
	}
	return f
}

func (f *feedFixture) run(consumer string, opts ...func(*Options)) (Envelope, Outcome, error) {
	f.t.Helper()
	o := Options{EntityType: "issue", Consumer: consumer}
	for _, fn := range opts {
		fn(&o)
	}
	var env Envelope
	out, err := f.eng.Run(context.Background(), o, func(e Envelope) error { env = e; return nil })
	return env, out, err
}

func sourceOf(env Envelope, q string) (Source, bool) {
	for _, s := range env.Sources {
		if s.Query == q {
			return s, true
		}
	}
	return Source{}, false
}

func TestFeedMixedPollListsBeadsAndReadsOwning(t *testing.T) {
	f := newFeedFixture(t, []string{"jira-q"}, []string{"beads-q"},
		feedAnswer(feedIdentity, 4, frow(3, "J-1", "status_changed"), frow(4, "J-2", "title_changed")))
	env, out, err := f.run("router")
	if err != nil || out.Partial {
		t.Fatalf("run = %+v, %v", out, err)
	}
	// The owning query is never listed; the beads query is.
	if !reflect.DeepEqual(f.lister.queries, []string{"beads-q"}) {
		t.Errorf("listed = %v, want only the beads query", f.lister.queries)
	}
	if len(f.feeder.lists) != 1 || f.feeder.lists[0] != (listCall{"issue", "", "pg-desk", feedInst, false}) {
		t.Errorf("feed calls = %+v", f.feeder.lists)
	}
	if len(env.Sources) != 2 || env.Sources[0].Query != "beads-q" || env.Sources[1].Query != "jira-q" {
		t.Fatalf("sources = %+v", env.Sources)
	}
	for _, s := range env.Sources {
		if s.Status != StatusOK {
			t.Errorf("source %+v", s)
		}
	}
	if len(env.Records) != 2 || env.Records[0].ID != "J-1" || env.Records[1].ID != "J-2" {
		t.Fatalf("records = %+v", env.Records)
	}
	if !reflect.DeepEqual(env.Records[0].Kinds, []string{"status_changed"}) {
		t.Errorf("kinds = %v", env.Records[0].Kinds)
	}
	// No membership is kept for the owning query.
	keys, err := f.st.ListMetaPrefix("change_flow.watchset.")
	if err != nil {
		t.Fatal(err)
	}
	for k := range keys {
		if strings.Contains(k, "jira-q") {
			t.Errorf("owning query wrote %s", k)
		}
	}
}

// T-9: no acknowledgement before the commit, and the ack is the scanned
// next_seq even when it passes the last row.
func TestFeedAckComesAfterCommitAndIsNextSeq(t *testing.T) {
	f := newFeedFixture(t, []string{"jira-q"}, nil, feedAnswer(feedIdentity, 9, frow(3, "J-1", "x"), frow(4, "J-2", "x")))
	if _, _, err := f.run("router"); err != nil {
		t.Fatal(err)
	}
	ev := f.ev.list()
	want := []string{"retry " + feedInst, "list reset=false", "ingest J-1 seq=3", "ingest J-2 seq=4"}
	if !reflect.DeepEqual(ev[:4], want) {
		t.Fatalf("events = %v", ev)
	}
	if len(ev) != 5 || !strings.HasPrefix(ev[4], "ack 9 query=\"\" consumer=pg-desk backend="+feedInst) {
		t.Fatalf("events = %v", ev)
	}
	// The committed rows are in the store at the time of the ack, and the
	// marker is the last row (not next_seq).
	m, ok, _ := f.st.FeedMarker(feedInst)
	if !ok || m.Seq != 4 || m.StoreIdentity != feedIdentity {
		t.Errorf("marker = %+v %v", m, ok)
	}
}

func TestFeedEmptyPollAcknowledgesNextSeq(t *testing.T) {
	f := newFeedFixture(t, []string{"jira-q"}, nil, feedAnswer(feedIdentity, 12))
	env, out, err := f.run("router")
	if err != nil || out.Partial || len(env.Records) != 0 {
		t.Fatalf("run = %+v %+v %v", env, out, err)
	}
	if !reflect.DeepEqual(f.feeder.acks, []int64{12}) {
		t.Errorf("acks = %v", f.feeder.acks)
	}
}

func TestFeedNilNextSeqAcknowledgesNothing(t *testing.T) {
	ans := feedAnswer(feedIdentity, 0, frow(1, "J-1", "x"))
	ans.Sources[0].NextSeq = nil
	f := newFeedFixture(t, []string{"jira-q"}, nil, ans)
	if _, _, err := f.run("router"); err != nil {
		t.Fatal(err)
	}
	if len(f.feeder.acks) != 0 {
		t.Errorf("acks = %v", f.feeder.acks)
	}
}

func TestFeedFailedRowIsRecordedAckPassesItAndRetriedEveryPoll(t *testing.T) {
	f := newFeedFixture(t, []string{"jira-q"}, nil,
		feedAnswer(feedIdentity, 2, frow(1, "J-bad", "x"), frow(2, "J-ok", "x")),
		feedAnswer(feedIdentity, 2))
	f.ing.failID["J-bad"] = true
	env, out, err := f.run("router")
	if err != nil {
		t.Fatal(err)
	}
	if !out.Partial {
		t.Error("a failed row should make the poll partial")
	}
	if len(env.Records) != 1 || env.Records[0].ID != "J-ok" {
		t.Fatalf("records = %+v", env.Records)
	}
	if !reflect.DeepEqual(f.feeder.acks, []int64{2}) {
		t.Errorf("acks = %v, want the ack to pass the failed row", f.feeder.acks)
	}
	fails, _ := f.st.ListIngestFailures(feedInst)
	if len(fails) != 1 || fails[0].EntityID != "J-bad" || fails[0].Seq != 1 {
		t.Fatalf("failed-ingest = %+v", fails)
	}

	// Next poll: retried first (still failing), then once healthy it commits.
	if _, _, err := f.run("router"); err != nil {
		t.Fatal(err)
	}
	if f.ing.retries.Load() != 2 {
		t.Errorf("retries = %d, want one per poll", f.ing.retries.Load())
	}
	delete(f.ing.failID, "J-bad")
	env, out, err = f.run("router")
	if err != nil || out.Partial {
		t.Fatalf("third poll = %+v %v", out, err)
	}
	if len(env.Records) != 1 || env.Records[0].ID != "J-bad" {
		t.Fatalf("recovered records = %+v", env.Records)
	}
	if fails, _ := f.st.ListIngestFailures(feedInst); len(fails) != 0 {
		t.Errorf("failed-ingest not cleared: %+v", fails)
	}
}

func TestFeedUnavailableReadCommitsAndAcknowledgesNothing(t *testing.T) {
	f := newFeedFixture(t, []string{"jira-q"}, nil)
	f.feeder.listErr = fmt.Errorf("wrap: %w", gather.ErrUnavailable)
	env, out, err := f.run("router")
	if !errors.Is(err, ErrTotalFailure) || !out.Partial {
		t.Fatalf("run = %+v, %v; want total failure", out, err)
	}
	s, _ := sourceOf(env, "jira-q")
	if s.Status != StatusDegraded || !strings.Contains(s.Reason, "unavailable") {
		t.Errorf("source = %+v", s)
	}
	if len(f.feeder.acks) != 0 || len(f.ing.rows) != 0 {
		t.Errorf("acks=%v rows=%v", f.feeder.acks, f.ing.rows)
	}
}

// A failed feed read is only a partial run when another source succeeded.
func TestFeedReadFailureWithAnotherSourceIsPartial(t *testing.T) {
	f := newFeedFixture(t, []string{"jira-q"}, []string{"beads-q"})
	f.feeder.listErr = errors.New("boom")
	env, out, err := f.run("router")
	if err != nil || !out.Partial {
		t.Fatalf("run = %+v, %v; want partial", out, err)
	}
	if s, _ := sourceOf(env, "beads-q"); s.Status != StatusOK {
		t.Errorf("beads source = %+v", s)
	}
	if s, _ := sourceOf(env, "jira-q"); s.Status != StatusDegraded {
		t.Errorf("owning source = %+v", s)
	}
}

func TestFeedAckFailureIsDegradedAndNonFatal(t *testing.T) {
	f := newFeedFixture(t, []string{"jira-q"}, nil, feedAnswer(feedIdentity, 3, frow(3, "J-1", "x")))
	f.feeder.ackErr = gather.ErrUnavailable
	env, out, err := f.run("router")
	if err != nil || !out.Partial {
		t.Fatalf("run = %+v, %v", out, err)
	}
	if s, _ := sourceOf(env, "jira-q"); s.Status != StatusDegraded || !strings.Contains(s.Reason, "acknowledge") {
		t.Errorf("source = %+v", s)
	}
	if len(env.Records) != 1 {
		t.Errorf("the committed row must still be delivered: %+v", env.Records)
	}
	// The redelivery is harmless: the marker skips the row.
	f.feeder.ackErr = nil
	env, _, err = f.run("router")
	if err != nil || len(env.Records) != 0 {
		t.Fatalf("redelivery = %+v %v", env.Records, err)
	}
}

func TestFeedDegradedSourceIsNotAcknowledged(t *testing.T) {
	ans := feedAnswer(feedIdentity, 5)
	ans.Sources[0].Status = "degraded"
	ans.Sources[0].Reason = "rate limited"
	f := newFeedFixture(t, []string{"jira-q"}, nil, ans)
	env, out, err := f.run("router")
	if err == nil && !out.Partial {
		t.Fatal("a degraded source must not be an ok run")
	}
	if s, _ := sourceOf(env, "jira-q"); s.Status != StatusDegraded || s.Reason != "rate limited" {
		t.Errorf("source = %+v", s)
	}
	if len(f.feeder.acks) != 0 {
		t.Errorf("acks = %v", f.feeder.acks)
	}
}

func TestFeedStoreIdentityChangeResetsMarkerBeforeAnyRow(t *testing.T) {
	f := newFeedFixture(t, []string{"jira-q"}, nil, feedAnswer("store-B", 2, frow(1, "J-1", "x"), frow(2, "J-2", "x")))
	if err := f.st.AdvanceFeedMarker(store.FeedMarker{Instance: feedInst, StoreIdentity: feedIdentity, Seq: 50}); err != nil {
		t.Fatal(err)
	}
	env, _, err := f.run("router")
	if err != nil {
		t.Fatal(err)
	}
	// seq 1 and 2 are below the OLD marker's 50: only a reset lets them in.
	if len(env.Records) != 2 {
		t.Fatalf("records = %+v", env.Records)
	}
	m, _, _ := f.st.FeedMarker(feedInst)
	if m.StoreIdentity != "store-B" || m.Seq != 2 {
		t.Errorf("marker = %+v", m)
	}
}

func TestFeedCursorExpiredRecordsGapResetsAndRereadsOnce(t *testing.T) {
	expired := feedAnswer(feedIdentity, 0)
	expired.Sources[0].Status = "degraded"
	expired.Sources[0].Reason = "cursor_expired: the consumer's key was dropped"
	expired.Sources[0].CursorExpired = true
	replay := feedAnswer(feedIdentity, 77, gather.FeedRow{EntityID: "J-1"}, gather.FeedRow{EntityID: "J-2"}, gather.FeedRow{EntityID: "J-3"})
	f := newFeedFixture(t, []string{"jira-q"}, nil, expired, replay, feedAnswer(feedIdentity, 78))
	cfg := f.eng.Cfg
	max := 2 // fewer than the three replay rows
	cfg.Hydration.MaxPerPoll = &max
	if err := f.st.AdvanceFeedMarker(store.FeedMarker{Instance: feedInst, StoreIdentity: feedIdentity, Seq: 9}); err != nil {
		t.Fatal(err)
	}

	env, out, err := f.run("router")
	if err != nil || !out.Partial {
		t.Fatalf("run = %+v, %v; want a partial run", out, err)
	}
	if len(f.feeder.lists) != 2 || f.feeder.lists[0].Reset || !f.feeder.lists[1].Reset {
		t.Fatalf("feed calls = %+v, want one read then one reset re-read", f.feeder.lists)
	}
	if len(env.Records) != 3 {
		t.Fatalf("records = %+v, want every replay row despite the budget", env.Records)
	}
	for _, r := range env.Records {
		if r.Origin != "reset" || !reflect.DeepEqual(r.Kinds, []string{"added"}) {
			t.Errorf("record = %+v", r)
		}
	}
	if !reflect.DeepEqual(f.feeder.acks, []int64{77}) {
		t.Errorf("acks = %v, want the source's next_seq", f.feeder.acks)
	}
	if s, _ := sourceOf(env, "jira-q"); s.Status != StatusDegraded || !strings.Contains(s.Reason, "cursor_expired") {
		t.Errorf("source = %+v", s)
	}
	if v, found, _ := f.st.GetMeta(MetaFeedCursorExpiredPrefix + feedInst); !found || v != "2026-10-01T12:00:00Z" {
		t.Errorf("cursor_expired key = %q %v", v, found)
	}
	if _, found, _ := f.st.FeedMarker(feedInst); found {
		t.Error("the marker survived the reset")
	}

	// The next ordinary read is ok, and the key is NOT deleted.
	env, out, err = f.run("router")
	if err != nil || out.Partial {
		t.Fatalf("next poll = %+v %v", out, err)
	}
	if _, found, _ := f.st.GetMeta(MetaFeedCursorExpiredPrefix + feedInst); !found {
		t.Error("the poll deleted the cursor_expired key")
	}
}

func TestFeedBudgetCutAcknowledgesLastProcessedRow(t *testing.T) {
	f := newFeedFixture(t, []string{"jira-q"}, nil,
		feedAnswer(feedIdentity, 10, frow(3, "J-1", "x"), frow(5, "J-2", "x"), frow(7, "J-3", "x"), frow(9, "J-4", "x")))
	max := 2
	f.eng.Cfg.Hydration.MaxPerPoll = &max
	env, out, err := f.run("router")
	if err != nil || !out.Partial {
		t.Fatalf("run = %+v, %v", out, err)
	}
	if len(env.Records) != 2 {
		t.Fatalf("records = %+v", env.Records)
	}
	if !reflect.DeepEqual(f.feeder.acks, []int64{5}) {
		t.Errorf("acks = %v, want the last processed row's seq", f.feeder.acks)
	}
	if s, _ := sourceOf(env, "jira-q"); s.Status != StatusDegraded || !strings.Contains(s.Reason, ReasonHydrationBudget) {
		t.Errorf("source = %+v", s)
	}
}

// One ListFeed and one AckFeed per instance, with an empty query, and one
// feed_observed key per freshness row of an owning query.
func TestFeedTwoQueriesOneInstanceOneReadAndObservedKeys(t *testing.T) {
	ans := feedAnswer(feedIdentity, 6, frow(6, "J-1", "x"))
	ans.Freshness = []gather.FeedFreshness{
		{Query: "q1", LastSuccessAt: "2026-10-01T11:58:00Z"},
		{Query: "q2", LastSuccessAt: "2026-10-01T11:59:00Z"},
		{Query: "unrelated", LastSuccessAt: "2026-10-01T11:59:30Z"},
		{Query: "q1-never", LastSuccessAt: ""},
	}
	f := newFeedFixture(t, []string{"q1", "q2", "q1-never"}, nil, ans)
	if _, _, err := f.run("router"); err != nil {
		t.Fatal(err)
	}
	if len(f.feeder.lists) != 1 || f.feeder.lists[0].Query != "" || len(f.feeder.acks) != 1 {
		t.Fatalf("lists = %+v acks = %v", f.feeder.lists, f.feeder.acks)
	}
	for q, want := range map[string]string{"q1": "2026-10-01T11:58:00Z", "q2": "2026-10-01T11:59:00Z"} {
		if v, found, _ := f.st.GetMeta(MetaFeedObservedPrefix + feedInst + "." + q); !found || v != want {
			t.Errorf("observed %s = %q %v", q, v, found)
		}
	}
	keys, _ := f.st.ListMetaPrefix(MetaFeedObservedPrefix)
	if len(keys) != 2 {
		t.Errorf("observed keys = %v", keys)
	}
}

func TestFeedObservedKeyIsNotWrittenOnAFailedRead(t *testing.T) {
	f := newFeedFixture(t, []string{"q1"}, nil)
	f.feeder.listErr = errors.New("boom")
	_, _, _ = f.run("router")
	keys, _ := f.st.ListMetaPrefix(MetaFeedObservedPrefix)
	if len(keys) != 0 {
		t.Errorf("observed keys = %v", keys)
	}
}

// T-9: two concurrent polls serialise across the fetch AND the commit.
func TestFeedConcurrentPollsSerialise(t *testing.T) {
	f := newFeedFixture(t, []string{"jira-q"}, nil, feedAnswer(feedIdentity, 2, frow(1, "J-1", "x")))
	f.feeder.delay = 60 * time.Millisecond
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, c := range []string{"router-a", "router-b"} {
		wg.Add(1)
		go func(c string) {
			defer wg.Done()
			_, _, err := f.run(c)
			errs <- err
		}(c)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := f.feeder.maxIn.Load(); got != 1 {
		t.Errorf("max concurrent feed polls = %d, want 1", got)
	}
	if len(f.feeder.acks) != 2 {
		t.Errorf("acks = %v", f.feeder.acks)
	}
}

// --query naming an owning query selects that query's instance only; naming
// a listed query skips the feed step.
func TestFeedQuerySelection(t *testing.T) {
	f := newFeedFixture(t, []string{"jira-q"}, []string{"beads-q"}, feedAnswer(feedIdentity, 1))
	env, _, err := f.run("router", func(o *Options) { o.Query = "beads-q" })
	if err != nil {
		t.Fatal(err)
	}
	if len(f.feeder.lists) != 0 || len(env.Sources) != 1 || env.Sources[0].Query != "beads-q" {
		t.Errorf("feed calls = %+v sources = %+v", f.feeder.lists, env.Sources)
	}
	f.lister.queries = nil
	env, _, err = f.run("router", func(o *Options) { o.Query = "jira-q" })
	if err != nil {
		t.Fatal(err)
	}
	if len(f.feeder.lists) != 1 || len(f.lister.queries) != 0 || len(env.Sources) != 1 || env.Sources[0].Query != "jira-q" {
		t.Errorf("feed calls = %+v listed = %v sources = %+v", f.feeder.lists, f.lister.queries, env.Sources)
	}
}

func TestFeedOwningOnlyConfigDoesNotMarkTheRunTotalOrSkipDelivery(t *testing.T) {
	f := newFeedFixture(t, []string{"jira-q"}, nil, feedAnswer(feedIdentity, 1, frow(1, "J-1", "x")))
	f.lister.err = errors.New("must not be called")
	env, out, err := f.run("router")
	if err != nil || out.Partial || len(env.Records) != 1 {
		t.Fatalf("run = %+v %+v %v", env, out, err)
	}
	if got := cursorOf(t, f.st, "router"); got != env.Cursor.To || got == 0 {
		t.Errorf("cursor = %d, envelope %+v", got, env.Cursor)
	}
}

func TestFeedOwningQueryWithoutFeederIsAnError(t *testing.T) {
	f := newFeedFixture(t, []string{"jira-q"}, nil)
	f.eng.Feeder = nil
	if _, _, err := f.run("router"); err == nil || !strings.Contains(err.Error(), "feed reader") {
		t.Fatalf("err = %v", err)
	}
}

// --reset replays only WATCHED owning issues, as kind added with origin reset,
// through the ingester (a replay row, no marker); an unwatched owning issue
// appends nothing; a beads issue replays through the hydrator as before.
func TestFeedResetReplaysOnlyWatchedOwningIssues(t *testing.T) {
	f := newFeedFixture(t, []string{"jira-q"}, []string{"beads-q"}, feedAnswer(feedIdentity, 1))
	seed := func(id, backend string, watched bool) {
		t.Helper()
		if _, err := f.st.WriteEntityStateWithLog(store.Entity{Repo: "o/r", EntityType: "issue", EntityID: id, Facts: `{}`, AsOf: "x"},
			0, "2026-10-01T11:00:00Z", true, []string{"reconcile"}, "sweep", "2026-10-01T11:55:00Z"); err != nil {
			t.Fatal(err)
		}
		if err := f.st.SetEntityColumns("o/r", "issue", id, &backend, &watched); err != nil {
			t.Fatal(err)
		}
	}
	seed("J-watched", feedInst, true)
	seed("J-unwatched", feedInst, false)
	seed("bd-1", "beads", false)

	if _, _, err := f.run("router", func(o *Options) { o.Reset = true }); err != nil {
		t.Fatal(err)
	}
	var replayed []string
	for _, r := range f.ing.rows {
		if r.Row.Seq == 0 {
			replayed = append(replayed, r.Row.EntityID)
			if r.Instance != feedInst || !reflect.DeepEqual(r.Row.Kinds, []string{"added"}) {
				t.Errorf("replay row = %+v", r)
			}
		}
	}
	if !reflect.DeepEqual(replayed, []string{"J-watched"}) {
		t.Errorf("replayed = %v", replayed)
	}
	if !reflect.DeepEqual(f.hyd.calls, []string{"bd-1"}) {
		t.Errorf("hydrated = %v, want only the beads issue", f.hyd.calls)
	}
	if h := historyOf(t, f.st, "J-unwatched"); len(h) != 1 { // the seed row only
		t.Errorf("unwatched owning issue history = %+v", h)
	}
	h := historyOf(t, f.st, "J-watched")
	if len(h) != 2 || h[0].Origin != "reset" || !reflect.DeepEqual(h[0].Kinds, []string{"added"}) {
		t.Errorf("watched owning history = %+v", h)
	}
}
