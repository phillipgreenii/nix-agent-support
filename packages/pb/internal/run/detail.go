package run

import (
	"encoding/json"
	"strings"
)

// maxDetail caps the stdout text echoed into an error message.
const maxDetail = 1000

// Detail returns the human-readable reason a failed command gave, never empty.
// Tools that take --json (bd) report their errors on STDOUT with an empty
// stderr, so an stderr-only message ends in a bare colon (pg2-cjakt). Detail
// joins the trimmed stderr with stdout's error text: the "error"/"message"
// field of a JSON object when stdout is one, else the trimmed stdout text.
// With no output at all it says so, naming the tool.
func Detail(name string, res Result) string {
	var parts []string
	if s := strings.TrimSpace(res.Stderr); s != "" {
		parts = append(parts, s)
	}
	if s := stdoutDetail(res.Stdout); s != "" {
		parts = append(parts, s)
	}
	if len(parts) == 0 {
		return "no output from " + name
	}
	return strings.Join(parts, "; ")
}

func stdoutDetail(stdout string) string {
	s := strings.TrimSpace(stdout)
	if s == "" {
		return ""
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal([]byte(s), &obj) == nil {
		for _, k := range []string{"error", "message"} {
			if m := jsonMessage(obj[k]); m != "" {
				return m
			}
		}
	}
	if len(s) > maxDetail {
		return s[:maxDetail] + "... (truncated)"
	}
	return s
}

// jsonMessage reads a JSON string, or the "message"/"error" string inside a
// JSON object; anything else (absent, null, other shapes) yields "".
func jsonMessage(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var str string
	if json.Unmarshal(raw, &str) == nil {
		return strings.TrimSpace(str)
	}
	var obj struct {
		Message string `json:"message"`
		Error   string `json:"error"`
	}
	if json.Unmarshal(raw, &obj) == nil {
		if obj.Message != "" {
			return strings.TrimSpace(obj.Message)
		}
		return strings.TrimSpace(obj.Error)
	}
	return ""
}
