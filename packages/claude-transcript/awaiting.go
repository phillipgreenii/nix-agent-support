package claudetranscript

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
	defer func() { _ = f.Close() }()

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

	// Walk events, folding consecutive assistant events that share a message id
	// into a single logical turn. A turn is written as one JSONL line per content
	// block, all sharing the same Message.ID (see events.go), so we must reset the
	// pending-question set only when a NEW turn begins — not on every assistant
	// event. Resetting per event would let a later line of the same turn (e.g. a
	// trailing text block) wipe a dangling AskUserQuestion from an earlier line,
	// falsely reporting "not awaiting". Resolve questions on user tool_result.
	pending := make(map[string]bool)
	inTurn := false
	turnID := ""
	for _, ev := range events {
		switch ev.Type {
		case "assistant":
			if !inTurn || ev.Message.ID != turnID {
				// New assistant turn: start a fresh pending set.
				pending = make(map[string]bool)
				inTurn = true
				turnID = ev.Message.ID
			}
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
			// A user message ends the assistant's turn; the next assistant event
			// begins a new turn.
			inTurn = false
		}
	}
	return len(pending) > 0, nil
}
