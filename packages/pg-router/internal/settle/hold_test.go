package settle

import (
	"context"
	"sync"
	"testing"
	"time"
)

type holdFixture struct {
	mu        sync.Mutex
	now       time.Time
	reported  []string
	abandoned []string
}

func newHold(t *testing.T, limit time.Duration) (*holdFixture, *Settler) {
	t.Helper()
	f := &holdFixture{now: t0}
	l, _ := OpenLedger("")
	st := NewSettler(context.Background(), l, func(_ context.Context, _, _, token string) error {
		f.mu.Lock()
		f.reported = append(f.reported, token)
		f.mu.Unlock()
		return nil
	}, nil)
	st.ConfigureHold(func(string) time.Duration { return limit }, func() time.Time {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.now
	}, func(q, typ string) {
		f.mu.Lock()
		f.abandoned = append(f.abandoned, q+"/"+typ)
		f.mu.Unlock()
	})
	return f, st
}

func (f *holdFixture) advance(d time.Duration) {
	f.mu.Lock()
	f.now = f.now.Add(d)
	f.mu.Unlock()
}

func TestHoldLimitAbandonsOldestAndFrontierMovesToNext(t *testing.T) {
	f, st := newHold(t, time.Hour)
	_ = st.Ledger().Poll("q", "a", []Item{{ID: "e1", Token: "t1", Type: "ty"}, {ID: "e2", Token: "t2", Type: "ty"}}, t0)
	f.advance(30 * time.Minute)
	// e2 is listed again by a newer poll (keeps first-seen t0); e3 is new at +30m.
	_ = st.Ledger().Poll("q", "a", []Item{{ID: "e1", Token: "t1", Type: "ty"}, {ID: "e2", Token: "t2", Type: "ty"}, {ID: "e3", Token: "t3", Type: "ty"}}, f.now)
	_, _ = st.Ledger().MarkSettled("e2")
	st.Evaluate("q")
	st.Wait()
	if len(f.reported) != 0 || len(f.abandoned) != 0 {
		t.Fatalf("inside the limit nothing is reported or abandoned: reported=%v abandoned=%v", f.reported, f.abandoned)
	}
	f.advance(45 * time.Minute) // e1 is 75m old (over), e3 only 45m
	st.Evaluate("q")
	st.Wait()
	if len(f.abandoned) != 1 || f.abandoned[0] != "q/ty" {
		t.Fatalf("abandoned = %v, want [q/ty]", f.abandoned)
	}
	// e1 abandoned counts as settled, e2 settled: frontier is t2, held behind e3.
	if len(f.reported) != 1 || f.reported[0] != "t2" {
		t.Fatalf("reported = %v, want [t2]", f.reported)
	}
	f.advance(30 * time.Minute) // e3 now 75m old: it has its own age and is abandoned next
	st.Evaluate("q")
	st.Wait()
	if len(f.abandoned) != 2 || f.reported[len(f.reported)-1] != "t3" {
		t.Fatalf("abandoned=%v reported=%v, want e3 abandoned and t3 reported", f.abandoned, f.reported)
	}
}

// An event the core refused at ingest has a ledger entry and no queue entry; it
// is unsettled and the hold limit abandons it like any other.
func TestHoldLimitAbandonsRefusedAtIngestEvent(t *testing.T) {
	f, st := newHold(t, time.Hour)
	_ = st.Ledger().Poll("q", "a", []Item{{ID: "refused", Token: "t1", Type: "ty"}}, t0)
	st.Evaluate("q")
	st.Wait()
	if len(f.reported) != 0 {
		t.Fatalf("reported %v past an unsettled refused event", f.reported)
	}
	f.advance(2 * time.Hour)
	st.Evaluate("q")
	st.Wait()
	if len(f.abandoned) != 1 || len(f.reported) != 1 || f.reported[0] != "t1" {
		t.Fatalf("abandoned=%v reported=%v", f.abandoned, f.reported)
	}
}

// Nothing but settlement or abandonment moves the reported position.
func TestNoReportOnAnyOtherBasis(t *testing.T) {
	f, st := newHold(t, 24*time.Hour)
	_ = st.Ledger().Poll("q", "a", []Item{{ID: "e1", Token: "t1"}, {ID: "e2", Token: "t2"}}, t0)
	_, _ = st.Ledger().MarkSettled("e2") // later event settled, earlier not
	f.advance(23 * time.Hour)
	for i := 0; i < 3; i++ {
		st.Evaluate("q")
	}
	st.Wait()
	if len(f.reported) != 0 {
		t.Fatalf("reported %v while e1 is unsettled and inside the limit", f.reported)
	}
}

func TestHeldFollowsTheFrontier(t *testing.T) {
	l, _ := OpenLedger("")
	_ = l.Poll("q", "a", items("e1", "t1", "e2", "t2", "e3", "t3"), t0)
	_, _ = l.MarkSettled("e2")
	for _, id := range []string{"e1", "e2", "e3"} {
		if !l.Held(id) {
			t.Fatalf("%s must be held while e1 is unsettled", id)
		}
	}
	_, _ = l.MarkSettled("e1")
	if l.Held("e1") || l.Held("e2") {
		t.Fatal("e1 and e2 are passed by the frontier")
	}
	if !l.Held("e3") {
		t.Fatal("e3 is itself unsettled")
	}
	if l.Held("unknown") {
		t.Fatal("an unlisted id is not held")
	}
	// A newer poll that no longer lists e1 releases it.
	_ = l.Poll("q", "a", items("e3", "t3"), t0)
	if l.Held("e1") {
		t.Fatal("a dropped id is released")
	}
}

func TestHeldSurvivesRestart(t *testing.T) {
	p := t.TempDir() + "/l.json"
	l, _ := OpenLedger(p)
	_ = l.Poll("q", "a", items("e1", "t1", "e2", "t2"), t0)
	_, _ = l.MarkSettled("e2")
	l2, err := OpenLedger(p)
	if err != nil {
		t.Fatal(err)
	}
	if !l2.Held("e1") || !l2.Held("e2") {
		t.Fatal("held set must be rebuilt from the persisted ledger")
	}
}

func TestFrontierAges(t *testing.T) {
	f, st := newHold(t, 24*time.Hour)
	_ = st.Ledger().Poll("q", "a", items("e1", "t1", "e2", "t2"), t0)
	_ = st.Ledger().Poll("q", "b", items("x", "t"), t0)
	_, _ = st.Ledger().MarkSettled("x")
	f.advance(90 * time.Second)
	ages := st.FrontierAges()
	if len(ages) != 2 || ages[0].Lane != "a" || ages[0].Age != 90*time.Second || ages[1].Lane != "b" || ages[1].Age != 0 {
		t.Fatalf("ages = %+v", ages)
	}
}

// newTestSettler is NewSettler with the clock pinned to t0, the instant the
// fixtures poll at, so no fixture event is old enough to be abandoned.
func newTestSettler(ctx context.Context, l *Ledger, run Runner, onFail func(string)) *Settler {
	s := NewSettler(ctx, l, run, onFail)
	s.ConfigureHold(nil, func() time.Time { return t0 }, nil)
	return s
}
