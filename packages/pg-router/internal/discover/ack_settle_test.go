package discover

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/internal/core"
	"github.com/phillipgreenii/pg-router/internal/eventqueue"
	"github.com/phillipgreenii/pg-router/internal/query"
	"github.com/phillipgreenii/pg-router/internal/settle"
)

// ackCmd is a Commander that answers the query command with canned jsonl and
// records every settle invocation (argv carrying --lane).
type ackCmd struct {
	mu       sync.Mutex
	out      string
	settles  [][]string
	queries  int
	failSett bool
}

func (c *ackCmd) Run(_ context.Context, argv []string) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if argv[0] == "settle-cmd" {
		c.settles = append(c.settles, argv)
		if c.failSett {
			return nil, errors.New("exit status 1")
		}
		return nil, nil
	}
	c.queries++
	return []byte(c.out), nil
}

func (c *ackCmd) settleTokens() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, a := range c.settles {
		out = append(out, strings.Join(a[1:], " "))
	}
	return out
}

// acceptListener matches type "ev" and accepts only the item ids in accept.
type acceptListener struct{ accept map[string]bool }

func (l acceptListener) ID() string                      { return "h" }
func (l acceptListener) Matches(e eventqueue.Event) bool { return e.Type == "ev" }
func (l acceptListener) Offer(o eventqueue.Offering) eventqueue.OfferResult {
	id, _ := o.Event.Payload["id"].(string)
	return eventqueue.OfferResult{Accepted: l.accept[id], Decline: eventqueue.DeclineBusy}
}

type ackEnv struct {
	cmd     *ackCmd
	settler *settle.Settler
	q       *eventqueue.Queue
	src     query.SourceSet
	env     query.Env
	fails   *int
}

func newAckEnv(t *testing.T, withSettle bool, l eventqueue.Listener) *ackEnv {
	t.Helper()
	ledger, _ := settle.OpenLedger("")
	cmd := &ackCmd{}
	e := &ackEnv{cmd: cmd, fails: new(int)}
	cq := query.CommandQuery{Meta: query.Meta{EmitTypes: []string{"ev"}}, Argv: []string{"feed"}, Format: query.FormatJSONL}
	if withSettle {
		cq.Settle = []string{"settle-cmd"}
		cq.AckHoldLimit = time.Hour
	}
	e.src = query.SourceSet{{Name: "feed", Query: cq}}
	e.env = query.Env{Cmd: cmd}
	e.settler = settle.NewSettler(context.Background(), ledger, func(ctx context.Context, qn, lane, tok string) error {
		return query.RunSettle(ctx, e.env, cq, lane, tok)
	}, func(string) { *e.fails++ })
	e.env.Settler = e.settler
	opts := []eventqueue.Option{eventqueue.WithAcceptHook(e.settler.OnEventSettled)}
	q, err := eventqueue.New(eventqueue.NewMemStore(), opts...)
	if err != nil {
		t.Fatal(err)
	}
	if l != nil {
		q.Register(l)
	}
	e.q = q
	return e
}

func (e *ackEnv) produce(t *testing.T, declared core.Bindings) {
	t.Helper()
	if _, err := Produce(context.Background(), e.env, e.src, e.q, declared); err != nil {
		t.Fatal(err)
	}
	e.settler.Wait()
}

const twoEvents = `{"id":"1","type":"t","ack":{"lane":"L","token":"t1"}}
{"id":"2","type":"t","ack":{"lane":"L","token":"t2"}}
`

func TestAckNoMatchingHandlerSettlesAndReportsFrontier(t *testing.T) {
	e := newAckEnv(t, true, nil)
	e.cmd.out = twoEvents
	e.produce(t, core.NewBindings("ev"))
	got := e.cmd.settleTokens()
	if len(got) != 1 || got[0] != "--lane L --through t2" {
		t.Fatalf("settle invocations = %v", got)
	}
}

func TestAckRefusedAtIngestStaysUnsettledAndHoldsTheFrontier(t *testing.T) {
	e := newAckEnv(t, true, nil)
	e.cmd.out = twoEvents
	// No binding declares "ev": every event is refused at ingest, never enqueued.
	e.produce(t, core.NewBindings("other"))
	if got := e.cmd.settleTokens(); len(got) != 0 {
		t.Fatalf("frontier passed refused events: %v", got)
	}
	ents := e.settler.Ledger().Entries("feed", "L")
	if len(ents) != 2 || ents[0].Settled || ents[1].Settled || ents[0].FirstSeen.IsZero() {
		t.Fatalf("refused events must be listed unsettled with a first-seen time: %+v", ents)
	}
}

func TestAckHandlerAcceptanceSettlesAndReports(t *testing.T) {
	l := acceptListener{accept: map[string]bool{"1": true, "2": true}}
	e := newAckEnv(t, true, l)
	e.cmd.out = twoEvents
	e.produce(t, core.NewBindings("ev"))
	// Offered but not yet accepted: matching handler outstanding, nothing reported.
	if got := e.cmd.settleTokens(); len(got) != 0 {
		t.Fatalf("reported before any acceptance: %v", got)
	}
	e.q.Dispatch()
	e.q.Dispatch()
	e.settler.Wait()
	got := e.cmd.settleTokens()
	if len(got) == 0 || got[len(got)-1] != "--lane L --through t2" {
		t.Fatalf("settle invocations = %v", got)
	}
}

func TestAckIgnoredOnAQueryWithoutSettle(t *testing.T) {
	e := newAckEnv(t, false, nil)
	e.cmd.out = twoEvents
	e.produce(t, core.NewBindings("ev"))
	if len(e.settler.Ledger().Queries()) != 0 || len(e.cmd.settleTokens()) != 0 {
		t.Fatal("an ack on a query without settle must create no ledger and run nothing")
	}
}

func TestAckMalformedHandleIsIgnored(t *testing.T) {
	e := newAckEnv(t, true, nil)
	e.cmd.out = `{"id":"1","type":"t","ack":{"lane":"L"}}
{"id":"2","type":"t","ack":{"lane":1,"token":"x"}}
{"id":"3","type":"t","ack":"nope"}
`
	e.produce(t, core.NewBindings("ev"))
	if len(e.settler.Ledger().Queries()) != 0 {
		t.Fatal("a malformed ack must not create ledger state")
	}
}

func TestAckSettleFailureIsCountedAndRetriedOnNextTrigger(t *testing.T) {
	e := newAckEnv(t, true, nil)
	e.cmd.out = twoEvents
	e.cmd.failSett = true
	e.produce(t, core.NewBindings("ev"))
	if *e.fails != 1 {
		t.Fatalf("failures counted = %d, want 1", *e.fails)
	}
	if e.q.DepthByType()["ev"] != 2 {
		t.Fatalf("a failed report must never touch events; depth=%v", e.q.DepthByType())
	}
	e.cmd.mu.Lock()
	e.cmd.failSett = false
	e.cmd.mu.Unlock()
	e.produce(t, core.NewBindings("ev")) // next trigger of the query: the retry
	if got := e.cmd.settleTokens(); len(got) != 2 || got[1] != "--lane L --through t2" {
		t.Fatalf("settle invocations = %v", got)
	}
}

func TestAckNewerPollReplacesTheLane(t *testing.T) {
	e := newAckEnv(t, true, acceptListener{accept: map[string]bool{}})
	e.cmd.out = twoEvents
	e.produce(t, core.NewBindings("ev"))
	e.cmd.out = `{"id":"2","type":"t","ack":{"lane":"L","token":"t2b"}}
{"id":"3","type":"t","ack":{"lane":"L","token":"t3"}}
`
	e.produce(t, core.NewBindings("ev"))
	ents := e.settler.Ledger().Entries("feed", "L")
	if len(ents) != 2 || ents[0].ID != "ev:2" || ents[0].Token != "t2b" || ents[1].ID != "ev:3" {
		t.Fatalf("lane list not replaced: %+v", ents)
	}
}
