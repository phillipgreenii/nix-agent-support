// Package claudetranscript is the claude-transcript source: session
// transcripts (projects/{project}/{session}.jsonl) and subagent transcripts
// (projects/{project}/{session}/subagents/agent-{agent}.jsonl). It yields one
// record per line and one child record per message.content item.
//
// Only direct reads of a field are recorded (ADR 0015): no light readings, no
// de-duplication, no interpretation.
package claudetranscript

import (
	"bytes"
	"encoding/json"
	"reflect"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/source"
)

// Name is the source name.
const Name = "claude-transcript"

// ParserVersion is stamped on every record; bump it to re-parse stored copies.
// Version 2 added the saved_to field of a tool_result.
const ParserVersion = 2

// savedToRe finds the path a tool_result preview says the full output was saved
// to: the part from "projects/" on, a suffix of the saved-to path and the
// rel_path of the tool-results body it names.
var savedToRe = regexp.MustCompile(`\S*/(projects/[^\s"']+/tool-results/[^\s"']+)`)

// SavedTo returns the tool-results path a tool_result's text names, or "".
func SavedTo(text string) string {
	if m := savedToRe.FindStringSubmatch(text); m != nil {
		return m[1]
	}
	return ""
}

// Line is one transcript line (the keys Claude Code writes that the recorder
// knows; an unlisted key is reported as an unknown key).
type Line struct {
	Type        string   `json:"type"        ingest:"keyword"`
	UUID        string   `json:"uuid"        ingest:"id"`
	ParentUUID  string   `json:"parentUuid"  ingest:"id"`
	SessionID   string   `json:"sessionId"   ingest:"session"`
	Timestamp   string   `json:"timestamp"   ingest:"time"`
	Cwd         string   `json:"cwd"         ingest:"path" store:"record:cwd_id"`
	IsSidechain *bool    `json:"isSidechain" ingest:"bool"`
	Message     *Message `json:"message"`

	GitBranch      string   `json:"gitBranch"      ingest:"keyword" store:"record:branch_id"`
	Version        string   `json:"version"        ingest:"keyword" store:"record:version_id"`
	Entrypoint     string   `json:"entrypoint"     ingest:"keyword" store:"record:entrypoint_id"`
	UserType       string   `json:"userType"       ingest:"keyword" store:"record:user_type_id"`
	PermissionMode string   `json:"permissionMode" ingest:"keyword"`
	Subtype        string   `json:"subtype"        ingest:"keyword"`
	Level          string   `json:"level"          ingest:"keyword"`
	Operation      string   `json:"operation"      ingest:"keyword"`
	Slug           string   `json:"slug"           ingest:"keyword"`
	IsMeta         *bool    `json:"isMeta"         ingest:"bool"`
	IsCompact      *bool    `json:"isCompactSummary" ingest:"bool"`
	IsAPIError     *bool    `json:"isApiErrorMessage" ingest:"bool"`
	RequestID      string   `json:"requestId"      ingest:"id"`
	AgentID        string   `json:"agentId"        ingest:"id"`
	PromptID       string   `json:"promptId"       ingest:"id"`
	LeafUUID       string   `json:"leafUuid"       ingest:"id"`
	SourceToolUUID string   `json:"sourceToolAssistantUUID" ingest:"id"`
	DurationMs     *float64 `json:"durationMs"     ingest:"float"`
	Summary        string   `json:"summary"        ingest:"text"`

	// Known keys the recorder does not index.
	Content            json.RawMessage `json:"content"`
	ToolUseResult      json.RawMessage `json:"toolUseResult"`
	Error              json.RawMessage `json:"error"`
	HookCount          json.RawMessage `json:"hookCount"`
	HookErrors         json.RawMessage `json:"hookErrors"`
	HookInfos          json.RawMessage `json:"hookInfos"`
	PreventedContinue  json.RawMessage `json:"preventedContinuation"`
	StopReason         json.RawMessage `json:"stopReason"`
	SourceToolUseID    json.RawMessage `json:"sourceToolUseID"`
	ToolUseID          json.RawMessage `json:"toolUseID"`
	ParentToolUseID    json.RawMessage `json:"parentToolUseID"`
	LogicalParentUUID  json.RawMessage `json:"logicalParentUuid"`
	CompactMetadata    json.RawMessage `json:"compactMetadata"`
	Snapshot           json.RawMessage `json:"snapshot"`
	IsSnapshotUpdate   json.RawMessage `json:"isSnapshotUpdate"`
	MessageID          json.RawMessage `json:"messageId"`
	Todos              json.RawMessage `json:"todos"`
	ThinkingMetadata   json.RawMessage `json:"thinkingMetadata"`
	ImagePasteIDs      json.RawMessage `json:"imagePasteIds"`
	Attachment         json.RawMessage `json:"attachment"`
	PlanContent        json.RawMessage `json:"planContent"`
	VisibleOnlyInTrans json.RawMessage `json:"isVisibleInTranscriptOnly"`
}

// Message is a line's message object.
type Message struct {
	ID         string          `json:"id"          ingest:"id"`
	Type       string          `json:"type"        ingest:"keyword"`
	Role       string          `json:"role"        ingest:"keyword"`
	Model      string          `json:"model"       ingest:"keyword"`
	StopReason string          `json:"stop_reason" ingest:"keyword"`
	Usage      *Usage          `json:"usage"`
	Content    json.RawMessage `json:"content"`

	StopSequence      json.RawMessage `json:"stop_sequence"`
	Container         json.RawMessage `json:"container"`
	ContextManagement json.RawMessage `json:"context_management"`
}

// Usage is the token usage of a message.
type Usage struct {
	InputTokens              *int64         `json:"input_tokens"                ingest:"int"`
	OutputTokens             *int64         `json:"output_tokens"               ingest:"int"`
	CacheCreationInputTokens *int64         `json:"cache_creation_input_tokens" ingest:"int"`
	CacheReadInputTokens     *int64         `json:"cache_read_input_tokens"     ingest:"int"`
	CacheCreation            *CacheCreation `json:"cache_creation"`
	ServiceTier              string         `json:"service_tier"                ingest:"keyword"`
	InferenceGeo             string         `json:"inference_geo"               ingest:"keyword"`
	Speed                    string         `json:"speed"                       ingest:"keyword"`

	ServerToolUse json.RawMessage `json:"server_tool_use"`
	Iterations    json.RawMessage `json:"iterations"`
}

// CacheCreation is the per-TTL breakdown of cache writes.
type CacheCreation struct {
	Ephemeral1h *int64 `json:"ephemeral_1h_input_tokens" ingest:"int"`
	Ephemeral5m *int64 `json:"ephemeral_5m_input_tokens" ingest:"int"`
}

// Block is one message.content item (a union of the block types).
type Block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"        ingest:"text"   field:"text"`
	Thinking  string          `json:"thinking"    ingest:"text"   field:"thinking"`
	ID        string          `json:"id"          ingest:"id"     field:"tool_use_id"`
	Name      string          `json:"name"        ingest:"keyword" field:"name"`
	Input     json.RawMessage `json:"input"       ingest:"opaque" field:"tool_use.input"`
	ToolUseID string          `json:"tool_use_id" ingest:"id"     field:"tool_use_id"`
	IsError   *bool           `json:"is_error"    ingest:"bool"   field:"is_error"`
	Content   json.RawMessage `json:"content"`

	Signature    json.RawMessage `json:"signature"`
	Source       json.RawMessage `json:"source"`
	CacheControl json.RawMessage `json:"cache_control"`
	Caller       json.RawMessage `json:"caller"`
	Citations    json.RawMessage `json:"citations"`
}

// Source implements source.Source for transcripts.
type Source struct{}

// New returns the source.
func New() *Source { return &Source{} }

// Name implements source.Source.
func (*Source) Name() string { return Name }

// Version implements source.Source.
func (*Source) Version() int { return ParserVersion }

// Mode implements source.Source.
func (*Source) Mode() source.Mode { return source.Append }

// Match takes the path relative to the Claude directory.
func (*Source) Match(path string) (source.Captures, bool) {
	parts := strings.Split(path, "/")
	if len(parts) < 3 || parts[0] != "projects" {
		return nil, false
	}
	base := parts[len(parts)-1]
	switch {
	case len(parts) == 3 && strings.HasSuffix(base, ".jsonl") && !strings.HasSuffix(base, ".status.jsonl"):
		return source.Captures{"project": parts[1], "session": strings.TrimSuffix(base, ".jsonl")}, true
	case len(parts) == 5 && parts[3] == "subagents" && strings.HasPrefix(base, "agent-") && strings.HasSuffix(base, ".jsonl"):
		return source.Captures{
			"project": parts[1], "session": parts[2],
			"agent": strings.TrimSuffix(strings.TrimPrefix(base, "agent-"), ".jsonl"),
		}, true
	}
	return nil, false
}

// Catalog lists the fields the source can produce.
func (*Source) Catalog() map[string]source.Kind {
	c := source.Catalog(Line{}, Block{})
	c["project"] = source.KindKeyword
	c["file_session"] = source.KindID
	c["agent_id"] = source.KindID
	c["body_length"] = source.KindInt
	c["tool_result.body"] = source.KindOpaque
	c["saved_to"] = source.KindPath
	c["assistant_text"] = source.KindText
	c["user_text"] = source.KindText
	c["other_text"] = source.KindText
	delete(c, "text")
	return c
}

var (
	lineType  = reflect.TypeOf(Line{})
	blockType = reflect.TypeOf(Block{})
)

// Parse parses one line (without its newline).
func (*Source) Parse(c source.Captures, raw []byte) ([]source.Record, error) {
	raw = bytes.TrimSpace(raw)
	var line Line
	if err := json.Unmarshal(raw, &line); err != nil {
		return nil, err
	}
	lineRec := source.Record{Type: line.Type, Item: source.ItemLine, Session: line.SessionID}
	lineRec.Fields = source.Extract(&line, "")
	if line.Timestamp != "" {
		if t, ok := source.ParseTime(line.Timestamp); ok {
			lineRec.Time = t
		}
	}
	// The path captures are facts separate from the line's own sessionId.
	if c["session"] != "" {
		lineRec.Fields = append(lineRec.Fields, source.Field{Name: "file_session", Kind: source.KindID, S: c["session"]})
		if lineRec.Session == "" {
			lineRec.Session = c["session"]
		}
	}
	if c["project"] != "" {
		lineRec.Fields = append(lineRec.Fields, source.Field{Name: "project", Kind: source.KindKeyword, S: c["project"]})
	}
	if c["agent"] != "" {
		lineRec.Fields = append(lineRec.Fields, source.Field{Name: "agent_id", Kind: source.KindID, S: c["agent"]})
	}
	lineRec.Unknown = source.UnknownKeys(raw, lineType, "")
	if line.Type == "" {
		lineRec.Type = "unknown"
	}
	out := []source.Record{lineRec}

	if line.Message == nil || len(bytes.TrimSpace(line.Message.Content)) == 0 {
		if line.Summary == "" {
			return out, nil
		}
		return out, nil
	}
	role := roleWord(line)
	content := bytes.TrimSpace(line.Message.Content)
	if content[0] == '"' {
		var s string
		if err := json.Unmarshal(content, &s); err != nil {
			return nil, err
		}
		if s != "" {
			out = append(out, child(lineRec, 0, "text", []source.Field{{Name: role + "_text", Kind: source.KindText, S: s}}, nil))
		}
		return out, nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(content, &items); err != nil {
		return nil, err
	}
	for i, it := range items {
		var b Block
		if err := json.Unmarshal(it, &b); err != nil {
			return nil, err
		}
		typ := b.Type
		if typ == "" {
			typ = "unknown"
		}
		fields := source.Extract(&b, "")
		for k := range fields {
			if fields[k].Name == "text" {
				fields[k].Name = role + "_text"
			}
		}
		if typ == "tool_result" {
			body := flatten(b.Content)
			fields = append(fields,
				source.Field{Name: "tool_result.body", Kind: source.KindOpaque, S: body},
				source.Field{Name: "body_length", Kind: source.KindInt, N: int64(len(body))})
			if !utf8.ValidString(body) {
				fields[len(fields)-2].S = strings.ToValidUTF8(body, "�")
			}
			if to := SavedTo(body); to != "" {
				fields = append(fields, source.Field{Name: "saved_to", Kind: source.KindPath, S: to})
			}
		}
		unk := source.UnknownKeys(it, blockType, "message.content[]")
		out = append(out, child(lineRec, i, typ, fields, unk))
	}
	return out, nil
}

func child(line source.Record, item int, typ string, fields []source.Field, unk []string) source.Record {
	return source.Record{Type: typ, Item: item, Session: line.Session, Time: line.Time, Fields: fields, Unknown: unk}
}

func roleWord(l Line) string {
	switch {
	case l.Message != nil && l.Message.Role == "assistant", l.Type == "assistant":
		return "assistant"
	case l.Message != nil && l.Message.Role == "user", l.Type == "user":
		return "user"
	}
	return "other"
}

// flatten renders a tool_result body: a string is itself; an array contributes
// its text blocks joined by a single space; anything else is its JSON text.
func flatten(raw json.RawMessage) string {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || bytes.Equal(t, []byte("null")) {
		return ""
	}
	var s string
	if json.Unmarshal(t, &s) == nil {
		return s
	}
	var arr []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(t, &arr) == nil {
		parts := make([]string, 0, len(arr))
		for _, b := range arr {
			if b.Type == "text" {
				parts = append(parts, b.Text)
			}
		}
		return strings.Join(parts, " ")
	}
	return string(t)
}

// Storage lists where each field is stored (implements source.Stored).
func (*Source) Storage() map[string]source.Storage { return source.MustCatalogStorage(Line{}, Block{}) }
