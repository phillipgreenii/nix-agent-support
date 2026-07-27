package discover

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pr-pool/internal/event"
	"github.com/phillipgreenii/pr-pool/internal/eventbus"
	"github.com/phillipgreenii/pr-pool/internal/item"
	"github.com/phillipgreenii/pr-pool/internal/query"
	"github.com/phillipgreenii/pr-pool/internal/roles"
)

// fakeQuery is a stand-in producer: it embeds query.Meta (for Emits/Trigger) and
// returns canned events (or an error).
type fakeQuery struct {
	query.Meta
	events []event.Event
	err    error
}

func (f fakeQuery) Validate() error { return nil }
func (f fakeQuery) Run(context.Context, query.Env) ([]event.Event, error) {
	return f.events, f.err
}

func itemEvt(typ, id string) event.Event {
	return event.NewItemEvent(typ, "", item.Item{ID: id, Type: "task"})
}

var epoch = time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)

func TestProduce_periodQueriesPublishToBoundRoles(t *testing.T) {
	sources := query.SourceSet{
		{Name: "feedback-source", Query: fakeQuery{
			Meta:   query.Meta{EmitTypes: []string{"feedback.ready"}, Trig: query.PeriodTrigger{}},
			events: []event.Event{itemEvt("feedback.ready", "fb-1")},
		}},
		{Name: "worker-source", Query: fakeQuery{
			Meta:   query.Meta{EmitTypes: []string{"work.ready"}, Trig: query.PeriodTrigger{}},
			events: []event.Event{itemEvt("work.ready", "wk-1"), itemEvt("work.ready", "wk-2")},
		}},
	}
	bus := eventbus.New()
	bus.Subscribe("feedback", "feedback.ready")
	bus.Subscribe("worker", "work.ready")

	if err := Produce(context.Background(), query.Env{}, sources, bus, epoch); err != nil {
		t.Fatal(err)
	}
	fb, _ := bus.Lease(context.Background(), "feedback", 10)
	wk, _ := bus.Lease(context.Background(), "worker", 10)
	if len(fb) != 1 || fb[0].Item.ID != "fb-1" {
		t.Fatalf("feedback queue wrong: %+v", fb)
	}
	if len(wk) != 2 {
		t.Fatalf("worker queue wrong: %+v", wk)
	}
	// Provenance stamped from the source name.
	if fb[0].Source != "feedback-source" {
		t.Fatalf("event source must be stamped from the query name, got %q", fb[0].Source)
	}
}

func TestProduce_queryErrorPropagates(t *testing.T) {
	sentinel := errors.New("bd down")
	sources := query.SourceSet{
		{Name: "boom", Query: fakeQuery{Meta: query.Meta{EmitTypes: []string{"x"}}, err: sentinel}},
	}
	err := Produce(context.Background(), query.Env{}, sources, eventbus.New(), epoch)
	if err == nil || !errors.Is(err, sentinel) {
		t.Fatalf("a query error must propagate; got %v", err)
	}
}

func TestProduce_thresholdFiresOnlyWhenEnough(t *testing.T) {
	// upstream (period) emits one "up" event; downstream (threshold Count=1 on
	// "up") should fire and emit a "down" event.
	newSources := func(count int) query.SourceSet {
		return query.SourceSet{
			{Name: "up-source", Query: fakeQuery{
				Meta:   query.Meta{EmitTypes: []string{"up"}, Trig: query.PeriodTrigger{}},
				events: []event.Event{itemEvt("up", "u1")},
			}},
			{Name: "down-source", Query: fakeQuery{
				Meta:   query.Meta{EmitTypes: []string{"down"}, Trig: query.ThresholdTrigger{Binds: []string{"up"}, Count: count}},
				events: []event.Event{itemEvt("down", "d1")},
			}},
		}
	}

	// Count=1: one "up" is enough — the threshold query fires.
	bus := eventbus.New()
	bus.Subscribe("upR", "up")
	bus.Subscribe("downR", "down")
	if err := Produce(context.Background(), query.Env{}, newSources(1), bus, epoch); err != nil {
		t.Fatal(err)
	}
	if got, _ := bus.Lease(context.Background(), "downR", 10); len(got) != 1 {
		t.Fatalf("threshold(Count=1) must fire on one upstream event, got %d", len(got))
	}

	// Count=2: one "up" is NOT enough — the threshold query does not fire.
	bus2 := eventbus.New()
	bus2.Subscribe("upR", "up")
	bus2.Subscribe("downR", "down")
	if err := Produce(context.Background(), query.Env{}, newSources(2), bus2, epoch); err != nil {
		t.Fatal(err)
	}
	if got, _ := bus2.Lease(context.Background(), "downR", 10); len(got) != 0 {
		t.Fatalf("threshold(Count=2) must NOT fire on one upstream event, got %d", len(got))
	}
}

func TestProduce_manualNeverFiresOnTick(t *testing.T) {
	sources := query.SourceSet{
		{Name: "manual", Query: fakeQuery{
			Meta:   query.Meta{EmitTypes: []string{"m"}, Trig: query.ManualTrigger{}},
			events: []event.Event{itemEvt("m", "m1")},
		}},
	}
	bus := eventbus.New()
	bus.Subscribe("mR", "m")
	if err := Produce(context.Background(), query.Env{}, sources, bus, epoch); err != nil {
		t.Fatal(err)
	}
	if got, _ := bus.Lease(context.Background(), "mR", 10); len(got) != 0 {
		t.Fatalf("manual trigger must not fire on a tick, got %d", len(got))
	}
}

func TestDeriveContext(t *testing.T) {
	role := roles.Role{Name: "worker"}
	e := itemEvt("work.ready", "zr-9")
	d := DeriveContext(role, e)
	if d.Role.Name != "worker" || d.Item.ID != "zr-9" {
		t.Fatalf("derived context wrong: %+v", d)
	}
}

func TestQueriesForRole(t *testing.T) {
	sources := query.SourceSet{
		{Name: "a", Query: fakeQuery{Meta: query.Meta{EmitTypes: []string{"feedback.ready"}}}},
		{Name: "b", Query: fakeQuery{Meta: query.Meta{EmitTypes: []string{"work.ready"}}}},
		{Name: "c", Query: fakeQuery{Meta: query.Meta{EmitTypes: []string{"feedback.ready"}}}},
	}
	role := roles.Role{Name: "feedback", Binds: []string{"feedback.ready"}}
	got := QueriesForRole(sources, role)
	if len(got) != 2 || got[0].Name != "a" || got[1].Name != "c" {
		t.Fatalf("QueriesForRole should return the feedback-emitting sources, got %+v", got)
	}
}

func TestDispatchContext_Validate(t *testing.T) {
	cases := []struct {
		name     string
		d        DispatchContext
		wantErr  bool
		wantSubs []string
	}{
		{"valid", DispatchContext{Role: roles.Role{Name: "worker"}, Item: item.Item{ID: "zr-1"}}, false, nil},
		{"missing-item", DispatchContext{Role: roles.Role{Name: "worker"}}, true, []string{"item"}},
		{"missing-role", DispatchContext{Item: item.Item{ID: "zr-1"}}, true, []string{"role"}},
		{"missing-both", DispatchContext{}, true, []string{"role", "item"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.d.Validate()
			if tc.wantErr != (err != nil) {
				t.Fatalf("Validate() err=%v, wantErr=%v", err, tc.wantErr)
			}
			for _, sub := range tc.wantSubs {
				if !strings.Contains(err.Error(), sub) {
					t.Errorf("err %q should mention %q", err, sub)
				}
			}
		})
	}
}
