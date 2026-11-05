// Package claudetoolresult is the claude-tool-result-body source:
// projects/{project}/{session}/tool-results/*, the full output of a tool call
// too large for the transcript. One record per version; its body is the whole
// file. A file named toolu_<id>.* links to its tool_result by tool_use_id; any
// other name links by the path the transcript's preview names (rel_path is the
// path under the Claude directory, a suffix of that saved-to path).
package claudetoolresult

import (
	"strings"
	"unicode/utf8"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/source"
)

// Name is the source name.
const Name = "claude-tool-result-body"

// ParserVersion is stamped on every record.
const ParserVersion = 2

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
	if len(parts) < 5 || parts[0] != "projects" || parts[3] != "tool-results" {
		return nil, false
	}
	return source.Captures{
		"project": parts[1], "session": parts[2],
		"name": strings.Join(parts[4:], "/"), "rel_path": path,
	}, true
}

// Catalog lists the fields the source can produce.
func (*Source) Catalog() map[string]source.Kind {
	return map[string]source.Kind{
		"project": source.KindKeyword, "file_session": source.KindID,
		"tool_use_id": source.KindID, "name": source.KindKeyword, "rel_path": source.KindPath,
		"body": source.KindOpaque, "body_length": source.KindInt,
	}
}

// Parse records the whole file as one record.
func (*Source) Parse(c source.Captures, raw []byte) ([]source.Record, error) {
	rec := source.Record{Type: "tool_result_body", Item: source.ItemLine, Session: c["session"]}
	rec.Fields = append(rec.Fields, source.Field{Name: "body_length", Kind: source.KindInt, N: int64(len(raw))})
	if len(raw) > 0 && utf8.Valid(raw) {
		rec.Fields = append(rec.Fields, source.Field{Name: "body", Kind: source.KindOpaque, S: string(raw)})
	}
	rec.Fields = append(rec.Fields, source.Str("project", source.KindKeyword, c["project"])...)
	rec.Fields = append(rec.Fields, source.Str("file_session", source.KindID, c["session"])...)
	rec.Fields = append(rec.Fields, source.Str("name", source.KindKeyword, c["name"])...)
	rec.Fields = append(rec.Fields, source.Str("rel_path", source.KindPath, c["rel_path"])...)
	if strings.HasPrefix(c["name"], "toolu_") {
		id, _, _ := strings.Cut(c["name"], ".")
		rec.Fields = append(rec.Fields, source.Field{Name: "tool_use_id", Kind: source.KindID, S: id})
	}
	return []source.Record{rec}, nil
}
