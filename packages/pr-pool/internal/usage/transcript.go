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
		u := ev.Message.Usage
		s.InputTokens += u.InputTokens
		s.CacheCreationTokens += u.CacheCreationInputTokens
		s.CacheReadTokens += u.CacheReadInputTokens
		s.OutputTokens += u.OutputTokens
		if ev.Message.Model != "" {
			s.Model = ev.Message.Model
		}
	}
	if err := sc.Err(); err != nil {
		return Snapshot{}, err
	}
	return s, nil
}
