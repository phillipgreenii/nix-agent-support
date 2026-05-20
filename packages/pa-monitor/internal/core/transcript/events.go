package transcript

import "time"

type Event struct {
	Type      string    `json:"type"`
	UUID      string    `json:"uuid"`
	Timestamp time.Time `json:"timestamp"`
	Message   Message   `json:"message"`
}

type Message struct {
	Model   string      `json:"model"`
	Role    string      `json:"role"`
	Content ContentList `json:"content"`
	Usage   Usage       `json:"usage"`
}

type Usage struct {
	InputTokens              int `json:"input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	OutputTokens             int `json:"output_tokens"`
}

// ContentList is either a plain string or a JSON array of blocks.
// We normalize to []Block. A string body becomes a single Block{Type:"text", Text: s}.
type ContentList []Block

type Block struct {
	Type      string `json:"type"`
	Text      string `json:"text,omitempty"`
	ID        string `json:"id,omitempty"`          // for tool_use
	Name      string `json:"name,omitempty"`        // for tool_use
	ToolUseID string `json:"tool_use_id,omitempty"` // for tool_result
}

// UnmarshalJSON handles both string and array content.
func (c *ContentList) UnmarshalJSON(data []byte) error {
	if len(data) > 0 && data[0] == '"' {
		// string — treat as single text block
		var s string
		if err := jsonUnmarshalString(data, &s); err != nil {
			return err
		}
		*c = []Block{{Type: "text", Text: s}}
		return nil
	}
	var arr []Block
	if err := jsonUnmarshalArray(data, &arr); err != nil {
		return err
	}
	*c = arr
	return nil
}
