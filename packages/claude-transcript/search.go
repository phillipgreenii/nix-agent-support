// search.go: a naive, unindexed per-call scan of one transcript's text
// content for a substring query, optionally bounded by time — the
// concrete backing for pg-connector's agentsession search.Provider
// implementation. Deliberately no index: an efficient/cross-history
// search is explicitly deferred (design decision, 2026-09-18). The time
// bound narrows the scan; it does not index it.
package claudetranscript

import (
	"encoding/json"
	"os"
	"strings"
	"time"
)

// Match is one line of one transcript that matched a Search query.
type Match struct {
	// Role is the matching event's message role ("user" or "assistant").
	Role string
	// Line is the 1-indexed line number within the transcript file.
	Line int
	// Snippet is the matching text block's full text (callers needing a
	// bounded-length preview truncate it themselves).
	Snippet string
	// Timestamp is the matching event's own Timestamp, as already parsed
	// from the transcript (Event.Timestamp) — not separately computed.
	Timestamp time.Time
}

// Search scans path (a transcript .jsonl file) for query as a
// case-insensitive substring match against every user/assistant text
// block whose own Timestamp falls within [since, before] (either bound
// may be the zero time.Time, meaning unbounded on that side), returning
// one Match per matching event. An empty result (nil, nil) is a
// well-formed "no matches," not an error.
func Search(path, query string, since, before time.Time) ([]Match, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	lower := strings.ToLower(query)
	var matches []Match
	scanner := newTranscriptScanner(f)
	line := 0
	for scanner.Scan() {
		line++
		var ev Event
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			continue // tolerate non-event lines, mirrors LastAssistantText
		}
		if ev.Type != "user" && ev.Type != "assistant" {
			continue
		}
		if !since.IsZero() && ev.Timestamp.Before(since) {
			continue
		}
		if !before.IsZero() && ev.Timestamp.After(before) {
			continue
		}
		var b strings.Builder
		for _, blk := range ev.Message.Content {
			if blk.Type == "text" {
				b.WriteString(blk.Text)
			}
		}
		text := b.String()
		if text == "" {
			continue
		}
		if strings.Contains(strings.ToLower(text), lower) {
			matches = append(matches, Match{Role: ev.Type, Line: line, Snippet: text, Timestamp: ev.Timestamp})
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return matches, nil
}
