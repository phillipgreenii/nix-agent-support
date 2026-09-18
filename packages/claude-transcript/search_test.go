package claudetranscript

import (
	"testing"
	"time"
)

func TestSearch_MatchesCaseInsensitiveSubstring(t *testing.T) {
	matches, err := Search("testdata/search-basic.jsonl", "flaky", time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(matches) != 2 {
		t.Fatalf("got %d matches, want 2: %+v", len(matches), matches)
	}
	if matches[0].Role != "user" || matches[0].Line != 1 {
		t.Errorf("match[0] = %+v, want role=user line=1", matches[0])
	}
	if matches[1].Role != "assistant" || matches[1].Line != 2 {
		t.Errorf("match[1] = %+v, want role=assistant line=2", matches[1])
	}
}

func TestSearch_NoMatches(t *testing.T) {
	matches, err := Search("testdata/search-basic.jsonl", "nonexistent-token", time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("got %d matches, want 0", len(matches))
	}
}

func TestSearch_MissingFile(t *testing.T) {
	if _, err := Search("testdata/does-not-exist.jsonl", "x", time.Time{}, time.Time{}); err == nil {
		t.Fatal("expected an error for a missing file, got nil")
	}
}

func TestSearch_SinceExcludesEarlierMatches(t *testing.T) {
	// "flaky" matches lines 1 (10:00) and 2 (10:05). since=10:03 must keep
	// only line 2.
	since := time.Date(2026, 9, 18, 10, 3, 0, 0, time.UTC)
	matches, err := Search("testdata/search-basic.jsonl", "flaky", since, time.Time{})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(matches) != 1 || matches[0].Line != 2 {
		t.Fatalf("got %+v, want exactly line 2", matches)
	}
}

func TestSearch_BeforeExcludesLaterMatches(t *testing.T) {
	before := time.Date(2026, 9, 18, 10, 3, 0, 0, time.UTC)
	matches, err := Search("testdata/search-basic.jsonl", "flaky", time.Time{}, before)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(matches) != 1 || matches[0].Line != 1 {
		t.Fatalf("got %+v, want exactly line 1", matches)
	}
}

func TestSearch_SinceAndBeforeNarrowToOneWindow(t *testing.T) {
	since := time.Date(2026, 9, 18, 10, 1, 0, 0, time.UTC)
	before := time.Date(2026, 9, 18, 10, 30, 0, 0, time.UTC)
	matches, err := Search("testdata/search-basic.jsonl", "flaky", since, before)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(matches) != 1 || matches[0].Line != 2 {
		t.Fatalf("got %+v, want exactly line 2", matches)
	}
}
