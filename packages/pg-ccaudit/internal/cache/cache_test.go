package cache

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAppendThenLoadRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.jsonl")
	e := Entry{
		ID: "typed-turn:a.jsonl#1", Classifier: "cli:claude", PromptVersion: 2,
		Class: "self-caught-mistake", Confidence: "high", What: "w", Prevention: "p",
		RunID: "run-1", ClassifiedAt: time.Now().UTC(),
	}
	if err := Append(path, []Entry{e}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1", len(got))
	}
	if got[e.Key()].Class != "self-caught-mistake" {
		t.Errorf("class=%q, want self-caught-mistake", got[e.Key()].Class)
	}
}

// TestAppendIsCumulativeAcrossCalls is the property the resilience story
// depends on: Append is called once per completed batch, never rewriting the
// file, so calling it N times must leave all N calls' entries readable.
func TestAppendIsCumulativeAcrossCalls(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.jsonl")
	for i := 0; i < 3; i++ {
		e := Entry{ID: "id-" + string(rune('a'+i)), Classifier: "cli:claude", PromptVersion: 2, Class: "not-a-mistake"}
		if err := Append(path, []Entry{e}); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d entries after 3 separate Append calls, want 3", len(got))
	}
}

// TestLoadKeepsTheLastEntryForARepeatedKey is what makes append-only updates
// correct: re-classifying (or re-caching) the same candidate under the same
// classifier and prompt version must not require rewriting the file — the
// newest line for that key simply wins on load.
func TestLoadKeepsTheLastEntryForARepeatedKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.jsonl")
	key := Entry{ID: "x", Classifier: "cli:claude", PromptVersion: 2}
	first := key
	first.Class = "not-a-mistake"
	second := key
	second.Class = "user-correction"
	if err := Append(path, []Entry{first}); err != nil {
		t.Fatal(err)
	}
	if err := Append(path, []Entry{second}); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got[key.Key()].Class != "user-correction" {
		t.Errorf("class=%q, want the LATER entry (user-correction) to win", got[key.Key()].Class)
	}
}

// TestKeyScopesByClassifierAndPromptVersion is the correctness guard the
// package doc promises: a verdict cached under one classifier or one prompt
// version must never answer a lookup for another.
func TestKeyScopesByClassifierAndPromptVersion(t *testing.T) {
	a := Entry{ID: "x", Classifier: "cli:claude", PromptVersion: 2}
	b := Entry{ID: "x", Classifier: "cli:claude", PromptVersion: 3}
	c := Entry{ID: "x", Classifier: "baseline", PromptVersion: 2}
	if a.Key() == b.Key() {
		t.Error("a prompt-version bump must produce a different cache key")
	}
	if a.Key() == c.Key() {
		t.Error("a different classifier must produce a different cache key")
	}
}

func TestLoadOfMissingFileIsNotExist(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "nope.jsonl"))
	if err == nil || !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Load of a missing cache = %v, want an errors.Is(err, os.ErrNotExist) error (callers check this like gold.Load's callers do)", err)
	}
}

// TestLoadToleratesATruncatedLastLine is the durability property that
// matters for a process killed mid-write: everything before the interrupted
// line must still load, and the interrupted line itself must not fail the
// whole load.
func TestLoadToleratesATruncatedLastLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.jsonl")
	good := Entry{ID: "good", Classifier: "cli:claude", PromptVersion: 2, Class: "not-a-mistake"}
	if err := Append(path, []Entry{good}); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"id":"truncated","classif`); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load must tolerate a truncated LAST line, got error: %v", err)
	}
	if len(got) != 1 || got[good.Key()].ID != "good" {
		t.Errorf("got %+v, want only the complete entry", got)
	}
}
