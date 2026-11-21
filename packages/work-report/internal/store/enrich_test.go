package store

import (
	"encoding/json"
	"testing"
)

func enriched(id string) Entry {
	e := entry(id)
	e.Type = "commit"
	e.Labels = []string{"repo:r", "branch:main"}
	e.Fields = json.RawMessage(`{"sha":"abc","insertions":3,"deletions":1}`)
	return e
}

func unenriched(id string) Entry {
	e := enriched(id)
	e.Labels = []string{"repo:r"}
	e.Fields = json.RawMessage(`{"sha":"abc"}`)
	return e
}

func latest(t *testing.T, s *Store, id string) Entry {
	t.Helper()
	es, err := s.Query(ctx, Filter{OpenStart: true, ID: id})
	if err != nil || len(es) != 1 {
		t.Fatalf("Query(%s) = %v, %v", id, es, err)
	}
	return es[0]
}

func TestKeepEnrichedKeepsStoredCopyOnEnrichmentOnlyDifference(t *testing.T) {
	s := openTemp(t)
	mustAppend(t, s, enriched("c1"), utc(2026, 3, 1, 13, 0, 0))
	r, err := s.AppendKeepEnriched(ctx, unenriched("c1"), utc(2026, 3, 1, 14, 0, 0))
	if err != nil || r != Unchanged {
		t.Fatalf("AppendKeepEnriched = %v, %v; want Unchanged", r, err)
	}
	if n := rowCount(t, s, "entry"); n != 1 {
		t.Errorf("entry rows = %d; want 1", n)
	}
	if got := latest(t, s, "c1"); len(got.Labels) != 2 || string(got.Fields) == `{"sha":"abc"}` {
		t.Errorf("latest = %+v; want the enriched copy", got)
	}
}

func TestKeepEnrichedStillVersionsOtherDifferences(t *testing.T) {
	cases := map[string]func(*Entry){
		"summary":     func(e *Entry) { e.Summary = "changed" },
		"url":         func(e *Entry) { e.URL = "https://example.invalid/other" },
		"other label": func(e *Entry) { e.Labels = append(e.Labels, "extra") },
		"other field": func(e *Entry) { e.Fields = json.RawMessage(`{"sha":"def"}`) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s := openTemp(t)
			mustAppend(t, s, enriched("c1"), utc(2026, 3, 1, 13, 0, 0))
			e := unenriched("c1")
			mutate(&e)
			r, err := s.AppendKeepEnriched(ctx, e, utc(2026, 3, 1, 14, 0, 0))
			if err != nil || r != Appended {
				t.Fatalf("AppendKeepEnriched = %v, %v; want Appended", r, err)
			}
			if n := rowCount(t, s, "entry"); n != 2 {
				t.Errorf("entry rows = %d; want 2", n)
			}
		})
	}
}

func TestKeepEnrichedFirstSightingStoresUnenriched(t *testing.T) {
	s := openTemp(t)
	r, err := s.AppendKeepEnriched(ctx, unenriched("c1"), utc(2026, 3, 1, 14, 0, 0))
	if err != nil || r != Appended {
		t.Fatalf("AppendKeepEnriched = %v, %v; want Appended", r, err)
	}
	if got := latest(t, s, "c1"); len(got.Labels) != 1 {
		t.Errorf("labels = %v; want the unenriched item as returned", got.Labels)
	}
}

func TestPlainAppendOverwritesEnrichedWithUnenriched(t *testing.T) {
	s := openTemp(t)
	mustAppend(t, s, enriched("c1"), utc(2026, 3, 1, 13, 0, 0))
	if r := mustAppend(t, s, unenriched("c1"), utc(2026, 3, 1, 14, 0, 0)); r != Appended {
		t.Fatalf("Append = %v; want Appended", r)
	}
	if got := latest(t, s, "c1"); len(got.Labels) != 1 {
		t.Errorf("labels = %v; want the unenriched version latest", got.Labels)
	}
}
