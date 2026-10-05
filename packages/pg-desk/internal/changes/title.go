package changes

import (
	"encoding/json"
	"strings"
)

// threadTitleMaxRunes bounds a thread's derived title.
const threadTitleMaxRunes = 80

// TitleFromFacts returns the display title for an entity from its stored
// Facts JSON object: pr_show.title for pr, issue_show.title for issue, and
// for thread (which has no title field) the first non-empty line of
// thread_show.text, trimmed and truncated to 80 runes. It returns "" when
// the field is absent, the type is unknown, or factsJSON is malformed.
func TitleFromFacts(entityType, factsJSON string) string {
	var facts map[string]json.RawMessage
	if err := json.Unmarshal([]byte(factsJSON), &facts); err != nil {
		return ""
	}
	switch entityType {
	case "pr":
		return stringField(facts["pr_show"], "title")
	case "issue":
		return stringField(facts["issue_show"], "title")
	case "thread":
		return firstLine(stringField(facts["thread_show"], "text"))
	}
	return ""
}

func stringField(raw json.RawMessage, key string) string {
	if len(raw) == 0 {
		return ""
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return ""
	}
	var s string
	if err := json.Unmarshal(obj[key], &s); err != nil {
		return ""
	}
	return s
}

func firstLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if r := []rune(line); len(r) > threadTitleMaxRunes {
			return string(r[:threadTitleMaxRunes])
		}
		return line
	}
	return ""
}
