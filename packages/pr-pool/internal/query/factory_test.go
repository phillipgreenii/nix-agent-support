package query

import (
	"testing"

	"github.com/BurntSushi/toml"
)

func TestQueryFactories_decodeByType(t *testing.T) {
	body := `
type = "beads-ready"
[beads-ready]
labels = ["worker-ready"]
exclude_labels = ["human"]
`
	var holder map[string]toml.Primitive
	md, err := toml.Decode(body, &holder)
	if err != nil {
		t.Fatal(err)
	}
	reg := NewQueryFactories()
	q, err := reg.Decode("beads-ready", md, holder["beads-ready"])
	if err != nil {
		t.Fatal(err)
	}
	br, ok := q.(BeadsReady)
	if !ok || len(br.Labels) != 1 || br.Labels[0] != "worker-ready" || len(br.ExcludeLabels) != 1 || br.ExcludeLabels[0] != "human" {
		t.Fatalf("decoded query wrong: %#v", q)
	}
	if _, err := reg.Decode("nope", md, toml.Primitive{}); err == nil {
		t.Fatal("unknown query type must error")
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
	q, err := NewQueryFactories().Decode("command", md, holder["command"])
	if err != nil {
		t.Fatal(err)
	}
	cq, ok := q.(CommandQuery)
	if !ok || len(cq.Argv) != 2 || cq.Format != FormatJSONL {
		t.Fatalf("decoded command query wrong: %#v", q)
	}
}
