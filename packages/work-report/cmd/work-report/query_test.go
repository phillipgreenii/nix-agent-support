package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/store"
)

// seedQueryStore builds a store with multi-day, multi-source, multi-label
// entries and one re-observed (superseded) id, and returns its path. Dates are
// absolute, so the tests use explicit YYYY-MM-DD ranges.
func seedQueryStore(t *testing.T) string {
	t.Helper()
	cfgDir := t.TempDir()
	path := filepath.Join(cfgDir, "store.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	d := func(day, hour int) time.Time { return time.Date(2026, 3, day, hour, 0, 0, 0, time.UTC) }
	now := d(9, 0)
	for _, e := range []store.Entry{
		{ID: "a", ExternalID: "x1", SourceID: "backend-one", Type: "change", OccurredAt: d(1, 10), Summary: "a v1", Labels: []string{"x"}},
		{ID: "b", ExternalID: "x2", SourceID: "backend-two", Type: "issue", OccurredAt: d(1, 11), Summary: "b", Labels: []string{"x", "y"}, Fields: json.RawMessage(`{"k":1}`)},
		{ID: "c", ExternalID: "x3", SourceID: "backend-one", Type: "review", OccurredAt: d(2, 10), Summary: "c", Labels: []string{"z"}},
		{ID: "a", ExternalID: "x1", SourceID: "backend-one", Type: "change", OccurredAt: d(1, 10), Summary: "a v2", Labels: []string{"x"}},
	} {
		if _, err := st.Append(context.Background(), e, now); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func queryIDs(t *testing.T, args ...string) []string {
	t.Helper()
	isolate(t)
	out, err := runCLI(t, args...)
	if err != nil {
		t.Fatalf("%v: %v", args, err)
	}
	var got []queryEntryJSON
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not a JSON array: %v\n%s", err, out)
	}
	ids := make([]string, len(got))
	for i, e := range got {
		ids[i] = e.ID
	}
	sort.Strings(ids)
	return ids
}

func TestQueryNarrowing(t *testing.T) {
	storePath := seedQueryStore(t)
	base := []string{"--store", storePath, "query", "--range", "2026-03-01..2026-03-02"}
	cases := []struct {
		name string
		args []string
		want []string
	}{
		{"one entry per id", nil, []string{"a", "b", "c"}},
		{"single day", []string{"--range", "2026-03-01"}, []string{"a", "b"}},
		{"id", []string{"--id", "c"}, []string{"c"}},
		{"type OR within", []string{"--type", "change", "--type", "issue"}, []string{"a", "b"}},
		{"type AND label", []string{"--type", "change", "--type", "issue", "--label", "y"}, []string{"b"}},
		{"label OR within", []string{"--label", "y", "--label", "z"}, []string{"b", "c"}},
		{"source", []string{"--source", "backend-one"}, []string{"a", "c"}},
		{"source AND type AND label", []string{"--source", "backend-one", "--type", "change", "--label", "x"}, []string{"a"}},
		{"no match", []string{"--type", "change", "--label", "z"}, []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := queryIDs(t, append(append([]string{}, base...), tc.args...)...)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ids = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestQueryJSONShapeAndSupersession(t *testing.T) {
	storePath := seedQueryStore(t)
	isolate(t)
	out, err := runCLI(t, "--store", storePath, "query", "--range", "2026-03-01", "--id", "a", "--output", "json")
	if err != nil {
		t.Fatal(err)
	}
	var raw []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw) != 1 {
		t.Fatalf("want exactly one entry for a re-observed id, got %d:\n%s", len(raw), out)
	}
	keys := make([]string, 0, len(raw[0]))
	for k := range raw[0] {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	want := []string{"external_id", "fields", "id", "ingested_at", "labels", "occurred_at", "source_id", "summary", "type", "url"}
	if !reflect.DeepEqual(keys, want) {
		t.Errorf("keys = %v, want %v", keys, want)
	}
	if string(raw[0]["summary"]) != `"a v2"` {
		t.Errorf("summary = %s, want the latest observation", raw[0]["summary"])
	}
	if string(raw[0]["occurred_at"]) != `"2026-03-01T10:00:00Z"` {
		t.Errorf("occurred_at = %s, want UTC RFC3339", raw[0]["occurred_at"])
	}
}

func TestQueryDefaultOutputIsJSON(t *testing.T) {
	storePath := seedQueryStore(t)
	isolate(t)
	def, err := runCLI(t, "--store", storePath, "query", "--range", "2026-03-01")
	if err != nil {
		t.Fatal(err)
	}
	explicit, err := runCLI(t, "--store", storePath, "query", "--range", "2026-03-01", "--output", "json")
	if err != nil {
		t.Fatal(err)
	}
	if def != explicit {
		t.Errorf("default output differs from --output json:\n%s\nvs\n%s", def, explicit)
	}
}

func TestQueryEmptyRangePrintsEmptyArray(t *testing.T) {
	storePath := seedQueryStore(t)
	isolate(t)
	out, err := runCLI(t, "--store", storePath, "query", "--range", "2020-01-01", "--output", "json")
	if err != nil {
		t.Fatalf("empty result must exit 0: %v", err)
	}
	if strings.TrimSpace(out) != "[]" {
		t.Errorf("output = %q, want []", out)
	}
}

func TestQueryHumanTable(t *testing.T) {
	storePath := seedQueryStore(t)
	isolate(t)
	out, err := runCLI(t, "--store", storePath, "query", "--range", "2026-03-01", "--output", "human")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"OCCURRED", "SOURCE", "backend-two", "issue", "x,y"} {
		if !strings.Contains(out, want) {
			t.Errorf("human table lacks %q:\n%s", want, out)
		}
	}
	empty, err := runCLI(t, "--store", storePath, "query", "--range", "2020-01-01", "--output", "human")
	if err != nil || !strings.Contains(empty, "no entries") {
		t.Errorf("empty human output = %q, %v", empty, err)
	}
}

func TestQueryOutcomesExitNonZero(t *testing.T) {
	storePath := seedQueryStore(t)
	isolate(t)
	if _, err := runCLI(t, "--store", storePath, "query", "--range", "not-a-range"); err == nil {
		t.Error("an unrecognized range spec must be an error (exit 1)")
	}
	if _, err := runCLI(t, "--store", storePath, "query", "--range", "today", "--output", "yaml"); err == nil {
		t.Error("an unsupported --output must be an error")
	}
	// A store path that cannot be created (a regular file stands where its
	// parent directory must be) is an outcome, not a crash.
	if _, err := runCLI(t, "--store", filepath.Join(storePath, "sub", "x.db"), "query", "--range", "today"); err == nil {
		t.Error("a store that cannot be opened must be an error (exit 1)")
	}
}
