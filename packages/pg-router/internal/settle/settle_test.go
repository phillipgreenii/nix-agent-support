package settle

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

var t0 = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func items(pairs ...string) []Item {
	var out []Item
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, Item{ID: pairs[i], Token: pairs[i+1]})
	}
	return out
}

func TestFrontierNeverPassesAnUnsettledEvent(t *testing.T) {
	l, _ := OpenLedger("")
	_ = l.Poll("q", "a", items("e1", "t1", "e2", "t2", "e3", "t3"), t0)
	if _, ok := l.Frontier("q", "a"); ok {
		t.Fatal("frontier with nothing settled must be absent")
	}
	// e3 settled out of order: the frontier still must not pass unsettled e1.
	_, _ = l.MarkSettled("e3")
	if _, ok := l.Frontier("q", "a"); ok {
		t.Fatal("frontier passed unsettled e1")
	}
	_, _ = l.MarkSettled("e1")
	if tok, _ := l.Frontier("q", "a"); tok != "t1" {
		t.Fatalf("frontier = %q, want t1 (e2 unsettled)", tok)
	}
	_, _ = l.MarkSettled("e2")
	if tok, _ := l.Frontier("q", "a"); tok != "t3" {
		t.Fatalf("frontier = %q, want t3", tok)
	}
}

func TestFrontierIsListOrderNotTokenOrder(t *testing.T) {
	l, _ := OpenLedger("")
	// Tokens are opaque: "z" sorts after "a" but comes first in emission order.
	_ = l.Poll("q", "a", items("e1", "z", "e2", "a"), t0)
	_, _ = l.MarkSettled("e1")
	if tok, _ := l.Frontier("q", "a"); tok != "z" {
		t.Fatalf("frontier = %q, want z", tok)
	}
}

func TestPollReplacesListKeepsSettlementAndFirstSeen(t *testing.T) {
	l, _ := OpenLedger("")
	_ = l.Poll("q", "a", items("e1", "t1", "e2", "t2"), t0)
	_, _ = l.MarkSettled("e1")
	t1 := t0.Add(time.Hour)
	_ = l.Poll("q", "a", items("e1", "t1b", "e3", "t3"), t1)
	got := l.Entries("q", "a")
	if len(got) != 2 || got[0].ID != "e1" || got[1].ID != "e3" {
		t.Fatalf("list not replaced: %+v", got)
	}
	if !got[0].Settled || got[0].Token != "t1b" || !got[0].FirstSeen.Equal(t0) {
		t.Fatalf("re-emitted e1 must keep settlement and first-seen, take the new token: %+v", got[0])
	}
	if got[1].Settled || !got[1].FirstSeen.Equal(t1) {
		t.Fatalf("new e3 must be unsettled and first seen now: %+v", got[1])
	}
}

func TestLedgerSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.json")
	l, _ := OpenLedger(path)
	_ = l.Poll("q", "a", items("e1", "t1", "e2", "t2"), t0)
	_, _ = l.MarkSettled("e1")
	l2, err := OpenLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	if tok, ok := l2.Frontier("q", "a"); !ok || tok != "t1" {
		t.Fatalf("restarted frontier = %q %v", tok, ok)
	}
	if ids := l2.UnsettledIDs(); len(ids) != 1 || ids[0] != "e2" {
		t.Fatalf("unsettled = %v", ids)
	}
	if e := l2.Entries("q", "a"); !e[0].FirstSeen.Equal(t0) {
		t.Fatalf("first-seen lost: %+v", e)
	}
}

type call struct{ query, lane, token string }

func TestSettlerReportsFrontierAndRetriesAfterFailure(t *testing.T) {
	l, _ := OpenLedger("")
	_ = l.Poll("q", "a", items("e1", "t1", "e2", "t2"), t0)
	var mu sync.Mutex
	var calls []call
	failNext := true
	fails := 0
	s := NewSettler(context.Background(), l, func(_ context.Context, q, lane, tok string) error {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, call{q, lane, tok})
		if failNext {
			failNext = false
			return errors.New("exit 1")
		}
		return nil
	}, func(string) { fails++ })
	s.OnEventSettled("e1") // not listed as changed? it is: marks e1
	s.Wait()
	if len(calls) != 1 || calls[0] != (call{"q", "a", "t1"}) || fails != 1 {
		t.Fatalf("calls=%v fails=%d", calls, fails)
	}
	// Failed report: nothing re-enqueued; the next evaluation retries.
	s.Evaluate("q")
	s.Wait()
	if len(calls) != 2 || calls[1].token != "t1" {
		t.Fatalf("not retried: %v", calls)
	}
	// Same frontier is not re-reported.
	s.Evaluate("q")
	s.Wait()
	if len(calls) != 2 {
		t.Fatalf("re-reported an unchanged frontier: %v", calls)
	}
}

func TestSettlerOneInFlightPerLaneAndCoalesces(t *testing.T) {
	l, _ := OpenLedger("")
	_ = l.Poll("q", "a", items("e1", "t1", "e2", "t2", "e3", "t3"), t0)
	started := make(chan string, 8)
	release := make(chan struct{})
	var mu sync.Mutex
	var toks []string
	inFlight, maxInFlight := 0, 0
	s := NewSettler(context.Background(), l, func(_ context.Context, _, _, tok string) error {
		mu.Lock()
		inFlight++
		if inFlight > maxInFlight {
			maxInFlight = inFlight
		}
		toks = append(toks, tok)
		mu.Unlock()
		started <- tok
		<-release
		mu.Lock()
		inFlight--
		mu.Unlock()
		return nil
	}, nil)
	s.OnEventSettled("e1")
	<-started // first report (t1) is running
	// Two advances while it runs, each evaluated: must coalesce.
	s.OnEventSettled("e2")
	s.OnEventSettled("e3")
	close(release)
	s.Wait()
	mu.Lock()
	defer mu.Unlock()
	if maxInFlight != 1 {
		t.Fatalf("max in flight = %d, want 1", maxInFlight)
	}
	if len(toks) != 2 || toks[0] != "t1" || toks[1] != "t3" {
		t.Fatalf("reports = %v, want [t1 t3] (one follow-up with the newest frontier)", toks)
	}
}

func TestRestartReadsSettlementFromQueueAndResends(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.json")
	l, _ := OpenLedger(path)
	_ = l.Poll("q", "a", items("e1", "t1", "e2", "t2"), t0)
	// Crash point: the queue recorded e1's acceptance, the ledger never did.
	l2, _ := OpenLedger(path)
	var calls []call
	s := NewSettler(context.Background(), l2, func(_ context.Context, q, lane, tok string) error {
		calls = append(calls, call{q, lane, tok})
		return nil
	}, nil)
	s.Restart(func(id string) bool { return id == "e1" })
	s.Wait()
	if len(calls) != 1 || calls[0].token != "t1" {
		t.Fatalf("restart report = %v", calls)
	}
	// Crash after the ledger write but before the report: a new process resends.
	l3, _ := OpenLedger(path)
	var again []call
	s3 := NewSettler(context.Background(), l3, func(_ context.Context, q, lane, tok string) error {
		again = append(again, call{q, lane, tok})
		return nil
	}, nil)
	s3.Restart(func(string) bool { return false })
	s3.Wait()
	if len(again) != 1 || again[0].token != "t1" {
		t.Fatalf("resend after restart = %v", again)
	}
}
