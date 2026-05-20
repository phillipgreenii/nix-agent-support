package transcript

import (
	"bufio"
	"encoding/json"
	"os"
)

// OpenSubagents returns the count of Task tool_use events that have no matching tool_result yet.
func OpenSubagents(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	open := make(map[string]bool)
	for scanner.Scan() {
		var ev Event
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			continue
		}
		for _, b := range ev.Message.Content {
			switch b.Type {
			case "tool_use":
				if b.Name == "Task" && b.ID != "" {
					open[b.ID] = true
				}
			case "tool_result":
				if b.ToolUseID != "" {
					delete(open, b.ToolUseID)
				}
			}
		}
	}
	return len(open), scanner.Err()
}
