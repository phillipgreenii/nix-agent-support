package classify

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the golden files under testdata")

// fixture reads a file of testdata.
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}

// golden compares res with testdata/<name>.golden.json (rewritten under -update).
func golden(t *testing.T, name string, res Result) {
	t.Helper()
	view := struct {
		Kinds  []string `json:"kinds"`
		Fields []Field  `json:"fields"`
	}{Kinds: res.Kinds, Fields: res.Fields}
	if view.Kinds == nil {
		view.Kinds = []string{}
	}
	if view.Fields == nil {
		view.Fields = []Field{}
	}
	got, err := json.MarshalIndent(view, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	path := filepath.Join("testdata", name+".golden.json")
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s (run with -update to create it): %v", path, err)
	}
	// Compared as JSON values, not bytes: the repo's formatter reflows the
	// golden files.
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(want, &w); err != nil {
		t.Fatalf("golden %s is not JSON: %v", path, err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Errorf("%s differs from its golden file:\n got: %s\nwant: %s", name, got, want)
	}
}

func fieldNames(r Result) []string {
	var out []string
	for _, f := range r.Fields {
		out = append(out, f.Name)
	}
	return out
}

// wantResult asserts the kinds exactly and that the field names are exactly
// names (nil means none).
func wantResult(t *testing.T, got Result, kinds []string, names []string) {
	t.Helper()
	if !slices.Equal(got.Kinds, kinds) {
		t.Errorf("kinds = %v, want %v (fields %v)", got.Kinds, kinds, fieldNames(got))
	}
	if gn := fieldNames(got); !slices.Equal(gn, names) {
		t.Errorf("field names = %v, want %v", gn, names)
	}
}
