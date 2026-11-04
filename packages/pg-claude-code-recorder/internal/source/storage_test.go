package source

import (
	"encoding/json"
	"testing"
)

type storedInner struct {
	Out *int64 `json:"output_tokens" ingest:"int" store:"ext:assistant_usage.output_tokens"`
}

type stored struct {
	CWD   string       `json:"cwd" ingest:"path" store:"record:cwd_id"`
	Plain string       `json:"plain" ingest:"keyword"`
	Body  string       `json:"body" ingest:"text"`
	Usage *storedInner `json:"usage"`
}

func TestParseStorage(t *testing.T) {
	for in, want := range map[string]Storage{
		"":                  {Kind: StoreFields},
		"fields":            {Kind: StoreFields},
		"record:cwd_id":     {Kind: StoreRecord, Column: "cwd_id"},
		"ext:usage.in_tok2": {Kind: StoreExt, Table: "usage", Column: "in_tok2"},
	} {
		got, err := ParseStorage(in)
		if err != nil || got != want {
			t.Errorf("ParseStorage(%q) = %v, %v; want %v", in, got, err, want)
		}
		if in != "" && got.String() != in {
			t.Errorf("String() of %q = %q", in, got.String())
		}
	}
	for _, bad := range []string{"record:", "record:A", "ext:t", "ext:t.", "ext:.c", "ext:t.c.d", "other", "record:1a", "fields:x"} {
		if _, err := ParseStorage(bad); err == nil {
			t.Errorf("ParseStorage(%q) should fail", bad)
		}
	}
}

func TestCatalogStorageDefaultsToFieldsAndMatchesCatalog(t *testing.T) {
	st, err := CatalogStorage(stored{})
	if err != nil {
		t.Fatal(err)
	}
	if st["cwd"].String() != "record:cwd_id" || st["usage.output_tokens"].String() != "ext:assistant_usage.output_tokens" ||
		st["plain"].Kind != StoreFields || st["body"].Kind != StoreFields {
		t.Fatalf("storage %v", st)
	}
	// Exactly one storage per catalogued field: the same names, no more or less.
	cat := Catalog(stored{})
	if len(cat) != len(st) {
		t.Fatalf("catalog %v, storage %v", cat, st)
	}
	for name := range cat {
		if _, ok := st[name]; !ok {
			t.Errorf("%s has no storage", name)
		}
	}
}

func TestCatalogStorageRejectsBadDeclarations(t *testing.T) {
	type badSyntax struct {
		A string `json:"a" ingest:"keyword" store:"nope"`
	}
	type noIngest struct {
		A string `json:"a" store:"record:a"`
	}
	type searchable struct {
		A string `json:"a" ingest:"text" store:"record:a"`
	}
	type dup struct {
		A string `json:"a" ingest:"keyword" store:"record:c"`
		B string `json:"b" ingest:"keyword" store:"record:c"`
	}
	for name, v := range map[string]any{"syntax": badSyntax{}, "no ingest": noIngest{}, "searchable": searchable{}, "duplicate": dup{}} {
		if _, err := CatalogStorage(v); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestConvertIsLossless(t *testing.T) {
	type tc struct {
		kind Kind
		in   string
		ok   bool
		want Converted
	}
	for _, c := range []tc{
		{KindInt, `42`, true, Converted{N: 42}},
		{KindInt, `-7`, true, Converted{N: -7}},
		{KindInt, `3.0`, true, Converted{N: 3}},
		{KindInt, `3e2`, true, Converted{N: 300}},
		{KindInt, `9223372036854775807`, true, Converted{N: 9223372036854775807}},
		{KindInt, `9223372036854775808`, false, Converted{}},
		{KindInt, `3.5`, false, Converted{}},
		{KindInt, `1e400`, false, Converted{}},
		{KindInt, `"123"`, true, Converted{N: 123}},
		{KindInt, `"0123"`, false, Converted{}},
		{KindInt, `"+5"`, false, Converted{}},
		{KindInt, `"12 "`, false, Converted{}},
		{KindInt, `"abc"`, false, Converted{}},
		{KindInt, `true`, false, Converted{}},
		{KindInt, `null`, false, Converted{}},
		{KindInt, `[1]`, false, Converted{}},
		{KindFloat, `1.25`, true, Converted{F: 1.25}},
		{KindFloat, `3`, true, Converted{F: 3}},
		{KindFloat, `1e999`, false, Converted{}},
		{KindFloat, `"1.5"`, true, Converted{F: 1.5}},
		{KindFloat, `"1.50"`, false, Converted{}},
		{KindFloat, `"NaN"`, false, Converted{}},
		{KindBool, `true`, true, Converted{N: 1}},
		{KindBool, `false`, true, Converted{}},
		{KindBool, `1`, false, Converted{}},
		{KindBool, `"true"`, false, Converted{}},
		{KindKeyword, `"x"`, true, Converted{S: "x"}},
		{KindKeyword, `5`, false, Converted{}},
		{KindKeyword, `""`, false, Converted{}},
		{KindID, `"u-1"`, true, Converted{S: "u-1"}},
		{KindTime, `"2026-01-02T03:04:05Z"`, true, Converted{N: 1767323045000000000}},
		{KindTime, `"yesterday"`, false, Converted{}},
		{KindTime, `1767323045`, true, Converted{N: 1767323045000000000}},
		{KindTime, `1767323045.5`, false, Converted{}},
		{KindTime, `-5`, false, Converted{}},
		{KindText, `"words"`, false, Converted{}},
		{KindOpaque, `{"a":1}`, false, Converted{}},
		{KindInt, `1 2`, false, Converted{}},
		{KindInt, ``, false, Converted{}},
	} {
		got, ok := Convert(c.kind, json.RawMessage(c.in))
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("Convert(%s, %s) = %+v, %v; want %+v, %v", c.kind, c.in, got, ok, c.want, c.ok)
		}
	}
}
