package usage

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"

	ct "github.com/phillipgreenii/claude-transcript"
)

type transcriptReader struct{}

// NewTranscriptReader returns a Reader backed by Claude transcript JSONL files.
func NewTranscriptReader() Reader { return transcriptReader{} }

func (transcriptReader) Read(_ context.Context, path string) (Snapshot, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Snapshot{}, nil // worker hasn't produced a transcript yet
		}
		return Snapshot{}, err
	}
	defer func() { _ = f.Close() }()

	var s Snapshot
	seen := make(map[string]bool) // assistant message ids already counted (pg2-u2sv)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<24) // transcript lines are huge
	for sc.Scan() {
		var ev ct.Event
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			continue // tolerate a malformed line
		}
		if ev.Type != "assistant" { // decision 2: assistant turns only
			continue
		}
		// One assistant turn is written as several JSONL lines (one per content
		// block) that all carry the SAME message id and REPEAT the same
		// cumulative usage. Count each non-empty id once, else we over-count the
		// turn N-fold and trip budgets early. Lines with no id (older
		// transcripts) are always counted, preserving per-turn summing. (pg2-u2sv)
		if id := ev.Message.ID; id != "" {
			if seen[id] {
				continue
			}
			seen[id] = true
		}
		u := ev.Message.Usage
		s.InputTokens += u.InputTokens
		s.CacheCreationTokens += u.CacheCreationInputTokens
		s.CacheReadTokens += u.CacheReadInputTokens
		s.OutputTokens += u.OutputTokens
		s.CacheCreationEphemeral1hTokens += u.CacheCreation.Ephemeral1hInputTokens
		s.CacheCreationEphemeral5mTokens += u.CacheCreation.Ephemeral5mInputTokens
		if ev.Message.Model != "" {
			s.Model = ev.Message.Model
		}
	}
	if err := sc.Err(); err != nil {
		return Snapshot{}, err
	}
	return s, nil
}
