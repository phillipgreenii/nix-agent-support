package ingest

import (
	"bytes"
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// line is one JSONL record. Every field the schema carries is decoded here and
// nowhere else.
//
// Optional scalars are pointers or json.RawMessage so ABSENT is distinguishable
// from PRESENT-AND-ZERO. That is not tidiness: T-9 exists because
// `"is_error":false` occurs thousands of times in the corpus, so `is_error` read
// as a plain bool with a zero value would make "not an error" and "no result
// recorded" the same row, and every error count derived from it would be wrong
// in a direction nothing else would reveal.
type line struct {
	Type                    string          `json:"type"`
	UUID                    *string         `json:"uuid"`
	ParentUUID              *string         `json:"parentUuid"`
	SessionID               *string         `json:"sessionId"`
	Timestamp               *string         `json:"timestamp"`
	IsSidechain             *bool           `json:"isSidechain"`
	Cwd                     *string         `json:"cwd"`
	GitBranch               *string         `json:"gitBranch"`
	PermissionMode          *string         `json:"permissionMode"`
	DurationMs              *int64          `json:"durationMs"`
	HookCount               *int64          `json:"hookCount"`
	HookErrors              json.RawMessage `json:"hookErrors"`
	PromptSource            *string         `json:"promptSource"`
	UserType                *string         `json:"userType"`
	SourceToolAssistantUUID *string         `json:"sourceToolAssistantUUID"`
	PromptID                *string         `json:"promptId"`
	Entrypoint              *string         `json:"entrypoint"`
	Message                 *struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// block is one element of a message's content array.
type block struct {
	Type string `json:"type"`
	Text string `json:"text"`
	// Thinking blocks name their payload `thinking`, not `text`.
	Thinking string `json:"thinking"`
	// tool_use
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
	// tool_result
	ToolUseID string `json:"tool_use_id"`
	// IsError is RAW, never *bool: a body that types is_error as something
	// unexpected must not fail the whole block array (and with it every record
	// on the line). Presence-and-true is decided in isError below.
	IsError json.RawMessage `json:"is_error"`
	Content json.RawMessage `json:"content"`
}

// isError implements T-9 exactly: 1 only when the key is PRESENT and its value
// is literally true. Absent is not an error; present-and-false is not an error;
// and the two remain distinguishable upstream because content_len is populated
// either way.
func (b block) isError() bool {
	if len(b.IsError) == 0 {
		return false
	}
	return bytes.Equal(bytes.TrimSpace(b.IsError), []byte("true"))
}

// blocks decodes a message content payload into blocks. Content is either a
// plain string (a prose message, no blocks) or an array of blocks.
func blocks(raw json.RawMessage) []block {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return nil
	}
	var out []block
	if err := json.Unmarshal(trimmed, &out); err != nil {
		return nil
	}
	return out
}

// flattenContent renders a tool_result body as text, mirroring the differential
// baseline's `txt` helper so lengths and bodies compare like with like:
// a string is itself; an array contributes its text blocks joined by a single
// space; anything else is its JSON encoding.
func flattenContent(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return ""
	}
	var s string
	if err := json.Unmarshal(trimmed, &s); err == nil {
		return s
	}
	var arr []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(trimmed, &arr); err == nil {
		parts := make([]string, 0, len(arr))
		for _, b := range arr {
			if b.Type == "text" {
				parts = append(parts, b.Text)
			}
		}
		return strings.Join(parts, " ")
	}
	return string(trimmed)
}

// contentLen is the recorded length of a tool-result body. It counts RUNES, not
// bytes, so the number means the same thing the shell prototype's `length`
// meant (jq strings are counted in codepoints) and the sizing measurements stay
// comparable.
func contentLen(s string) int64 {
	return int64(utf8.RuneCountInString(s))
}

// assistantText joins every text block on one assistant line. One line yields
// at most one assistant_text row, which is what makes the error-then-narration
// join (`assistant_text` at seq+1) a plain adjacency lookup.
func assistantText(bs []block) string {
	var sb strings.Builder
	for _, b := range bs {
		if b.Type == "text" && b.Text != "" {
			sb.WriteString(b.Text)
		}
	}
	return sb.String()
}

func thinkingText(bs []block) string {
	var sb strings.Builder
	for _, b := range bs {
		if b.Type == "thinking" && b.Thinking != "" {
			sb.WriteString(b.Thinking)
		}
	}
	return sb.String()
}

// hookErrorsText stores hookErrors verbatim when the key is present and not
// JSON null, INCLUDING the empty array. `[]` is a real observation — the hooks
// ran and rejected nothing — and conflating it with "no hook data recorded"
// would make the hook-rejection totals unprovable. Queries filter `[]` out;
// ingest does not throw it away.
func hookErrorsText(raw json.RawMessage) *string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	s := string(trimmed)
	return &s
}

func boolToInt(b *bool) *int64 {
	if b == nil {
		return nil
	}
	var v int64
	if *b {
		v = 1
	}
	return &v
}
