package query

import (
	"context"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestEventQuery_emitsCorrelatedEvent(t *testing.T) {
	q := EventQuery{
		Meta:          Meta{EmitTypes: []string{"work.done"}},
		ItemID:        "zr-1",
		ItemType:      "task",
		Title:         "done",
		CorrelationID: "PR-7",
	}
	if err := q.Validate(); err != nil {
		t.Fatalf("valid event query must pass Validate: %v", err)
	}
	evts, err := q.Run(context.Background(), Env{})
	if err != nil {
		t.Fatal(err)
	}
	if len(evts) != 1 {
		t.Fatalf("event query must emit exactly one event, got %d", len(evts))
	}
	e := evts[0]
	if e.Type != "work.done" || e.Item.ID != "zr-1" || e.CorrelationID != "PR-7" {
		t.Fatalf("emitted event wrong: %+v", e)
	}
}

func TestEventQuery_validateRequiresItemAndEmits(t *testing.T) {
	if err := (EventQuery{Meta: Meta{EmitTypes: []string{"x"}}}).Validate(); err == nil {
		t.Fatal("missing item_id must fail Validate")
	}
	if err := (EventQuery{ItemID: "zr-1"}).Validate(); err == nil {
		t.Fatal("missing emits must fail Validate")
	}
}

// The M5 event query type is registered in the factory (spec C deferred it).
func TestFactory_eventTypeRegistered(t *testing.T) {
	body := `
type = "event"
[event]
item_id = "zr-9"
item_type = "task"
correlation_id = "grp"
`
	var holder map[string]toml.Primitive
	md, err := toml.Decode(body, &holder)
	if err != nil {
		t.Fatal(err)
	}
	q, err := NewQueryFactories().Decode("event", Meta{EmitTypes: []string{"agg.in"}}, md, holder["event"])
	if err != nil {
		t.Fatalf("event query type must be registered and decode: %v", err)
	}
	eq, ok := q.(EventQuery)
	if !ok || eq.ItemID != "zr-9" || eq.CorrelationID != "grp" {
		t.Fatalf("decoded event query wrong: %#v", q)
	}
}
