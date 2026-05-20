package transcript

import (
	"bufio"
	"encoding/json"
	"os"
)

// IsAwaitingInput returns true if the last assistant turn in the transcript
// contains an AskUserQuestion tool_use with no matching tool_result yet.
func IsAwaitingInput(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()

	var events []Event
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for scanner.Scan() {
		var ev Event
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			continue
		}
		events = append(events, ev)
	}
	if err := scanner.Err(); err != nil {
		return false, err
	}

	// Walk events: reset pending set on each assistant turn, resolve on tool_result.
	pending := make(map[string]bool)
	for _, ev := range events {
		switch ev.Type {
		case "assistant":
			pending = make(map[string]bool)
			for _, b := range ev.Message.Content {
				if b.Type == "tool_use" && b.Name == "AskUserQuestion" && b.ID != "" {
					pending[b.ID] = true
				}
			}
		case "user":
			for _, b := range ev.Message.Content {
				if b.Type == "tool_result" && b.ToolUseID != "" {
					delete(pending, b.ToolUseID)
				}
			}
		}
	}
	return len(pending) > 0, nil
}
