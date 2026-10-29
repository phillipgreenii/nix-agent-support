// Package hook turns Claude Code hook JSON (read from stdin by hook mode) into
// tracker events. It never produces output a hook reader treats as a decision.
package hook

import (
	"bytes"
	"encoding/json"
	"io"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/claude-tool-tracker/internal/events"
)

// MaxInputBytes bounds how much hook JSON hook mode reads.
const MaxInputBytes = 8 << 20

// Source is the producer name of every hook-mode event.
const Source = "tracker"

type input struct {
	HookEventName         string          `json:"hook_event_name"`
	SessionID             string          `json:"session_id"`
	CWD                   string          `json:"cwd"`
	PermissionMode        string          `json:"permission_mode"`
	AgentID               string          `json:"agent_id"`
	AgentType             string          `json:"agent_type"`
	ToolName              string          `json:"tool_name"`
	ToolUseID             string          `json:"tool_use_id"`
	ToolInput             json.RawMessage `json:"tool_input"`
	ToolResponse          json.RawMessage `json:"tool_response"`
	DurationMS            *float64        `json:"duration_ms"`
	Error                 string          `json:"error"`
	IsInterrupt           *bool           `json:"is_interrupt"`
	Reason                string          `json:"reason"`
	PermissionSuggestions json.RawMessage `json:"permission_suggestions"`
}

// Parse reads hook JSON and returns the event to write, or ok=false when the
// input is empty, malformed, unusable, or of an event this tracker ignores.
func Parse(r io.Reader, now time.Time) (env *events.Envelope, ok bool) {
	raw, err := io.ReadAll(io.LimitReader(r, MaxInputBytes))
	if err != nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil, false
	}
	var in input
	if err := json.Unmarshal(raw, &in); err != nil || in.SessionID == "" {
		return nil, false
	}
	data := map[string]any{}
	set := func(k, v string) {
		if v != "" {
			data[k] = v
		}
	}
	var kind string
	switch in.HookEventName {
	case "PreToolUse":
		kind = "tracker.pre_tool"
		set("permission_mode", in.PermissionMode)
		set("agent_type", in.AgentType)
		set("tool_name", in.ToolName)
		set("cwd", in.CWD)
	case "PostToolUse", "PostToolUseFailure":
		kind = "tracker.post_tool"
		data["hook_event"] = in.HookEventName
		set("tool_name", in.ToolName)
		set("permission_mode", in.PermissionMode)
		set("agent_type", in.AgentType)
		if in.DurationMS != nil {
			data["duration_ms"] = *in.DurationMS
		}
		if in.HookEventName == "PostToolUseFailure" {
			data["is_error"] = true
			set("error", in.Error)
			if in.IsInterrupt != nil {
				data["is_interrupt"] = *in.IsInterrupt
			}
		} else {
			summarizeResponse(data, in.ToolResponse)
		}
	case "PermissionRequest":
		kind = "tracker.permission_offer"
		set("tool_name", in.ToolName)
		set("permission_mode", in.PermissionMode)
		set("agent_type", in.AgentType)
		if len(in.ToolInput) > 0 {
			data["tool_input"] = in.ToolInput
		}
		if len(in.PermissionSuggestions) > 0 {
			data["suggestions"] = in.PermissionSuggestions
		}
	case "PermissionDenied":
		kind = "tracker.denied"
		set("tool_name", in.ToolName)
		set("permission_mode", in.PermissionMode)
		set("agent_type", in.AgentType)
		set("reason", in.Reason)
	default:
		return nil, false
	}
	// json.RawMessage values must survive Shorten's generic walk: decode them.
	for k, v := range data {
		if rm, isRaw := v.(json.RawMessage); isRaw {
			var x any
			if json.Unmarshal(rm, &x) != nil {
				delete(data, k)
				continue
			}
			data[k] = x
		}
	}
	return &events.Envelope{
		Schema:    events.Schema,
		EventID:   events.NewEventID(now),
		TS:        now.UnixMilli(),
		Source:    Source,
		Kind:      kind,
		SessionID: in.SessionID,
		ToolUseID: in.ToolUseID,
		AgentID:   in.AgentID,
		Data:      data,
	}, true
}

// summarizeResponse records is_error, exit_code and response_bytes of a
// tool_response; never the payload itself.
func summarizeResponse(data map[string]any, resp json.RawMessage) {
	if len(resp) == 0 {
		return
	}
	data["response_bytes"] = len(resp)
	var obj map[string]any
	if json.Unmarshal(resp, &obj) != nil {
		return
	}
	if b, ok := obj["is_error"].(bool); ok {
		data["is_error"] = b
	}
	for _, k := range []string{"exit_code", "exitCode"} {
		if n, ok := obj[k].(float64); ok {
			data["exit_code"] = n
			break
		}
	}
}

// Run is hook mode: parse stdin and append the event. Every failure is
// swallowed; it prints nothing and the caller exits 0.
func Run(stdin io.Reader, now time.Time) {
	env, ok := Parse(stdin, now)
	if !ok {
		return
	}
	_, _ = events.Append(env)
}
