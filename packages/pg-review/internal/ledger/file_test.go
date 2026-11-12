package ledger

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/validate"
)

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// clock is an injectable clock.
type clock struct{ now time.Time }

func (c *clock) Now() time.Time          { return c.now }
func (c *clock) Advance(d time.Duration) { c.now = c.now.Add(d) }

func openClock(t *testing.T) (*Ledger, *clock) {
	t.Helper()
	l := open(t)
	c := &clock{now: t0}
	l.SetClock(c.Now)
	return l, c
}

func startRun(t *testing.T, l *Ledger, lens, scope string) Run {
	t.Helper()
	var r Run
	err := l.WithTx(ctx, func(tx *Tx) error {
		var err error
		r, err = tx.InsertRun(ctx, Run{Lens: lens, Scope: scope, PID: 1, StartedAt: t0})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func finding(subject string) validate.Finding {
	return validate.Finding{
		SubjectKey: subject, Kind: "stale", Title: "title " + subject,
		Situation: "s", Evidence: []string{"E-" + subject},
	}
}

func evidence(subject string, metrics map[string]float64, lastSeen string) validate.EvidenceRow {
	return validate.EvidenceRow{
		ID: "E-" + subject, SubjectKey: subject, DefinedIn: "a.md", Type: "file",
		Metrics: metrics, LastSeen: lastSeen,
	}
}

func fileOnce(t *testing.T, l *Ledger, run Run, maxPending int, rows []validate.EvidenceRow, fs ...validate.Finding) FileResult {
	t.Helper()
	res, err := l.FileBatch(ctx, Batch{
		RunID: run.ID, Doc: validate.Document{Lens: run.Lens, Scope: run.Scope, Findings: fs},
		Evidence: rows, MaxPending: maxPending,
	})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func topicOf(t *testing.T, l *Ledger, subject string) Topic {
	t.Helper()
	tp, err := l.GetTopic(ctx, TopicID("a.md", subject, "stale"))
	if err != nil {
		t.Fatal(err)
	}
	return tp
}

func occurrences(t *testing.T, l *Ledger, tp Topic) int {
	t.Helper()
	var n int
	if err := l.DB().QueryRow(`SELECT COUNT(*) FROM occurrence WHERE topic_id=?`, tp.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func rule(t *testing.T, l *Ledger, tp Topic, ruling, reconsider, rate string) {
	t.Helper()
	err := l.WithTx(ctx, func(tx *Tx) error {
		_, err := tx.RecordRuling(ctx, Ruling{TopicID: tp.ID, Ruling: ruling, ReconsiderWhen: reconsider, RateAtDecision: rate})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestFileCreatesPendingAndDedupesAcrossLensAndScope(t *testing.T) {
	l, _ := openClock(t)
	rows := []validate.EvidenceRow{evidence("x", nil, "")}
	r1 := startRun(t, l, "lens-a", "scope-1")
	res := fileOnce(t, l, r1, 10, rows, finding("x"))
	if res.Filed != 1 || res.Deduplicated != 0 {
		t.Fatalf("%+v", res)
	}
	tp := topicOf(t, l, "x")
	if tp.State != StatePending || tp.Revision != 1 {
		t.Fatalf("%+v", tp)
	}
	// Same subject and kind from another lens and scope: one item, a new occurrence.
	r2 := startRun(t, l, "lens-b", "scope-2")
	res = fileOnce(t, l, r2, 10, rows, finding("x"))
	if res.Filed != 0 || res.Deduplicated != 1 {
		t.Fatalf("%+v", res)
	}
	var topics int
	_ = l.DB().QueryRow(`SELECT COUNT(*) FROM topic`).Scan(&topics)
	if topics != 1 || occurrences(t, l, tp) != 2 {
		t.Fatalf("topics %d occurrences %d", topics, occurrences(t, l, tp))
	}
	if got := topicOf(t, l, "x"); got.Revision != 1 {
		t.Errorf("an identical refile must not bump the revision, got %d", got.Revision)
	}
	// Changed content bumps it.
	r3 := startRun(t, l, "lens-a", "scope-1")
	f := finding("x")
	f.Title = "changed"
	fileOnce(t, l, r3, 10, rows, f)
	if got := topicOf(t, l, "x"); got.Revision != 2 {
		t.Errorf("revision %d, want 2", got.Revision)
	}
	run, _ := l.GetRun(ctx, r2.ID)
	if run.State != RunFiled || run.ItemsDeduplicated != 1 {
		t.Errorf("%+v", run)
	}
}

func TestAdoptedTopicGetsOccurrenceOnly(t *testing.T) {
	l, _ := openClock(t)
	rows := []validate.EvidenceRow{evidence("x", nil, "")}
	fileOnce(t, l, startRun(t, l, "l", "s"), 10, rows, finding("x"))
	tp := topicOf(t, l, "x")
	rule(t, l, tp, RulingAdopted, "", "")
	rev := topicOf(t, l, "x").Revision
	res := fileOnce(t, l, startRun(t, l, "l", "s"), 10, rows, finding("x"))
	if res.Deduplicated != 1 || topicOf(t, l, "x").State != StateAdopted || topicOf(t, l, "x").Revision != rev || occurrences(t, l, tp) != 2 {
		t.Fatalf("%+v %+v", res, topicOf(t, l, "x"))
	}
}

func TestPendingCapWaitingAndPromotion(t *testing.T) {
	l, c := openClock(t)
	rows := []validate.EvidenceRow{evidence("a", nil, ""), evidence("b", nil, ""), evidence("c", nil, "")}
	c.Advance(time.Hour)
	res := fileOnce(t, l, startRun(t, l, "l", "s"), 1, rows, finding("a"), finding("b"))
	if res.Filed != 2 || res.Waiting != 1 || res.PendingAfter != 1 {
		t.Fatalf("%+v", res)
	}
	if topicOf(t, l, "a").State != StatePending || topicOf(t, l, "b").State != StateWaiting {
		t.Fatal("wrong states")
	}
	c.Advance(time.Hour)
	fileOnce(t, l, startRun(t, l, "l", "s"), 1, rows, finding("c"))
	if topicOf(t, l, "c").State != StateWaiting {
		t.Fatal("c should wait")
	}
	// No free slot: promotion does nothing and pending never exceeds the cap.
	ids, err := l.PromoteWaiting(ctx, 1)
	if err != nil || len(ids) != 0 {
		t.Fatalf("%v %v", ids, err)
	}
	rule(t, l, topicOf(t, l, "a"), RulingRejected, "", "")
	ids, err = l.PromoteWaiting(ctx, 1)
	if err != nil || len(ids) != 1 || ids[0] != topicOf(t, l, "b").ID {
		t.Fatalf("oldest first: %v %v", ids, err)
	}
	if topicOf(t, l, "b").State != StatePending || topicOf(t, l, "c").State != StateWaiting {
		t.Fatal("wrong states after promotion")
	}
}

func TestBatchRollbackLeavesNothing(t *testing.T) {
	l, _ := openClock(t)
	r := startRun(t, l, "l", "s")
	_, err := l.FileBatch(ctx, Batch{
		RunID: r.ID, MaxPending: 10,
		Doc:      validate.Document{Lens: "l", Scope: "s", Findings: []validate.Finding{finding("ok"), finding("ghost")}},
		Evidence: []validate.EvidenceRow{evidence("ok", nil, "")},
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("want ErrInvalid, got %v", err)
	}
	for _, q := range []string{"topic", "occurrence", "revision", "subject"} {
		var n int
		_ = l.DB().QueryRow(`SELECT COUNT(*) FROM ` + q).Scan(&n)
		if n != 0 {
			t.Errorf("%s has %d rows after a rolled-back batch", q, n)
		}
	}
	if got, _ := l.GetRun(ctx, r.ID); got.State != RunRunning {
		t.Errorf("run state %s", got.State)
	}
}

func TestFileIsIdempotentPerRun(t *testing.T) {
	l, _ := openClock(t)
	rows := []validate.EvidenceRow{evidence("x", nil, ""), evidence("y", nil, "")}
	r := startRun(t, l, "l", "s")
	fileOnce(t, l, r, 10, rows, finding("x"))
	res, err := l.FileBatch(ctx, Batch{
		RunID: r.ID, MaxPending: 10, Evidence: rows,
		Doc: validate.Document{Lens: "l", Scope: "s", Findings: []validate.Finding{finding("x")}},
	})
	if err != nil || !res.Replayed {
		t.Fatalf("%+v %v", res, err)
	}
	_, err = l.FileBatch(ctx, Batch{
		RunID: r.ID, MaxPending: 10, Evidence: rows,
		Doc: validate.Document{Lens: "l", Scope: "s", Findings: []validate.Finding{finding("y")}},
	})
	if !errors.Is(err, ErrBatchConflict) {
		t.Fatalf("want conflict, got %v", err)
	}
	if occurrences(t, l, topicOf(t, l, "x")) != 1 {
		t.Error("replay stored a second occurrence")
	}
}

func TestFailedRunMayFileAfterRepair(t *testing.T) {
	l, _ := openClock(t)
	r := startRun(t, l, "l", "s")
	if err := l.FailRun(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := l.GetRun(ctx, r.ID); got.State != RunFailed || got.RepairCount != 1 {
		t.Fatalf("%+v", got)
	}
	fileOnce(t, l, r, 10, []validate.EvidenceRow{evidence("x", nil, "")}, finding("x"))
	// A run that failed some other way is not refilable.
	r2 := startRun(t, l, "l", "s")
	_ = l.WithTx(ctx, func(tx *Tx) error { r2.State = RunSkipped; return tx.UpdateRun(ctx, r2) })
	_, err := l.FileBatch(ctx, Batch{RunID: r2.ID, MaxPending: 10, Doc: validate.Document{Lens: "l"}})
	if !errors.Is(err, ErrRunState) {
		t.Fatalf("want ErrRunState, got %v", err)
	}
}

func TestTwoConcurrentFileProcessesAreConsistent(t *testing.T) {
	l, _ := openClock(t)
	other, err := OpenPath(ctx, l.Path()) // a second handle: a second process's view
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	rows := []validate.EvidenceRow{evidence("x", nil, "")}
	r1, r2 := startRun(t, l, "l", "s1"), startRun(t, l, "l", "s2")
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, h := range []struct {
		l *Ledger
		r Run
	}{{l, r1}, {other, r2}} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = h.l.FileBatch(ctx, Batch{
				RunID: h.r.ID, MaxPending: 10, Evidence: rows,
				Doc: validate.Document{Lens: "l", Scope: h.r.Scope, Findings: []validate.Finding{finding("x")}},
			})
		}()
	}
	wg.Wait()
	for _, e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	var topics, occ int
	_ = l.DB().QueryRow(`SELECT COUNT(*) FROM topic`).Scan(&topics)
	_ = l.DB().QueryRow(`SELECT COUNT(*) FROM occurrence`).Scan(&occ)
	if topics != 1 || occ != 2 {
		t.Fatalf("topics %d occurrences %d", topics, occ)
	}
	if err := l.IntegrityCheck(ctx); err != nil {
		t.Fatal(err)
	}
}

// deferTopic files x, rules it Deferred with no recorded condition and returns it.
func deferTopic(t *testing.T, l *Ledger, rows []validate.EvidenceRow) {
	t.Helper()
	fileOnce(t, l, startRun(t, l, "l", "s"), 10, rows, finding("x"))
	rule(t, l, topicOf(t, l, "x"), RulingDeferred, "", "")
}

func refile(t *testing.T, l *Ledger, rows []validate.EvidenceRow) FileResult {
	t.Helper()
	return fileOnce(t, l, startRun(t, l, "l", "s"), 10, rows, finding("x"))
}

func TestDeferredBackoffIs30Then60Then120Days(t *testing.T) {
	l, c := openClock(t)
	rows := []validate.EvidenceRow{evidence("x", nil, "")}
	deferTopic(t, l, rows)
	steps := []int{30, 60, 120, 120}
	for i, days := range steps {
		c.Advance(time.Duration(days)*day - time.Hour)
		if res := refile(t, l, rows); res.Reconsidered != 0 || topicOf(t, l, "x").State != StateDeferred {
			t.Fatalf("step %d: returned early at %d days minus an hour", i, days)
		}
		c.Advance(time.Hour)
		res := refile(t, l, rows)
		if res.Reconsidered != 1 || topicOf(t, l, "x").State != StatePending {
			t.Fatalf("step %d: did not return after %d days: %+v", i, days, res)
		}
		// The operator defers again; the next return is measured from that point.
		rule(t, l, topicOf(t, l, "x"), RulingDeferred, "", "")
	}
	ev, _ := l.TopicEvents(ctx, topicOf(t, l, "x").ID)
	n := 0
	for _, e := range ev {
		if e.Kind == EventReconsidered {
			n++
		}
	}
	if n != 4 {
		t.Errorf("%d reconsidered events", n)
	}
}

func TestRejectedReturnsOnlyAtTwiceTheRate(t *testing.T) {
	l, _ := openClock(t)
	rate := func(v float64) []validate.EvidenceRow {
		return []validate.EvidenceRow{evidence("x", map[string]float64{"rate": v}, "")}
	}
	fileOnce(t, l, startRun(t, l, "l", "s"), 10, rate(0.1), finding("x"))
	rule(t, l, topicOf(t, l, "x"), RulingRejected, "", "0.1")
	if res := refile(t, l, rate(0.19)); res.Reconsidered != 0 || res.Skipped != 1 {
		t.Fatalf("returned below twice the rate: %+v", res)
	}
	if res := refile(t, l, rate(0.2)); res.Reconsidered != 1 || topicOf(t, l, "x").State != StatePending {
		t.Fatalf("did not return at twice the rate: %+v", res)
	}
}

func TestReconsiderationTriggersFireAndDoNot(t *testing.T) {
	type tc struct {
		name, cond, rate string
		fire, hold       []validate.EvidenceRow
		advance          time.Duration
	}
	m := func(kv map[string]float64, seen string) []validate.EvidenceRow {
		return []validate.EvidenceRow{evidence("x", kv, seen)}
	}
	cases := []tc{
		{name: "date", cond: "date:2026-02-01", advance: 40 * day, fire: m(nil, ""), hold: nil},
		{name: "rate multiple", cond: "rate-multiple:3", rate: "10", fire: m(map[string]float64{"rate": 30}, ""), hold: m(map[string]float64{"rate": 29}, "")},
		{name: "cost threshold", cond: "cost:500", fire: m(map[string]float64{"cost": 500}, ""), hold: m(map[string]float64{"cost": 499}, "")},
		{name: "target change", cond: "target-changed", fire: m(nil, "2026-01-10T00:00:00Z"), hold: m(nil, "2025-12-31T00:00:00Z"), advance: 20 * day},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for _, wantFire := range []bool{false, true} {
				l, clk := openClock(t)
				base := m(map[string]float64{"rate": 1, "cost": 1}, "2025-12-01T00:00:00Z")
				fileOnce(t, l, startRun(t, l, "l", "s"), 10, base, finding("x"))
				rule(t, l, topicOf(t, l, "x"), RulingDeferred, c.cond, c.rate)
				clk.Advance(c.advance)
				rows := c.hold
				if wantFire {
					rows = c.fire
				}
				if c.name == "date" && !wantFire {
					clk.now = t0 // before the date
					rows = base
				}
				res := fileOnce(t, l, startRun(t, l, "l", "s"), 10, rows, finding("x"))
				if got := res.Reconsidered == 1; got != wantFire {
					t.Fatalf("fire=%v want %v (%+v)", got, wantFire, res)
				}
			}
		})
	}
}

func TestVersionTriggerOnlyForVersionSensitiveTopics(t *testing.T) {
	for _, tagged := range []bool{true, false} {
		l, _ := openClock(t)
		first := []validate.EvidenceRow{evidence("x", nil, "")}
		next := first
		if tagged {
			first = []validate.EvidenceRow{evidence("x", map[string]float64{"version": 1}, "")}
			next = []validate.EvidenceRow{evidence("x", map[string]float64{"version": 2}, "")}
		}
		fileOnce(t, l, startRun(t, l, "l", "s"), 10, first, finding("x"))
		if topicOf(t, l, "x").VersionSensitive != tagged {
			t.Fatalf("tagged=%v: topic flag %v", tagged, topicOf(t, l, "x").VersionSensitive)
		}
		rule(t, l, topicOf(t, l, "x"), RulingDeferred, "version-changed", "")
		res := fileOnce(t, l, startRun(t, l, "l", "s"), 10, next, finding("x"))
		if (res.Reconsidered == 1) != tagged {
			t.Errorf("tagged=%v: %+v", tagged, res)
		}
	}
	// A tagged topic whose version did not change holds.
	l, _ := openClock(t)
	rows := []validate.EvidenceRow{evidence("x", map[string]float64{"version": 1}, "")}
	fileOnce(t, l, startRun(t, l, "l", "s"), 10, rows, finding("x"))
	rule(t, l, topicOf(t, l, "x"), RulingDeferred, "version-changed", "")
	if res := refile(t, l, rows); res.Reconsidered != 0 {
		t.Errorf("unchanged version fired: %+v", res)
	}
}

func TestReconsideredTopicAtCapIsWaiting(t *testing.T) {
	l, c := openClock(t)
	rows := []validate.EvidenceRow{evidence("x", nil, ""), evidence("y", nil, "")}
	fileOnce(t, l, startRun(t, l, "l", "s"), 1, rows, finding("x"))
	rule(t, l, topicOf(t, l, "x"), RulingDeferred, "", "")
	fileOnce(t, l, startRun(t, l, "l", "s"), 1, rows, finding("y")) // fills the cap
	c.Advance(31 * day)
	res := fileOnce(t, l, startRun(t, l, "l", "s"), 1, rows, finding("x"))
	if res.Reconsidered != 1 || topicOf(t, l, "x").State != StateWaiting {
		t.Fatalf("%+v %s", res, topicOf(t, l, "x").State)
	}
}

func TestAutoDeferAfterThirtyDays(t *testing.T) {
	l, c := openClock(t)
	rows := []validate.EvidenceRow{evidence("x", nil, "")}
	fileOnce(t, l, startRun(t, l, "l", "s"), 10, rows, finding("x"))
	c.Advance(29 * day)
	if ids, err := l.AutoDefer(ctx, 30); err != nil || len(ids) != 0 {
		t.Fatalf("deferred early: %v %v", ids, err)
	}
	c.Advance(2 * day) // 31 days
	rev := topicOf(t, l, "x").Revision
	ids, err := l.AutoDefer(ctx, 30)
	if err != nil || len(ids) != 1 {
		t.Fatalf("%v %v", ids, err)
	}
	tp := topicOf(t, l, "x")
	if tp.State != StateDeferred || tp.Revision != rev+1 {
		t.Fatalf("%+v", tp)
	}
	ev, _ := l.TopicEvents(ctx, tp.ID)
	if len(ev) != 1 || ev[0].Note != "deferred automatically: no ruling for 31 days" {
		t.Fatalf("%+v", ev)
	}
	// Not a ruling: no decision row, so it cannot lift a lens pause.
	var n int
	_ = l.DB().QueryRow(`SELECT COUNT(*) FROM decision`).Scan(&n)
	if n != 0 {
		t.Errorf("%d decisions", n)
	}
	// An automatically deferred topic returns on the default backoff, from the auto-deferral.
	c.Advance(30 * day)
	if res := refile(t, l, rows); res.Reconsidered != 1 {
		t.Errorf("%+v", res)
	}
}

func TestSchemaHasTopicEvent(t *testing.T) {
	l := open(t)
	var n int
	if err := l.DB().QueryRow(`SELECT COUNT(*) FROM topic_event`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if SchemaVersion() < 2 {
		t.Errorf("schema version %d", SchemaVersion())
	}
}

func TestFindingsAreRedactedBeforeStorage(t *testing.T) {
	l, _ := openClock(t)
	f := finding("x")
	f.Situation = "key sk-" + strings.Repeat("a1", 12) + " end"
	fileOnce(t, l, startRun(t, l, "l", "s"), 10, []validate.EvidenceRow{evidence("x", nil, "")}, f)
	wj, _, err := l.LatestWriteup(ctx, topicOf(t, l, "x").ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(wj, "a1a1a1") || !strings.Contains(wj, "[REDACTED:sk]") {
		t.Errorf("stored write-up: %s", wj)
	}
}
