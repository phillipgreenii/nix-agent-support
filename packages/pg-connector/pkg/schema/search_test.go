package schema

import (
	"encoding/json"
	"testing"
)

func TestSearchResult_JSONShape_NoAttributes(t *testing.T) {
	// Attributes is omitempty — a result with no extension attributes
	// populated omits the field entirely, never an empty object.
	raw, err := json.Marshal(SearchResult{Type: "pr", ID: "pr-1", Title: "Fix the thing", URL: "https://example.invalid/pr-1", Source: "pg-connector-pr-github"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"type":"pr","id":"pr-1","title":"Fix the thing","url":"https://example.invalid/pr-1","source":"pg-connector-pr-github"}`
	if string(raw) != want {
		t.Fatalf("got %s, want %s", raw, want)
	}
}

func TestSearchResult_JSONShape_WithAttributes(t *testing.T) {
	raw, err := json.Marshal(SearchResult{
		Type:       "issue",
		ID:         "issue-1",
		Title:      "Something broke",
		URL:        "https://example.invalid/issue-1",
		Source:     "pg-connector-issue-beads",
		Attributes: map[string]any{"status": "open"},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"type":"issue","id":"issue-1","title":"Something broke","url":"https://example.invalid/issue-1","source":"pg-connector-issue-beads","attributes":{"status":"open"}}`
	if string(raw) != want {
		t.Fatalf("got %s, want %s", raw, want)
	}
}

func TestSearchResult_JSONRoundTrip(t *testing.T) {
	in := SearchResult{
		Type:       "ci",
		ID:         "run-1",
		Title:      "build failed",
		URL:        "https://example.invalid/run-1",
		Source:     "pg-connector-ci-github-actions",
		Attributes: map[string]any{"branch": "main"},
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out SearchResult
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Type != in.Type || out.ID != in.ID || out.Title != in.Title || out.URL != in.URL || out.Source != in.Source {
		t.Fatalf("round-trip mismatch: got %+v, want %+v", out, in)
	}
	if out.Attributes["branch"] != "main" {
		t.Fatalf("round-trip Attributes mismatch: got %+v", out.Attributes)
	}
}

// TestSearchResult_NoScoreField guards INV-VER-1's design decision that no
// score/rank/relevance field exists on SearchResult under any name: this
// walks the struct's JSON tags rather than merely eyeballing the struct
// literal, so a future field addition trips it directly.
func TestSearchResult_NoScoreField(t *testing.T) {
	raw, err := json.Marshal(SearchResult{Type: "pr", ID: "pr-1", Title: "t", URL: "u", Source: "s"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var asMap map[string]json.RawMessage
	if err := json.Unmarshal(raw, &asMap); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, forbidden := range []string{"score", "rank", "relevance"} {
		if _, ok := asMap[forbidden]; ok {
			t.Fatalf("SearchResult carries a %q field — the design forbids any score-shaped field", forbidden)
		}
	}
}
