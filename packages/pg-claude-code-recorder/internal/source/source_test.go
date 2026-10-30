package source

import (
	"reflect"
	"testing"
)

type inner struct {
	N *int64 `json:"output_tokens" ingest:"int"`
}

type outer struct {
	ParentUUID string  `json:"parentUuid" ingest:"id"`
	Raw        string  `json:"raw"`
	Usage      *inner  `json:"usage"`
	Items      []inner `json:"items"`
	Alias      string  `json:"x" ingest:"keyword" field:"renamed"`
}

func TestSnake(t *testing.T) {
	for in, want := range map[string]string{
		"parentUuid": "parent_uuid", "sessionId": "session_id", "isSidechain": "is_sidechain",
		"sourceToolAssistantUUID": "source_tool_assistant_uuid", "input_tokens": "input_tokens", "uuid": "uuid",
	} {
		if got := Snake(in); got != want {
			t.Errorf("Snake(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestExtractNamesNestedFieldsAndSkipsAbsent(t *testing.T) {
	n := int64(4)
	fs := Extract(&outer{ParentUUID: "p", Usage: &inner{N: &n}, Alias: "a"}, "message")
	got := map[string]Field{}
	for _, f := range fs {
		got[f.Name] = f
	}
	if got["message.parent_uuid"].S != "p" || got["message.usage.output_tokens"].N != 4 || got["renamed"].S != "a" || len(got) != 3 {
		t.Fatalf("fields %+v", got)
	}
	if len(Extract(&outer{}, "")) != 0 {
		t.Fatal("absent values must yield no field")
	}
}

func TestUnknownKeysReportDottedPaths(t *testing.T) {
	raw := []byte(`{"parentUuid":"p","extra":1,"usage":{"output_tokens":1,"newer":2},"items":[{"zzz":1},{"output_tokens":2}]}`)
	got := UnknownKeys(raw, reflect.TypeOf(outer{}), "")
	want := []string{"extra", "items[].zzz", "usage.newer"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestCatalogListsTaggedFields(t *testing.T) {
	c := Catalog(outer{})
	if c["parent_uuid"] != KindID || c["usage.output_tokens"] != KindInt || c["renamed"] != KindKeyword {
		t.Fatalf("catalog %v", c)
	}
}
