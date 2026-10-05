package collect

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/beads-exporter/internal/bd"
	"github.com/phillipgreenii/beads-exporter/internal/failure"
	"github.com/phillipgreenii/beads-exporter/internal/metrics"
	"github.com/phillipgreenii/beads-exporter/internal/queue"
)

func mustQueue(t *testing.T, name string, args ...string) queue.Queue {
	t.Helper()
	q, err := queue.New(name, args)
	if err != nil {
		t.Fatal(err)
	}
	return q
}

func baseAdapter(clock *fakeClock) *fakeAdapter {
	created := clock.Now().Add(-48 * time.Hour)
	return &fakeAdapter{
		list: []bd.Bead{
			mkBead("alpha-1", "open", "task", 1, created),
			mkBead("alpha-2", "in_progress", "task", 2, created),
		},
		ready:    []bd.Bead{mkBead("alpha-1", "open", "task", 1, created)},
		counts:   map[string]int{"closed": 3, "open": 1},
		statuses: []string{"open", "in_progress", "closed"},
	}
}

func samplesByFamily(c *Collector) map[string][]metrics.Sample {
	out := map[string][]metrics.Sample{}
	for _, s := range c.Snapshot().Samples() {
		out[s.Family] = append(out[s.Family], s)
	}
	return out
}

func valueOf(t *testing.T, ss []metrics.Sample, kv ...string) (float64, bool) {
	t.Helper()
	want := metrics.SeriesLabel(kv...)
	for _, s := range ss {
		if reflect.DeepEqual(s.Labels, want) {
			return s.Value, true
		}
	}
	return 0, false
}

func newCollector(clock *fakeClock, dbs []DB, queues []queue.Queue) (*Collector, *MainPass) {
	mp := NewMainPass(queues, 5)
	return New(clock, dbs, []Pass{mp, ThroughputPass{}}, quietLogger()), mp
}

func TestMainPassMakesFourSpawnsPerDBInSteadyState(t *testing.T) {
	clock := newClock()
	ad := baseAdapter(clock)
	dbs := []DB{{Name: "alpha", Adapter: ad}}
	c, mp := newCollector(clock, dbs, []queue.Queue{mustQueue(t, "drain", "--exclude-type", "epic")})

	mp.Init(context.Background(), dbs)
	if got := ad.callNames(); !reflect.DeepEqual(got, []string{"statuses"}) {
		t.Fatalf("start-up calls = %v, want one statuses fetch", got)
	}
	ad.resetCalls()

	c.RunPass(context.Background(), PassMain)
	want := []string{"list", "ready", "blocked", "count"}
	if got := ad.callNames(); !reflect.DeepEqual(got, want) {
		t.Fatalf("first cycle calls = %v, want %v", got, want)
	}
	ad.resetCalls()
	c.RunPass(context.Background(), PassMain)
	if got := ad.callNames(); !reflect.DeepEqual(got, want) {
		t.Fatalf("steady cycle calls = %v, want %v", got, want)
	}
}

func TestStatusesRefetchedOnlyWhenAnUnknownStatusAppears(t *testing.T) {
	clock := newClock()
	ad := baseAdapter(clock)
	dbs := []DB{{Name: "alpha", Adapter: ad}}
	c, mp := newCollector(clock, dbs, nil)
	mp.Init(context.Background(), dbs)
	ad.resetCalls()

	ad.list = append(ad.list, mkBead("alpha-3", "review", "task", 2, clock.Now()))
	c.RunPass(context.Background(), PassMain)
	if got := ad.callNames(); !reflect.DeepEqual(got, []string{"list", "ready", "blocked", "count", "statuses"}) {
		t.Fatalf("calls = %v, want a statuses refetch after the unknown status", got)
	}
	// The unknown status is remembered even though bd's set did not name it, so
	// the next cycle does not refetch again.
	ad.resetCalls()
	c.RunPass(context.Background(), PassMain)
	if got := ad.callNames(); len(got) != 4 {
		t.Fatalf("calls = %v, want 4", got)
	}
	stored := samplesByFamily(c)[metrics.FamIssuesStored]
	if v, ok := valueOf(t, stored, "db", "alpha", "status", "review"); !ok || v != 1 {
		t.Fatalf("review series = %v %v", v, ok)
	}
}

func TestStatusesFetchedWhenStartupFetchFailed(t *testing.T) {
	clock := newClock()
	ad := baseAdapter(clock)
	ad.fail = map[string]error{"statuses": errf("boom")}
	dbs := []DB{{Name: "alpha", Adapter: ad}}
	c, mp := newCollector(clock, dbs, nil)
	mp.Init(context.Background(), dbs) // fails quietly
	ad.resetCalls()
	c.RunPass(context.Background(), PassMain)
	snap := c.Snapshot()
	if got := snap.DBs[0].Passes[PassMain].Meta; got.OK || got.Errors[failure.BDError] != 1 {
		t.Fatalf("meta = %+v, want a bd_error because statuses could not be fetched", got)
	}
	ad.fail = nil
	c.RunPass(context.Background(), PassMain)
	if got := c.Snapshot().DBs[0].Passes[PassMain].Meta; !got.OK {
		t.Fatalf("pass should recover once statuses are fetchable: %+v", got)
	}
}

func TestSpawnOnlyQueueCostsOneExtraSpawn(t *testing.T) {
	clock := newClock()
	ad := baseAdapter(clock)
	created := clock.Now().Add(-5 * time.Hour)
	ad.spawnReady = map[string][]bd.Bead{"--priority 1": {mkBead("alpha-1", "open", "task", 1, created)}}
	dbs := []DB{{Name: "alpha", Adapter: ad}}
	qs := []queue.Queue{mustQueue(t, "drain", "--exclude-type", "epic"), mustQueue(t, "prio", "--priority", "1")}
	c, mp := newCollector(clock, dbs, qs)
	mp.Init(context.Background(), dbs)
	ad.resetCalls()
	c.RunPass(context.Background(), PassMain)
	want := []string{"list", "ready", "blocked", "count", "ready:--priority 1"}
	if got := ad.callNames(); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
	cands := samplesByFamily(c)[metrics.FamQueueCandidates]
	if v, ok := valueOf(t, cands, "db", "alpha", "queue", "prio"); !ok || v != 1 {
		t.Fatalf("prio candidates = %v %v", v, ok)
	}
	if v, ok := valueOf(t, cands, "db", "alpha", "queue", "drain"); !ok || v != 1 {
		t.Fatalf("drain candidates = %v %v", v, ok)
	}
}

func TestThroughputPassMakesTwoSpawnsOverTheLastDay(t *testing.T) {
	clock := newClock()
	ad := baseAdapter(clock)
	ad.created = []bd.Bead{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	ad.closed = []bd.Bead{{ID: "d"}}
	c, _ := newCollector(clock, []DB{{Name: "alpha", Adapter: ad}}, nil)
	c.RunPass(context.Background(), PassThroughput)
	if got := ad.callNames(); !reflect.DeepEqual(got, []string{"list-all", "list-all"}) {
		t.Fatalf("calls = %v", got)
	}
	since := clock.Now().Add(-24 * time.Hour)
	want := []bd.ListOpts{{All: true, CreatedAfter: since}, {All: true, ClosedAfter: since}}
	if !reflect.DeepEqual(ad.listOpts, want) {
		t.Fatalf("list opts = %+v, want %+v", ad.listOpts, want)
	}
	fams := samplesByFamily(c)
	if v, _ := valueOf(t, fams[metrics.FamCreated24h], "db", "alpha"); v != 3 {
		t.Fatalf("created = %v", v)
	}
	if v, _ := valueOf(t, fams[metrics.FamClosed24h], "db", "alpha"); v != 1 {
		t.Fatalf("closed = %v", v)
	}
	if _, ok := valueOf(t, fams[metrics.FamPassLastSuccess], "db", "alpha", "pass", "throughput"); !ok {
		t.Fatal("throughput last-success missing")
	}
}

func TestMainFailureDropsOnlyThatPassSeriesAndFreezesTimestamp(t *testing.T) {
	clock := newClock()
	ad := baseAdapter(clock)
	dbs := []DB{{Name: "alpha", Adapter: ad}}
	c, mp := newCollector(clock, dbs, nil)
	mp.Init(context.Background(), dbs)

	c.RunPass(context.Background(), PassMain)
	c.RunPass(context.Background(), PassThroughput)
	okAt := clock.Now()
	fams := samplesByFamily(c)
	if len(fams[metrics.FamIssues]) == 0 {
		t.Fatal("main series missing after a good cycle")
	}
	if v, _ := valueOf(t, fams[metrics.FamExporterUp], "db", "alpha"); v != 1 {
		t.Fatalf("up = %v, want 1", v)
	}

	clock.Advance(time.Minute)
	ad.fail = map[string]error{"blocked": failure.New(failure.Timeout, "blocked", errf("slow"))}
	c.RunPass(context.Background(), PassMain)

	fams = samplesByFamily(c)
	for _, f := range []string{metrics.FamIssues, metrics.FamIssuesStored, metrics.FamByType, metrics.FamByPriority, metrics.FamOldest} {
		if len(fams[f]) != 0 {
			t.Fatalf("family %s still served after a failed main pass", f)
		}
	}
	if v, ok := valueOf(t, fams[metrics.FamExporterUp], "db", "alpha"); !ok || v != 0 {
		t.Fatalf("up = %v %v, want 0", v, ok)
	}
	if v, _ := valueOf(t, fams[metrics.FamCollectErrors], "db", "alpha", "pass", "main", "reason", "timeout"); v != 1 {
		t.Fatalf("timeout counter = %v, want 1", v)
	}
	if v, _ := valueOf(t, fams[metrics.FamCollectErrors], "db", "alpha", "pass", "main", "reason", "bd_error"); v != 0 {
		t.Fatalf("bd_error counter = %v, want a zero-filled 0", v)
	}
	if v, _ := valueOf(t, fams[metrics.FamPassLastSuccess], "db", "alpha", "pass", "main"); v != float64(okAt.UnixNano())/1e9 {
		t.Fatalf("last success = %v, want it frozen at %v", v, okAt)
	}
	// The throughput pass is untouched by the main pass failure.
	if _, ok := valueOf(t, fams[metrics.FamCreated24h], "db", "alpha"); !ok {
		t.Fatal("throughput series lost to a main failure")
	}
	if v, _ := valueOf(t, fams[metrics.FamCollectErrors], "db", "alpha", "pass", "throughput", "reason", "timeout"); v != 0 {
		t.Fatalf("throughput timeout counter = %v, want 0", v)
	}

	// Recovery: the timestamp advances again and the series return.
	clock.Advance(time.Minute)
	ad.fail = nil
	c.RunPass(context.Background(), PassMain)
	fams = samplesByFamily(c)
	if v, _ := valueOf(t, fams[metrics.FamPassLastSuccess], "db", "alpha", "pass", "main"); v != float64(clock.Now().UnixNano())/1e9 {
		t.Fatalf("last success after recovery = %v", v)
	}
	if len(fams[metrics.FamIssues]) == 0 {
		t.Fatal("series did not return after recovery")
	}
	if v, _ := valueOf(t, fams[metrics.FamExporterUp], "db", "alpha"); v != 1 {
		t.Fatalf("up after recovery = %v", v)
	}
	// The counter is monotonic across the recovery.
	if v, _ := valueOf(t, fams[metrics.FamCollectErrors], "db", "alpha", "pass", "main", "reason", "timeout"); v != 1 {
		t.Fatalf("timeout counter after recovery = %v, want it to stay 1", v)
	}
}

func TestThroughputFailureDoesNotAffectMain(t *testing.T) {
	clock := newClock()
	ad := baseAdapter(clock)
	dbs := []DB{{Name: "alpha", Adapter: ad}}
	c, mp := newCollector(clock, dbs, nil)
	mp.Init(context.Background(), dbs)
	c.RunPass(context.Background(), PassMain)
	ad.fail = map[string]error{"list-all": failure.New(failure.ParseError, "list", errf("junk"))}
	c.RunPass(context.Background(), PassThroughput)
	fams := samplesByFamily(c)
	if len(fams[metrics.FamIssues]) == 0 {
		t.Fatal("main series dropped by a throughput failure")
	}
	if len(fams[metrics.FamCreated24h]) != 0 || len(fams[metrics.FamClosed24h]) != 0 {
		t.Fatal("throughput series served after a throughput failure")
	}
	if v, _ := valueOf(t, fams[metrics.FamCollectErrors], "db", "alpha", "pass", "throughput", "reason", "parse_error"); v != 1 {
		t.Fatalf("parse_error counter = %v", v)
	}
	if v, _ := valueOf(t, fams[metrics.FamExporterUp], "db", "alpha"); v != 1 {
		t.Fatalf("up = %v, want 1: up tracks the main pass only", v)
	}
	if _, ok := valueOf(t, fams[metrics.FamPassLastSuccess], "db", "alpha", "pass", "throughput"); ok {
		t.Fatal("a never-succeeded pass must not report a last-success timestamp")
	}
}

func TestFirstEverFailureEmitsUpZero(t *testing.T) {
	clock := newClock()
	ad := baseAdapter(clock)
	ad.fail = map[string]error{"list": failure.New(failure.StaleIssuesJSONL, "guard", errf("x"))}
	c, _ := newCollector(clock, []DB{{Name: "alpha", Adapter: ad}}, nil)
	if len(c.Snapshot().Samples()) != 0 {
		t.Fatal("snapshot must be empty before any attempt")
	}
	c.RunPass(context.Background(), PassMain)
	fams := samplesByFamily(c)
	if v, ok := valueOf(t, fams[metrics.FamExporterUp], "db", "alpha"); !ok || v != 0 {
		t.Fatalf("up = %v %v, want an explicit 0", v, ok)
	}
	if _, ok := valueOf(t, fams[metrics.FamPassLastSuccess], "db", "alpha", "pass", "main"); ok {
		t.Fatal("never-succeeded main pass reported a last-success timestamp")
	}
	if v, _ := valueOf(t, fams[metrics.FamCollectErrors], "db", "alpha", "pass", "main", "reason", "stale_issues_jsonl"); v != 1 {
		t.Fatalf("stale counter = %v", v)
	}
	if len(fams[metrics.FamIssues]) != 0 {
		t.Fatal("domain series served for a database that never succeeded")
	}
}

func TestUnclassifiedErrorFallsInsideTheEnum(t *testing.T) {
	clock := newClock()
	ad := baseAdapter(clock)
	ad.fail = map[string]error{"list": errf("some unexpected text with a \"quote\" and \n newline")}
	c, _ := newCollector(clock, []DB{{Name: "alpha", Adapter: ad}}, nil)
	c.RunPass(context.Background(), PassMain)
	for _, s := range samplesByFamily(c)[metrics.FamCollectErrors] {
		for _, l := range s.Labels {
			if l.Name == "reason" && !failure.Reason(l.Value).Valid() {
				t.Fatalf("reason label %q is outside the enum", l.Value)
			}
		}
	}
	if v, _ := valueOf(t, samplesByFamily(c)[metrics.FamCollectErrors], "db", "alpha", "pass", "main", "reason", "bd_error"); v != 1 {
		t.Fatalf("bd_error counter = %v", v)
	}
}

func TestErrorTextNeverBecomesALabel(t *testing.T) {
	clock := newClock()
	ad := baseAdapter(clock)
	secret := "SECRET-ERROR-TEXT"
	ad.fail = map[string]error{"list": failure.New(failure.BDError, "list", errf("%s", secret))}
	c, _ := newCollector(clock, []DB{{Name: "alpha", Adapter: ad}}, nil)
	c.RunPass(context.Background(), PassMain)
	for _, s := range c.Snapshot().Samples() {
		for _, l := range s.Labels {
			if strings.Contains(l.Value, secret) {
				t.Fatalf("error text leaked into label %+v", l)
			}
		}
	}
}

func TestAHungDBDoesNotWithholdAnotherDB(t *testing.T) {
	clock := newClock()
	good := baseAdapter(clock)
	hung := baseAdapter(clock)
	release := make(chan struct{})
	entered := make(chan struct{})
	var once sync.Once
	hung.onCall = func(name string) error {
		if name == "list" {
			once.Do(func() { close(entered) })
			<-release
			return failure.New(failure.Timeout, "list", errf("hung"))
		}
		return nil
	}
	dbs := []DB{{Name: "beta", Adapter: good}, {Name: "alpha", Adapter: hung}}
	c, mp := newCollector(clock, dbs, nil)
	mp.statuses["beta"] = []string{"open", "closed"}
	mp.statuses["alpha"] = []string{"open", "closed"}

	done := make(chan struct{})
	go func() {
		c.RunPass(context.Background(), PassMain)
		close(done)
	}()
	<-entered
	// alpha is mid-call. beta has already been published.
	var betaUp float64 = -1
	for _, s := range c.Snapshot().Samples() {
		if s.Family == metrics.FamExporterUp && s.Labels[0].Value == "beta" {
			betaUp = s.Value
		}
	}
	if betaUp != 1 {
		t.Fatalf("beta up = %v while alpha hangs, want 1", betaUp)
	}
	close(release)
	<-done
	var alphaUp float64 = -1
	for _, s := range c.Snapshot().Samples() {
		if s.Family == metrics.FamExporterUp && s.Labels[0].Value == "alpha" {
			alphaUp = s.Value
		}
	}
	if alphaUp != 0 {
		t.Fatalf("alpha up = %v after the timeout, want 0", alphaUp)
	}
}

func TestScrapeNeverBlocksAndSeesThePreviousSnapshot(t *testing.T) {
	clock := newClock()
	ad := baseAdapter(clock)
	dbs := []DB{{Name: "alpha", Adapter: ad}}
	c, mp := newCollector(clock, dbs, nil)
	mp.Init(context.Background(), dbs)
	c.RunPass(context.Background(), PassMain)
	before := len(c.Snapshot().Samples())
	if before == 0 {
		t.Fatal("no samples after the first cycle")
	}

	block := make(chan struct{})
	entered := make(chan struct{})
	var once sync.Once
	ad.onCall = func(name string) error {
		if name == "list" {
			once.Do(func() { close(entered) })
			<-block
		}
		return nil
	}
	done := make(chan struct{})
	go func() {
		c.RunPass(context.Background(), PassMain)
		close(done)
	}()
	<-entered

	got := make(chan int, 1)
	go func() { got <- len(c.Snapshot().Samples()) }()
	select {
	case n := <-got:
		if n != before {
			t.Fatalf("scrape during a cycle saw %d samples, want the previous snapshot's %d", n, before)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("scrape blocked on a running cycle")
	}
	close(block)
	<-done
}

func TestInterruptedRunRecordsNothing(t *testing.T) {
	clock := newClock()
	ad := baseAdapter(clock)
	ctx, cancel := context.WithCancel(context.Background())
	ad.onCall = func(name string) error {
		cancel()
		return context.Canceled
	}
	c, _ := newCollector(clock, []DB{{Name: "alpha", Adapter: ad}}, nil)
	c.RunPass(ctx, PassMain)
	if n := len(c.Snapshot().Samples()); n != 0 {
		t.Fatalf("%d samples recorded for a cancelled run", n)
	}
}

func TestCancelledContextSkipsLaterDatabases(t *testing.T) {
	clock := newClock()
	first, second := baseAdapter(clock), baseAdapter(clock)
	ctx, cancel := context.WithCancel(context.Background())
	first.onCall = func(string) error { cancel(); return nil }
	c, mp := newCollector(clock, []DB{{Name: "alpha", Adapter: first}, {Name: "beta", Adapter: second}}, nil)
	mp.statuses["alpha"] = []string{"open"}
	c.RunPass(ctx, PassMain)
	if len(second.callNames()) != 0 {
		t.Fatalf("second database was collected after cancellation: %v", second.callNames())
	}
}

func TestUnknownPassIsIgnored(t *testing.T) {
	clock := newClock()
	c, _ := newCollector(clock, []DB{{Name: "alpha", Adapter: baseAdapter(clock)}}, nil)
	c.RunPass(context.Background(), "nope")
	if len(c.Snapshot().Samples()) != 0 {
		t.Fatal("unknown pass produced samples")
	}
}

func TestDurationUsesTheClock(t *testing.T) {
	clock := newClock()
	ad := baseAdapter(clock)
	ad.onCall = func(string) error { clock.Advance(250 * time.Millisecond); return nil }
	dbs := []DB{{Name: "alpha", Adapter: ad}}
	c, mp := newCollector(clock, dbs, nil)
	mp.statuses["alpha"] = []string{"open", "in_progress"}
	c.RunPass(context.Background(), PassMain)
	v, ok := valueOf(t, samplesByFamily(c)[metrics.FamCollectDuration], "db", "alpha", "pass", "main")
	if !ok || v != 1.0 {
		t.Fatalf("duration = %v %v, want 1 (4 calls x 250ms)", v, ok)
	}
}

func renderSnap(t *testing.T, s *Snapshot) string {
	t.Helper()
	var b strings.Builder
	if err := metrics.Default().Render(&b, s.Samples()); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestSnapshotsAreImmutableOnceHandedOut(t *testing.T) {
	clock := newClock()
	ad := baseAdapter(clock)
	dbs := []DB{{Name: "alpha", Adapter: ad}}
	c, mp := newCollector(clock, dbs, nil)
	mp.Init(context.Background(), dbs)
	c.RunPass(context.Background(), PassMain)
	old := c.Snapshot()
	oldText := renderSnap(t, old)
	ad.fail = map[string]error{"list": failure.New(failure.BDError, "list", errf("x"))}
	c.RunPass(context.Background(), PassMain)
	if renderSnap(t, old) != oldText {
		t.Fatal("a published snapshot changed after a later cycle")
	}
	if renderSnap(t, c.Snapshot()) == oldText {
		t.Fatal("the new snapshot did not change")
	}
}

func TestDatabasesPublishSortedByName(t *testing.T) {
	clock := newClock()
	dbs := []DB{{Name: "beta", Adapter: baseAdapter(clock)}, {Name: "alpha", Adapter: baseAdapter(clock)}}
	c, mp := newCollector(clock, dbs, nil)
	mp.Init(context.Background(), dbs)
	c.RunPass(context.Background(), PassMain)
	snap := c.Snapshot()
	if len(snap.DBs) != 2 || snap.DBs[0].Name != "alpha" || snap.DBs[1].Name != "beta" {
		t.Fatalf("DBs = %+v", snap.DBs)
	}
}

func TestNilSnapshotHasNoSamples(t *testing.T) {
	var s *Snapshot
	if s.Samples() != nil {
		t.Fatal("nil snapshot produced samples")
	}
}

func TestPassNamesAndReasons(t *testing.T) {
	mp := NewMainPass(nil, 1)
	if mp.Name() != "main" || (ThroughputPass{}).Name() != "throughput" {
		t.Fatal("pass names changed")
	}
	want := []failure.Reason{failure.StaleIssuesJSONL, failure.Timeout, failure.BDError, failure.SchemaSkew, failure.ParseError}
	if !reflect.DeepEqual(mp.Reasons(), want) || !reflect.DeepEqual((ThroughputPass{}).Reasons(), want) {
		t.Fatalf("reasons = %v", mp.Reasons())
	}
	for _, r := range failure.All() {
		if !r.Valid() {
			t.Fatalf("%s should be valid", r)
		}
	}
	if failure.Reason("nope").Valid() {
		t.Fatal("unknown reason accepted")
	}
}
