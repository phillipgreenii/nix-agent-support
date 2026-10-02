package transcript

import (
	"bufio"
	"encoding/json"
	"os"

	ct "github.com/phillipgreenii/claude-transcript"
)

type ContextSnapshot struct {
	Model         string
	ContextTokens int
	TotalTokens   int // cumulative output_tokens, once per distinct assistant message id
}

// LatestContext returns the Model, ContextTokens, and TotalTokens from the
// transcript at path. ContextTokens is the input context size from the last
// assistant event with a non-zero usage payload. TotalTokens is the sum of
// output_tokens across all qualifying assistant events, counting each distinct
// assistant message id once (a multi-block turn is written as one line per
// block, each repeating the same usage; see ct.OutputTally).
func LatestContext(path string) (ContextSnapshot, error) {
	f, err := os.Open(path)
	if err != nil {
		return ContextSnapshot{}, err
	}
	defer func() { _ = f.Close() }()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	var last ContextSnapshot
	var totalOut ct.OutputTally
	for scanner.Scan() {
		var ev Event
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			continue
		}
		if ev.Type != "assistant" {
			continue
		}
		u := ev.Message.Usage
		contextTotal := u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens
		if contextTotal == 0 {
			continue
		}
		totalOut.Add(ev.Message.ID, u.OutputTokens)
		last = ContextSnapshot{Model: ev.Message.Model, ContextTokens: contextTotal}
	}
	last.TotalTokens = int(totalOut.Total())
	return last, scanner.Err()
}
