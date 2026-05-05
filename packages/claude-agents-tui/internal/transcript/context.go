package transcript

import (
	"bufio"
	"encoding/json"
	"os"
)

type ContextSnapshot struct {
	Model         string
	ContextTokens int
	TotalTokens   int // cumulative output_tokens across all assistant events
}

// LatestContext returns the Model, ContextTokens, and TotalTokens from the
// transcript at path. ContextTokens is the input context size from the last
// assistant event with a non-zero usage payload. TotalTokens is the sum of
// output_tokens across all qualifying assistant events.
func LatestContext(path string) (ContextSnapshot, error) {
	f, err := os.Open(path)
	if err != nil {
		return ContextSnapshot{}, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	var last ContextSnapshot
	var totalOut int
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
		totalOut += u.OutputTokens
		last = ContextSnapshot{Model: ev.Message.Model, ContextTokens: contextTotal}
	}
	last.TotalTokens = totalOut
	return last, scanner.Err()
}
