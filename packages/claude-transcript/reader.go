package claudetranscript

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
)

// LastAssistantText returns the concatenated text blocks of the LAST assistant
// event in the transcript. Empty string if the last assistant turn has no text
// (e.g. it ended in a tool_use) or there is no assistant event. This is what
// ccpool's `send` returns as the reply (spec §8.3 step 5/6).
func LastAssistantText(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	var last string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for scanner.Scan() {
		var ev Event
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			continue // tolerate non-event lines
		}
		if ev.Type != "assistant" {
			continue
		}
		var b strings.Builder
		for _, blk := range ev.Message.Content {
			if blk.Type == "text" {
				b.WriteString(blk.Text)
			}
		}
		last = b.String()
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return last, nil
}
