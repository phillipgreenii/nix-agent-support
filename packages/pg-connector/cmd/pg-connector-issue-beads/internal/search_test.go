package internal

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

func searchBead(id, title, status string, priority int, updatedAt string, labels ...string) string {
	lj, _ := json.Marshal(labels)
	tj, _ := json.Marshal(title)
	return `{"id":"` + id + `","title":` + string(tj) + `,"status":"` + status + `","priority":` +
		string(rune('0'+priority)) + `,"issue_type":"bug","assignee":"alice","owner":"bob","updated_at":"` + updatedAt +
		`","labels":` + string(lj) + `}`
}

func TestSearch_EmptyQueryIsInvalidArgumentBeforeAnyBdCall(t *testing.T) {
	for _, q := range []string{"", "   ", "\t\n"} {
		fr := listFake()
		_, err := New(fr).Search(context.Background(), q, nil)
		if !errors.Is(err, scriptout.ErrInvalidArgument) {
			t.Fatalf("query %q: err = %v, want ErrInvalidArgument", q, err)
		}
		if len(fr.calls) != 0 {
			t.Errorf("query %q: bd calls = %v, want none", q, fr.calls)
		}
	}
}

func TestSearch_BdInvocationIsReadOnlyAndQueryIsBoundToItsFlag(t *testing.T) {
	fr := listFake()
	// A query that looks like a flag must reach bd as the VALUE of --query,
	// never as a flag of its own.
	if _, err := New(fr).Search(context.Background(), "  --help  ", nil); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(fr.calls) != 1 {
		t.Fatalf("bd calls = %v, want exactly one", fr.calls)
	}
	args := fr.calls[0]
	if args[0] != "search" {
		t.Errorf("args = %v, want a bd search call", args)
	}
	for _, want := range []string{"--readonly", "--json", "--query=--help"} {
		if !containsArg(args, want) {
			t.Errorf("args = %v, missing %q", args, want)
		}
	}
	if containsArg(args, "--help") {
		t.Errorf("args = %v, the query leaked as a bare flag", args)
	}
	if containsArg(args, "--status") || containsArg(args, "-s") {
		t.Errorf("args = %v, search must not narrow by status (closed beads stay findable)", args)
	}
}

func TestSearch_CoreShapeAndNoFieldsMeansNoAttributes(t *testing.T) {
	fr := listFake(
		searchBead("tp-1", "Fix login", "open", 1, "2026-10-01T00:00:00Z", "agent-support"),
		searchBead("tp-2", "Login docs", "closed", 3, "2026-09-01T00:00:00Z"),
	)
	got, err := New(fr).Search(context.Background(), "login", nil)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("results = %+v, want 2 (in bd's returned order)", got)
	}
	r := got[0]
	if r.Type != "issue" || r.ID != "tp-1" || r.Title != "Fix login" || r.Source != "pg-connector-issue-beads" {
		t.Errorf("core = %+v", r)
	}
	if r.URL != "" {
		t.Errorf("url = %q, want empty (bd has no hosted page)", r.URL)
	}
	if r.Attributes != nil {
		t.Errorf("attributes = %v, want none when no fields were requested", r.Attributes)
	}
	if got[1].ID != "tp-2" {
		t.Errorf("order = %s,%s, want bd's own order kept", got[0].ID, got[1].ID)
	}
}

func TestSearch_RequestedFieldsPopulateDeclaredAttributes(t *testing.T) {
	fr := &fakeRunner{workspace: "/ws/tracker-a", handle: func([]string) (string, error) {
		return bdListJSON(searchBead("tp-1", "Fix login", "in_progress", 1, "2026-10-01T00:00:00Z", "a", "b")), nil
	}}
	got, err := New(fr).Search(context.Background(), "login", []string{"status", "priority", "labels", "issue_type", "assignee", "owner", "tracker", "no_such_field", "title"})
	if err != nil || len(got) != 1 {
		t.Fatalf("Search = %+v, %v", got, err)
	}
	want := map[string]any{
		"status":     "in_progress",
		"priority":   "P1",
		"labels":     []string{"a", "b"},
		"issue_type": "bug",
		"assignee":   "alice",
		"owner":      "bob",
		"tracker":    "/ws/tracker-a",
	}
	if !reflect.DeepEqual(got[0].Attributes, want) {
		t.Errorf("attributes = %#v, want %#v (unknown and core fields silently ignored)", got[0].Attributes, want)
	}
}

func TestSearch_OnlyAskedForFieldsAppear(t *testing.T) {
	fr := listFake(searchBead("tp-1", "Fix login", "open", 2, "2026-10-01T00:00:00Z"))
	got, err := New(fr).Search(context.Background(), "login", []string{"status"})
	if err != nil || len(got) != 1 {
		t.Fatalf("Search = %+v, %v", got, err)
	}
	if want := (map[string]any{"status": "open"}); !reflect.DeepEqual(got[0].Attributes, want) {
		t.Errorf("attributes = %#v, want %#v", got[0].Attributes, want)
	}
}

func TestSearch_AttributeVocabularyMatchesWhatIsPopulated(t *testing.T) {
	fr := listFake(searchBead("tp-1", "Fix login", "open", 2, "2026-10-01T00:00:00Z", "x"))
	got, err := New(fr).Search(context.Background(), "login", SearchAttributes)
	if err != nil || len(got) != 1 {
		t.Fatalf("Search = %+v, %v", got, err)
	}
	for _, name := range SearchAttributes {
		if _, ok := got[0].Attributes[name]; !ok {
			t.Errorf("declared attribute %q was not populated when requested", name)
		}
	}
	if len(got[0].Attributes) != len(SearchAttributes) {
		t.Errorf("attributes = %v, want exactly the declared set %v", got[0].Attributes, SearchAttributes)
	}
}

func TestSearch_NoHitsIsNonNilEmptyList(t *testing.T) {
	got, err := New(listFake()).Search(context.Background(), "zzz", nil)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("results = %#v, want a non-nil empty list", got)
	}
}

func TestSearch_ClassifiesBdErrors(t *testing.T) {
	cases := []struct {
		name string
		out  string
		err  error
		want error
	}{
		{"no issues found envelope", `{"data":{"error":"no issues found matching x"},"schema_version":1}`, errors.New("exit 1"), scriptout.ErrNotFound},
		{"bd missing", "", errors.New(`exec: "bd": executable file not found in $PATH`), scriptout.ErrUnavailable},
		{"garbled output", "not json", nil, scriptout.ErrUnavailable},
		{"workspace not configured", "", ErrWorkspaceNotConfigured, scriptout.ErrUnavailable},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fr := &fakeRunner{handle: func([]string) (string, error) { return c.out, c.err }}
			_, err := New(fr).Search(context.Background(), "q", nil)
			if !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
		})
	}
}

func searchRangeCtx(since, before string) context.Context {
	cfg := map[string]string{}
	if since != "" {
		cfg["search_since"] = since
	}
	if before != "" {
		cfg["search_before"] = before
	}
	raw, _ := json.Marshal(cfg)
	return scriptout.WithConfig(context.Background(), raw)
}

func TestSearch_TimeBoundFiltersOnUpdatedAt(t *testing.T) {
	fr := listFake(
		searchBead("tp-old", "t", "open", 2, "2026-08-31T23:59:59Z"),
		searchBead("tp-at-since", "t", "open", 2, "2026-09-01T00:00:00Z"),
		searchBead("tp-mid", "t", "open", 2, "2026-09-15T00:00:00Z"),
		searchBead("tp-at-before", "t", "open", 2, "2026-10-01T00:00:00Z"),
		searchBead("tp-undated", "t", "open", 2, ""),
	)
	got, err := New(fr).Search(searchRangeCtx("2026-09-01T00:00:00Z", "2026-10-01T00:00:00Z"), "t", nil)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	var ids []string
	for _, r := range got {
		ids = append(ids, r.ID)
	}
	// Since inclusive, before exclusive; an undatable bead cannot be placed in
	// a bounded window, so it is dropped rather than guessed.
	if want := "tp-at-since,tp-mid"; strings.Join(ids, ",") != want {
		t.Errorf("ids = %v, want %s", ids, want)
	}
}

func TestSearch_NoBoundKeepsUndatedBeads(t *testing.T) {
	fr := listFake(searchBead("tp-undated", "t", "open", 2, ""))
	got, err := New(fr).Search(context.Background(), "t", nil)
	if err != nil || len(got) != 1 {
		t.Fatalf("Search = %+v, %v; an unbounded search keeps every hit", got, err)
	}
}

func TestSearch_MalformedBoundIsInvalidArgument(t *testing.T) {
	fr := listFake()
	_, err := New(fr).Search(searchRangeCtx("yesterday", ""), "t", nil)
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("err = %v, want ErrInvalidArgument", err)
	}
	if len(fr.calls) != 0 {
		t.Errorf("bd calls = %v, want none", fr.calls)
	}
}

func TestSearch_BoundedWindowHelperUsesUTC(t *testing.T) {
	// updated_at carrying a non-UTC offset is compared as an instant.
	fr := listFake(searchBead("tp-1", "t", "open", 2, "2026-09-30T20:00:00-05:00")) // 2026-10-01T01:00Z
	since := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)
	before := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)
	got, err := New(fr).Search(searchRangeCtx(since, before), "t", nil)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("results = %+v, want none (the instant is after the window)", got)
	}
}
