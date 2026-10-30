// Package claudesubagentmeta is the claude-subagent-meta source:
// projects/{project}/{session}/subagents/agent-{agent}.meta.json, one record per
// version linking the subagent to the tool call (toolUseId) that spawned it.
package claudesubagentmeta

import (
	"encoding/json"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/source"
)

// Name is the source name.
const Name = "claude-subagent-meta"

// ParserVersion is stamped on every record.
const ParserVersion = 1

// Meta is one meta.json file.
type Meta struct {
	AgentType             string          `json:"agentType"             ingest:"keyword"`
	Description           string          `json:"description"           ingest:"text"`
	RequestNonInteractive *bool           `json:"requestNonInteractive" ingest:"bool"`
	RequestShape          json.RawMessage `json:"requestShape"`
	SpawnDepth            *int64          `json:"spawnDepth"            ingest:"int"`
	ToolUseID             string          `json:"toolUseId"             ingest:"id" field:"tool_use_id"`
}

// Source implements source.Source.
type Source struct{}

// New returns the source.
func New() *Source { return &Source{} }

// Name implements source.Source.
func (*Source) Name() string { return Name }

// Version implements source.Source.
func (*Source) Version() int { return ParserVersion }

// Mode implements source.Source.
func (*Source) Mode() source.Mode { return source.Snapshot }

// Match takes the path relative to the Claude directory.
func (*Source) Match(path string) (source.Captures, bool) {
	parts := strings.Split(path, "/")
	if len(parts) != 5 || parts[0] != "projects" || parts[3] != "subagents" {
		return nil, false
	}
	base := parts[4]
	if !strings.HasPrefix(base, "agent-") || !strings.HasSuffix(base, ".meta.json") {
		return nil, false
	}
	return source.Captures{
		"project": parts[1], "session": parts[2],
		"agent": strings.TrimSuffix(strings.TrimPrefix(base, "agent-"), ".meta.json"),
	}, true
}

// Catalog lists the fields the source can produce.
func (*Source) Catalog() map[string]source.Kind {
	c := source.Catalog(Meta{})
	c["project"] = source.KindKeyword
	c["file_session"] = source.KindID
	c["agent_id"] = source.KindID
	return c
}

// Parse parses one stored version.
func (*Source) Parse(c source.Captures, raw []byte) ([]source.Record, error) {
	var m Meta
	rec, err := source.JSONRecord("subagent_meta", c["session"], raw, &m)
	if err != nil {
		return nil, err
	}
	rec.Fields = append(rec.Fields, source.Str("project", source.KindKeyword, c["project"])...)
	rec.Fields = append(rec.Fields, source.Str("file_session", source.KindID, c["session"])...)
	rec.Fields = append(rec.Fields, source.Str("agent_id", source.KindID, c["agent"])...)
	return []source.Record{rec}, nil
}
