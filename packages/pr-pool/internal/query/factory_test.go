package query

import (
	"testing"

	"github.com/BurntSushi/toml"
)

func TestQueryFactories_decodeByType(t *testing.T) {
	body := `
type = "event"
[event]
item_id = "i1"
item_type = "task"
`
	var holder map[string]toml.Primitive
	md, err := toml.Decode(body, &holder)
	if err != nil {
		t.Fatal(err)
	}
	reg := NewQueryFactories()
	meta := Meta{EmitTypes: []string{"work.ready"}, Trig: PeriodTrigger{}}
	q, err := reg.Decode("event", meta, md, holder["event"])
	if err != nil {
		t.Fatal(err)
	}
	eq, ok := q.(EventQuery)
	if !ok || eq.ItemID != "i1" || eq.ItemType != "task" {
		t.Fatalf("decoded query wrong: %#v", q)
	}
	// The [[query]]-level meta (emits/trigger) is installed post-decode.
	if len(eq.Emits()) != 1 || eq.Emits()[0] != "work.ready" {
		t.Fatalf("meta emits not installed: %#v", eq.Emits())
	}
	if !IsPeriod(eq.Trigger()) {
		t.Fatalf("meta trigger not installed: %#v", eq.Trigger())
	}
	if _, err := reg.Decode("nope", meta, md, toml.Primitive{}); err == nil {
		t.Fatal("unknown query type must error")
	}
	if _, err := reg.Decode("beads-ready", meta, md, toml.Primitive{}); err == nil {
		t.Fatal("beads-ready must no longer be a decodable query type (pg2-n75tk)")
	}
}

func TestQueryFactories_commandDecodeAndValidate(t *testing.T) {
	body := `
type = "command"
[command]
argv = ["my-lister", "--ready"]
format = "jsonl"
`
	var holder map[string]toml.Primitive
	md, err := toml.Decode(body, &holder)
	if err != nil {
		t.Fatal(err)
	}
	q, err := NewQueryFactories().Decode("command", Meta{EmitTypes: []string{"cmd.ready"}}, md, holder["command"])
	if err != nil {
		t.Fatal(err)
	}
	cq, ok := q.(CommandQuery)
	if !ok || len(cq.Argv) != 2 || cq.Format != FormatJSONL {
		t.Fatalf("decoded command query wrong: %#v", q)
	}
}
