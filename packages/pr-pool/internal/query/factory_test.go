package query

import (
	"testing"

	"github.com/BurntSushi/toml"
)

func TestQueryFactories_decodeByType(t *testing.T) {
	body := `
type = "command"
[command]
argv = ["lister", "--ready"]
format = "jsonl"
`
	var holder map[string]toml.Primitive
	md, err := toml.Decode(body, &holder)
	if err != nil {
		t.Fatal(err)
	}
	reg := NewQueryFactories()
	meta := Meta{EmitTypes: []string{"work.ready"}, Trig: PeriodTrigger{}}
	q, err := reg.Decode("command", meta, md, holder["command"])
	if err != nil {
		t.Fatal(err)
	}
	cq, ok := q.(CommandQuery)
	if !ok || len(cq.Argv) != 2 || cq.Argv[0] != "lister" {
		t.Fatalf("decoded query wrong: %#v", q)
	}
	// The [[query]]-level meta (emits/trigger) is installed post-decode.
	if len(cq.Emits()) != 1 || cq.Emits()[0] != "work.ready" {
		t.Fatalf("meta emits not installed: %#v", cq.Emits())
	}
	if !IsPeriod(cq.Trigger()) {
		t.Fatalf("meta trigger not installed: %#v", cq.Trigger())
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
